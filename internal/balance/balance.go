// Package balance plans moves that even out how full each drive is. It
// moves whole folders (or single large files) that haven't been touched in a
// while, so things stay easy to find and nothing in use is disturbed.
package balance

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/fsutil"
	"github.com/phillip8232/local-file-organizer/internal/plan"
	"github.com/phillip8232/local-file-organizer/internal/platform"
)

// Drive is a volume with its balance target and the plan's projected use.
type Drive struct {
	platform.Volume
	Target  int64 `json:"target"`  // bytes used at an equal share
	Planned int64 `json:"planned"` // bytes used if the whole plan is applied
}

// Result is a balance plan plus what it would do to each drive.
type Result struct {
	Plan    *plan.Plan `json:"plan"`
	Drives  []Drive    `json:"drives"`
	Note    string     `json:"note,omitempty"`
	Skipped []string   `json:"skipped,omitempty"` // big folders left alone, with why
}

type candidate struct {
	path   string
	isDir  bool
	size   int64
	count  int64
	mtime  int64 // files: modification time; folders: newest inside
	vol    int
	warm   bool // used recently: offered, not selected
	reason string
}

// maxDepth limits how far into a big folder the planner looks for smaller
// pieces to move.
const maxDepth = 3

// Build plans moves towards an equal percentage used on every drive.
func Build(cfg *config.Config, vols []platform.Volume, now time.Time) *Result {
	res := &Result{Plan: &plan.Plan{Kind: "balance", Created: now.Unix()}}
	vols = pickDrives(cfg, vols)
	for _, v := range vols {
		res.Drives = append(res.Drives, Drive{Volume: v, Planned: v.Used()})
	}
	if len(vols) < 2 {
		res.Note = "Only one drive found, so there's nothing to balance."
		return res
	}
	var total, used int64
	for _, v := range vols {
		total += v.Total
		used += v.Used()
	}
	for i := range res.Drives {
		res.Drives[i].Target = int64(float64(used) / float64(total) * float64(res.Drives[i].Total))
	}
	tolerance := total / 100 // within 1% of the whole is balanced
	excess := func(i int) int64 { return res.Drives[i].Planned - res.Drives[i].Target }

	minSize := cfg.BalanceMinMB << 20
	cutoff := now.AddDate(0, 0, -cfg.BalanceColdDays).UnixNano()
	var cands []candidate
	for _, src := range cfg.BalancePaths {
		vi := volumeOf(src, vols)
		if vi < 0 || excess(vi) <= tolerance {
			continue
		}
		cands = append(cands, gather(cfg, src, vi, excess(vi), minSize, cutoff, 1, true, res)...)
	}
	// Unused before recently used, then biggest first: fewer, larger moves.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].warm != cands[j].warm {
			return !cands[i].warm
		}
		return cands[i].size > cands[j].size
	})

	// Recently used offers don't count towards the projection until ticked,
	// so track what they'd add separately to keep each offer sensible.
	offered := make([]int64, len(res.Drives))
	for _, c := range cands {
		s := c.vol
		if excess(s)+offered[s] <= tolerance {
			continue
		}
		// Don't overshoot the source's target by more than the tolerance.
		if c.size > excess(s)+offered[s]+tolerance {
			continue
		}
		best, room := -1, int64(0)
		for d := range res.Drives {
			if d == s {
				continue
			}
			r := res.Drives[d].Target - res.Drives[d].Planned - offered[d]
			free := res.Drives[d].Total - res.Drives[d].Planned - offered[d]
			if r+tolerance >= c.size && free-c.size > 1<<30 && r > room {
				best, room = d, r
			}
		}
		if best < 0 {
			continue
		}
		dst := mirror(c.path, res.Drives[s].Root, res.Drives[best].Root)
		if c.warm {
			offered[s] -= c.size
			offered[best] += c.size
		} else {
			res.Drives[s].Planned -= c.size
			res.Drives[best].Planned += c.size
		}
		a := plan.Action{
			ID: len(res.Plan.Actions) + 1, Op: plan.OpMove, Category: "balance",
			Src: c.path, Dst: dst, Size: c.size, Count: c.count, MTime: c.mtime, IsDir: c.isDir,
			FromVol: res.Drives[s].Root, ToVol: res.Drives[best].Root, Selected: !c.warm,
			Reason: "last changed " + time.Unix(0, c.mtime).Format("2 Jan 2006") + c.reason,
		}
		if !c.isDir {
			a.Count = 1
		}
		res.Plan.Actions = append(res.Plan.Actions, a)
	}
	balanced := true
	for i := range res.Drives {
		if excess(i) > tolerance {
			balanced = false
		}
	}
	switch {
	case len(res.Plan.Actions) == 0 && balanced:
		res.Note = "Your drives are already balanced."
	case !balanced:
		res.Note = "There isn't enough movable, unused data to balance the drives fully. Programs, app data, projects and recently used files are never moved."
	}
	return res
}

func pickDrives(cfg *config.Config, vols []platform.Volume) []platform.Volume {
	if len(cfg.BalanceDrives) == 0 {
		return vols
	}
	var out []platform.Volume
	for _, v := range vols {
		for _, want := range cfg.BalanceDrives {
			if fsutil.SamePath(strings.TrimRight(v.Root, `\/`), strings.TrimRight(want, `\/`)) {
				out = append(out, v)
			}
		}
	}
	return out
}

// volumeOf returns the index of the volume holding path (longest root
// match), or -1.
func volumeOf(path string, vols []platform.Volume) int {
	best, bestLen := -1, -1
	for i, v := range vols {
		if fsutil.Within(path, v.Root) && len(v.Root) > bestLen {
			best, bestLen = i, len(v.Root)
		}
	}
	return best
}

// mirror maps a path to the same place on another drive: C:\Users\Me\Videos
// becomes D:\Users\Me\Videos.
func mirror(path, fromRoot, toRoot string) string {
	rel, err := filepath.Rel(fromRoot, path)
	if err != nil {
		rel = strings.TrimPrefix(path, filepath.VolumeName(path))
	}
	return filepath.Join(toRoot, rel)
}

var oneDrive = func() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("OneDrive")
	}
	return ""
}()

// gather lists movable pieces of dir. A folder that's too big is opened up
// to find smaller pieces. A folder that's partly in use or holds a project
// is opened up only to its subfolders: its loose files stay together, since
// apps working on that folder may refer to them by path.
func gather(cfg *config.Config, dir string, vol int, need, minSize, cutoff int64, depth int, filesToo bool, res *Result) []candidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []candidate
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if cfg.IsProtected(p) || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		note := ""
		if oneDrive != "" && fsutil.Within(p, oneDrive) {
			note = "; it's in your OneDrive folder, so it won't sync from the new drive"
		}
		if !e.IsDir() {
			if !filesToo || !info.Mode().IsRegular() || info.Size() < minSize || info.ModTime().UnixNano() > cutoff ||
				fsutil.IsIgnorableFile(e.Name()) || platform.MetaOf(p, info).Placeholder {
				continue
			}
			out = append(out, candidate{path: p, size: info.Size(), mtime: info.ModTime().UnixNano(), vol: vol, reason: note})
			continue
		}
		if cfg.IgnoreDir(e.Name()) || fsutil.IsProjectMarker(e.Name(), true) || platform.MetaOf(p, info).Reparse {
			continue
		}
		if isProject(p) {
			// Never split a project apart; it stays where its tools expect it.
			continue
		}
		st := fsutil.StatDir(p)
		if st.Size < minSize {
			continue
		}
		splittable := depth < maxDepth
		switch {
		case st.Unsafe != "":
			if len(res.Skipped) < 50 && st.Size >= 1<<30 {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %s, %s", p, gb(st.Size), st.Unsafe))
			}
			if splittable {
				out = append(out, gather(cfg, p, vol, need, minSize, cutoff, depth+1, false, res)...)
			}
		case st.Size > need+need/2 && splittable && hasSubdirs(p):
			out = append(out, gather(cfg, p, vol, need, minSize, cutoff, depth+1, true, res)...)
		case st.Newest > cutoff:
			// Offered whole but unticked: you decide whether it's safe to move.
			out = append(out, candidate{path: p, isDir: true, size: st.Size, count: st.Count, mtime: st.Newest, vol: vol, warm: true,
				reason: note + "; used in the last " + fmt.Sprint(cfg.BalanceColdDays) + " days, so it's not ticked"})
		default:
			out = append(out, candidate{path: p, isDir: true, size: st.Size, count: st.Count, mtime: st.Newest, vol: vol, reason: note})
		}
	}
	return out
}

func gb(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/(1<<30)) }

func isProject(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true // can't tell: leave it alone
	}
	for _, e := range entries {
		if fsutil.IsProjectMarker(e.Name(), e.IsDir()) {
			return true
		}
	}
	return false
}

func hasSubdirs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
}
