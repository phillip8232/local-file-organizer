package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	attrOffline            = 0x00001000
	attrRecallOnOpen       = 0x00040000
	attrRecallOnDataAccess = 0x00400000
	cloudAttrs             = attrOffline | attrRecallOnOpen | attrRecallOnDataAccess
)

// MetaOf reads attributes from the directory listing data, so it never opens
// the file (opening an online-only OneDrive file downloads it).
func MetaOf(_ string, fi fs.FileInfo) Meta {
	var m Meta
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		m.Birth = d.CreationTime.Nanoseconds()
		m.Reparse = d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
		// Reparse-point files are cloud or dedup files: treat them as untouchable.
		m.Placeholder = d.FileAttributes&cloudAttrs != 0 || m.Reparse
	}
	return m
}

// BirthTime is only needed where MetaOf can't supply one (Linux).
func BirthTime(string) int64 { return 0 }

// FileID returns a stable identity for the file (volume serial + file index)
// and its hard link count. It opens the file for attributes only.
func FileID(path string) (string, uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", 0, err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", 0, err
	}
	defer windows.CloseHandle(h)
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x:%x%08x", fi.VolumeSerialNumber, fi.FileIndexHigh, fi.FileIndexLow), uint64(fi.NumberOfLinks), nil
}

// VolumeRoot returns the root of the volume holding path, e.g. `D:\`.
func VolumeRoot(path string) string { return filepath.VolumeName(path) + `\` }

const (
	processModeBackgroundBegin = 0x00100000
	processModeBackgroundEnd   = 0x00200000
)

// Background lowers CPU and I/O priority for the process; call the returned
// func to restore it.
func Background() func() {
	if windows.SetPriorityClass(windows.CurrentProcess(), processModeBackgroundBegin) != nil {
		return func() {}
	}
	return func() { _ = windows.SetPriorityClass(windows.CurrentProcess(), processModeBackgroundEnd) }
}

func isCrossDeviceErrno(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// Reveal shows path selected in Explorer.
func Reveal(path string) error {
	return exec.Command("explorer", "/select,", path).Start()
}
