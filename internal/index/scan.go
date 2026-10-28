package index

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charlievieth/fastwalk"

	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/fsutil"
	"github.com/phillip8232/local-file-organizer/internal/hashing"
	"github.com/phillip8232/local-file-organizer/internal/platform"
)

// Progress is a snapshot of a running or finished scan.
type Progress struct {
	Phase   string `json:"phase"` // walking | projects | prehash | hashing | done | failed
	Files   int64  `json:"files"`
	Dirs    int64  `json:"dirs"`
	Bytes   int64  `json:"bytes"`
	Skipped int64  `json:"skipped"`
	Hashed  int64  `json:"hashed"`
	ToHash  int64  `json:"toHash"`
	Started int64  `json:"started"`
	Elapsed int64  `json:"elapsedMs"`
	Error   string `json:"error,omitempty"`
}

// Scanner walks the configured roots into the index, then hashes duplicate
// candidates. Unchanged files keep their cached hashes.
type Scanner struct {
	DB  *DB
	Cfg *config.Config

	files, dirs, bytes, skipped, hashed, toHash atomic.Int64
	phase                                       atomic.Value
	started                                     atomic.Int64 // unix ns
}

func (s *Scanner) Progress() Progress {
	ph, _ := s.phase.Load().(string)
	p := Progress{Phase: ph, Files: s.files.Load(), Dirs: s.dirs.Load(), Bytes: s.bytes.Load(),
		Skipped: s.skipped.Load(), Hashed: s.hashed.Load(), ToHash: s.toHash.Load()}
	if st := s.started.Load(); st != 0 {
		p.Started = st / 1e9
		p.Elapsed = time.Since(time.Unix(0, st)).Milliseconds()
	}
	return p
}

type entry struct {
	path, root, parent, name string
	dir, opaque, project     bool
	size, mtime, btime       int64
	placeholder              bool
}

const batchSize = 2000

// Run performs a full incremental scan of every configured root.
func (s *Scanner) Run(ctx context.Context) (err error) {
	now := time.Now()
	s.started.Store(now.UnixNano())
	s.phase.Store("walking")
	defer platform.Background()()

	res, err := s.DB.Exec(`INSERT INTO scans(started, roots) VALUES(?, ?)`, now.Unix(), strings.Join(s.Cfg.RootPaths, "\n"))
	if err != nil {
		return err
	}
	scanID, _ := res.LastInsertId()
	defer func() {
		msg := sql.NullString{}
		if err != nil {
			msg = sql.NullString{String: err.Error(), Valid: true}
			s.phase.Store("failed")
		} else {
			s.phase.Store("done")
		}
		_, _ = s.DB.Exec(`UPDATE scans SET finished=?, files=?, bytes=?, hashed=?, skipped=?, error=? WHERE id=?`,
			time.Now().Unix(), s.files.Load(), s.bytes.Load(), s.hashed.Load(), s.skipped.Load(), msg, scanID)
	}()

	for _, root := range s.Cfg.RootPaths {
		if err := s.walkRoot(ctx, scanID, root); err != nil {
			return err
		}
	}
	s.phase.Store("projects")
	if err := s.markProjects(); err != nil {
		return err
	}
	if err := s.hashCandidates(ctx); err != nil {
		return err
	}
	_, err = s.DB.Exec(`PRAGMA optimize`)
	return err
}

func (s *Scanner) walkRoot(ctx context.Context, scanID int64, root string) error {
	out := make(chan entry, 4096)
	writeErr := make(chan error, 1)
	go func() { writeErr <- s.write(scanID, out) }()

	conf := fastwalk.Config{Follow: false}
	walkErr := fastwalk.Walk(&conf, root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		parent := filepath.Dir(path)
		if err != nil {
			// Unreadable entry: count it as content so its folder never looks empty.
			s.skipped.Add(1)
			if path != root {
				out <- entry{path: path, root: root, parent: parent, dir: true, opaque: true}
			}
			if d != nil && d.IsDir() {
				return fastwalk.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				out <- entry{path: path, root: root, parent: parent, dir: true}
				return nil
			}
			if parent != root && fsutil.IsProjectMarker(name, true) {
				out <- entry{path: parent, root: root, project: true}
			}
			info, ierr := d.Info()
			if s.Cfg.IgnoreDir(name) || s.Cfg.IsProtected(path) || ierr != nil || platform.MetaOf(path, info).Reparse {
				out <- entry{path: path, root: root, parent: parent, dir: true, opaque: true}
				return fastwalk.SkipDir
			}
			s.dirs.Add(1)
			out <- entry{path: path, root: root, parent: parent, dir: true}
			return nil
		}
		// Markers loose in a root (a downloaded package.json) don't make the
		// whole root a project.
		if parent != root && fsutil.IsProjectMarker(name, false) {
			out <- entry{path: parent, root: root, project: true}
		}
		if !d.Type().IsRegular() && d.Type()&fs.ModeIrregular == 0 {
			// Symlinks, sockets, devices: never followed, never touched.
			out <- entry{path: path, root: root, parent: parent, dir: true, opaque: true}
			return nil
		}
		if fsutil.IsIgnorableFile(name) {
			return nil
		}
		if s.Cfg.IsProtectedName(name) {
			// Never indexed, so never sorted or deleted; still counts as content.
			out <- entry{path: path, root: root, parent: parent, dir: true, opaque: true}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			s.skipped.Add(1)
			out <- entry{path: path, root: root, parent: parent, dir: true, opaque: true}
			return nil
		}
		m := platform.MetaOf(path, info)
		if d.Type()&fs.ModeIrregular != 0 {
			m.Placeholder = true
		}
		s.files.Add(1)
		s.bytes.Add(info.Size())
		out <- entry{path: path, root: root, parent: parent, name: name, size: info.Size(),
			mtime: info.ModTime().UnixNano(), btime: m.Birth, placeholder: m.Placeholder}
		return nil
	})
	close(out)
	if err := <-writeErr; err != nil {
		return err
	}
	if walkErr != nil {
		return walkErr
	}
	// Forget anything under this root that the walk didn't see.
	if _, err := s.DB.Exec(`DELETE FROM files WHERE root=? AND scan<?`, root, scanID); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM dirs WHERE root=? AND scan<?`, root, scanID)
	return err
}

const upsertFile = `INSERT INTO files(path,root,parent,name,ext,size,mtime,btime,placeholder,in_project,scan)
VALUES(?,?,?,?,?,?,?,?,?,0,?)
ON CONFLICT(path) DO UPDATE SET
  root=excluded.root, parent=excluded.parent, name=excluded.name, ext=excluded.ext,
  prehash=CASE WHEN files.size=excluded.size AND files.mtime=excluded.mtime THEN files.prehash END,
  fullhash=CASE WHEN files.size=excluded.size AND files.mtime=excluded.mtime THEN files.fullhash END,
  size=excluded.size, mtime=excluded.mtime, btime=excluded.btime,
  placeholder=excluded.placeholder, in_project=0, scan=excluded.scan`

const upsertDir = `INSERT INTO dirs(path,root,parent,opaque,project,in_project,scan) VALUES(?,?,?,?,0,0,?)
ON CONFLICT(path) DO UPDATE SET root=excluded.root, parent=excluded.parent, opaque=excluded.opaque,
  project=0, in_project=0, scan=excluded.scan`

// write is the single DB writer: it batches walker entries into transactions.
func (s *Scanner) write(scanID int64, in <-chan entry) error {
	var projects []entry
	var tx *sql.Tx
	var fileStmt, dirStmt *sql.Stmt
	n := 0
	var firstErr error
	commit := func() {
		if tx == nil {
			return
		}
		if err := tx.Commit(); err != nil && firstErr == nil {
			firstErr = err
		}
		tx, n = nil, 0
	}
	for e := range in {
		if firstErr != nil {
			continue // drain so the walker never blocks
		}
		if e.project {
			projects = append(projects, e)
			continue
		}
		if tx == nil {
			var err error
			if tx, err = s.DB.Begin(); err != nil {
				firstErr = err
				continue
			}
			if fileStmt, err = tx.Prepare(upsertFile); err == nil {
				dirStmt, err = tx.Prepare(upsertDir)
			}
			if err != nil {
				firstErr = err
				continue
			}
		}
		var err error
		if e.dir {
			_, err = dirStmt.Exec(e.path, e.root, e.parent, e.opaque, scanID)
		} else {
			_, err = fileStmt.Exec(e.path, e.root, e.parent, e.name, strings.ToLower(filepath.Ext(e.name)),
				e.size, e.mtime, e.btime, e.placeholder, scanID)
		}
		if err != nil {
			firstErr = err
			continue
		}
		if n++; n >= batchSize {
			commit()
		}
	}
	commit()
	if firstErr != nil {
		return firstErr
	}
	for _, p := range projects {
		if _, err := s.DB.Exec(`UPDATE dirs SET project=1 WHERE path=?`, p.path); err != nil {
			return err
		}
	}
	return nil
}

// markProjects flags every file and folder inside a project folder.
func (s *Scanner) markProjects() error {
	rows, err := s.DB.Query(`SELECT path FROM dirs WHERE project=1`)
	if err != nil {
		return err
	}
	var projects []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, p)
	}
	rows.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range projects {
		lo, hi := fsutil.PrefixRange(p)
		if _, err := tx.Exec(`UPDATE files SET in_project=1 WHERE path>=? AND path<?`, lo, hi); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE dirs SET in_project=1 WHERE path=? OR (path>=? AND path<?)`, p, lo, hi); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type hashJob struct {
	path        string
	size, mtime int64
}

// candidateWhere limits hashing to files the analyzers may act on.
const candidateWhere = `placeholder=0 AND in_project=0 AND size>=?`

func (s *Scanner) hashCandidates(ctx context.Context) error {
	minSize := s.Cfg.MinDuplicateSize
	if minSize < 1 {
		minSize = 1
	}
	// Stage 1: sampled prehash for files sharing a size.
	s.phase.Store("prehash")
	jobs, err := s.loadJobs(`SELECT path,size,mtime FROM files WHERE `+candidateWhere+` AND prehash IS NULL
		AND size IN (SELECT size FROM files WHERE `+candidateWhere+` GROUP BY size HAVING COUNT(*)>1)`, minSize, minSize)
	if err != nil {
		return err
	}
	if err := s.runHash(ctx, jobs, true); err != nil {
		return err
	}
	// Stage 2: full BLAKE3 for files sharing size and prehash.
	s.phase.Store("hashing")
	jobs, err = s.loadJobs(`SELECT f.path,f.size,f.mtime FROM files f JOIN (
		SELECT size, prehash FROM files WHERE `+candidateWhere+` AND prehash IS NOT NULL
		GROUP BY size, prehash HAVING COUNT(*)>1) g ON f.size=g.size AND f.prehash=g.prehash
		WHERE f.fullhash IS NULL AND f.placeholder=0 AND f.in_project=0`, minSize)
	if err != nil {
		return err
	}
	return s.runHash(ctx, jobs, false)
}

func (s *Scanner) loadJobs(q string, args ...any) ([]hashJob, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []hashJob
	for rows.Next() {
		var j hashJob
		if err := rows.Scan(&j.path, &j.size, &j.mtime); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

type hashResult struct {
	job       hashJob
	pre, full []byte
}

func (s *Scanner) runHash(ctx context.Context, jobs []hashJob, pre bool) error {
	s.toHash.Store(int64(len(jobs)))
	s.hashed.Store(0)
	if len(jobs) == 0 {
		return nil
	}
	in := make(chan hashJob)
	out := make(chan hashResult, 256)
	var wg sync.WaitGroup
	for range s.Cfg.IOWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range in {
				var r hashResult
				r.job = j
				var err error
				if pre {
					r.pre, r.full, err = hashing.Prehash(j.path, j.size)
				} else {
					r.full, err = hashing.Full(ctx, j.path)
				}
				if err != nil {
					s.skipped.Add(1) // locked or vanished; next scan retries
					continue
				}
				out <- r
			}
		}()
	}
	go func() {
		defer close(in)
		for _, j := range jobs {
			select {
			case in <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(out) }()

	var tx *sql.Tx
	n := 0
	var firstErr error
	for r := range out {
		s.hashed.Add(1)
		if firstErr != nil {
			continue
		}
		if tx == nil {
			var err error
			if tx, err = s.DB.Begin(); err != nil {
				firstErr = err
				continue
			}
		}
		// The size/mtime guard drops results for files that changed mid-scan.
		var err error
		if pre {
			_, err = tx.Exec(`UPDATE files SET prehash=?, fullhash=COALESCE(?, fullhash) WHERE path=? AND size=? AND mtime=?`,
				r.pre, r.full, r.job.path, r.job.size, r.job.mtime)
		} else {
			_, err = tx.Exec(`UPDATE files SET fullhash=? WHERE path=? AND size=? AND mtime=?`,
				r.full, r.job.path, r.job.size, r.job.mtime)
		}
		if err != nil {
			firstErr = err
			continue
		}
		if n++; n >= 500 {
			firstErr = tx.Commit()
			tx, n = nil, 0
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		return ctx.Err()
	}
	return firstErr
}
