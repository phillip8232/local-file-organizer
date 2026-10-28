package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillip8232/local-file-organizer/internal/fsutil"
	"github.com/phillip8232/local-file-organizer/internal/plan"
)

// The test machine has one drive, so ForceCopy makes every move take the
// copy-verify-remove path used between drives.
func TestCrossDriveFolderMoveAndUndo(t *testing.T) {
	e := setup(t)
	videos := filepath.Join(e.root, "..", "Videos")
	write(t, filepath.Join(videos, "Trip", "day1.mp4"), "day one footage")
	write(t, filepath.Join(videos, "Trip", "raw", "clip.mov"), "raw clip")
	ro := filepath.Join(videos, "Trip", "notes.txt")
	write(t, ro, "read-only notes")
	if err := os.Chmod(ro, 0o444); err != nil {
		t.Fatal(err)
	}
	e.cfg.BalancePaths = []string{videos}
	e.ex.ForceCopy = true

	src := filepath.Join(videos, "Trip")
	dst := filepath.Join(e.root, "..", "OtherDrive", "Videos", "Trip")
	st := fsutil.StatDir(src)
	a := plan.Action{ID: 1, Op: plan.OpMove, Category: "balance", Src: src, Dst: dst, Size: st.Size, Count: st.Count, IsDir: true}

	res, err := e.ex.Apply(context.Background(), 0, "balance", []plan.Action{a})
	if err != nil || res.Done != 1 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if exists(src) {
		t.Error("original folder should be removed after a verified copy")
	}
	if b, err := os.ReadFile(filepath.Join(dst, "raw", "clip.mov")); err != nil || string(b) != "raw clip" {
		t.Errorf("copied content wrong: %q %v", b, err)
	}
	if exists(filepath.Join(filepath.Dir(dst), ".organizer-partial-Trip")) {
		t.Error("partial copy left behind")
	}

	u, err := e.ex.Undo(res.Batch)
	if err != nil || u.Done != 1 {
		t.Fatalf("undo: %v %+v", err, u)
	}
	if !exists(filepath.Join(src, "raw", "clip.mov")) || exists(dst) {
		t.Error("undo should copy the folder back and remove the moved copy")
	}
}

func TestCrossDriveSkipsChangedFolder(t *testing.T) {
	e := setup(t)
	videos := filepath.Join(e.root, "..", "Videos")
	write(t, filepath.Join(videos, "Trip", "a.mp4"), "aaa")
	e.cfg.BalancePaths = []string{videos}
	e.ex.ForceCopy = true
	src := filepath.Join(videos, "Trip")
	st := fsutil.StatDir(src)
	// A file added after planning: the move must be skipped, nothing removed.
	write(t, filepath.Join(src, "new.mp4"), "new")
	a := plan.Action{ID: 1, Op: plan.OpMove, Category: "balance", Src: src, Dst: filepath.Join(e.root, "..", "D", "Trip"),
		Size: st.Size, Count: st.Count, IsDir: true}
	res, _ := e.ex.Apply(context.Background(), 0, "balance", []plan.Action{a})
	if res.Done != 0 || res.Skipped != 1 || !exists(filepath.Join(src, "new.mp4")) {
		t.Fatalf("changed folder should be skipped untouched: %+v", res)
	}
	// A project folder is never moved.
	write(t, filepath.Join(videos, "Edit", "film.prproj"), "project")
	st = fsutil.StatDir(filepath.Join(videos, "Edit"))
	a = plan.Action{ID: 2, Op: plan.OpMove, Category: "balance", Src: filepath.Join(videos, "Edit"), Dst: filepath.Join(e.root, "..", "D", "Edit"),
		Size: st.Size, Count: st.Count, IsDir: true}
	res, _ = e.ex.Apply(context.Background(), 0, "balance", []plan.Action{a})
	if res.Done != 0 {
		t.Fatalf("project folder was moved: %+v", res)
	}
}
