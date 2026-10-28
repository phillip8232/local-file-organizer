// Package index owns the SQLite database: the file index and hash cache, and
// the plans, batches and journal the executor writes.
package index

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the organizer's SQLite database.
type DB struct{ *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS files(
  path TEXT PRIMARY KEY,
  root TEXT NOT NULL,
  parent TEXT NOT NULL,
  name TEXT NOT NULL,
  ext TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime INTEGER NOT NULL,
  btime INTEGER NOT NULL,
  placeholder INTEGER NOT NULL DEFAULT 0,
  in_project INTEGER NOT NULL DEFAULT 0,
  prehash BLOB,
  fullhash BLOB,
  scan INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS files_size ON files(size);
CREATE INDEX IF NOT EXISTS files_parent ON files(parent);
CREATE INDEX IF NOT EXISTS files_root_scan ON files(root, scan);
CREATE INDEX IF NOT EXISTS files_fullhash ON files(fullhash) WHERE fullhash IS NOT NULL;

-- Folders seen by the walker. opaque=1 rows are things the walker skipped
-- (ignored folders, symlinks, unreadable entries): they make their parent
-- count as not empty.
CREATE TABLE IF NOT EXISTS dirs(
  path TEXT PRIMARY KEY,
  root TEXT NOT NULL,
  parent TEXT NOT NULL,
  opaque INTEGER NOT NULL DEFAULT 0,
  project INTEGER NOT NULL DEFAULT 0,
  in_project INTEGER NOT NULL DEFAULT 0,
  scan INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS dirs_root_scan ON dirs(root, scan);

CREATE TABLE IF NOT EXISTS scans(
  id INTEGER PRIMARY KEY,
  started INTEGER NOT NULL,
  finished INTEGER,
  roots TEXT NOT NULL,
  files INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  hashed INTEGER NOT NULL DEFAULT 0,
  skipped INTEGER NOT NULL DEFAULT 0,
  error TEXT
);

CREATE TABLE IF NOT EXISTS plans(
  id INTEGER PRIMARY KEY,
  created INTEGER NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS batches(
  id INTEGER PRIMARY KEY,
  created INTEGER NOT NULL,
  plan_id INTEGER,
  summary TEXT NOT NULL,
  state TEXT NOT NULL
);

-- Write-ahead journal: a row is written as 'pending' before the filesystem
-- change and marked 'done' after, so an interrupted batch can be recovered
-- and every finished step can be undone.
CREATE TABLE IF NOT EXISTS journal(
  id INTEGER PRIMARY KEY,
  batch INTEGER NOT NULL,
  op TEXT NOT NULL,          -- hold | move | mkdir | trash
  category TEXT NOT NULL DEFAULT '',
  src TEXT NOT NULL,
  dst TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  isdir INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,       -- pending | done | failed | undone | purged | erased
  error TEXT NOT NULL DEFAULT '',
  ts INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS journal_batch ON journal(batch);
CREATE INDEX IF NOT EXISTS journal_state ON journal(state);
`

// Open opens (creating if needed) the database at path.
func Open(path string) (*DB, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &DB{db}, nil
}

// Stats is a quick summary of the index for status displays.
type Stats struct {
	Files        int64 `json:"files"`
	Bytes        int64 `json:"bytes"`
	Placeholders int64 `json:"placeholders"`
	InProjects   int64 `json:"inProjects"`
	LastScan     int64 `json:"lastScan"` // unix seconds, 0 if never
	LastScanID   int64 `json:"lastScanId"`
}

func (db *DB) Stats() (Stats, error) {
	var s Stats
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0), COALESCE(SUM(placeholder),0), COALESCE(SUM(in_project),0) FROM files`).
		Scan(&s.Files, &s.Bytes, &s.Placeholders, &s.InProjects)
	if err != nil {
		return s, err
	}
	_ = db.QueryRow(`SELECT id, COALESCE(finished,0) FROM scans WHERE finished IS NOT NULL ORDER BY id DESC LIMIT 1`).
		Scan(&s.LastScanID, &s.LastScan)
	return s, nil
}
