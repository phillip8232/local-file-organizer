// Package server is the review page: a small HTTP server on 127.0.0.1 with
// a per-run secret, serving an embedded single-page UI and a JSON API.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phillip8232/local-file-organizer/internal/analyze"
	"github.com/phillip8232/local-file-organizer/internal/balance"
	"github.com/phillip8232/local-file-organizer/internal/config"
	"github.com/phillip8232/local-file-organizer/internal/executor"
	"github.com/phillip8232/local-file-organizer/internal/fsutil"
	"github.com/phillip8232/local-file-organizer/internal/index"
	"github.com/phillip8232/local-file-organizer/internal/plan"
	"github.com/phillip8232/local-file-organizer/internal/platform"
	"github.com/phillip8232/local-file-organizer/internal/rules"
)

//go:embed static
var static embed.FS

const cookieName = "organizer_session"

// Server serves the review UI.
type Server struct {
	cfg   *config.Config
	db    *index.DB
	ex    *executor.Executor
	token string
	addr  string

	mu         sync.Mutex
	busy       string // "", "scan", "apply", "undo" or "purge"
	scanner    *index.Scanner
	scanStop   context.CancelFunc
	lastErr    string
	lastResult *jobResult // the latest finished apply or undo
}

// jobResult is a finished background apply or undo, for the page to show.
type jobResult struct {
	Job    string           `json:"job"`
	ID     int64            `json:"id"` // increases with every job
	Result *executor.Result `json:"result,omitempty"`
	Error  string           `json:"error,omitempty"`
}

func New(cfg *config.Config, db *index.DB, ex *executor.Executor) *Server {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return &Server{cfg: cfg, db: db, ex: ex, token: hex.EncodeToString(b)}
}

// Run listens on 127.0.0.1 only and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context, port int, openBrowser bool) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	url := "http://" + s.addr + "/?t=" + s.token
	fmt.Println("Review page:", url)
	fmt.Println("Only this computer can open it. Press Ctrl+C to stop.")
	if openBrowser {
		if err := platform.OpenBrowser(url); err != nil {
			fmt.Println("Couldn't open the browser; open the link above.")
		}
	}
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if s.scanStop != nil {
			s.scanStop()
		}
		s.mu.Unlock()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/scan", s.startScan)
	mux.HandleFunc("POST /api/scan/cancel", s.cancelScan)
	mux.HandleFunc("GET /api/report", s.report)
	mux.HandleFunc("POST /api/plan/cleanup", s.planCleanup)
	mux.HandleFunc("POST /api/plan/sort", s.planSort)
	mux.HandleFunc("POST /api/plan/balance", s.planBalance)
	mux.HandleFunc("GET /api/drives", s.drives)
	mux.HandleFunc("POST /api/plan/{id}/apply", s.apply)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/batch/{id}", s.batch)
	mux.HandleFunc("POST /api/batch/{id}/undo", s.undo)
	mux.HandleFunc("POST /api/purge", s.purge)
	mux.HandleFunc("GET /api/rules", s.getRules)
	mux.HandleFunc("PUT /api/rules", s.putRules)
	mux.HandleFunc("POST /api/reveal", s.reveal)
	return s.guard(mux)
}

// guard enforces: the right Host (blocks DNS rebinding), the session cookie
// (set from the one-time link), and a custom header on writes (blocks
// cross-site form posts).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(s.addr)
		if r.Host != s.addr && r.Host != "localhost:"+port {
			http.Error(w, "wrong host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		if t := r.URL.Query().Get("t"); t != "" && r.Method == http.MethodGet && r.URL.Path == "/" {
			if !s.validToken(t) {
				http.Error(w, "this link has expired; use the one printed in the terminal", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !s.validToken(c.Value) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, "Open the link printed in the terminal where you ran organizer ui.")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Organizer") != "1" {
			http.Error(w, "missing request header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validToken(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// claim marks the server busy with job, or reports what it's busy with.
func (s *Server) claim(job string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy != "" {
		return fmt.Errorf("busy: a %s is running", s.busy)
	}
	s.busy = job
	return nil
}

func (s *Server) release() {
	s.mu.Lock()
	s.busy = ""
	s.mu.Unlock()
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	st, err := s.db.Stats()
	if err != nil {
		fail(w, 500, err)
		return
	}
	h, _ := s.ex.Holding()
	s.mu.Lock()
	out := map[string]any{
		"stats": st, "holding": h, "busy": s.busy, "lastError": s.lastErr,
		"roots": s.cfg.RootPaths, "dataDir": s.cfg.DataDir,
		"oldDownloadsDays": s.cfg.OldDownloadsDays, "holdingDays": s.cfg.HoldingDays,
	}
	if s.scanner != nil {
		out["scan"] = s.scanner.Progress()
	}
	if s.busy == "apply" || s.busy == "undo" {
		out["progress"] = s.ex.Progress()
	}
	if s.lastResult != nil {
		out["lastResult"] = s.lastResult
	}
	s.mu.Unlock()
	writeJSON(w, out)
}

// runJob runs an apply or undo in the background; the page follows its
// progress through /api/status and shows lastResult when it's done.
func (s *Server) runJob(job string, fn func() (*executor.Result, error)) {
	go func() {
		defer s.release()
		res, err := fn()
		jr := &jobResult{Job: job, Result: res}
		if err != nil {
			jr.Error = err.Error()
		}
		s.mu.Lock()
		if s.lastResult != nil {
			jr.ID = s.lastResult.ID + 1
		} else {
			jr.ID = 1
		}
		s.lastResult = jr
		s.mu.Unlock()
	}()
}

func (s *Server) drives(w http.ResponseWriter, _ *http.Request) {
	vols, err := platform.Volumes()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"drives": vols})
}

func (s *Server) planBalance(w http.ResponseWriter, _ *http.Request) {
	vols, err := platform.Volumes()
	if err != nil {
		fail(w, 500, err)
		return
	}
	res := balance.Build(s.cfg, vols, time.Now())
	if err := plan.Save(s.db, res.Plan); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) startScan(w http.ResponseWriter, _ *http.Request) {
	if len(s.cfg.RootPaths) == 0 {
		fail(w, 400, errors.New("no folders to scan; set roots in config.yaml"))
		return
	}
	if err := s.claim("scan"); err != nil {
		fail(w, 409, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sc := &index.Scanner{DB: s.db, Cfg: s.cfg}
	s.mu.Lock()
	s.scanner, s.scanStop, s.lastErr = sc, cancel, ""
	s.mu.Unlock()
	go func() {
		err := sc.Run(ctx)
		s.mu.Lock()
		if err != nil {
			s.lastErr = "scan: " + err.Error()
		}
		s.busy, s.scanStop = "", nil
		s.mu.Unlock()
		cancel()
	}()
	writeJSON(w, map[string]bool{"started": true})
}

func (s *Server) cancelScan(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if s.scanStop != nil {
		s.scanStop()
	}
	s.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) report(w http.ResponseWriter, _ *http.Request) {
	r, err := analyze.Analyze(s.db, s.cfg)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, r)
}

func (s *Server) planCleanup(w http.ResponseWriter, _ *http.Request) {
	r, err := analyze.Analyze(s.db, s.cfg)
	if err != nil {
		fail(w, 500, err)
		return
	}
	p := plan.Cleanup(r)
	if err := plan.Save(s.db, p); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"plan": p, "report": r})
}

func (s *Server) planSort(w http.ResponseWriter, _ *http.Request) {
	rf, _, err := rules.Load(s.cfg.DataDir)
	if err != nil {
		fail(w, 400, err)
		return
	}
	p, err := rules.Plan(s.db, s.cfg, rf)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if err := plan.Save(s.db, p); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"plan": p})
}

func idParam(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	var appr plan.Approval
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&appr); err != nil {
		fail(w, 400, err)
		return
	}
	p, err := plan.Load(s.db, id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	actions, err := p.Resolve(appr)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if len(actions) == 0 {
		fail(w, 400, errors.New("nothing selected"))
		return
	}
	if err := s.claim("apply"); err != nil {
		fail(w, 409, err)
		return
	}
	var moves, deletes int
	for _, a := range actions {
		if a.Op == plan.OpMove {
			moves++
		} else {
			deletes++
		}
	}
	summary := fmt.Sprintf("%s plan %d: %d moves, %d deletes", p.Kind, p.ID, moves, deletes)
	s.runJob("apply", func() (*executor.Result, error) {
		return s.ex.Apply(context.Background(), p.ID, summary, actions)
	})
	writeJSON(w, map[string]bool{"started": true})
}

func (s *Server) history(w http.ResponseWriter, _ *http.Request) {
	bs, err := s.ex.History(100)
	if err != nil {
		fail(w, 500, err)
		return
	}
	h, _ := s.ex.Holding()
	writeJSON(w, map[string]any{"batches": bs, "holding": h})
}

func (s *Server) batch(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	steps, err := s.ex.Steps(id)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"steps": steps})
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.claim("undo"); err != nil {
		fail(w, 409, err)
		return
	}
	s.runJob("undo", func() (*executor.Result, error) { return s.ex.Undo(id) })
	writeJSON(w, map[string]bool{"started": true})
}

// purge sends held items to the OS trash. Permanent deletion is CLI-only.
func (s *Server) purge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Days < 0 {
		fail(w, 400, errors.New("bad request"))
		return
	}
	if err := s.claim("purge"); err != nil {
		fail(w, 409, err)
		return
	}
	defer s.release()
	res, err := s.ex.Purge(time.Duration(req.Days)*24*time.Hour, false)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) getRules(w http.ResponseWriter, _ *http.Request) {
	_, text, err := rules.Load(s.cfg.DataDir)
	out := map[string]string{"text": text}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

func (s *Server) putRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	if err := rules.Save(s.cfg.DataDir, req.Text); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// reveal shows a file in Explorer/Finder. It never opens or runs the file,
// and only for paths inside the organised folders.
func (s *Server) reveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, err)
		return
	}
	if s.cfg.RootOf(req.Path) == "" && !fsutil.Within(req.Path, s.cfg.DataDir) || strings.ContainsRune(req.Path, 0) {
		fail(w, 400, errors.New("not an organised path"))
		return
	}
	if err := platform.Reveal(req.Path); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
