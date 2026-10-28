// Package rules reads the user's sort rules and turns matching files into a
// plan of moves and renames.
package rules

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/fsutil"
	"github.com/phillip8232/local-file-organizer/internal/index"
	"github.com/phillip8232/local-file-organizer/internal/plan"
)

// File is rules.yaml.
type File struct {
	Version int `yaml:"version"`
	// Give auto-named files (IMG_1234, download (3), Untitled) a name based on
	// what they are.
	RenameGeneric bool `yaml:"rename_generic"`
	// Rename generic files in these folders even when no rule moves them.
	RenameIn []string `yaml:"rename_in"`
	// Put group_min or more loose files that share a name into a folder
	// named after it.
	GroupRelated bool   `yaml:"group_related"`
	GroupMin     int    `yaml:"group_min"`
	Rules        []Rule `yaml:"rules"`
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
	Type          []string `yaml:"type"` // image, video, audio, document, …
	Kind          []string `yaml:"kind"` // screenshot, camera, recording
	Ext           []string `yaml:"ext"`
	Name          string   `yaml:"name"` // glob, case-insensitive, e.g. "Screenshot*"
	OlderThanDays int      `yaml:"older_than_days"`
	MinSize       string   `yaml:"min_size"` // e.g. 10MB
	MaxSize       string   `yaml:"max_size"`
}

// Types maps type names to extensions.
var Types = map[string][]string{
	"image":        {"jpg", "jpeg", "png", "gif", "webp", "heic", "heif", "bmp", "tif", "tiff", "svg", "raw", "cr2", "nef", "arw", "dng", "psd", "ico", "avif", "jxl"},
	"video":        {"mp4", "mov", "mkv", "avi", "wmv", "webm", "m4v", "flv", "mpg", "mpeg", "3gp", "mts", "m2ts"},
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

// Kinds are what a file is beyond its type, worked out from its name and
// metadata.
var Kinds = map[string]bool{"screenshot": true, "camera": true, "recording": true}

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
# Nothing moves or is renamed until you approve the plan, and every change
# can be undone.
#
#   in:        Downloads, Desktop, Documents, or a folder path (default: all roots)
#   recursive: false (default) sorts only files sitting directly in the folder
#   match:     type, kind, ext, name (glob), older_than_days, min_size, max_size
#   move_to:   "@Pictures/..." means your Pictures folder (also @Documents,
#              @Videos, @Music, @Downloads, @Desktop); a plain relative path is
#              inside the folder the file was found in; ~/... or a full path
#              goes exactly there. Quote values starting with @ or {.
#              Tokens: {year} {month} {day} use when the photo was taken or
#              the date in the file's name, else when it was last modified;
#              {type} {ext}.
#
# Types: image video audio document spreadsheet presentation archive
#        installer code font 3d ebook
# Kinds: screenshot, camera (phone/camera photos and videos), recording
#        (screen recordings)
version: 2

# Give auto-named files (IMG_20261021_113648, download (3), Untitled, hash
# names) a name based on what they are, e.g. "Photo 2026-10-21 11.36.48.jpg",
# "Screenshot 2026-10-21 14.03.22.png", or the title inside a PDF or song.
# Names you chose yourself are never changed.
rename_generic: true
# Rename generic files here even when no rule below moves them.
rename_in: [Downloads, Desktop]

# Put 3 or more loose files that share a name (Acme_logo.png,
# Acme_banner.jpg, Acme_pack.zip) into a folder named after it.
group_related: true
group_min: 3

rules:
  - name: Screenshots
    in: [Downloads, Desktop]
    match: { type: [image], kind: [screenshot] }
    move_to: "@Pictures/Screenshots/{year}-{month}"

  - name: Phone and camera photos
    in: [Downloads, Desktop]
    match: { type: [image], kind: [camera] }
    move_to: "@Pictures/Camera/{year}/{month}"

  - name: Phone and camera videos
    in: [Downloads, Desktop]
    match: { type: [video], kind: [camera] }
    move_to: "@Videos/Camera/{year}/{month}"

  - name: Screen recordings
    in: [Downloads, Desktop]
    match: { type: [video], kind: [recording] }
    move_to: "@Videos/Recordings/{year}-{month}"

  - name: Pictures
    in: [Downloads]
    match: { type: [image] }
    move_to: "@Pictures/Downloaded/{year}"

  - name: Videos
    in: [Downloads]
    match: { type: [video] }
    move_to: "@Videos/Downloaded"

  - name: Music and audio
    in: [Downloads]
    match: { type: [audio] }
    move_to: "@Music/Downloaded"

  - name: PDFs
    in: [Downloads]
    match: { ext: [pdf] }
    move_to: "@Documents/PDFs/{year}"

  - name: Documents
    in: [Downloads]
    match: { type: [document] }
    move_to: "@Documents/Downloaded/{year}"

  - name: Spreadsheets
    in: [Downloads]
    match: { type: [spreadsheet] }
    move_to: "@Documents/Spreadsheets"

  - name: Presentations
    in: [Downloads]
    match: { type: [presentation] }
    move_to: "@Documents/Presentations"

  - name: eBooks
    in: [Downloads]
    match: { type: [ebook] }
    move_to: "@Documents/eBooks"

  - name: 3D and game assets
    in: [Downloads]
    match: { type: ["3d"] }
    move_to: "@Documents/3D Assets"

  - name: Fonts
    in: [Downloads]
    match: { type: [font] }
    move_to: Fonts

  - name: Archives
    in: [Downloads]
    match: { type: [archive] }
    move_to: Archives

  - name: Installers
    in: [Downloads]
    match: { type: [installer] }
    move_to: Installers
`

// legacyDefaultV1 is the first release's default; an untouched copy is
// upgraded to Default automatically.
const legacyDefaultV1 = "# Sort rules. Checked top to bottom; the first rule that matches a file wins.\n# Nothing moves until you approve the plan, and every move can be undone.\n#\n#   in:        Downloads, Desktop, Documents, or a folder path (default: all roots)\n#   recursive: false (default) sorts only files sitting directly in the folder\n#   match:     type, ext, name (glob), older_than_days, min_size, max_size\n#   move_to:   a relative path is inside the folder the file was found in;\n#              @Pictures/... (any known folder), ~/... or a full path moves\n#              it elsewhere. Quote values starting with @ or {, e.g.\n#              move_to: \"@Pictures/{year}\"\n#              Tokens: {year} {month} {day} {type} {ext}\n#\n# Types: image video audio document spreadsheet presentation archive\n#        installer code font 3d ebook\nversion: 1\nrules:\n  - name: Screenshots\n    in: [Downloads, Desktop]\n    match: { type: [image], name: \"Screenshot*\" }\n    move_to: Screenshots/{year}-{month}\n\n  - name: Images\n    in: [Downloads]\n    match: { type: [image] }\n    move_to: Images/{year}\n\n  - name: Videos\n    in: [Downloads]\n    match: { type: [video] }\n    move_to: Videos\n\n  - name: Audio\n    in: [Downloads]\n    match: { type: [audio] }\n    move_to: Audio\n\n  - name: Documents\n    in: [Downloads]\n    match: { type: [document, spreadsheet, presentation, ebook] }\n    move_to: Documents/{year}\n\n  - name: Archives\n    in: [Downloads]\n    match: { type: [archive] }\n    move_to: Archives\n\n  - name: Installers\n    in: [Downloads]\n    match: { type: [installer] }\n    move_to: Installers\n\n  - name: 3D and game assets\n    in: [Downloads]\n    match: { type: [\"3d\"] }\n    move_to: 3D\n"

// Load reads dataDir/rules.yaml, writing the default rules if it's missing
// and upgrading an untouched copy of the old default (keeping a backup).
func Load(dataDir string) (*File, string, error) {
	path := filepath.Join(dataDir, "rules.yaml")
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.WriteFile(path, []byte(Default), 0o600); err != nil {
			return nil, "", err
		}
		b = []byte(Default)
	case err != nil:
		return nil, "", err
	case strings.ReplaceAll(string(b), "\r\n", "\n") == legacyDefaultV1:
		_ = os.WriteFile(filepath.Join(dataDir, "rules.v1.yaml"), b, 0o600)
		if err := os.WriteFile(path, []byte(Default), 0o600); err != nil {
			return nil, "", err
		}
		b = []byte(Default)
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
	f := File{GroupMin: 3}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("rules.yaml: %w", err)
	}
	if f.GroupMin < 2 {
		f.GroupMin = 2
	}
	for i, r := range f.Rules {
		label := r.Name
		if label == "" {
			label = "rule " + strconv.Itoa(i+1)
		}
		m := r.Match
		if len(m.Type) == 0 && len(m.Kind) == 0 && len(m.Ext) == 0 && m.Name == "" && m.OlderThanDays == 0 && m.MinSize == "" && m.MaxSize == "" {
			return nil, fmt.Errorf("%s: needs at least one match condition", label)
		}
		for _, t := range m.Type {
			if _, ok := Types[t]; !ok {
				return nil, fmt.Errorf("%s: unknown type %q", label, t)
			}
		}
		for _, k := range m.Kind {
			if !Kinds[k] {
				return nil, fmt.Errorf("%s: unknown kind %q (use screenshot, camera or recording)", label, k)
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
	info                          Info
}

type compiled struct {
	Rule
	folders  []string
	exts     map[string]bool
	kinds    map[string]bool
	min, max int64
}

// Plan matches rules against the index and returns a plan of moves and
// renames. Only loose files are considered unless a rule is recursive.
func Plan(db *index.DB, cfg *config.Config, f *File) (*plan.Plan, error) {
	rows, err := db.Query(`SELECT path, root, parent, name, ext, size, mtime FROM files WHERE placeholder=0 AND in_project=0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	var all []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.path, &c.root, &c.parent, &c.name, &c.ext, &c.size, &c.mtime); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rs := compile(cfg, f)
	var renameIn []string
	for _, in := range f.RenameIn {
		if p := cfg.ResolvePath(in); p != "" {
			renameIn = append(renameIn, p)
		}
	}
	// Only files some rule, rename folder or grouping could touch are
	// identified, since that reads a little of each file.
	var files []candidate
	for _, c := range all {
		if inScope(c, rs, renameIn) {
			c.info = Identify(c.path, c.name, c.mtime)
			files = append(files, c)
		}
	}

	groups := map[string]string{} // path -> group folder name
	if f.GroupRelated {
		groups = findGroups(files, f.GroupMin)
	}

	p := &plan.Plan{Kind: "sort", Created: time.Now().Unix()}
	taken := map[string]bool{} // destinations already used by this plan
	now := time.Now()
	for _, fl := range files {
		newName := fl.name
		renamed := false
		if f.RenameGeneric && fl.info.Generic {
			newName = SuggestName(fl.info)
			renamed = newName != fl.name
		} else if fl.info.Ext != strings.ToLower(filepath.Ext(fl.name)) && filepath.Ext(fl.name) == "" {
			newName, renamed = fl.name+fl.info.Ext, true // add a missing extension
		}

		destDir, rule, category := "", "", ""
		if g, ok := groups[fl.path]; ok {
			destDir, rule, category = groupDest(cfg, fl, g, files, groups), "Group: "+g, "group"
		} else if r := firstMatch(rs, fl, now); r != nil {
			destDir, rule, category = expand(r.MoveTo, fl, cfg), r.Name, "rule"
		} else if renamed && withinAny(fl.parent, renameIn, false) {
			destDir, rule, category = fl.parent, "Renames", "rename"
		}
		if destDir == "" || cfg.IsProtected(destDir) {
			continue
		}
		if fsutil.SamePath(destDir, fl.parent) && !renamed {
			continue // already where it belongs
		}
		dst := uniqueInPlan(filepath.Join(destDir, newName), fl.path, taken)
		if fsutil.SamePath(dst, fl.path) {
			continue
		}
		reason := rule
		if renamed {
			reason += " · renamed from " + fl.name
		}
		p.Actions = append(p.Actions, plan.Action{
			ID: len(p.Actions) + 1, Op: plan.OpMove, Category: category,
			Src: fl.path, Dst: dst, Size: fl.size, MTime: fl.mtime,
			Reason: reason, Selected: true, Rule: rule,
		})
	}
	return p, nil
}

func compile(cfg *config.Config, f *File) []compiled {
	var rs []compiled
	for _, r := range f.Rules {
		c := compiled{Rule: r, exts: map[string]bool{}, kinds: map[string]bool{}}
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
		for _, k := range r.Match.Kind {
			c.kinds[k] = true
		}
		c.min, _ = ParseSize(r.Match.MinSize)
		c.max, _ = ParseSize(r.Match.MaxSize)
		rs = append(rs, c)
	}
	return rs
}

func withinAny(dir string, folders []string, recursive bool) bool {
	for _, d := range folders {
		if fsutil.SamePath(dir, d) || (recursive && fsutil.Within(dir, d)) {
			return true
		}
	}
	return false
}

func inScope(c candidate, rs []compiled, renameIn []string) bool {
	if withinAny(c.parent, renameIn, false) {
		return true
	}
	for _, r := range rs {
		if withinAny(c.parent, r.folders, r.Recursive) {
			return true
		}
	}
	return false
}

func firstMatch(rs []compiled, fl candidate, now time.Time) *compiled {
	for i := range rs {
		r := &rs[i]
		if !withinAny(fl.parent, r.folders, r.Recursive) {
			continue
		}
		m := r.Match
		// A type match uses the detected type, so a file with a missing or
		// wrong extension still lands in the right place.
		if len(r.exts) > 0 && !r.exts[fl.info.Ext] && !typeMatches(m.Type, fl.info.Type) {
			continue
		}
		if len(r.kinds) > 0 && !r.kinds[fl.info.Kind] {
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
		return r
	}
	return nil
}

func typeMatches(types []string, t string) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

// uniqueInPlan makes sure two actions in one plan never target the same name.
func uniqueInPlan(dst, src string, taken map[string]bool) string {
	key := func(p string) string { return strings.ToLower(filepath.Clean(p)) }
	if !taken[key(dst)] {
		taken[key(dst)] = true
		return dst
	}
	dir, base := filepath.Split(dst)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; ; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if !taken[key(p)] || fsutil.SamePath(p, src) {
			taken[key(p)] = true
			return p
		}
	}
}

func expand(tmpl string, f candidate, cfg *config.Config) string {
	t := f.info.Date
	if t.IsZero() {
		t = time.Unix(0, f.mtime)
	}
	s := tokenRe.ReplaceAllStringFunc(tmpl, func(tok string) string {
		switch tok {
		case "{year}":
			return t.Format("2006")
		case "{month}":
			return t.Format("01")
		case "{day}":
			return t.Format("02")
		case "{type}":
			return f.info.Type
		case "{ext}":
			if e := strings.TrimPrefix(f.info.Ext, "."); e != "" {
				return e
			}
			return "no-extension"
		}
		return tok
	})
	return resolveDest(s, f.parent, cfg)
}

// resolveDest turns a move_to value into a folder: "@Pictures/x" is the
// Pictures known folder, ~/ and absolute paths are used as given, anything
// else is relative to the folder the file is in.
func resolveDest(s, folder string, cfg *config.Config) string {
	if strings.HasPrefix(s, "~") || filepath.IsAbs(s) || filepath.VolumeName(s) != "" {
		return cfg.ResolvePath(s)
	}
	if strings.HasPrefix(s, "@") {
		name, rest, _ := strings.Cut(filepath.ToSlash(s[1:]), "/")
		if base, ok := cfg.Known[strings.ToLower(name)]; ok && base != "" {
			return filepath.Join(base, filepath.FromSlash(rest))
		}
		return "" // unknown folder name: the rule doesn't apply
	}
	return filepath.Join(folder, filepath.FromSlash(s))
}

// libraryFor is where a group of same-type files goes.
var libraryFor = map[string]string{
	"image": "@Pictures", "video": "@Videos", "audio": "@Music",
	"document": "@Documents", "spreadsheet": "@Documents", "presentation": "@Documents", "ebook": "@Documents",
}

// groupDest sends a group of files that are all one type to that type's
// library folder; mixed groups get a folder next to where they are.
func groupDest(cfg *config.Config, fl candidate, name string, files []candidate, groups map[string]string) string {
	typ := ""
	mixed := false
	for _, o := range files {
		if groups[o.path] != name || !fsutil.SamePath(o.parent, fl.parent) {
			continue
		}
		if typ == "" {
			typ = o.info.Type
		} else if typ != o.info.Type {
			mixed = true
		}
	}
	if lib := libraryFor[typ]; !mixed && lib != "" {
		return resolveDest(lib+"/"+name, fl.parent, cfg)
	}
	return filepath.Join(fl.parent, name)
}

// Words too common to name a group after.
var groupStop = map[string]bool{
	"the": true, "new": true, "copy": true, "final": true, "untitled": true, "document": true, "file": true,
	"image": true, "photo": true, "video": true, "screenshot": true, "download": true, "scan": true,
	"whatsapp": true, "instagram": true, "img": true, "pxl": true, "vid": true,
}

// findGroups finds loose files in the same folder whose names start with
// the same word (split on spaces, punctuation and CamelCase), at least min
// of them. Generic names never form groups.
func findGroups(files []candidate, min int) map[string]string {
	type key struct{ parent, word string }
	members := map[key][]candidate{}
	display := map[key]string{}
	for _, f := range files {
		if f.info.Generic {
			continue
		}
		w := firstWord(strings.TrimSuffix(f.name, filepath.Ext(f.name)))
		if len([]rune(w)) < 4 || groupStop[strings.ToLower(w)] {
			continue
		}
		k := key{strings.ToLower(f.parent), strings.ToLower(w)}
		members[k] = append(members[k], f)
		if _, ok := display[k]; !ok {
			display[k] = w
		}
	}
	out := map[string]string{}
	keys := make([]key, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].word < keys[j].word })
	for _, k := range keys {
		if len(members[k]) < min {
			continue
		}
		for _, f := range members[k] {
			out[f.path] = SafeFileName(display[k])
		}
	}
	return out
}

// firstWord returns the leading word of a name: "AcmeLogo" → "Acme",
// "Acme_Voice_Nova" → "Acme", "company logo svg" → "company".
func firstWord(s string) string {
	runes := []rune(s)
	start := -1
	for i, r := range runes {
		isLetter := unicode.IsLetter(r)
		if start < 0 {
			if isLetter {
				start = i
			}
			continue
		}
		if !isLetter || (unicode.IsUpper(r) && i > start && unicode.IsLower(runes[i-1])) {
			return string(runes[start:i])
		}
	}
	if start < 0 {
		return ""
	}
	return string(runes[start:])
}
