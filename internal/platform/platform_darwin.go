package platform

import (
	"io/fs"
	"os/exec"
	"strings"
	"syscall"
)

// sfDataless marks a file whose content lives in the cloud (iCloud Drive,
// OneDrive and other File Provider apps on macOS 12+).
const sfDataless = 0x40000000

func MetaOf(_ string, fi fs.FileInfo) Meta {
	var m Meta
	name := fi.Name()
	m.Placeholder = strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".icloud")
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		m.Birth = st.Birthtimespec.Nano()
		if st.Flags&sfDataless != 0 {
			m.Placeholder = true
		}
	}
	return m
}

func BirthTime(string) int64 { return 0 }

func OpenBrowser(url string) error { return exec.Command("open", url).Start() }

func Reveal(path string) error { return exec.Command("open", "-R", path).Start() }
