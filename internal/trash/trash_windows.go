// Package trash moves a file or folder to the OS trash: the Recycle Bin on
// Windows, the Finder trash on macOS and the FreeDesktop trash on Linux.
package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	foDelete           = 0x0003
	fofSilent          = 0x0004
	fofNoConfirmation  = 0x0010
	fofAllowUndo       = 0x0040
	fofNoErrorUI       = 0x0400
	fofWantNukeWarning = 0x4000
)

// shFileOpStruct mirrors SHFILEOPSTRUCTW on 64-bit Windows (8-byte packing).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

var procSHFileOperationW = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// Move sends path to the Recycle Bin. If Windows can't recycle it (a drive
// without a Recycle Bin, a file too big for it) it asks before deleting
// permanently instead of doing so silently.
func Move(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err != nil {
		return err
	}
	from, err := windows.UTF16FromString(abs)
	if err != nil {
		return err
	}
	from = append(from, 0) // pFrom is double-NUL terminated
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI | fofWantNukeWarning,
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(from)
	if r != 0 {
		return fmt.Errorf("recycle %s: shell error 0x%x", abs, r)
	}
	if op.fAnyOperationsAborted != 0 {
		return errors.New("recycle " + abs + ": cancelled")
	}
	if _, err := os.Lstat(abs); err == nil {
		return errors.New("recycle " + abs + ": file is still there")
	}
	return nil
}
