package executor_test

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const canSetBirth = true

// setBirth backdates a file's creation time.
func setBirth(t *testing.T, path string, at time.Time) {
	t.Helper()
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	ft := windows.NsecToFiletime(at.UnixNano())
	if err := windows.SetFileTime(h, &ft, nil, nil); err != nil {
		t.Fatal(err)
	}
}
