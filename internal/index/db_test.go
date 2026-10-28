package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A damaged database is moved aside and rebuilt, keeping the undo journal.
func TestOpenRecoversDamagedDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "organizer.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO journal(batch, op, src, dst, state, ts) VALUES(1,'hold','a','b','done',1)`); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	for i := 0; i < 3000; i++ {
		_, _ = tx.Exec(`INSERT INTO files(path,root,parent,name,ext,size,mtime,btime,scan) VALUES(?,?,?,?,?,?,?,?,?)`,
			filepath.Join("r", strings.Repeat("x", 40), string(rune('a'+i%26)), string(rune(i))), "r", "p", "n", ".x", i, i, 0, 1)
	}
	_ = tx.Commit()
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	db.Close()

	// Scribble over the middle of the file, where the files table lives.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	garbage := make([]byte, 64<<10)
	for i := range garbage {
		garbage[i] = byte(i * 7)
	}
	_, _ = f.WriteAt(garbage, fi.Size()/2)
	f.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open after damage: %v", err)
	}
	defer db.Close()
	if db.Recovered == "" {
		t.Fatal("damage wasn't detected")
	}
	matches, _ := filepath.Glob(path + ".damaged-*")
	if len(matches) == 0 {
		t.Error("damaged file wasn't kept aside")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal`).Scan(&n); err != nil || n != 1 {
		t.Errorf("journal rows after recovery = %d (%v), want 1", n, err)
	}
	t.Log(db.Recovered)
}
