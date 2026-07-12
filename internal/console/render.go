package console

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

//go:embed templates
var templateFS embed.FS

//go:embed static/console.css
var consoleCSS []byte

// pageNames are the page templates; each is parsed together with the shared
// layout so it can supply the "content" (and optional "scripts") blocks. Pages
// are added here as their handlers land, phase by phase.
var pageNames = []string{
	"login", "overview", "sessions", "session", "memories", "notes", "retrieval",
	"tasks", "gardener", "settings", "event",
}

// peekNames are the entity detail templates. Each templates/peek_<name>.html
// defines a "peek-body" block, parsed twice: as a standalone fragment (executed
// for ?peek=1 drawer fetches) and -- for the entities without a pre-existing
// full page -- wrapped in layout+detail for a shareable, no-JS page.
var peekNames = []string{"memory", "note", "task", "project", "session", "event"}

// detailPageNames are the peek entities that have no pre-existing full page and
// so get the generic layout+detail wrapper as their default (non-peek) render.
// session/event are excluded: they already own session.html/event.html.
var detailPageNames = []string{"memory", "note", "task", "project"}

// pageData is the envelope every rendered page receives. Data holds the
// page-specific payload; Nav/Active/Title drive the shared chrome.
type pageData struct {
	Title  string
	Active string // nav key to highlight
	Nav    navCounts
	Data   any
}

// funcs are the template helpers shared by every page.
var funcs = template.FuncMap{
	"ago":           ago,
	"shortID":       shortID,
	"pct":           func(n, d int) int { return percent(n, d) },
	"add":           func(a, b int) int { return a + b },
	"sub":           func(a, b int) int { return a - b },
	"hasPrefix":     strings.HasPrefix,
	"evtTone":       evtTone,
	"taskTone":      taskTone,
	"icon":          icon,
	"kindLegend":    kindLegend,
	"kindBars":      kindBars,
	"areaChart":     areaChart,
	"barChart":      barChart,
	"stackedBar":    stackedBar,
	"coverageTrend": coverageTrend,
}

// evtTone maps an event kind to a chip tone class (see console.css .kind.*), so
// the activity/timeline tables read at a glance: injections brand, reads/writes
// green, gardener coral, supersede/archive amber. Empty string = neutral.
func evtTone(kind string) string {
	switch {
	case strings.HasPrefix(kind, "retrieval"):
		return "brand"
	case strings.HasPrefix(kind, "gardener"):
		return "pop"
	case kind == "memory.superseded" || kind == "memory.archived":
		return "warn"
	case strings.HasPrefix(kind, "memory.read"), strings.HasPrefix(kind, "memory.written"), strings.HasPrefix(kind, "note."):
		return "ok"
	default:
		return ""
	}
}

// taskTone maps a task status to a badge tone class (see console.css .badge.*),
// so status chips read at a glance: done green, in-progress brand, open amber,
// dropped neutral.
func taskTone(status string) string {
	switch status {
	case "done":
		return "ok"
	case "in_progress":
		return "accent"
	case "open":
		return "warn"
	default: // dropped
		return ""
	}
}

// parseTemplates parses the layout + every page into its own template set, plus
// the peek entities: a full layout+detail page per detailPageNames entry (keyed
// "peek-<name>") and a standalone body fragment per peekNames entry.
func parseTemplates() (pages, fragments map[string]*template.Template, err error) {
	pages = make(map[string]*template.Template, len(pageNames)+len(detailPageNames))
	for _, name := range pageNames {
		t := template.New("layout").Funcs(funcs)
		t, perr := t.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if perr != nil {
			return nil, nil, fmt.Errorf("console: parse %s: %w", name, perr)
		}
		pages[name] = t
	}
	// Full detail pages: layout + generic detail wrapper + the entity's body.
	for _, name := range detailPageNames {
		t := template.New("layout").Funcs(funcs)
		t, perr := t.ParseFS(templateFS,
			"templates/layout.html", "templates/detail.html", "templates/peek_"+name+".html")
		if perr != nil {
			return nil, nil, fmt.Errorf("console: parse peek page %s: %w", name, perr)
		}
		pages["peek-"+name] = t
	}
	// Standalone fragments: just the peek body, executed into the drawer.
	fragments = make(map[string]*template.Template, len(peekNames))
	for _, name := range peekNames {
		t := template.New("peek_" + name).Funcs(funcs)
		t, perr := t.ParseFS(templateFS, "templates/peek_"+name+".html")
		if perr != nil {
			return nil, nil, fmt.Errorf("console: parse peek fragment %s: %w", name, perr)
		}
		fragments[name] = t
	}
	return pages, fragments, nil
}

// renderFragment writes an entity's peek body -- the "peek-body" block of
// templates/peek_<name>.html -- as a standalone HTML fragment, for a drawer
// fetch (?peek=1). data is the entity payload (the fragment's dot).
func (s *Service) renderFragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	tmpl, ok := s.fragments[name]
	if !ok {
		s.serverError(w, r, fmt.Errorf("console: no peek fragment %q", name))
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "peek-body", data); err != nil {
		s.serverError(w, r, fmt.Errorf("console: render peek %s: %w", name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// renderDetail serves an entity detail three ways: JSON for the CLI (wantsJSON),
// an HTML fragment for a drawer fetch (?peek=1), or -- by default -- a full
// layout-wrapped page (a shareable, no-JS fallback URL). name is the peek entity
// ("memory", "task", ...); pd.Data is its payload.
func (s *Service) renderDetail(w http.ResponseWriter, r *http.Request, name string, pd pageData) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, pd.Data)
		return
	}
	if r.URL.Query().Get("peek") == "1" {
		s.renderFragment(w, r, name, pd.Data)
		return
	}
	s.render(w, r, "peek-"+name, pd)
}

// render writes a page as HTML, or -- when the caller asked for JSON (the CLI) --
// encodes just the page's Data payload. Nav counts are filled in best-effort.
func (s *Service) render(w http.ResponseWriter, r *http.Request, page string, pd pageData) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, pd.Data)
		return
	}
	pd.Nav = s.navCounts(r.Context())
	tmpl, ok := s.pages[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("console: no such page %q", page))
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", pd); err != nil {
		s.serverError(w, r, fmt.Errorf("console: render %s: %w", page, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// wantsJSON reports whether the request prefers a JSON response (the seam CLI
// sets ?format=json; a browser never does).
func wantsJSON(r *http.Request) bool {
	if r.URL.Query().Get("format") == "json" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// serverError logs and reports a 500 in the caller's preferred format.
func (s *Service) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("console: request failed", "path", r.URL.Path, "error", err)
	if wantsJSON(r) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

// ago renders a compact relative age ("3d", "5h", "just now"). It accepts a
// time.Time or *time.Time (nil/zero -> "-") so templates can pass nullable
// timestamps directly.
func ago(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return "-"
		}
		t = *x
	default:
		return "-"
	}
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
	}
}

// shortID returns the first 8 chars of a ULID for compact display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// percent computes n/d as a rounded whole percentage (0 when d == 0).
func percent(n, d int) int {
	if d == 0 {
		return 0
	}
	return int(float64(n)/float64(d)*100 + 0.5)
}

// readAfterInject is the share of injections that were read back, capped at
// 100%. Reads can exceed injections (a memory read directly, or more often than
// it was surfaced via recall -- hook injections record no per-item ids), so the
// raw ratio is clamped to stay a sensible coverage percentage.
func readAfterInject(reads, injects int) int {
	return min(percent(reads, injects), 100)
}
