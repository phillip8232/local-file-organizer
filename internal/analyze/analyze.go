// Package analyze turns the index into findings: duplicates, empty files and
// folders, junk, old downloads and large files. It only reads.
package analyze

import (
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"organizer/internal/config"
	"organizer/internal/fsutil"
	"organizer/internal/index"
	"organizer/internal/platform"
)

// File is one indexed file as the analyzers and UI see it.
type File struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix ns
	BTime int64  `json:"btime"` // unix ns, 0 if unknown
}

// DupGroup is a set of files with identical content.
type DupGroup struct {
	ID    string `json:"id"` // BLAKE3 hex
	Size  int64  `json:"size"`
	Files []File `json:"files"`
	Keep  int    `json:"keep"` // index into Files of the suggested keeper
}

// Report is everything the analyzers found.
type Report struct {
	Duplicates   []DupGroup  `json:"duplicates"`
	DupWaste     int64       `json:"dupWaste"`
	EmptyDirs    []string    `json:"emptyDirs"`
	EmptyFiles   []File      `json:"emptyFiles"`
	Junk         []File      `json:"junk"`
	OldDownloads []File      `json:"oldDownloads"`
	Large        []File      `json:"large"`
	Projects     []string    `json:"projects"`
	Stats        index.Stats `json:"stats"`
}

// Analyze runs every analyzer against the index.
func Analyze(db *index.DB, cfg *config.Config) (*Report, error) {
	r := &Report{}
	var err error
	if r.Stats, err = db.Stats(); err != nil {
		return nil, err
	}
	if r.Duplicates, err = duplicates(db, cfg); err != nil {
		return nil, err
	}
	for _, g := range r.Duplicates {
		r.DupWaste += g.Size * int64(len(g.Files)-1)
	}
	if r.EmptyDirs, err = emptyDirs(db, cfg); err != nil {
		return nil, err
	}
	actionable := `placeholder=0 AND in_project=0`
	if r.EmptyFiles, err = queryFiles(db, `SELECT path,size,mtime,btime FROM files WHERE `+actionable+` AND size=0 ORDER BY path`); err != nil {
		return nil, err
	}
	if r.Junk, err = junk(db); err != nil {
		return nil, err
	}
	if r.OldDownloads, err = oldDownloads(db, cfg); err != nil {
		return nil, err
	}
	if r.Large, err = queryFiles(db, `SELECT path,size,mtime,btime FROM files WHERE `+actionable+` ORDER BY size DESC LIMIT ?`, cfg.LargeTopN); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT path FROM dirs WHERE project=1 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		r.Projects = append(r.Projects, p)
	}
	return r, rows.Err()
}

func queryFiles(db *index.DB, q string, args ...any) ([]File, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Size, &f.MTime, &f.BTime); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func duplicates(db *index.DB, cfg *config.Config) ([]DupGroup, error) {
	rows, err := db.Query(`SELECT fullhash,size,path,mtime,btime FROM files
		WHERE placeholder=0 AND in_project=0 AND size>=? AND fullhash IN (
		  SELECT fullhash FROM files WHERE fullhash IS NOT NULL AND placeholder=0 AND in_project=0
		  GROUP BY fullhash HAVING COUNT(*)>1)
		ORDER BY size DESC, fullhash, path`, max(cfg.MinDuplicateSize, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []DupGroup
	for rows.Next() {
		var h []byte
		var f File
		if err := rows.Scan(&h, &f.Size, &f.Path, &f.MTime, &f.BTime); err != nil {
			return nil, err
		}
		id := hex.EncodeToString(h)
		if n := len(groups); n == 0 || groups[n-1].ID != id {
			groups = append(groups, DupGroup{ID: id, Size: f.Size})
		}
		g := &groups[len(groups)-1]
		g.Files = append(g.Files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hard links are one file under several names: deleting one frees
	// nothing and is not a duplicate. Collapse them.
	out := groups[:0]
	for _, g := range groups {
		seen := map[string]bool{}
		files := g.Files[:0]
		for _, f := range g.Files {
			id, _, err := platform.FileID(f.Path)
			if err != nil {
				continue // vanished since the scan
			}
			if !seen[id] {
				seen[id] = true
				files = append(files, f)
			}
		}
		if len(files) < 2 {
			continue
		}
		g.Files = files
		g.Keep = PickKeeper(g.Files, cfg)
		out = append(out, g)
	}
	return out, nil
}

var copyName = regexp.MustCompile(`(?i)( \(\d+\)| - copy( \(\d+\))?|_copy\d*| copy\d*)$`)

// PickKeeper chooses which copy to keep. "smart" prefers a file that isn't
// in Downloads or Desktop, doesn't look like "name (1)" or "name - Copy",
// then the oldest.
func PickKeeper(files []File, cfg *config.Config) int {
	age := func(f File) int64 {
		if f.BTime > 0 && f.BTime < f.MTime {
			return f.BTime
		}
		return f.MTime
	}
	best := 0
	score := func(f File) int {
		s := 0
		if cfg.DownloadsDir != "" && fsutil.Within(f.Path, cfg.DownloadsDir) {
			s += 2
		}
		if strings.EqualFold(filepath.Base(filepath.Dir(f.Path)), "Desktop") {
			s++
		}
		stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		if copyName.MatchString(stem) {
			s += 4
		}
		return s
	}
	for i := 1; i < len(files); i++ {
		a, b := files[i], files[best]
		switch cfg.Keep {
		case "newest":
			if age(a) > age(b) {
				best = i
			}
		case "oldest":
			if age(a) < age(b) {
				best = i
			}
		default:
			sa, sb := score(a), score(b)
			if sa < sb || (sa == sb && age(a) < age(b)) {
				best = i
			}
		}
	}
	return best
}

// emptyDirs finds folders holding nothing but empty folders and OS metadata
// files, listing only the topmost of each empty tree. Roots, projects and
// folders containing anything the walker skipped never count as empty.
func emptyDirs(db *index.DB, cfg *config.Config) ([]string, error) {
	type node struct {
		parent   string
		opaque   bool
		files    int
		children []string
	}
	nodes := map[string]*node{}
	rows, err := db.Query(`SELECT path,parent,opaque,in_project FROM dirs`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p, parent string
		var opaque, inProject bool
		if err := rows.Scan(&p, &parent, &opaque, &inProject); err != nil {
			rows.Close()
			return nil, err
		}
		nodes[p] = &node{parent: parent, opaque: opaque || inProject}
	}
	rows.Close()
	rows, err = db.Query(`SELECT parent, COUNT(*) FROM files GROUP BY parent`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if nd := nodes[p]; nd != nil {
			nd.files = n
		}
	}
	rows.Close()
	for p, n := range nodes {
		if par := nodes[n.parent]; par != nil && n.parent != p {
			par.children = append(par.children, p)
		}
	}
	memo := map[string]bool{}
	var empty func(string) bool
	empty = func(p string) bool {
		if v, ok := memo[p]; ok {
			return v
		}
		n := nodes[p]
		ok := n != nil && !n.opaque && n.files == 0
		if ok {
			for _, c := range n.children {
				if !empty(c) {
					ok = false
					break
				}
			}
		}
		memo[p] = ok
		return ok
	}
	isRoot := map[string]bool{}
	for _, r := range cfg.RootPaths {
		isRoot[r] = true
	}
	var out []string
	for p, n := range nodes {
		if isRoot[p] || cfg.IsProtected(p) || !empty(p) {
			continue
		}
		if !isRoot[n.parent] && empty(n.parent) {
			continue // the parent will be listed instead
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// Partial downloads and editor lock files. Only stale ones (older than a day)
// are listed, so an in-progress download is never touched.
var junkPattern = regexp.MustCompile(`(?i)(\.crdownload|\.part|\.partial|\.opdownload|\.tmp|\.temp)$|^~\$`)

func junk(db *index.DB) ([]File, error) {
	all, err := queryFiles(db, `SELECT path,size,mtime,btime FROM files WHERE placeholder=0 AND in_project=0
		AND (ext IN ('.crdownload','.part','.partial','.opdownload','.tmp','.temp') OR name LIKE '~$%') AND mtime<?`,
		time.Now().Add(-24*time.Hour).UnixNano())
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, f := range all {
		if junkPattern.MatchString(filepath.Base(f.Path)) {
			out = append(out, f)
		}
	}
	return out, nil
}

// oldDownloads lists files sitting directly in Downloads that haven't been
// created or modified in OldDownloadsDays. Subfolders are left alone: a
// folder someone made inside Downloads is organised already.
func oldDownloads(db *index.DB, cfg *config.Config) ([]File, error) {
	if cfg.DownloadsDir == "" || cfg.OldDownloadsDays <= 0 || cfg.RootOf(cfg.DownloadsDir) == "" {
		return nil, nil
	}
	cutoff := time.Now().AddDate(0, 0, -cfg.OldDownloadsDays).UnixNano()
	files, err := queryFiles(db, `SELECT path,size,mtime,btime FROM files WHERE placeholder=0 AND in_project=0
		AND parent=? AND mtime<? ORDER BY mtime`, cfg.DownloadsDir, cutoff)
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, f := range files {
		if f.BTime == 0 {
			f.BTime = platform.BirthTime(f.Path)
		}
		// A file copied or downloaded recently keeps an old mtime but has a
		// new birth time; that's not an old download.
		if f.BTime > cutoff {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}
