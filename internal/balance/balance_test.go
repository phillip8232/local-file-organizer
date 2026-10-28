package balance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/platform"
)

func mk(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestBuildMovesColdFoldersOnly(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "A"), filepath.Join(base, "B")
	videos := filepath.Join(a, "Videos")
	old := time.Now().AddDate(0, -6, 0)
	mk(t, filepath.Join(videos, "Holiday", "1.mp4"), "holiday video", old)
	mk(t, filepath.Join(videos, "Editing", "cut.mp4"), "in progress", time.Now())
	mk(t, filepath.Join(videos, "Film", "film.prproj"), "project", old)
	mk(t, filepath.Join(videos, "Film", "take.mp4"), "take", old)
	mk(t, filepath.Join(b, "keep.txt"), "x", old)

	cfg := &config.Config{BalancePaths: []string{videos}, BalanceColdDays: 30, BalanceMinMB: 0}
	const gig = int64(1) << 30
	vols := []platform.Volume{
		{Root: a, Total: 100 * gig, Free: 10 * gig},
		{Root: b, Total: 100 * gig, Free: 100 * gig},
	}
	r := Build(cfg, vols, time.Now())
	got := map[string]bool{}
	for _, act := range r.Plan.Actions {
		got[filepath.Base(act.Src)] = act.Selected
		if filepath.Base(act.Src) == "Holiday" && act.Dst != filepath.Join(b, "Videos", "Holiday") {
			t.Errorf("Holiday → %s, want the same path on drive B", act.Dst)
		}
	}
	if sel, ok := got["Holiday"]; !ok || !sel {
		t.Errorf("unused folder should be planned and ticked: %v", got)
	}
	if sel, ok := got["Editing"]; !ok || sel {
		t.Errorf("recently used folder should be offered unticked: %v", got)
	}
	if _, ok := got["Film"]; ok {
		t.Error("a project folder must never be planned")
	}
	if r.Drives[0].Target != r.Drives[1].Target {
		t.Errorf("equal-size drives should get equal targets: %+v", r.Drives)
	}
}

func TestBuildSingleDrive(t *testing.T) {
	r := Build(&config.Config{}, []platform.Volume{{Root: "C:\\", Total: 10, Free: 5}}, time.Now())
	if len(r.Plan.Actions) != 0 || r.Note == "" {
		t.Errorf("one drive: want no actions and a note, got %+v", r)
	}
}
