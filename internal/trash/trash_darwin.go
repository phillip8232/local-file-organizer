// Package trash moves a file or folder to the OS trash: the Recycle Bin on
// Windows, the Finder trash on macOS and the FreeDesktop trash on Linux.
package trash

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Move asks Finder to trash path, so "Put Back" works. The path is passed as
// an argument, never spliced into the script. The first use triggers macOS's
// "allow organizer to control Finder" prompt.
func Move(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err != nil {
		return err
	}
	out, err := exec.Command("osascript",
		"-e", "on run argv",
		"-e", `tell application "Finder" to delete (POSIX file (item 1 of argv) as alias)`,
		"-e", "end run", abs).CombinedOutput()
	if err != nil {
		return fmt.Errorf("trash %s: %v: %s", abs, err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("trash %s: file is still there", abs)
	}
	return nil
}
