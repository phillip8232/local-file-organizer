//go:build darwin || linux

package platform

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func statVolume(root string) (Volume, bool) {
	var st unix.Statfs_t
	if unix.Statfs(root, &st) != nil {
		return Volume{}, false
	}
	bs := int64(st.Bsize)
	v := Volume{Root: root, Total: int64(st.Blocks) * bs, Free: int64(st.Bavail) * bs, System: root == "/"}
	return v, v.Total > 0
}

// FreeSpace returns the bytes available to this user on path's volume.
func FreeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// CopyTimes gives dst the modification time of src.
func CopyTimes(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	return os.Chtimes(dst, time.Time{}, fi.ModTime())
}

// MakeWritable adds owner write permission so a file can be removed.
func MakeWritable(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return os.Chmod(path, fi.Mode().Perm()|0o200)
}
