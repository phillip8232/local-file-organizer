package config

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func knownFolders() map[string]string {
	m := map[string]string{}
	for name, id := range map[string]*windows.KNOWNFOLDERID{
		"downloads": windows.FOLDERID_Downloads,
		"desktop":   windows.FOLDERID_Desktop,
		"documents": windows.FOLDERID_Documents,
		"pictures":  windows.FOLDERID_Pictures,
		"videos":    windows.FOLDERID_Videos,
		"music":     windows.FOLDERID_Music,
	} {
		if p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			m[name] = p
		}
	}
	return m
}

func defaultProtected() []string {
	var out []string
	for _, env := range []string{"WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData", "APPDATA", "LOCALAPPDATA"} {
		if v := os.Getenv(env); v != "" {
			out = append(out, v)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		vol := filepath.VolumeName(home) + `\`
		out = append(out, filepath.Join(vol, "$Recycle.Bin"), filepath.Join(vol, "System Volume Information"),
			filepath.Join(home, "AppData"))
	}
	return out
}

func dataDir() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return filepath.Join(v, "organizer")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".organizer")
}
