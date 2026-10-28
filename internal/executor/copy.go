package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zeebo/blake3"

	"github.com/phillip8232/local-file-organizer/internal/fsutil"
	"github.com/phillip8232/local-file-organizer/internal/hashing"
	"github.com/phillip8232/local-file-organizer/internal/platform"
)

// spaceMargin is left free on a destination drive after a copy.
const spaceMargin = 1 << 30

const partialPrefix = ".organizer-partial-"

// partialPath is where a copy is built before it's verified and renamed
// into place, so a half-finished copy never looks like the real thing.
func partialPath(dst string) string {
	return filepath.Join(filepath.Dir(dst), partialPrefix+filepath.Base(dst))
}

// copyVerified copies a file or folder to dst. Every file is hashed while
// it's read and again after it's written; dst appears only once all of it
// matches and the source hasn't changed in the meantime.
func copyVerified(ctx context.Context, src, dst string, onBytes func(int64)) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	tmp := partialPath(dst)
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	var before fsutil.DirStats
	if fi.IsDir() {
		before = fsutil.StatDir(src)
		if before.Unsafe != "" {
			return errors.New("folder " + before.Unsafe)
		}
		err = copyTree(ctx, src, tmp, onBytes)
	} else {
		err = copyFile(ctx, src, tmp, fi, onBytes)
	}
	if err == nil && fi.IsDir() {
		// A file added or changed while copying would otherwise be lost
		// when the original folder is removed.
		if after := fsutil.StatDir(src); after.Size != before.Size || after.Count != before.Count || after.Newest != before.Newest {
			err = errors.New("the folder changed while it was being copied; nothing was removed")
		}
	}
	if err == nil && exists(dst) {
		err = errors.New("something appeared at the destination during the copy")
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = removeAll(tmp)
	}
	return err
}

func copyTree(ctx context.Context, src, dst string, onBytes func(int64)) error {
	var dirs []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, p)
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(ctx, p, target, info, onBytes)
		default:
			return fmt.Errorf("%s is a link or special file", p)
		}
	})
	if err != nil {
		return err
	}
	// Folder dates last, deepest first, since adding files changes them.
	for i := len(dirs) - 1; i >= 0; i-- {
		rel, _ := filepath.Rel(src, dirs[i])
		_ = platform.CopyTimes(dirs[i], filepath.Join(dst, rel))
	}
	return nil
}

func copyFile(ctx context.Context, src, dst string, fi fs.FileInfo, onBytes func(int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	h := blake3.New()
	buf := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			onBytes(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if after, err := os.Lstat(src); err != nil || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		return fmt.Errorf("%s changed while it was being copied", src)
	}
	got, err := hashing.Full(ctx, dst)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, h.Sum(nil)) {
		return fmt.Errorf("verification failed for %s: the copy doesn't match", src)
	}
	if err := platform.CopyTimes(src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, fi.Mode().Perm())
}

// removeAll deletes a file or folder, clearing read-only flags that would
// otherwise block it on Windows.
func removeAll(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err == nil {
			_ = platform.MakeWritable(q)
		}
		return nil
	})
	return os.RemoveAll(p)
}
