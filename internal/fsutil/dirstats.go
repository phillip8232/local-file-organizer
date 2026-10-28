package fsutil

import (
	"io/fs"
	"path/filepath"

	"github.com/phillip8232/local-file-organizer/internal/platform"
)

// DirStats summarises a folder before it is moved as a whole.
type DirStats struct {
	Size   int64
	Count  int64
	Newest int64  // latest file modification time inside, unix ns
	Unsafe string // why the folder must not be moved, or ""
}

// StatDir walks dir once. A folder holding a project, a link, a cloud
// placeholder or anything unreadable is marked unsafe to move.
func StatDir(dir string) DirStats {
	var st DirStats
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			st.Unsafe = "has unreadable items"
			return fs.SkipAll
		}
		if p != dir && IsProjectMarker(d.Name(), d.IsDir()) {
			st.Unsafe = "contains a project"
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			st.Unsafe = "has unreadable items"
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 || d.Type()&fs.ModeIrregular != 0 {
			st.Unsafe = "contains links or special files"
			return fs.SkipAll
		}
		m := platform.MetaOf(p, info)
		if m.Placeholder || (d.IsDir() && m.Reparse && p != dir) {
			st.Unsafe = "contains online-only cloud files"
			return fs.SkipAll
		}
		// Only file dates count as "use": folder dates change whenever the
		// OS writes thumbnails or settings into them.
		if !d.IsDir() {
			if t := info.ModTime().UnixNano(); t > st.Newest {
				st.Newest = t
			}
			st.Size += info.Size()
			st.Count++
		}
		return nil
	})
	return st
}
