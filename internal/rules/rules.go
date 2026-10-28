// Package rules reads the user's sort rules and turns matching files into a
// plan of moves.
package rules

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"organizer/internal/config"
	"organizer/internal/fsutil"
	"organizer/internal/index"
	"organizer/internal/plan"
)

// File is rules.yaml.
type File struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// Rule moves files that match into MoveTo.
type Rule struct {
	Name string `yaml:"name"`
	// Folders the rule looks in: Downloads, Desktop, Documents or a path.
	// Empty means every configured root.
	In []string `yaml:"in"`
	// By default only files sitting directly in the folder are sorted;
	// subfolders someone made are left alone.
	Recursive bool   `yaml:"recursive"`
	Match     Match  `yaml:"match"`
	MoveTo    string `yaml:"move_to"`
}

// Match criteria are ANDed; a rule needs at least one.
type Match struct {
	Type          []string `yaml:"type"` // image, video, audio, document, spreadsheet, presentation, archive, installer, code, font, 3d, ebook
	Ext           []string `yaml:"ext"`
	Name          string   `yaml:"name"` // glob, case-insensitive, e.g. "Screenshot*"
	OlderThanDays int      `yaml:"older_than_days"`
	MinSize       string   `yaml:"min_size"` // e.g. 10MB
	MaxSize       string   `yaml:"max_size"`
}

// Types maps type names to extensions.
var Types = map[string][]string{
	"image":        {"jpg", "jpeg", "png", "gif", "webp", "heic", "heif", "bmp", "tif", "tiff", "svg", "raw", "cr2", "nef", "arw", "dng", "psd", "ico", "avif"},
	"video":        {"mp4", "mov", "mkv", "avi", "wmv", "webm", "m4v", "flv", "mpg", "mpeg", "3gp"},
	"audio":        {"mp3", "wav", "flac", "aac", "m4a", "ogg", "opus", "wma", "aiff", "mid", "midi"},
	"document":     {"pdf", "doc", "docx", "odt", "rtf", "txt", "md", "pages"},
	"spreadsheet":  {"xls", "xlsx", "ods", "csv", "tsv", "numbers"},
	"presentation": {"ppt", "pptx", "odp", "key"},
	"archive":      {"zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz", "zst"},
	"installer":    {"exe", "msi", "msix", "appx", "dmg", "pkg", "deb", "rpm", "appimage", "apk", "flatpakref"},
	"code":         {"py", "js", "ts", "go", "rs", "c", "cpp", "h", "java", "cs", "lua", "luau", "sh", "ps1", "json", "yaml", "yml", "toml", "html", "css"},
	"font":         {"ttf", "otf", "woff", "woff2"},
	"3d":           {"obj", "fbx", "stl", "glb", "gltf", "blend", "3mf", "dae", "rbxm", "rbxl"},
	"ebook":        {"epub", "mobi", "azw3"},
}

var typeOf = func() map[string]string {
	m := map[string]string{}
	for t, exts := range Types {
		for _, e := range exts {
			m["."+e] = t
		}
	}
	return m
}()

// TypeOf returns the type name for an extension (with dot), or "other".
func TypeOf(ext string) string {
	if t, ok := typeOf[strings.ToLower(ext)]; ok {
		return t
	}
	return "other"
}

// Default is written to rules.yaml on first run.
const Default = `# Sort rules. Checked top to bottom; the first rule that matches a file wins.
# Nothing moves until you approve the plan, and every move can be undone.
#
#   in:        Downloads, Desktop, Documents, or a folder path (default: all roots)
#   recursive: false (default) sorts only files sitting directly in the folder
#   match:     type, ext, name (glob), older_than_days, min_size, max_size
#   move_to:   a relative path is inside the folder the file was found in;
#              @Pictures/... (any known folder), ~/... or a full path moves
#              it elsewhere. Quote values starting with @ or {, e.g.
#              move_to: "@Pictures/{year}"
#              Tokens: {year} {month} {day} {type} {ext}
#
# Types: image video audio document spreadsheet presentation archive
#        installer code font 3d ebook
version: 1
rules:
  - name: Screenshots
    in: [Downloads, Desktop]
    match: { type: [image], name: "Screenshot*" }
    move_to: Screenshots/{year}-{month}

  - name: Images
    in: [Downloads]
    match: { type: [image] }
    move_to: Images/{year}

  - name: Videos
    in: [Downloads]
    match: { type: [video] }
    move_to: Videos

  - name: Audio
    in: [Downloads]
    match: { type: [audio] }
    move_to: Audio

  - name: Documents
    in: [Downloads]
    match: { type: [document, spreadsheet, presentation, ebook] }
    move_to: Documents/{year}

  - name: Archives
    in: [Downloads]
    match: { type: [archive] }
    move_to: Archives

  - name: Installers
    in: [Downloads]
    match: { type: [installer] }
    move_to: Installers

  - name: 3D and game assets
    in: [Downloads]
    match: { type: ["3d"] }
    move_to: 3D
`

// Load reads dataDir/rules.yaml, writing the default rules if it's missing.
func Load(dataDir string) (*File, string, error) {
	path := filepath.Join(dataDir, "rules.yaml")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte(Default), 0o600); err != nil {
			return nil, "", err
		}
		b = []byte(Default)
	} else if err != nil {
		return nil, "", err
	}
	f, err := Parse(b)
	return f, string(b), err
}

// Save validates text and writes it as rules.yaml.
func Save(dataDir, text string) error {
	if _, err := Parse([]byte(text)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "rules.yaml"), []byte(text), 0o600)
}

var tokenRe = regexp.MustCompile(`\{([a-z]+)\}`)
var knownTokens = map[string]bool{"year": true, "month": true, "day": true, "type": true, "ext": true}

// Parse reads and validates rules.
func Parse(b []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("rules.yaml: %w", err)
	}
	for i, r := range f.Rules {
		label := r.Name
		if label == "" {
			label = "rule " + strconv.Itoa(i+1)
		}
		m := r.Match
		if len(m.Type) == 0 && len(m.Ext) == 0 && m.Name == "" && m.OlderThanDays == 0 && m.MinSize == "" && m.MaxSize == "" {
			return nil, fmt.Errorf("%s: needs at least one match condition", label)
		}
		for _, t := range m.Type {
			if _, ok := Types[t]; !ok {
				return nil, fmt.Errorf("%s: unknown type %q", label, t)
			}
		}
		if m.Name != "" {
			if _, err := filepath.Match(strings.ToLower(m.Name), "x"); err != nil {
				return nil, fmt.Errorf("%s: bad name pattern %q", label, m.Name)
			}
		}
		for _, s := range []string{m.MinSize, m.MaxSize} {
			if _, err := ParseSize(s); err != nil {
				return nil, fmt.Errorf("%s: %w", label, err)
			}
		}
		if strings.TrimSpace(r.MoveTo) == "" {
			return nil, fmt.Errorf("%s: move_to is required", label)
		}
		for _, t := range tokenRe.FindAllStringSubmatch(r.MoveTo, -1) {
			if !knownTokens[t[1]] {
				return nil, fmt.Errorf("%s: unknown token {%s}", label, t[1])
			}
		}
		if strings.Contains(filepath.ToSlash(r.MoveTo), "..") {
			return nil, fmt.Errorf("%s: move_to can't contain ..", label)
		}
	}
	return &f, nil
}

// ParseSize reads sizes like 500KB, 10MB, 2GB (powers of 1024). Empty is 0.
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suf) {
			mult, s = u.m, strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(f * float64(mult)), nil
}

type candidate struct {
	path, root, parent, name, ext string
	size, mtime                   int64
}

// Plan matches rules against the index and returns a sort plan.
func Plan(db *index.DB, cfg *config.Config, f *File) (*plan.Plan, error) {
	rows, err := db.Query(`SELECT path, root, parent, name, ext, size, mtime FROM files WHERE placeholder=0 AND in_project=0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	var files []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.path, &c.root, &c.parent, &c.name, &c.ext, &c.size, &c.mtime); err != nil {
			rows.Close()
			return nil, err
		}
		files = append(files, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type compiled struct {
		Rule
		folders  []string
		exts     map[string]bool
		min, max int64
	}
	var rs []compiled
	for _, r := range f.Rules {
		c := compiled{Rule: r, exts: map[string]bool{}}
		for _, in := range r.In {
			if p := cfg.ResolvePath(in); p != "" {
				c.folders = append(c.folders, p)
			}
		}
		if len(r.In) == 0 {
			c.folders = cfg.RootPaths
		}
		for _, t := range r.Match.Type {
			for _, e := range Types[t] {
				c.exts["."+e] = true
			}
		}
		for _, e := range r.Match.Ext {
			c.exts["."+strings.TrimPrefix(strings.ToLower(e), ".")] = true
		}
		c.min, _ = ParseSize(r.Match.MinSize)
		c.max, _ = ParseSize(r.Match.MaxSize)
		rs = append(rs, c)
	}
	p := &plan.Plan{Kind: "sort", Created: time.Now().Unix()}
	now := time.Now()
	for _, fl := range files {
		for _, r := range rs {
			folder := ""
			for _, d := range r.folders {
				if (r.Recursive && fsutil.Within(fl.parent, d)) || fsutil.SamePath(fl.parent, d) {
					folder = d
					break
				}
			}
			if folder == "" {
				continue
			}
			m := r.Match
			if len(r.exts) > 0 && !r.exts[fl.ext] {
				continue
			}
			if m.Name != "" {
				if ok, _ := filepath.Match(strings.ToLower(m.Name), strings.ToLower(fl.name)); !ok {
					continue
				}
			}
			if m.OlderThanDays > 0 && fl.mtime > now.AddDate(0, 0, -m.OlderThanDays).UnixNano() {
				continue
			}
			if (r.min > 0 && fl.size < r.min) || (r.max > 0 && fl.size > r.max) {
				continue
			}
			destDir := expand(r.MoveTo, fl, folder, cfg)
			if fsutil.SamePath(destDir, fl.parent) || cfg.IsProtected(destDir) {
				break // already sorted, or nowhere safe to go
			}
			p.Actions = append(p.Actions, plan.Action{
				ID: len(p.Actions) + 1, Op: plan.OpMove, Category: "rule",
				Src: fl.path, Dst: filepath.Join(destDir, fl.name), Size: fl.size, MTime: fl.mtime,
				Reason: r.Name, Selected: true,
			})
			break // first matching rule wins
		}
	}
	return p, nil
}

func expand(tmpl string, f candidate, folder string, cfg *config.Config) string {
	t := time.Unix(0, f.mtime)
	s := tokenRe.ReplaceAllStringFunc(tmpl, func(tok string) string {
		switch tok {
		case "{year}":
			return t.Format("2006")
		case "{month}":
			return t.Format("01")
		case "{day}":
			return t.Format("02")
		case "{type}":
			return TypeOf(f.ext)
		case "{ext}":
			if e := strings.TrimPrefix(f.ext, "."); e != "" {
				return e
			}
			return "no-extension"
		}
		return tok
	})
	if strings.HasPrefix(s, "~") || filepath.IsAbs(s) || filepath.VolumeName(s) != "" {
		return cfg.ResolvePath(s)
	}
	// "@Pictures/{year}" means the Pictures known folder; plain
	// "Pictures/{year}" is a subfolder of where the file was found.
	if strings.HasPrefix(s, "@") {
		name, rest, _ := strings.Cut(filepath.ToSlash(s[1:]), "/")
		if base := cfg.ResolvePath(name); base != "" {
			return filepath.Join(base, filepath.FromSlash(rest))
		}
	}
	return filepath.Join(folder, filepath.FromSlash(s))
}
