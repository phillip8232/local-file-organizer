package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillip8232/local-file-organizer/internal/analyze"
	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/executor"
	"github.com/phillip8232/local-file-organizer/internal/index"
	"github.com/phillip8232/local-file-organizer/internal/plan"
	"github.com/phillip8232/local-file-organizer/internal/rules"
)

type env struct {
	t       *testing.T
	root    string
	cfg     *config.Config
	db      *index.DB
	ex      *executor.Executor
	trashed []string
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	data := filepath.Join(base, "data")
	write(t, filepath.Join(data, "config.yaml"), "roots: ['"+root+"']\nmin_duplicate_size: 1\n")
	big := strings.Repeat("duplicate content ", 5000) // > 3 samples, exercises the sampled prehash

	write(t, filepath.Join(root, "Downloads", "report.pdf"), big)
	write(t, filepath.Join(root, "Downloads", "report (1).pdf"), big)
	write(t, filepath.Join(root, "Archive", "report.pdf"), big)
	write(t, filepath.Join(root, "small-a.txt"), "same small")
	write(t, filepath.Join(root, "Notes", "small-b.txt"), "same small")
	write(t, filepath.Join(root, "empty.txt"), "")
	write(t, filepath.Join(root, "Old", "Nested", "Thumbs.db"), "x") // only OS metadata: empty
	write(t, filepath.Join(root, "Downloads", "movie.mp4.crdownload"), "partial")
	write(t, filepath.Join(root, "Downloads", "ancient.zip"), "old zip")
	write(t, filepath.Join(root, "Downloads", "photo.jpg"), "jpeg bytes")
	write(t, filepath.Join(root, "package.json"), "{}") // loose marker: root is not a project
	// A project: its duplicate must never be reported.
	write(t, filepath.Join(root, "Code", "app", "go.mod"), "module app")
	write(t, filepath.Join(root, "Code", "app", "copy.txt"), "same small")
	if err := os.MkdirAll(filepath.Join(root, "Code", "app", "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A folder containing only an ignored folder is not empty.
	if err := os.MkdirAll(filepath.Join(root, "Repo", ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -200)
	for _, p := range []string{"movie.mp4.crdownload", "ancient.zip"} {
		if err := os.Chtimes(filepath.Join(root, "Downloads", p), old, old); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	// The temp folder lives under AppData on Windows, which is protected.
	cfg.ProtectedPaths = []string{cfg.DataDir}
	cfg.DownloadsDir = filepath.Join(root, "Downloads")
	cfg.Known = map[string]string{"downloads": cfg.DownloadsDir}
	setBirth(t, filepath.Join(root, "Downloads", "ancient.zip"), old)
	setBirth(t, filepath.Join(root, "Downloads", "movie.mp4.crdownload"), old)
	db, err := index.Open(filepath.Join(data, "organizer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, root: root, cfg: cfg, db: db}
	e.ex, err = executor.New(db, cfg, func(p string) error {
		e.trashed = append(e.trashed, p)
		return os.RemoveAll(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) scan() {
	e.t.Helper()
	s := &index.Scanner{DB: e.db, Cfg: e.cfg}
	if err := s.Run(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) p(rel string) string { return filepath.Join(e.root, filepath.FromSlash(rel)) }

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestCleanupApplyUndo(t *testing.T) {
	e := setup(t)
	e.scan()
	r, err := analyze.Analyze(e.db, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Duplicates) != 2 {
		t.Fatalf("want 2 duplicate groups, got %d: %+v", len(r.Duplicates), r.Duplicates)
	}
	for _, g := range r.Duplicates {
		keep := g.Files[g.Keep].Path
		if strings.Contains(keep, "Code") {
			t.Errorf("project file in a duplicate group: %v", g.Files)
		}
		if len(g.Files) == 3 && keep != e.p("Archive/report.pdf") {
			t.Errorf("smart keeper should be the Archive copy, got %s", keep)
		}
	}
	if len(r.EmptyDirs) != 1 || r.EmptyDirs[0] != e.p("Old") {
		t.Errorf("empty dirs = %v, want only %s", r.EmptyDirs, e.p("Old"))
	}
	if len(r.EmptyFiles) != 1 || r.EmptyFiles[0].Path != e.p("empty.txt") {
		t.Errorf("empty files = %v", r.EmptyFiles)
	}
	if len(r.Junk) != 1 || !strings.HasSuffix(r.Junk[0].Path, ".crdownload") {
		t.Errorf("junk = %v", r.Junk)
	}
	// Old downloads need an old birth time too, which tests can only set on Windows.
	if canSetBirth && len(r.OldDownloads) != 2 { // ancient.zip and the old partial download
		t.Errorf("old downloads = %v", r.OldDownloads)
	}
	if len(r.Projects) != 2 || r.Projects[0] != e.p("Code/app") || r.Projects[1] != e.p("Repo") {
		t.Errorf("projects = %v, want Code/app and Repo", r.Projects)
	}

	p := plan.Cleanup(r)
	if err := plan.Save(e.db, p); err != nil {
		t.Fatal(err)
	}
	actions, err := p.Resolve(p.DefaultApproval())
	if err != nil {
		t.Fatal(err)
	}
	// Change one file after planning: it must be skipped, not deleted.
	write(t, e.p("Notes/small-b.txt"), "same small")
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(e.p("Notes/small-b.txt"), later, later)
	_ = os.Chtimes(e.p("small-a.txt"), later, later)

	res, err := e.ex.Apply(context.Background(), p.ID, "test", actions)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("apply: %+v", res)
	if res.Failed != 0 {
		t.Fatalf("failures: %v", res.Messages)
	}
	if res.Skipped != 1 {
		t.Errorf("want 1 skip (the changed small file), got %d: %v", res.Skipped, res.Messages)
	}
	for _, gone := range []string{"Downloads/report.pdf", "Downloads/report (1).pdf", "empty.txt", "Old", "Downloads/movie.mp4.crdownload"} {
		if exists(e.p(gone)) {
			t.Errorf("%s should be in the holding area", gone)
		}
	}
	for _, kept := range []string{"Archive/report.pdf", "small-a.txt", "Notes/small-b.txt", "Downloads/ancient.zip", "Code/app/copy.txt", "Code/app/emptydir", "Repo/.git"} {
		if !exists(e.p(kept)) {
			t.Errorf("%s must not be touched", kept)
		}
	}
	h, _ := e.ex.Holding()
	if h.Items != int64(res.Done) {
		t.Errorf("holding has %d items, want %d", h.Items, res.Done)
	}

	// Recreate one original name so undo has to pick "(2)".
	write(t, e.p("empty.txt"), "new file in the way")
	u, err := e.ex.Undo(res.Batch)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("undo: %+v", u)
	if u.Failed != 0 || u.Done != res.Done {
		t.Fatalf("undo restored %d of %d: %v", u.Done, res.Done, u.Messages)
	}
	for _, back := range []string{"Downloads/report.pdf", "Downloads/report (1).pdf", "Old/Nested/Thumbs.db", "Downloads/movie.mp4.crdownload", "empty (2).txt"} {
		if !exists(e.p(back)) {
			t.Errorf("%s should be back", back)
		}
	}
}

func TestDuplicateGuardKeepsLastCopy(t *testing.T) {
	e := setup(t)
	e.scan()
	r, _ := analyze.Analyze(e.db, e.cfg)
	p := plan.Cleanup(r)
	_ = plan.Save(e.db, p)
	var big analyze.DupGroup
	for _, g := range r.Duplicates {
		if len(g.Files) == 3 {
			big = g
		}
	}
	keep := big.Files[big.Keep].Path
	var del []string
	for _, f := range big.Files {
		if f.Path != keep {
			del = append(del, f.Path)
		}
	}
	// Keeper vanishes after planning: nothing in the group may be deleted.
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	actions, err := p.Resolve(plan.Approval{Groups: map[string]plan.GroupChoice{big.ID: {Keep: keep, Delete: del}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.ex.Apply(context.Background(), p.ID, "test", actions)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 0 || res.Skipped != 2 {
		t.Fatalf("want 0 done / 2 skipped, got %+v", res)
	}
	for _, d := range del {
		if !exists(d) {
			t.Errorf("%s was deleted although the kept copy is gone", d)
		}
	}
	// Approvals can't name paths outside the group or keep and delete the same file.
	if _, err := p.Resolve(plan.Approval{Groups: map[string]plan.GroupChoice{big.ID: {Keep: keep, Delete: []string{keep}}}}); err == nil {
		t.Error("keep+delete of the same path was accepted")
	}
	if _, err := p.Resolve(plan.Approval{Groups: map[string]plan.GroupChoice{big.ID: {Keep: keep, Delete: []string{e.p("small-a.txt")}}}}); err == nil {
		t.Error("deleting a path outside the group was accepted")
	}
}

func TestSortRulesAndPurge(t *testing.T) {
	e := setup(t)
	e.scan()
	f, _, err := rules.Load(e.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := rules.Plan(e.db, e.cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	dsts := map[string]string{}
	for _, a := range p.Actions {
		dsts[filepath.Base(a.Src)] = a.Dst
	}
	year := time.Now().Format("2006")
	if want := e.p("Downloads/Images/" + year + "/photo.jpg"); dsts["photo.jpg"] != want {
		t.Errorf("photo.jpg → %q, want %q", dsts["photo.jpg"], want)
	}
	if _, ok := dsts["report.pdf"]; !ok {
		t.Error("report.pdf in Downloads should match the Documents rule")
	}
	if _, ok := dsts["small-b.txt"]; ok {
		t.Error("files in subfolders of other roots must not be sorted")
	}
	_ = plan.Save(e.db, p)
	actions, _ := p.Resolve(p.DefaultApproval())
	res, err := e.ex.Apply(context.Background(), p.ID, "sort", actions)
	if err != nil || res.Failed != 0 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if !exists(e.p("Downloads/Images/" + year + "/photo.jpg")) {
		t.Fatal("photo not moved")
	}
	u, err := e.ex.Undo(res.Batch)
	if err != nil || u.Failed != 0 {
		t.Fatalf("undo: %v %+v", err, u)
	}
	if !exists(e.p("Downloads/photo.jpg")) || exists(e.p("Downloads/Images")) {
		t.Error("undo should restore the photo and remove the folders it created")
	}

	// Purge: a held item goes to the trash from its original location.
	e.scan()
	r, _ := analyze.Analyze(e.db, e.cfg)
	cp := plan.Cleanup(r)
	_ = plan.Save(e.db, cp)
	var ids []int
	for _, a := range cp.Actions {
		if a.Category == "empty-file" {
			ids = append(ids, a.ID)
		}
	}
	acts, _ := cp.Resolve(plan.Approval{IDs: ids})
	if res, err := e.ex.Apply(context.Background(), cp.ID, "c", acts); err != nil || res.Done != 1 {
		t.Fatalf("apply empty file: %v %+v", err, res)
	}
	pr, err := e.ex.Purge(0, false)
	if err != nil || pr.Done != 1 {
		t.Fatalf("purge: %v %+v", err, pr)
	}
	if len(e.trashed) != 1 || e.trashed[0] != e.p("empty.txt") {
		t.Errorf("trashed %v, want the original path", e.trashed)
	}
}

func TestIncrementalScanKeepsHashes(t *testing.T) {
	e := setup(t)
	e.scan()
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM files WHERE fullhash IS NOT NULL`).Scan(&n)
	if n == 0 {
		t.Fatal("no hashes after first scan")
	}
	e.scan()
	var m int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM files WHERE fullhash IS NOT NULL`).Scan(&m)
	if m != n {
		t.Errorf("hashes after rescan = %d, want %d", m, n)
	}
	// Remove a file: the rescan forgets it.
	_ = os.Remove(e.p("small-a.txt"))
	e.scan()
	var c int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM files WHERE path=?`, e.p("small-a.txt")).Scan(&c)
	if c != 0 {
		t.Error("deleted file still indexed")
	}
}
