// Command organizer sorts files by rules and suggests deletions you approve.
// Nothing is moved or deleted without an explicit approve step; deletes go to
// a holding area first and every batch can be undone.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillip8232/local-file-organizer/internal/analyze"
	"github.com/phillip8232/local-file-organizer/internal/balance"
	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/executor"
	"github.com/phillip8232/local-file-organizer/internal/index"
	"github.com/phillip8232/local-file-organizer/internal/plan"
	"github.com/phillip8232/local-file-organizer/internal/platform"
	"github.com/phillip8232/local-file-organizer/internal/rules"
	"github.com/phillip8232/local-file-organizer/internal/server"
	"github.com/phillip8232/local-file-organizer/internal/trash"
)

const usage = `organizer: sort files by rules and clean up with approval.

Usage:
  organizer ui [--port N] [--no-browser]   open the review page (recommended)
  organizer scan                           index the configured folders
  organizer report [--limit N] [--json]    show what the analyzers found
  organizer plan cleanup|sort|balance      build a plan to review
  organizer drives                         show drives and free space
  organizer apply <plan> [--include c,c] [--yes]
                                           carry out a plan's selected actions
  organizer history                        list past batches
  organizer show <batch>                   list a batch's steps
  organizer undo <batch>                   reverse a batch
  organizer purge [--days N] [--permanent] empty old items from the holding area
  organizer rules                          check rules.yaml
  organizer where                          show where settings and data live

Global flag: --data DIR (default %s)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type app struct {
	cfg    *config.Config
	db     *index.DB
	ex     *executor.Executor
	unlock func()
}

// open loads settings and, when the command needs the database, takes the
// data folder's lock and opens it. Only one organizer at a time may use a
// data folder, so a scan and the review page can never write at once.
func open(dataDir string, needDB bool) (*app, error) {
	cfg, err := config.Load(dataDir)
	if err != nil {
		return nil, err
	}
	a := &app{cfg: cfg, unlock: func() {}}
	if !needDB {
		return a, nil
	}
	unlock, err := platform.Lock(filepath.Join(dataDir, "organizer.lock"))
	if errors.Is(err, platform.ErrLocked) {
		return nil, errors.New("organizer is already running with this data folder (probably the review page in another window).\n" +
			"Use that window, or stop it with Ctrl+C and try again")
	}
	if err != nil {
		return nil, err
	}
	a.unlock = unlock
	if a.db, err = index.Open(filepath.Join(dataDir, "organizer.db")); err != nil {
		unlock()
		return nil, err
	}
	if a.db.Recovered != "" {
		fmt.Fprintln(os.Stderr, "note:", a.db.Recovered)
	}
	if a.ex, err = executor.New(a.db, cfg, trash.Move); err != nil {
		a.close()
		return nil, err
	}
	if _, _, err := rules.Load(dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	return a, nil
}

func (a *app) close() {
	if a.db != nil {
		a.db.Close()
	}
	a.unlock()
}

func run(args []string) error {
	dataDir := config.DefaultDataDir()
	// --data may come before the subcommand.
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch {
		case args[0] == "--data" || args[0] == "-data":
			if len(args) < 2 {
				return errors.New("--data needs a folder")
			}
			dataDir, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--data="):
			dataDir, args = strings.TrimPrefix(args[0], "--data="), args[1:]
		case args[0] == "-h" || args[0] == "--help":
			fmt.Printf(usage, dataDir)
			return nil
		default:
			return fmt.Errorf("unknown flag %s", args[0])
		}
	}
	if len(args) == 0 {
		fmt.Printf(usage, dataDir)
		return nil
	}
	cmd, args := args[0], args[1:]
	if cmd == "help" {
		fmt.Printf(usage, dataDir)
		return nil
	}
	needDB := cmd != "rules" && cmd != "drives" && cmd != "where"
	a, err := open(dataDir, needDB)
	if err != nil {
		return err
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch cmd {
	case "scan":
		return a.scan(ctx)
	case "report":
		return a.report(args)
	case "plan":
		return a.plan(args)
	case "apply":
		return a.apply(ctx, args)
	case "history":
		return a.history()
	case "show":
		return a.show(args)
	case "undo":
		return a.undo(args)
	case "purge":
		return a.purge(args)
	case "rules":
		return a.rules()
	case "drives":
		return a.drives()
	case "where":
		return a.where()
	case "ui":
		return a.ui(ctx, args)
	}
	return fmt.Errorf("unknown command %q (try organizer help)", cmd)
}

func (a *app) scan(ctx context.Context) error {
	if len(a.cfg.RootPaths) == 0 {
		return errors.New("no folders to scan; set roots in config.yaml")
	}
	fmt.Println("Scanning:")
	for _, r := range a.cfg.RootPaths {
		fmt.Println("  " + r)
	}
	s := &index.Scanner{DB: a.db, Cfg: a.cfg}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case err := <-done:
			p := s.Progress()
			fmt.Fprintf(os.Stderr, "\r%-90s\r", "")
			if err != nil {
				return err
			}
			fmt.Printf("Indexed %s files (%s) in %s; %s unreadable or locked entries skipped.\n",
				commas(p.Files), human(p.Bytes), time.Duration(p.Elapsed)*time.Millisecond, commas(p.Skipped))
			fmt.Println("Next: organizer report, or organizer ui")
			return nil
		case <-t.C:
			p := s.Progress()
			line := fmt.Sprintf("%s: %s files, %s", p.Phase, commas(p.Files), human(p.Bytes))
			if p.Phase == "prehash" || p.Phase == "hashing" {
				line = fmt.Sprintf("%s: %s / %s files", p.Phase, commas(p.Hashed), commas(p.ToHash))
			}
			fmt.Fprintf(os.Stderr, "\r%-90s", line)
		}
	}
}

func (a *app) report(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	limit := fs.Int("limit", 10, "items to list per section")
	asJSON := fs.Bool("json", false, "print the full report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := analyze.Analyze(a.db, a.cfg)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	if r.Stats.LastScan == 0 {
		fmt.Println("No scan yet: run organizer scan first.")
		return nil
	}
	fmt.Printf("Index: %s files, %s, last scan %s\n", commas(r.Stats.Files), human(r.Stats.Bytes),
		time.Unix(r.Stats.LastScan, 0).Format("2 Jan 2006 15:04"))
	fmt.Printf("Left alone: %d project folders, %s cloud/online-only files\n\n", len(r.Projects), commas(r.Stats.Placeholders))
	fmt.Printf("Duplicates: %d groups, %s reclaimable\n", len(r.Duplicates), human(r.DupWaste))
	for i, g := range r.Duplicates {
		if i == *limit {
			fmt.Printf("  … %d more\n", len(r.Duplicates)-i)
			break
		}
		fmt.Printf("  %s × %d\n", human(g.Size), len(g.Files))
		for j, f := range g.Files {
			mark := "delete"
			if j == g.Keep {
				mark = "keep  "
			}
			fmt.Printf("    %s %s\n", mark, f.Path)
		}
	}
	section := func(title string, files []analyze.File) {
		var total int64
		for _, f := range files {
			total += f.Size
		}
		fmt.Printf("\n%s: %d (%s)\n", title, len(files), human(total))
		for i, f := range files {
			if i == *limit {
				fmt.Printf("  … %d more\n", len(files)-i)
				break
			}
			fmt.Printf("  %9s  %s\n", human(f.Size), f.Path)
		}
	}
	fmt.Printf("\nEmpty folders: %d\n", len(r.EmptyDirs))
	for i, d := range r.EmptyDirs {
		if i == *limit {
			fmt.Printf("  … %d more\n", len(r.EmptyDirs)-i)
			break
		}
		fmt.Println("  " + d)
	}
	section("Empty files", r.EmptyFiles)
	section("Junk (stale partial downloads, lock files)", r.Junk)
	section(fmt.Sprintf("Old downloads (> %d days)", a.cfg.OldDownloadsDays), r.OldDownloads)
	section("Largest files", r.Large)
	return nil
}

func (a *app) plan(args []string) error {
	if len(args) != 1 || (args[0] != "cleanup" && args[0] != "sort" && args[0] != "balance") {
		return errors.New("usage: organizer plan cleanup|sort|balance")
	}
	var p *plan.Plan
	switch args[0] {
	case "cleanup":
		r, err := analyze.Analyze(a.db, a.cfg)
		if err != nil {
			return err
		}
		p = plan.Cleanup(r)
	case "sort":
		rf, _, err := rules.Load(a.cfg.DataDir)
		if err != nil {
			return err
		}
		if p, err = rules.Plan(a.db, a.cfg, rf); err != nil {
			return err
		}
	case "balance":
		vols, err := platform.Volumes()
		if err != nil {
			return err
		}
		res := balance.Build(a.cfg, vols, time.Now())
		for _, d := range res.Drives {
			fmt.Printf("  %-4s %3.0f%% used → %3.0f%% after the ticked moves (equal share %3.0f%%)\n", d.Root,
				pct(d.Used(), d.Total), pct(d.Planned, d.Total), pct(d.Target, d.Total))
		}
		if res.Note != "" {
			fmt.Println("  " + res.Note)
		}
		p = res.Plan
	}
	if err := plan.Save(a.db, p); err != nil {
		return err
	}
	printPlan(p)
	fmt.Printf("\nPlan %d saved. Nothing has changed yet.\n", p.ID)
	fmt.Printf("Review in the browser with organizer ui, or apply the pre-selected items with:\n  organizer apply %d\n", p.ID)
	return nil
}

func printPlan(p *plan.Plan) {
	type tally struct {
		n, sel int
		bytes  int64
	}
	t := map[string]*tally{}
	var order []string
	for _, x := range p.Actions {
		if t[x.Category] == nil {
			t[x.Category] = &tally{}
			order = append(order, x.Category)
		}
		t[x.Category].n++
		t[x.Category].bytes += x.Size
		if x.Selected {
			t[x.Category].sel++
		}
	}
	if len(p.Groups) > 0 {
		var n int
		var b int64
		for _, g := range p.Groups {
			n += len(g.Files) - 1
			b += g.Size * int64(len(g.Files)-1)
		}
		fmt.Printf("  %-14s %6d files  %9s  (keep 1 of each group, selected)\n", "duplicate", n, human(b))
	}
	for _, c := range order {
		x := t[c]
		note := "selected"
		if x.sel == 0 {
			note = "not selected; add with --include " + c
		}
		fmt.Printf("  %-14s %6d items  %9s  (%s)\n", c, x.n, human(x.bytes), note)
	}
	if p.Kind == "sort" || p.Kind == "balance" {
		for i, x := range p.Actions {
			if i == 15 {
				fmt.Printf("  … %d more\n", len(p.Actions)-i)
				break
			}
			mark := " "
			if !x.Selected {
				mark = "?" // offered, not selected
			}
			fmt.Printf("  %s %s  [%s]\n      → %s\n", mark, x.Src, x.Reason, x.Dst)
		}
	}
	if len(p.Actions) == 0 && len(p.Groups) == 0 {
		fmt.Println("  Nothing to do.")
	}
}

func (a *app) apply(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	include := fs.String("include", "", "also apply these unselected categories, e.g. old-download,large")
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: organizer apply <plan> [--include c,c] [--yes]")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return err
	}
	p, err := plan.Load(a.db, id)
	if err != nil {
		return err
	}
	appr := p.DefaultApproval()
	extra := map[string]bool{}
	for _, c := range strings.Split(*include, ",") {
		if c = strings.TrimSpace(c); c != "" {
			extra[c] = true
		}
	}
	for _, x := range p.Actions {
		if !x.Selected && extra[x.Category] {
			appr.IDs = append(appr.IDs, x.ID)
		}
	}
	actions, err := p.Resolve(appr)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		fmt.Println("Nothing selected.")
		return nil
	}
	var moves, deletes int
	var bytes int64
	for _, x := range actions {
		if x.Op == plan.OpMove {
			moves++
		} else {
			deletes++
			bytes += x.Size
		}
	}
	fmt.Printf("Plan %d: %d moves, %d deletes (%s).\n", p.ID, moves, deletes, human(bytes))
	if deletes > 0 {
		fmt.Printf("Deleted items go to the holding area for %d days, then the OS trash.\n", a.cfg.HoldingDays)
	}
	fmt.Println("Each file is re-checked first; anything that changed since the scan is skipped.")
	if !*yes && !confirm("Type yes to go ahead: ", "yes") {
		fmt.Println("Cancelled; nothing changed.")
		return nil
	}
	summary := fmt.Sprintf("%s plan %d: %d moves, %d deletes", p.Kind, p.ID, moves, deletes)
	res, err := withProgress(a.ex, func() (*executor.Result, error) { return a.ex.Apply(ctx, p.ID, summary, actions) })
	if err != nil {
		return err
	}
	printResult(res)
	fmt.Printf("Batch %d. Undo with: organizer undo %d\n", res.Batch, res.Batch)
	return nil
}

func (a *app) history() error {
	bs, err := a.ex.History(50)
	if err != nil {
		return err
	}
	if len(bs) == 0 {
		fmt.Println("No batches yet.")
	}
	for _, b := range bs {
		var parts []string
		keys := make([]string, 0, len(b.Counts))
		for k := range b.Counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%d %s", b.Counts[k], k))
		}
		fmt.Printf("#%-4d %s  %-13s %s  [%s]\n", b.ID, time.Unix(b.Created, 0).Format("2006-01-02 15:04"), b.State, b.Summary, strings.Join(parts, ", "))
	}
	h, err := a.ex.Holding()
	if err == nil && h.Items > 0 {
		fmt.Printf("\nHolding area: %d items, %s (kept %d days)\n", h.Items, human(h.Bytes), h.Days)
	}
	return nil
}

func (a *app) show(args []string) error {
	id, err := batchArg(args, "show")
	if err != nil {
		return err
	}
	steps, err := a.ex.Steps(id)
	if err != nil {
		return err
	}
	for _, s := range steps {
		line := fmt.Sprintf("%-7s %-6s %s", s.State, s.Op, s.Src)
		if s.Dst != "" {
			line += "  →  " + s.Dst
		}
		if s.Error != "" {
			line += "  (" + s.Error + ")"
		}
		fmt.Println(line)
	}
	return nil
}

func (a *app) undo(args []string) error {
	id, err := batchArg(args, "undo")
	if err != nil {
		return err
	}
	res, err := withProgress(a.ex, func() (*executor.Result, error) { return a.ex.Undo(id) })
	if err != nil {
		return err
	}
	printResult(res)
	return nil
}

// withProgress runs fn, printing a progress line while it works (copies
// between drives can take minutes).
func withProgress(ex *executor.Executor, fn func() (*executor.Result, error)) (*executor.Result, error) {
	type out struct {
		r   *executor.Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		r, err := fn()
		done <- out{r, err}
	}()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case o := <-done:
			fmt.Fprintf(os.Stderr, "\r%-100s\r", "")
			return o.r, o.err
		case <-t.C:
			p := ex.Progress()
			line := fmt.Sprintf("%d of %d items", p.ItemsDone, p.Items)
			if p.Bytes > 0 {
				line += fmt.Sprintf(", %s of %s copied", human(p.BytesDone), human(p.Bytes))
			}
			fmt.Fprintf(os.Stderr, "\r%-100s", line)
		}
	}
}

func (a *app) drives() error {
	vols, err := platform.Volumes()
	if err != nil {
		return err
	}
	for _, v := range vols {
		label := v.Label
		if v.System {
			label += " (system)"
		}
		fmt.Printf("%-4s %-22s %9s of %9s used (%3.0f%%), %9s free\n", v.Root, label, human(v.Used()), human(v.Total), pct(v.Used(), v.Total), human(v.Free))
	}
	return nil
}

func pct(n, total int64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

func (a *app) purge(args []string) error {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	days := fs.Int("days", a.cfg.HoldingDays, "purge items held at least this many days (0 = all)")
	permanent := fs.Bool("permanent", false, "delete permanently instead of using the OS trash")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *permanent && !confirm("This deletes the items forever, skipping the OS trash. Type delete forever: ", "delete forever") {
		fmt.Println("Cancelled; nothing changed.")
		return nil
	}
	res, err := a.ex.Purge(time.Duration(*days)*24*time.Hour, *permanent)
	if err != nil {
		return err
	}
	where := "the OS trash"
	if *permanent {
		where = "permanent deletion"
	}
	fmt.Printf("%d items (%s) sent to %s.\n", res.Done, human(res.Bytes), where)
	for _, m := range res.Messages {
		fmt.Println("  " + m)
	}
	return nil
}

func (a *app) rules() error {
	f, _, err := rules.Load(a.cfg.DataDir)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d rules OK\n", filepath.Join(a.cfg.DataDir, "rules.yaml"), len(f.Rules))
	for _, r := range f.Rules {
		fmt.Printf("  %-22s → %s\n", r.Name, r.MoveTo)
	}
	return nil
}

func (a *app) where() error {
	d := a.cfg.DataDir
	fmt.Println("Data folder: ", d)
	fmt.Println("Settings:    ", filepath.Join(d, "config.yaml"))
	fmt.Println("Rules:       ", filepath.Join(d, "rules.yaml"))
	fmt.Println("Index:       ", filepath.Join(d, "organizer.db"))
	fmt.Println("Holding area:", filepath.Join(d, "holding"))
	fmt.Println("Folders organised:")
	for _, r := range a.cfg.RootPaths {
		fmt.Println("  " + r)
	}
	return nil
}

func (a *app) ui(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	port := fs.Int("port", 0, "port on 127.0.0.1 (0 picks a free one)")
	noBrowser := fs.Bool("no-browser", false, "don't open the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	autoPurge(a)
	srv := server.New(a.cfg, a.db, a.ex)
	return srv.Run(ctx, *port, !*noBrowser)
}

// autoPurge moves holding items past their holding period to the OS trash.
func autoPurge(a *app) {
	res, err := a.ex.Purge(time.Duration(a.cfg.HoldingDays)*24*time.Hour, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: purge:", err)
		return
	}
	if res.Done > 0 {
		fmt.Printf("Moved %d items held over %d days to the OS trash.\n", res.Done, a.cfg.HoldingDays)
	}
}

func printResult(r *executor.Result) {
	fmt.Printf("Done: %d, skipped: %d, failed: %d (%s)\n", r.Done, r.Skipped, r.Failed, human(r.Bytes))
	for i, m := range r.Messages {
		if i == 30 {
			fmt.Printf("  … %d more messages (organizer show %d)\n", len(r.Messages)-i, r.Batch)
			break
		}
		fmt.Println("  " + m)
	}
}

func batchArg(args []string, cmd string) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("usage: organizer %s <batch>", cmd)
	}
	return strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
}

// reorder puts flags before positional args so "apply 3 --yes" works.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && args[i] == "--include" && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, args[i])
	}
	return append(flags, pos...)
}

func confirm(prompt, want string) bool {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), want)
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
