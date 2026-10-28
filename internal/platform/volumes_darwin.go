package platform

import (
	"os"
	"path/filepath"
	"syscall"
)

// Volumes lists the startup disk and other mounted volumes in /Volumes.
func Volumes() ([]Volume, error) {
	var out []Volume
	root, ok := statVolume("/")
	if ok {
		root.Label = "Macintosh HD"
		out = append(out, root)
	}
	rootDev := devOf("/")
	entries, _ := os.ReadDir("/Volumes")
	for _, e := range entries {
		p := filepath.Join("/Volumes", e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || devOf(p) == rootDev {
			continue
		}
		if v, ok := statVolume(p); ok {
			v.Label = e.Name()
			out = append(out, v)
		}
	}
	return out, nil
}

func devOf(p string) int32 {
	fi, err := os.Lstat(p)
	if err != nil {
		return -1
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Dev
	}
	return -1
}
