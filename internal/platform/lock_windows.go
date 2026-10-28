package platform

import (
	"errors"

	"golang.org/x/sys/windows"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("locked by another process")

// Lock takes an exclusive lock on path, creating it if needed. Windows
// releases it automatically if the process dies.
func Lock(path string) (release func(), err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}
