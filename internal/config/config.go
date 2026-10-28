// Package config loads the user's settings and resolves known folders and
// protected paths for the current OS.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"organizer/internal/fsutil"
)

// Config is config.yaml plus the paths resolved from it.
type Config struct {
	// Folders to organise. Names Downloads, Desktop and Documents resolve to
	// the OS's known folders; anything else is a path (~ allowed).
	Roots []string `yaml:"roots"`
	// Extra folders never to touch, on top of the OS defaults.
	Protected []string `yaml:"protected"`
	// Folder names never descended into.
	IgnoreDirs []string `yaml:"ignore_dirs"`
	// Downloads untouched for this many days are suggested for deletion.
	OldDownloadsDays int `yaml:"old_downloads_days"`
	// Deleted items stay in the holding area this long, then go to the OS trash.
	HoldingDays int `yaml:"holding_days"`
	// Parallel file readers while hashing (4 suits an SSD, 1 an HDD).
	IOWorkers int `yaml:"io_workers"`
	// How many of the biggest files to list.
	LargeTopN int `yaml:"large_top_n"`
	// Duplicates smaller than this many bytes are not worth reviewing.
	MinDuplicateSize int64 `yaml:"min_duplicate_size"`
	// Which copy of a duplicate to keep by default: smart, oldest or newest.
	Keep string `yaml:"keep"`

	DataDir        string   `yaml:"-"`
	RootPaths      []string `yaml:"-"`
	ProtectedPaths []string `yaml:"-"`
	DownloadsDir   string   `yaml:"-"`
	// Known maps lower-case known-folder names (downloads, pictures, …) to paths.
	Known      map[string]string `yaml:"-"`
	ignoreDirs map[string]bool
}

func defaults() Config {
	return Config{
		Roots: []string{"Downloads", "Desktop", "Documents"},
		IgnoreDirs: []string{
			".git", "node_modules", ".venv", "venv", "__pycache__", ".cache",
			"$RECYCLE.BIN", "System Volume Information", ".Trash", ".Trashes",
			".organizer-holding", ".Spotlight-V100", ".fseventsd",
		},
		OldDownloadsDays: 90,
		HoldingDays:      30,
		IOWorkers:        4,
		LargeTopN:        50,
		MinDuplicateSize: 1024,
		Keep:             "smart",
	}
}

const header = `# Local File Organizer settings. Edit and re-run; nothing is moved or
# deleted without your approval.
`

// DefaultDataDir is where the index, settings, rules and holding area live.
func DefaultDataDir() string { return dataDir() }

// Load reads dataDir/config.yaml, writing the defaults first if it's missing.
func Load(dataDir string) (*Config, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	c := defaults()
	path := filepath.Join(dataDir, "config.yaml")
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		out, _ := yaml.Marshal(&c)
		if err := os.WriteFile(path, append([]byte(header), out...), 0o600); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	c.DataDir = dataDir
	if err := c.resolve(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) resolve() error {
	if c.IOWorkers < 1 {
		c.IOWorkers = 1
	}
	if c.HoldingDays < 1 {
		c.HoldingDays = 1
	}
	c.Known = knownFolders()
	c.DownloadsDir = c.Known["downloads"]
	c.RootPaths = nil
	for _, r := range c.Roots {
		p := c.ResolvePath(r)
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue // a missing Desktop on a server, say
		}
		c.RootPaths = append(c.RootPaths, p)
	}
	c.RootPaths = dropNested(c.RootPaths)
	c.ProtectedPaths = append(defaultProtected(), c.DataDir)
	for _, p := range c.Protected {
		if p = c.ResolvePath(p); p != "" {
			c.ProtectedPaths = append(c.ProtectedPaths, p)
		}
	}
	c.ignoreDirs = map[string]bool{}
	for _, d := range c.IgnoreDirs {
		c.ignoreDirs[strings.ToLower(d)] = true
	}
	return nil
}

// ResolvePath turns a known-folder name, ~ path or relative path into an
// absolute path.
func (c *Config) ResolvePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if k, ok := c.Known[strings.ToLower(p)]; ok {
		return k
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[1:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return abs
}

// IsProtected reports whether path is inside a folder the organizer must
// never touch.
func (c *Config) IsProtected(path string) bool {
	for _, p := range c.ProtectedPaths {
		if fsutil.Within(path, p) {
			return true
		}
	}
	return false
}

// IgnoreDir reports whether a folder with this name is skipped entirely.
func (c *Config) IgnoreDir(name string) bool {
	return c.ignoreDirs[strings.ToLower(name)] || strings.HasPrefix(name, ".Trash-")
}

// RootOf returns the configured root containing path, or "".
func (c *Config) RootOf(path string) string {
	for _, r := range c.RootPaths {
		if fsutil.Within(path, r) {
			return r
		}
	}
	return ""
}

// dropNested removes roots that sit inside another root.
func dropNested(roots []string) []string {
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) < len(roots[j]) })
	var out []string
outer:
	for _, r := range roots {
		for _, o := range out {
			if fsutil.Within(r, o) {
				continue outer
			}
		}
		out = append(out, r)
	}
	return out
}
