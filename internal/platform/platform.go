// Package platform hides the per-OS details: birth times, cloud placeholder
// detection, file identity, I/O priority and opening the browser.
package platform

import (
	"errors"
	"os"
)

// Meta is what the walker learns about an entry without opening it.
type Meta struct {
	Birth       int64 // creation time, unix ns; 0 when the OS doesn't say
	Placeholder bool  // cloud file whose content may not be on disk; never read it
	Reparse     bool  // Windows reparse point (junction, cloud file); never descend
}

// IsCrossDevice reports whether err came from renaming across volumes.
func IsCrossDevice(err error) bool {
	var le *os.LinkError
	if errors.As(err, &le) {
		return isCrossDeviceErrno(le.Err)
	}
	return isCrossDeviceErrno(err)
}
