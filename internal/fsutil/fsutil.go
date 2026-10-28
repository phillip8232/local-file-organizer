// Package fsutil holds small path and filesystem helpers shared by the
// scanner, analyzers and executor.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const sep = string(filepath.Separator)

// caseInsensitive is true where the default filesystems ignore case.
var caseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

func norm(p string) string {
	p = filepath.Clean(p)
	if caseInsensitive {
		p = strings.ToLower(p)
	}
	return p
}

// SamePath reports whether a and b name the same path (ignoring case where
// the OS does).
func SamePath(a, b string) bool { return norm(a) == norm(b) }

// Within reports whether path is parent itself or anywhere inside it.
func Within(path, parent string) bool {
	p, q := norm(path), norm(parent)
	if p == q {
		return true
	}
	if !strings.HasSuffix(q, sep) {
		q += sep
	}
	return strings.HasPrefix(p, q)
}

// PrefixRange returns [lo, hi) bounds that select, in a binary-collated
// index, every path strictly inside dir.
func PrefixRange(dir string) (lo, hi string) {
	lo = dir
	if !strings.HasSuffix(lo, sep) {
		lo += sep
	}
	hi = lo[:len(lo)-1] + string(rune(filepath.Separator+1))
	return lo, hi
}

// UniquePath returns path if nothing exists there, otherwise the first free
// "name (n).ext" next to it.
func UniquePath(path string) string {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return path
	}
	dir, base := filepath.Split(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; ; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p
		}
	}
}

// ignorable are OS-generated files that never make a folder "not empty" and
// are never worth reporting on their own.
var ignorable = map[string]bool{
	"thumbs.db": true, "ehthumbs.db": true, "desktop.ini": true,
	".ds_store": true, "._.ds_store": true, ".localized": true, "icon\r": true,
	".gitkeep": true, ".keep": true, ".nomedia": true,
}

// IsIgnorableFile reports whether name is an OS-generated metadata file.
func IsIgnorableFile(name string) bool { return ignorable[strings.ToLower(name)] }

// Project markers: a folder holding any of these is treated as one unit that
// the organizer never reorganises or deletes from.
var projectDirMarkers = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".claude": true, ".idea": true,
	".vscode": true, "node_modules": true, ".venv": true,
}

var projectFileMarkers = map[string]bool{
	"go.mod": true, "package.json": true, "cargo.toml": true, "pyproject.toml": true,
	"setup.py": true, "pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"cmakelists.txt": true, "makefile": true, "gemfile": true, "composer.json": true,
	"default.project.json": true, "wally.toml": true, "aftman.toml": true, "rokit.toml": true,
	"pubspec.yaml": true, "mix.exs": true, "deno.json": true, "claude.md": true,
}

var projectExtMarkers = map[string]bool{".sln": true, ".csproj": true, ".xcodeproj": true, ".uproject": true}

// IsProjectMarker reports whether an entry with this name marks its parent
// folder as a project.
func IsProjectMarker(name string, isDir bool) bool {
	l := strings.ToLower(name)
	if isDir {
		return projectDirMarkers[l] || projectExtMarkers[filepath.Ext(l)]
	}
	return projectFileMarkers[l] || projectExtMarkers[filepath.Ext(l)]
}

// InProject reports whether path lies inside a project folder, checking each
// ancestor below root (markers loose in the root itself don't count, as in
// the scanner). It re-reads the disk, so the executor can use it as a last
// check before acting.
func InProject(path, root string) bool {
	dir := filepath.Dir(path)
	for Within(dir, root) && !SamePath(dir, root) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if IsProjectMarker(e.Name(), e.IsDir()) {
					return true
				}
			}
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return false
}

// OnlyIgnorable reports whether dir (recursively) holds nothing but folders
// and OS metadata files. Symlinks and anything unreadable count as content.
func OnlyIgnorable(dir string) (bool, error) {
	seen := 0
	empty := true
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			empty = false
			return fs.SkipAll
		}
		if seen++; seen > 10000 {
			empty = false
			return fs.SkipAll
		}
		if p == dir {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || (!d.IsDir() && !IsIgnorableFile(d.Name())) {
			empty = false
			return fs.SkipAll
		}
		return nil
	})
	return empty, err
}
