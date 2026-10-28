package trash

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMove sends a real temp file to the OS trash, so it only runs when
// ORGANIZER_TEST_TRASH=1.
func TestMove(t *testing.T) {
	if os.Getenv("ORGANIZER_TEST_TRASH") != "1" {
		t.Skip("set ORGANIZER_TEST_TRASH=1 to put a test file in the OS trash")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "organizer-trash-test.txt")
	if err := os.WriteFile(p, []byte("safe to delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Move(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("file still exists")
	}
	sub := filepath.Join(dir, "organizer-trash-test-folder")
	if err := os.MkdirAll(filepath.Join(sub, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Move(sub); err != nil {
		t.Fatal(err)
	}
	if err := Move(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("trashing a missing file should fail")
	}
}
