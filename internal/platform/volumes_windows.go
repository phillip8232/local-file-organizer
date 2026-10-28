package platform

import (
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Volumes lists fixed local drives. Removable, network and optical drives
// are left out.
func Volumes() ([]Volume, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, err
	}
	sys := strings.ToUpper(os.Getenv("SystemDrive"))
	var out []Volume
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(p) != windows.DRIVE_FIXED {
			continue
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
			continue // not ready
		}
		label := make([]uint16, windows.MAX_PATH+1)
		fs := make([]uint16, windows.MAX_PATH+1)
		_ = windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, &fs[0], uint32(len(fs)))
		out = append(out, Volume{
			Root: root, Label: windows.UTF16ToString(label), FS: windows.UTF16ToString(fs),
			Total: int64(total), Free: int64(free), System: sys != "" && strings.HasPrefix(root, sys),
		})
	}
	return out, nil
}

// FreeSpace returns the bytes available to this user on path's volume.
func FreeSpace(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}

// CopyTimes gives dst the creation and modification times of src.
func CopyTimes(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.Chtimes(dst, time.Time{}, fi.ModTime()); err != nil {
		return err
	}
	if m := MetaOf(src, fi); m.Birth > 0 {
		p, _ := windows.UTF16PtrFromString(dst)
		h, err := windows.CreateFile(p, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		ft := windows.NsecToFiletime(m.Birth)
		return windows.SetFileTime(h, &ft, nil, nil)
	}
	return nil
}

// MakeWritable clears the read-only attribute so a file can be removed.
func MakeWritable(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
		return nil
	}
	return windows.SetFileAttributes(p, attrs&^windows.FILE_ATTRIBUTE_READONLY)
}
