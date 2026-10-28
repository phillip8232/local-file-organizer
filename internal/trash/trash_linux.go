// Package trash moves a file or folder to the OS trash: the Recycle Bin on
// Windows, the Finder trash on macOS and the FreeDesktop trash on Linux.
package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Move implements the FreeDesktop.org Trash spec 1.0: the home trash for
// files on the home volume, otherwise $topdir/.Trash/$uid or
// $topdir/.Trash-$uid on the file's own volume.
func Move(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	dev := devOf(st)
	home, _ := os.UserHomeDir()
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	homeTrash := filepath.Join(dataHome, "Trash")
	if err := os.MkdirAll(homeTrash, 0o700); err == nil {
		if hs, err := os.Stat(homeTrash); err == nil && devOf(hs) == dev {
			return moveInto(homeTrash, abs, abs)
		}
	}
	top := mountPoint(abs, dev)
	rel, err := filepath.Rel(top, abs)
	if err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	shared := filepath.Join(top, ".Trash")
	if fi, err := os.Lstat(shared); err == nil && fi.IsDir() && fi.Mode()&os.ModeSticky != 0 {
		dir := filepath.Join(shared, uid)
		if os.MkdirAll(dir, 0o700) == nil {
			return moveInto(dir, abs, rel)
		}
	}
	dir := filepath.Join(top, ".Trash-"+uid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("trash %s: no usable trash on this volume: %w", abs, err)
	}
	return moveInto(dir, abs, rel)
}

func moveInto(trashDir, abs, recorded string) error {
	files, info := filepath.Join(trashDir, "files"), filepath.Join(trashDir, "info")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	base := filepath.Base(abs)
	for i := 0; i < 10000; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		infoPath := filepath.Join(info, name+".trashinfo")
		f, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", escapePath(recorded), time.Now().Format("2006-01-02T15:04:05"))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(infoPath)
			return err
		}
		if err := os.Rename(abs, filepath.Join(files, name)); err != nil {
			os.Remove(infoPath)
			return fmt.Errorf("trash %s: %w", abs, err)
		}
		return nil
	}
	return fmt.Errorf("trash %s: too many items with this name", abs)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func devOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

// mountPoint walks up from path to the highest ancestor on the same device.
func mountPoint(path string, dev uint64) string {
	dir := filepath.Dir(path)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		fi, err := os.Stat(parent)
		if err != nil || devOf(fi) != dev {
			return dir
		}
		dir = parent
	}
}
