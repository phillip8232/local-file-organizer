package platform

import (
	"io/fs"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// MetaOf can't get a birth time from a plain stat on Linux; analyzers that
// need one call BirthTime for the few files they care about.
func MetaOf(string, fs.FileInfo) Meta { return Meta{} }

// BirthTime asks statx for the creation time; 0 if the filesystem lacks it.
func BirthTime(path string) int64 {
	var st unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &st) != nil {
		return 0
	}
	if st.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
}

func OpenBrowser(url string) error { return exec.Command("xdg-open", url).Start() }

func Reveal(path string) error { return exec.Command("xdg-open", filepath.Dir(path)).Start() }
