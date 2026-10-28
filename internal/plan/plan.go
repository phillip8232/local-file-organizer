// Package plan turns findings into a reviewable list of moves and deletes.
// A plan is a proposal only; the executor acts on the parts you approve.
package plan

import (
	"encoding/json"
	"fmt"
	"time"

	"organizer/internal/analyze"
	"organizer/internal/fsutil"
	"organizer/internal/index"
)

// Op values.
const (
	OpDelete = "delete" // to the holding area, then the OS trash
	OpMove   = "move"
)

// Action is one proposed change.
type Action struct {
	ID       int    `json:"id"`
	Op       string `json:"op"`
	Category string `json:"category"` // duplicate, empty-file, empty-dir, junk, old-download, large, rule
	Src      string `json:"src"`
	Dst      string `json:"dst,omitempty"`
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"`
	IsDir    bool   `json:"isDir,omitempty"`
	Hash     string `json:"hash,omitempty"` // duplicates: content the file must still have
	KeepPath string `json:"keep,omitempty"` // duplicates: the copy that must survive
	Reason   string `json:"reason"`
	Selected bool   `json:"selected"` // pre-ticked in review
}

// Plan is a stored proposal.
type Plan struct {
	ID      int64              `json:"id"`
	Kind    string             `json:"kind"` // cleanup | sort
	Created int64              `json:"created"`
	Actions []Action           `json:"actions"`
	Groups  []analyze.DupGroup `json:"groups,omitempty"`
}

// GroupChoice is the reviewer's decision for one duplicate group.
type GroupChoice struct {
	Keep   string   `json:"keep"`
	Delete []string `json:"delete"`
}

// Approval says which parts of a plan to carry out.
type Approval struct {
	IDs    []int                  `json:"ids"`
	Groups map[string]GroupChoice `json:"groups"`
}

// Cleanup builds a cleanup plan from a report. Safe categories are
// pre-selected; old downloads and large files are suggestions you opt into.
func Cleanup(r *analyze.Report) *Plan {
	p := &Plan{Kind: "cleanup", Created: time.Now().Unix(), Groups: r.Duplicates}
	add := func(a Action) {
		a.ID = len(p.Actions) + 1
		a.Op = OpDelete
		p.Actions = append(p.Actions, a)
	}
	for _, f := range r.EmptyFiles {
		add(Action{Category: "empty-file", Src: f.Path, MTime: f.MTime, Reason: "empty file", Selected: true})
	}
	for _, d := range r.EmptyDirs {
		add(Action{Category: "empty-dir", Src: d, IsDir: true, Reason: "folder holds nothing but empty folders", Selected: true})
	}
	for _, f := range r.Junk {
		add(Action{Category: "junk", Src: f.Path, Size: f.Size, MTime: f.MTime, Reason: "stale partial download or lock file", Selected: true})
	}
	for _, f := range r.OldDownloads {
		add(Action{Category: "old-download", Src: f.Path, Size: f.Size, MTime: f.MTime,
			Reason: "untouched in Downloads since " + time.Unix(0, f.MTime).Format("2 Jan 2006")})
	}
	for _, f := range r.Large {
		add(Action{Category: "large", Src: f.Path, Size: f.Size, MTime: f.MTime, Reason: "one of the largest files"})
	}
	return p
}

// DefaultApproval approves everything pre-selected, and for each duplicate
// group keeps the suggested copy and deletes the rest.
func (p *Plan) DefaultApproval() Approval {
	a := Approval{Groups: map[string]GroupChoice{}}
	for _, x := range p.Actions {
		if x.Selected {
			a.IDs = append(a.IDs, x.ID)
		}
	}
	for _, g := range p.Groups {
		c := GroupChoice{Keep: g.Files[g.Keep].Path}
		for i, f := range g.Files {
			if i != g.Keep {
				c.Delete = append(c.Delete, f.Path)
			}
		}
		a.Groups[g.ID] = c
	}
	return a
}

// Resolve checks an approval against the plan and returns the actions to run.
// Every path must come from the plan, and a duplicate group must keep one of
// its own files, which can't also be deleted.
func (p *Plan) Resolve(a Approval) ([]Action, error) {
	byID := map[int]Action{}
	for _, x := range p.Actions {
		byID[x.ID] = x
	}
	var out []Action
	seen := map[string]bool{}
	for _, id := range a.IDs {
		x, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("action %d is not in plan %d", id, p.ID)
		}
		if seen[x.Src] {
			continue
		}
		seen[x.Src] = true
		out = append(out, x)
	}
	groups := map[string]analyze.DupGroup{}
	for _, g := range p.Groups {
		groups[g.ID] = g
	}
	for gid, c := range a.Groups {
		if len(c.Delete) == 0 {
			continue
		}
		g, ok := groups[gid]
		if !ok {
			return nil, fmt.Errorf("duplicate group %s is not in plan %d", gid, p.ID)
		}
		members := map[string]analyze.File{}
		for _, f := range g.Files {
			members[f.Path] = f
		}
		if _, ok := members[c.Keep]; !ok {
			return nil, fmt.Errorf("group %s: keeper %q is not in the group", gid[:12], c.Keep)
		}
		for _, d := range c.Delete {
			f, ok := members[d]
			if !ok {
				return nil, fmt.Errorf("group %s: %q is not in the group", gid[:12], d)
			}
			if fsutil.SamePath(d, c.Keep) {
				return nil, fmt.Errorf("group %s: can't keep and delete %q", gid[:12], d)
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, Action{ID: -1, Op: OpDelete, Category: "duplicate", Src: d, Size: f.Size, MTime: f.MTime,
				Hash: gid, KeepPath: c.Keep, Reason: "duplicate of " + c.Keep})
		}
	}
	// A path kept in one group must not be deleted by another selection.
	for _, c := range a.Groups {
		if len(c.Delete) > 0 && seen[c.Keep] {
			return nil, fmt.Errorf("%q is both kept and selected for deletion", c.Keep)
		}
	}
	return out, nil
}

// Save stores the plan and sets its ID.
func Save(db *index.DB, p *Plan) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	res, err := db.Exec(`INSERT INTO plans(created, kind, body) VALUES(?,?,?)`, p.Created, p.Kind, string(b))
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	return err
}

// Load reads a stored plan.
func Load(db *index.DB, id int64) (*Plan, error) {
	var body string
	if err := db.QueryRow(`SELECT body FROM plans WHERE id=?`, id).Scan(&body); err != nil {
		return nil, fmt.Errorf("plan %d: %w", id, err)
	}
	var p Plan
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return nil, err
	}
	p.ID = id
	return &p, nil
}
