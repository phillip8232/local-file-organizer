//go:build darwin || linux

package config

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func knownFolders() map[string]string {
	home, _ := os.UserHomeDir()
	m := map[string]string{
		"downloads": filepath.Join(home, "Downloads"),
		"desktop":   filepath.Join(home, "Desktop"),
		"documents": filepath.Join(home, "Documents"),
		"pictures":  filepath.Join(home, "Pictures"),
		"videos":    filepath.Join(home, map[bool]string{true: "Movies", false: "Videos"}[runtime.GOOS == "darwin"]),
		"music":     filepath.Join(home, "Music"),
	}
	if runtime.GOOS == "linux" {
		readXDGUserDirs(home, m)
	}
	return m
}

// readXDGUserDirs applies ~/.config/user-dirs.dirs (localised folder names).
func readXDGUserDirs(home string, m map[string]string) {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return
	}
	defer f.Close()
	keys := map[string]string{
		"XDG_DOWNLOAD_DIR": "downloads", "XDG_DESKTOP_DIR": "desktop", "XDG_DOCUMENTS_DIR": "documents",
		"XDG_PICTURES_DIR": "pictures", "XDG_VIDEOS_DIR": "videos", "XDG_MUSIC_DIR": "music",
	}
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(s.Text()), "=")
		if !ok || keys[k] == "" {
			continue
		}
		v = strings.Trim(v, `"`)
		v = strings.Replace(v, "$HOME", home, 1)
		if filepath.IsAbs(v) && v != home {
			m[keys[k]] = v
		}
	}
}

func defaultProtected() []string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return []string{"/System", "/Library", "/Applications", "/private", "/usr", "/bin", "/sbin",
			"/Volumes/Recovery", filepath.Join(home, "Library"), filepath.Join(home, "Applications")}
	}
	return []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/proc", "/run",
		"/sbin", "/snap", "/sys", "/usr", "/var", filepath.Join(home, ".config"),
		filepath.Join(home, ".local"), filepath.Join(home, ".cache"), filepath.Join(home, "snap")}
}

func dataDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "organizer")
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "organizer")
	}
	return filepath.Join(home, ".local", "share", "organizer")
}
