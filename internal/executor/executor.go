// Package executor carries out approved actions. Every change is journaled
// before it happens, deletes go to a holding area first so undo is exact,
// and each file is re-checked against the plan right before it's touched.
package executor

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"organizer/internal/config"
	"organizer/internal/fsutil"
	"organizer/internal/hashing"
	"organizer/internal/index"
	"organizer/internal/plan"
	"organizer/internal/platform"
)

// HoldingDirName is the per-volume holding folder used when a file can't be
// renamed into the main holding area (it's on another drive).
const HoldingDirName = ".organizer-holding"

// Executor applies, undoes and purges batches. One operation runs at a time.
type Executor struct {
	DB    *index.DB
	Cfg   *config.Config
	Trash func(path string) error
	mu    sync.Mutex
}

// Result summarises a batch, an undo or a purge.
type Result struct {
	Batch    int64    `json:"batch"`
	Done     int      `json:"done"`
	Skipped  int      `json:"skipped"`
	Failed   int      `json:"failed"`
	Bytes    int64    `json:"bytes"`
	Messages []string `json:"messages"`
}

func (r *Result) note(format string, args ...any) {
	if len(r.Messages) < 500 {
		r.Messages = append(r.Messages, fmt.Sprintf(format, args...))
	}
}

// New returns an executor after reconciling any batch a crash interrupted.
func New(db *index.DB, cfg *config.Config, trash func(string) error) (*Executor, error) {
	e := &Executor{DB: db, Cfg: cfg, Trash: trash}
	return e, e.recover()
}

func (e *Executor) holdingRoot() string { return filepath.Join(e.Cfg.DataDir, "holding") }

// recover settles journal rows left 'pending' by a crash by looking at what
// actually happened on disk.
func (e *Executor) recover() error {
	rows, err := e.DB.Query(`SELECT id, src, dst FROM journal WHERE state='pending'`)
	if err != nil {
		return err
	}
	type pend struct {
		id       int64
		src, dst string
	}
	var ps []pend
	for rows.Next() {
		var p pend
		if err := rows.Scan(&p.id, &p.src, &p.dst); err != nil {
			rows.Close()
			return err
		}
		ps = append(ps, p)
	}
	rows.Close()
	for _, p := range ps {
		srcThere, dstThere := exists(p.src), p.dst != "" && exists(p.dst)
		state, msg := "failed", "interrupted before it ran"
		switch {
		case dstThere && !srcThere:
			state, msg = "done", ""
		case dstThere && srcThere:
			msg = "interrupted; both copies exist"
		}
		if _, err := e.DB.Exec(`UPDATE journal SET state=?, error=? WHERE id=?`, state, msg, p.id); err != nil {
			return err
		}
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// Apply runs approved actions as one batch. Files are handled before
// folders so a folder emptied by the batch is checked after its contents go.
func (e *Executor) Apply(ctx context.Context, planID int64, summary string, actions []plan.Action) (*Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	res, err := e.DB.Exec(`INSERT INTO batches(created, plan_id, summary, state) VALUES(?,?,?,'running')`, now.Unix(), planID, summary)
	if err != nil {
		return nil, err
	}
	batch, _ := res.LastInsertId()
	r := &Result{Batch: batch}
	sort.SliceStable(actions, func(i, j int) bool { return !actions[i].IsDir && actions[j].IsDir })
	holdDir := filepath.Join(e.holdingRoot(), strconv.FormatInt(batch, 10))
	for i, a := range actions {
		if ctx.Err() != nil {
			r.note("stopped: %v", ctx.Err())
			break
		}
		if reason := e.verify(ctx, a); reason != "" {
			r.Skipped++
			r.note("skipped %s: %s", a.Src, reason)
			continue
		}
		var err error
		switch a.Op {
		case plan.OpDelete:
			err = e.hold(batch, holdDir, i+1, a)
		case plan.OpMove:
			err = e.move(batch, a)
		default:
			err = fmt.Errorf("unknown op %q", a.Op)
		}
		if err != nil {
			r.Failed++
			r.note("failed %s: %v", a.Src, err)
			continue
		}
		r.Done++
		r.Bytes += a.Size
	}
	state := "done"
	switch {
	case r.Done == 0:
		state = "nothing done"
	case r.Failed > 0 || r.Skipped > 0:
		state = "partial"
	}
	_, err = e.DB.Exec(`UPDATE batches SET state=? WHERE id=?`, state, batch)
	return r, err
}

// verify re-checks an action against the disk. It returns why the action
// must be skipped, or "" if it's safe to go ahead.
func (e *Executor) verify(ctx context.Context, a plan.Action) string {
	fi, err := os.Lstat(a.Src)
	if err != nil {
		return "no longer there"
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "is a link"
	}
	if fi.IsDir() != a.IsDir {
		return "changed type"
	}
	root := e.Cfg.RootOf(a.Src)
	switch {
	case root == "":
		return "outside the folders being organised"
	case fsutil.SamePath(a.Src, root):
		return "is a root folder"
	case e.Cfg.IsProtected(a.Src):
		return "protected folder"
	case fsutil.InProject(a.Src, root):
		return "inside a project folder"
	}
	if a.IsDir {
		if ok, _ := fsutil.OnlyIgnorable(a.Src); !ok {
			return "folder is no longer empty"
		}
	} else if fi.Size() != a.Size || fi.ModTime().UnixNano() != a.MTime {
		return "changed since the scan"
	}
	if a.Op == plan.OpMove {
		if a.Dst == "" || e.Cfg.IsProtected(a.Dst) {
			return "destination is protected"
		}
		if fsutil.Within(a.Dst, a.Src) {
			return "destination is inside the source"
		}
	}
	if a.Category == "duplicate" {
		return e.verifyDuplicate(ctx, a)
	}
	return ""
}

// verifyDuplicate makes sure the copy being kept still exists, is a
// different file, and both still have the planned content. This is the
// guard against ever deleting the only copy.
func (e *Executor) verifyDuplicate(ctx context.Context, a plan.Action) string {
	if a.KeepPath == "" || a.Hash == "" || fsutil.SamePath(a.KeepPath, a.Src) {
		return "no copy to keep"
	}
	kfi, err := os.Lstat(a.KeepPath)
	if err != nil || !kfi.Mode().IsRegular() {
		return "the copy being kept is gone"
	}
	if kfi.Size() != a.Size {
		return "the copy being kept has changed"
	}
	sid, _, err1 := platform.FileID(a.Src)
	kid, _, err2 := platform.FileID(a.KeepPath)
	if err1 != nil || err2 != nil {
		return "couldn't identify the files"
	}
	if sid == kid {
		return "same file as the kept copy (hard link)"
	}
	want, err := hex.DecodeString(a.Hash)
	if err != nil {
		return "bad hash in plan"
	}
	for _, p := range []string{a.Src, a.KeepPath} {
		h, err := hashing.Full(ctx, p)
		if err != nil {
			return "couldn't re-read " + p
		}
		if !bytes.Equal(h, want) {
			return "content no longer matches"
		}
	}
	return ""
}

func (e *Executor) journal(batch int64, op, category, src, dst string, size int64, isDir bool, state string) (int64, error) {
	res, err := e.DB.Exec(`INSERT INTO journal(batch, op, category, src, dst, size, isdir, state, ts) VALUES(?,?,?,?,?,?,?,?,?)`,
		batch, op, category, src, dst, size, isDir, state, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (e *Executor) finish(id int64, err error) error {
	if err != nil {
		_, _ = e.DB.Exec(`UPDATE journal SET state='failed', error=? WHERE id=?`, err.Error(), id)
		return err
	}
	_, derr := e.DB.Exec(`UPDATE journal SET state='done' WHERE id=?`, id)
	return derr
}

// manifestEntry is appended to holding/<batch>/manifest.jsonl so the holding
// area describes itself even without the database.
type manifestEntry struct {
	Src  string `json:"src"`
	Held string `json:"held"`
	Time int64  `json:"time"`
}

func appendManifest(dir string, m manifestEntry) error {
	f, err := os.OpenFile(filepath.Join(dir, "manifest.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// hold moves a file or folder into the holding area. If it's on another
// drive it uses that drive's own holding folder; if neither works it goes
// straight to the OS trash.
func (e *Executor) hold(batch int64, holdDir string, seq int, a plan.Action) error {
	name := fmt.Sprintf("%05d-%s", seq, filepath.Base(a.Src))
	dirs := []string{holdDir, filepath.Join(platform.VolumeRoot(a.Src), HoldingDirName, strconv.FormatInt(batch, 10))}
	var lastErr error
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			lastErr = err
			continue
		}
		dst := filepath.Join(dir, name)
		id, err := e.journal(batch, "hold", a.Category, a.Src, dst, a.Size, a.IsDir, "pending")
		if err != nil {
			return err
		}
		if err := appendManifest(dir, manifestEntry{Src: a.Src, Held: dst, Time: time.Now().Unix()}); err != nil {
			_ = e.finish(id, err)
			return err
		}
		err = os.Rename(a.Src, dst)
		if err == nil {
			return e.finish(id, nil)
		}
		_ = e.finish(id, err)
		lastErr = err
		if !platform.IsCrossDevice(err) {
			return err
		}
	}
	if e.Trash == nil {
		return lastErr
	}
	id, err := e.journal(batch, "trash", a.Category, a.Src, "", a.Size, a.IsDir, "pending")
	if err != nil {
		return err
	}
	return e.finish(id, e.Trash(a.Src))
}

// move renames a file to its destination, never overwriting: a name clash
// gets " (2)" and so on. Folders it creates are journaled so undo can remove
// them again.
func (e *Executor) move(batch int64, a plan.Action) error {
	dst := fsutil.UniquePath(a.Dst)
	var missing []string
	for d := filepath.Dir(dst); !exists(d); d = filepath.Dir(d) {
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		d := missing[i]
		if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if _, err := e.journal(batch, "mkdir", a.Category, d, "", 0, true, "done"); err != nil {
			return err
		}
	}
	id, err := e.journal(batch, "move", a.Category, a.Src, dst, a.Size, false, "pending")
	if err != nil {
		return err
	}
	err = os.Rename(a.Src, dst)
	if platform.IsCrossDevice(err) {
		err = errors.New("moving between drives isn't supported yet")
	}
	return e.finish(id, err)
}

// Undo reverses every finished step of a batch, newest first. Anything whose
// original spot is taken now comes back as "name (2)".
func (e *Executor) Undo(batch int64) (*Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.DB.Query(`SELECT id, op, src, dst, size, state FROM journal WHERE batch=? AND state IN ('done','purged','erased') ORDER BY id DESC`, batch)
	if err != nil {
		return nil, err
	}
	type step struct {
		id           int64
		op, src, dst string
		size         int64
		state        string
	}
	var steps []step
	for rows.Next() {
		var s step
		if err := rows.Scan(&s.id, &s.op, &s.src, &s.dst, &s.size, &s.state); err != nil {
			rows.Close()
			return nil, err
		}
		steps = append(steps, s)
	}
	rows.Close()
	r := &Result{Batch: batch}
	for _, s := range steps {
		switch {
		case s.state == "purged":
			r.Skipped++
			r.note("%s is in the OS trash now; restore it from there", s.src)
			continue
		case s.state == "erased":
			r.Skipped++
			r.note("%s was permanently deleted", s.src)
			continue
		case s.op == "trash":
			r.Skipped++
			r.note("%s went straight to the OS trash; restore it from there", s.src)
			continue
		case s.op == "mkdir":
			if os.Remove(s.src) == nil {
				_, _ = e.DB.Exec(`UPDATE journal SET state='undone' WHERE id=?`, s.id)
			}
			continue
		}
		if !exists(s.dst) {
			r.Failed++
			r.note("%s: can't find %s any more", s.src, s.dst)
			continue
		}
		target := fsutil.UniquePath(s.src)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			r.Failed++
			r.note("%s: %v", s.src, err)
			continue
		}
		if err := os.Rename(s.dst, target); err != nil {
			r.Failed++
			r.note("%s: %v", s.src, err)
			continue
		}
		if target != s.src {
			r.note("%s restored as %s (the name was taken)", s.src, target)
		}
		_, _ = e.DB.Exec(`UPDATE journal SET state='undone' WHERE id=?`, s.id)
		r.Done++
		r.Bytes += s.size
	}
	state := "undone"
	if r.Failed > 0 || r.Skipped > 0 {
		state = "partly undone"
	}
	_, err = e.DB.Exec(`UPDATE batches SET state=? WHERE id=?`, state, batch)
	return r, err
}

// Purge empties holding items older than age. By default each one goes to
// the OS trash from its original location, so the OS's own "Restore" puts it
// back where it came from; permanent deletes it outright.
func (e *Executor) Purge(age time.Duration, permanent bool) (*Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.DB.Query(`SELECT id, src, dst, size FROM journal WHERE op='hold' AND state='done' AND ts<=?`,
		time.Now().Add(-age).Unix())
	if err != nil {
		return nil, err
	}
	type item struct {
		id       int64
		src, dst string
		size     int64
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.src, &it.dst, &it.size); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	r := &Result{}
	touched := map[string]bool{}
	for _, it := range items {
		touched[filepath.Dir(it.dst)] = true
		if !exists(it.dst) {
			_, _ = e.DB.Exec(`UPDATE journal SET state='failed', error='missing from holding area' WHERE id=?`, it.id)
			continue
		}
		var err error
		state := "purged"
		if permanent {
			state = "erased"
			err = os.RemoveAll(it.dst)
		} else {
			err = e.trashFromOrigin(it.src, it.dst)
		}
		if err != nil {
			r.Failed++
			r.note("%s: %v", it.src, err)
			continue
		}
		_, _ = e.DB.Exec(`UPDATE journal SET state=? WHERE id=?`, state, it.id)
		r.Done++
		r.Bytes += it.size
	}
	for dir := range touched {
		cleanHoldingDir(dir)
	}
	return r, nil
}

func (e *Executor) trashFromOrigin(src, held string) error {
	if e.Trash == nil {
		return errors.New("no OS trash available")
	}
	if !exists(src) && exists(filepath.Dir(src)) {
		if os.Rename(held, src) == nil {
			if err := e.Trash(src); err != nil {
				_ = os.Rename(src, held)
				return err
			}
			return nil
		}
	}
	return e.Trash(held)
}

// cleanHoldingDir removes a batch folder once only its manifest is left.
func cleanHoldingDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, en := range entries {
		if en.Name() != "manifest.jsonl" {
			return
		}
	}
	_ = os.Remove(filepath.Join(dir, "manifest.jsonl"))
	_ = os.Remove(dir)
}

// Holding describes what's waiting in the holding area.
type Holding struct {
	Items  int64 `json:"items"`
	Bytes  int64 `json:"bytes"`
	Oldest int64 `json:"oldest"` // unix seconds
	Days   int   `json:"days"`
}

func (e *Executor) Holding() (Holding, error) {
	h := Holding{Days: e.Cfg.HoldingDays}
	err := e.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0), COALESCE(MIN(ts),0) FROM journal WHERE op='hold' AND state='done'`).
		Scan(&h.Items, &h.Bytes, &h.Oldest)
	return h, err
}

// Batch is one row of history.
type Batch struct {
	ID      int64          `json:"id"`
	Created int64          `json:"created"`
	PlanID  int64          `json:"planId"`
	Summary string         `json:"summary"`
	State   string         `json:"state"`
	Counts  map[string]int `json:"counts"`
	Bytes   int64          `json:"bytes"`
}

func (e *Executor) History(limit int) ([]Batch, error) {
	rows, err := e.DB.Query(`SELECT id, created, COALESCE(plan_id,0), summary, state FROM batches ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var out []Batch
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.Created, &b.PlanID, &b.Summary, &b.State); err != nil {
			rows.Close()
			return nil, err
		}
		b.Counts = map[string]int{}
		out = append(out, b)
	}
	rows.Close()
	for i := range out {
		rows, err := e.DB.Query(`SELECT state, COUNT(*), COALESCE(SUM(size),0) FROM journal WHERE batch=? AND op!='mkdir' GROUP BY state`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			var n int
			var bytes int64
			if err := rows.Scan(&s, &n, &bytes); err != nil {
				rows.Close()
				return nil, err
			}
			out[i].Counts[s] = n
			if s == "done" {
				out[i].Bytes += bytes
			}
		}
		rows.Close()
	}
	return out, nil
}

// Steps lists a batch's journal for display.
type Step struct {
	Op       string `json:"op"`
	Category string `json:"category"`
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
}

func (e *Executor) Steps(batch int64) ([]Step, error) {
	rows, err := e.DB.Query(`SELECT op, category, src, dst, state, error FROM journal WHERE batch=? ORDER BY id`, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var s Step
		if err := rows.Scan(&s.Op, &s.Category, &s.Src, &s.Dst, &s.State, &s.Error); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
