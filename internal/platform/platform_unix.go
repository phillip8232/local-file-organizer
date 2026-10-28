//go:build darwin || linux

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// FileID returns device+inode and the hard link count.
func FileID(path string) (string, uint64, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, fmt.Errorf("no stat data for %s", path)
	}
	return fmt.Sprintf("%x:%x", uint64(st.Dev), uint64(st.Ino)), uint64(st.Nlink), nil
}

// Background lowers CPU priority. Raising it back needs privileges on most
// systems, so the returned func does nothing.
func Background() func() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10)
	return func() {}
}

func isCrossDeviceErrno(err error) bool { return errors.Is(err, syscall.EXDEV) }

// VolumeRoot returns the mount point holding path: the highest ancestor on
// the same device.
func VolumeRoot(path string) string {
	dev := func(p string) (uint64, bool) {
		fi, err := os.Lstat(p)
		if err != nil {
			return 0, false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		return uint64(st.Dev), ok
	}
	want, ok := dev(path)
	dir := filepath.Dir(path)
	if !ok {
		return dir
	}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		if d, ok := dev(parent); !ok || d != want {
			return dir
		}
		dir = parent
	}
}
