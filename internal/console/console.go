// Package console serves the Seamless observability UI: server-rendered
// html/template pages plus an SSE feed, with no node/npm/React or build step.
// It is read-mostly -- the only writes are archiving a memory and
// applying/dismissing a gardener proposal. Access is guarded by the same static
// bearer key as the MCP surface: a browser trades the key for a cookie at
// /console/login, and the seam CLI presents the key as a bearer token.
package console

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/files"
	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/store"
)

// cookieName holds the console session token (a hash of the static key, never
// the key itself). It is HttpOnly and scoped to /console.
const cookieName = "seamless_console"

// Config wires the console's dependencies. Files/Gardener/Events are used by the
// write actions (archive, apply/dismiss) and the live feed; DB backs every read.
type Config struct {
	DB          *sql.DB
	Files       *files.Manager
	Gardener    *gardener.Service
	Events      *events.Recorder
	APIKey      string
	DataDir     string // for resolving memory/note file paths to absolute editor links
	Budgets     config.Budgets
	GardenerCfg config.Gardener // for the Settings page (read-only display)
	Logger      *slog.Logger
}

// Service renders the console and serves its routes.
type Service struct {
	cfg    Config
	logger *slog.Logger
	pages  map[string]*template.Template
}

// New builds a console Service, parsing its templates once.
func New(cfg Config) (*Service, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	pages, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, logger: logger, pages: pages}, nil
}

// Register mounts the console routes on mux under /console. Public routes are the
// login page and the stylesheet; everything else requires the key.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /console/static/console.css", s.serveCSS)
	mux.HandleFunc("GET /console/login", s.loginForm)
	mux.HandleFunc("POST /console/login", s.loginSubmit)
	mux.HandleFunc("POST /console/logout", s.logout)

	mux.HandleFunc("GET /console/{$}", s.auth(s.overview))
	mux.HandleFunc("GET /console/sessions", s.auth(s.sessionsList))
	mux.HandleFunc("GET /console/sessions/{id}", s.auth(s.sessionDetail))
	mux.HandleFunc("GET /console/memories", s.auth(s.memoriesList))
	mux.HandleFunc("POST /console/memories/{id}/archive", s.auth(s.memoryArchive))
	mux.HandleFunc("GET /console/retrieval", s.auth(s.retrieval))
	mux.HandleFunc("GET /console/tasks", s.auth(s.tasks))
	mux.HandleFunc("GET /console/gardener", s.auth(s.gardenerPage))
	mux.HandleFunc("POST /console/gardener/{id}/apply", s.auth(s.gardenerApply))
	mux.HandleFunc("POST /console/gardener/{id}/dismiss", s.auth(s.gardenerDismiss))
	mux.HandleFunc("GET /console/settings", s.auth(s.settings))
	mux.HandleFunc("GET /console/events", s.auth(s.sse))
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// auth wraps a handler so it runs only for an authenticated request: a valid
// console cookie (browser) or the static bearer key (CLI). Unauthenticated
// browsers are redirected to the login page; JSON callers get a 401.
func (s *Service) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authed(r) {
			next(w, r)
			return
		}
		if wantsJSON(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized: bearer key required"})
			return
		}
		http.Redirect(w, r, "/console/login?next="+safeNext(r.URL.RequestURI()), http.StatusSeeOther)
	}
}

// authed reports whether the request carries a valid credential.
func (s *Service) authed(r *http.Request) bool {
	key := s.cfg.APIKey
	if key == "" {
		return false
	}
	if bearerEquals(r, key) {
		return true
	}
	if c, err := r.Cookie(cookieName); err == nil {
		want := consoleToken(key)
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// consoleToken derives the cookie value from the static key, so the raw key is
// never stored in the browser's cookie jar.
func consoleToken(key string) string {
	sum := sha256.Sum256([]byte("seamless-console\x00" + key))
	return hex.EncodeToString(sum[:])
}

// bearerEquals constant-time-compares the request's bearer token to key.
func bearerEquals(r *http.Request, key string) bool {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(key)) == 1
}

// safeNext returns a same-origin relative path to redirect to after login, or
// "/console/" if the candidate is absolute/off-site (open-redirect guard).
func safeNext(raw string) string {
	if strings.HasPrefix(raw, "/console/") && !strings.HasPrefix(raw, "/console//") {
		return raw
	}
	return "/console/"
}

func (s *Service) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.authed(r) {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, "")
}

func (s *Service) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, "could not read form")
		return
	}
	key := s.cfg.APIKey
	given := strings.TrimSpace(r.PostFormValue("key"))
	if key == "" || given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(key)) != 1 {
		s.renderLogin(w, r, "invalid key")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    consoleToken(key),
		Path:     "/console",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600,
	})
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/console",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/console/login", http.StatusSeeOther)
}

// renderLogin renders the standalone login page (no nav chrome).
func (s *Service) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string) {
	data := map[string]string{"Error": errMsg, "Next": safeNext(r.URL.Query().Get("next"))}
	tmpl := s.pages["login"]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "login", data); err != nil {
		s.serverError(w, r, err)
	}
}

func (s *Service) serveCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(consoleCSS)
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

// kindCount pairs a memory kind with its active count, for ordered display.
type kindCount struct {
	Kind string
	N    int
}

// overviewData is the pre-computed payload for the overview page.
type overviewData struct {
	Memories     int
	MemByKind    []kindCount
	Notes        int
	SessActive   int
	SessTotal    int
	TasksOpen    int
	TasksInProg  int
	TasksDone    int
	Injections   int
	Reads        int
	ReadRate     int              // reads as a % of injections
	Trend        []store.DayCount // 14-day injection trend, for the area chart
	TopInjected  []store.NamedCount
	Pending      int
	Recent       []eventRow
	Coverage     int           // % of sessions that retained knowledge
	Covered      int           // sessions with >=1 durable artifact
	CoverageRows []coverageRow // per-channel breakdown (findings/memories/notes/trials)
}

// coverageRow is one retention channel in the overview's "retained via"
// breakdown: how many sessions left this kind of artifact, and its share of all
// sessions (channels overlap, so the shares need not sum to 100%). Color is a
// server-controlled CSS token, safe to inline into the bar.
type coverageRow struct {
	Label string
	Count int
	Pct   int
	Color string
}

// coverageRows projects a SessionCoverage roll-up into the ordered channel rows
// the overview renders, each bar sized as its share of all sessions.
func coverageRows(c store.SessionCoverage) []coverageRow {
	return []coverageRow{
		{"Findings", c.Findings, percent(c.Findings, c.Total), "var(--brand)"},
		{"Memories", c.Memories, percent(c.Memories, c.Total), "var(--ok)"},
		{"Notes", c.Notes, percent(c.Notes, c.Total), "var(--pop)"},
		{"Trials", c.Trials, percent(c.Trials, c.Total), "var(--warn)"},
	}
}

func (s *Service) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Freshen retrieval stats so the numbers match the live event log.
	if err := store.RebuildRetrievalStats(ctx, s.cfg.DB); err != nil {
		s.logger.Warn("console: rebuild retrieval stats", "error", err)
	}
	sum, err := store.GetUsageSummary(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	recent, err := s.recentEvents(ctx, 12)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trend, err := store.InjectionsByDay(ctx, s.cfg.DB, 14)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cov, err := store.GetSessionCoverage(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := overviewData{
		Trend:        denseDays(trend, 14),
		Memories:     sum.Memories.Active,
		MemByKind:    orderKinds(sum.Memories.ByKind),
		Notes:        sum.Notes,
		SessActive:   sum.Sessions[string(core.SessionActive)],
		SessTotal:    sumValues(sum.Sessions),
		TasksOpen:    sum.Tasks[string(core.TaskOpen)],
		TasksInProg:  sum.Tasks[string(core.TaskInProgress)],
		TasksDone:    sum.Tasks[string(core.TaskDone)],
		Injections:   sum.Retrieval.Injections,
		Reads:        sum.Retrieval.Reads,
		ReadRate:     readAfterInject(sum.Retrieval.Reads, sum.Retrieval.Injections),
		TopInjected:  sum.Retrieval.TopInjected,
		Pending:      sumValues(sum.GardenerPending),
		Recent:       recent,
		Coverage:     percent(cov.Covered, cov.Total),
		Covered:      cov.Covered,
		CoverageRows: coverageRows(cov),
	}
	s.render(w, r, "overview", pageData{Title: "Overview", Active: "overview", Data: data})
}

// orderKinds lists memory kinds in canonical order, dropping absent ones.
func orderKinds(byKind map[string]int) []kindCount {
	var out []kindCount
	for _, k := range core.MemoryKinds {
		if n := byKind[string(k)]; n > 0 {
			out = append(out, kindCount{Kind: string(k), N: n})
		}
	}
	return out
}

func sumValues(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// navCounts fills the sidebar badges. Best-effort: a query error yields zeros
// rather than failing the page.
func (s *Service) navCounts(ctx context.Context) navCounts {
	n, err := store.GetNavCounts(ctx, s.cfg.DB)
	if err != nil {
		s.logger.Warn("console: nav counts", "error", err)
		return navCounts{}
	}
	return navCounts{
		Sessions:  n.Sessions,
		Memories:  n.Memories,
		Tasks:     n.OpenTasks,
		Proposals: n.PendingProposals,
	}
}

// navCounts are the sidebar badge numbers.
type navCounts struct {
	Sessions  int
	Memories  int
	Tasks     int
	Proposals int
}
