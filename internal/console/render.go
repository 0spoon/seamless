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

//go:embed static/interactions.js
var interactionsJS []byte

//go:embed static/search.js
var searchJS []byte

//go:embed static/favicon.svg
var faviconSVG []byte

// pageNames are the page templates; each is parsed together with the shared
// layout so it can supply the "content" (and optional "scripts") blocks. Pages
// are added here as their handlers land, phase by phase.
var pageNames = []string{
	"login", "overview", "search", "interactions", "projects", "projectdetail", "relations", "sessions", "session",
	"memories", "memory", "notes", "note", "retrieval", "tasks", "task", "plans", "plan", "gardener", "settings", "event", "error",
}

// peekNames are the entity detail templates. Each templates/peek_<name>.html
// defines a "peek-body" block (the standalone ?peek=1 fragment) wrapping a
// "detail-body" block (the entity's one body source).
var peekNames = []string{"memory", "note", "task", "project", "session", "event", "plan"}

// entityPeekPages are the entities whose bespoke full page composes the shared
// "detail-body" block from templates/peek_<name>.html, so the peek fragment and
// the full page render one source (page chrome aside). session and project are
// absent on purpose: their fragments are deliberate compact summaries of much
// richer bespoke surfaces (the session workspace, the tabbed project workspace).
var entityPeekPages = map[string]bool{"memory": true, "note": true, "task": true, "event": true, "plan": true}

// pageData is the envelope every rendered page receives. Data holds the
// page-specific payload; Nav/Active/Title drive the shared chrome.
type pageData struct {
	Title    string
	Active   string // nav key to highlight
	Nav      navCounts
	Notice   string // positive flash banner (from ?notice=)
	FlashErr string // error flash banner (from ?error=)
	Data     any
}

// withFlash populates the banner fields from the ?notice= / ?error= query params
// a redirect-after-mutate handler set, so any full page renders the flash without
// per-page plumbing.
func withFlash(r *http.Request, pd pageData) pageData {
	q := r.URL.Query()
	pd.Notice = q.Get("notice")
	pd.FlashErr = q.Get("error")
	return pd
}

// funcs are the template helpers shared by every page.
var funcs = template.FuncMap{
	"ago":           ago,
	"ts":            ts,
	"shortID":       shortID,
	"pct":           func(n, d int) int { return percent(n, d) },
	"add":           func(a, b int) int { return a + b },
	"sub":           func(a, b int) int { return a - b },
	"hasPrefix":     strings.HasPrefix,
	"copyBtn":       copyBtn,
	"evtTone":       evtTone,
	"evtIcon":       evtIcon,
	"taskTone":      taskTone,
	"planTone":      planTone,
	"phaseRows":     phaseRows,
	"icon":          icon,
	"kindLegend":    kindLegend,
	"kindBars":      kindBars,
	"areaChart":     areaChart,
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
	case kind == "plan.approved":
		return "ok"
	case strings.HasPrefix(kind, "plan."), kind == "subagent.captured":
		return "accent"
	case strings.HasPrefix(kind, "memory.read"), strings.HasPrefix(kind, "memory.written"), strings.HasPrefix(kind, "note."):
		return "ok"
	default:
		return ""
	}
}

// evtIcon maps an event kind to a lucide icon name for the Interactions feed's
// type glyph, so tool calls, injections, sessions, and plans read at a glance.
// Mirrors the JS icon map in interactions.js. Unknown kinds get the activity mark.
func evtIcon(kind string) string {
	switch {
	case kind == "tool.call":
		return "terminal"
	case kind == "retrieval.injected":
		return "brain"
	case kind == "hook.prompt":
		return "search"
	case strings.HasPrefix(kind, "session."):
		return "circle"
	case kind == "subagent.captured":
		return "git-fork"
	case strings.HasPrefix(kind, "plan."):
		return "map"
	default:
		return "activity"
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

// parseTemplates parses the layout + every page into its own template set (an
// entity page also parses its peek_<name>.html companion so its content block
// can compose the shared "detail-body"), plus a standalone body fragment per
// peekNames entry.
func parseTemplates() (pages, fragments map[string]*template.Template, err error) {
	pages = make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t := template.New("layout").Funcs(funcs)
		files := []string{"templates/layout.html", "templates/" + name + ".html"}
		if entityPeekPages[name] {
			files = append(files, "templates/peek_"+name+".html")
		}
		t, perr := t.ParseFS(templateFS, files...)
		if perr != nil {
			return nil, nil, fmt.Errorf("console: parse %s: %w", name, perr)
		}
		pages[name] = t
	}
	// Standalone fragments: just the peek body, executed into the detail pane.
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
// templates/peek_<name>.html -- as a standalone HTML fragment, for a detail
// pane fetch (?peek=1). data is the entity payload (the fragment's dot).
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
// an HTML fragment for a detail-pane fetch (?peek=1), or -- by default -- a full
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
	// Default: the entity's bespoke full page, whose content block composes the
	// same "detail-body" the fragment renders. Every full-page caller has one
	// (project routes non-peek requests to its workspace before reaching here);
	// render 500s on an unregistered page.
	s.render(w, r, name, pd)
}

// render writes a page as HTML, or -- when the caller asked for JSON (the CLI) --
// encodes just the page's Data payload. Nav counts are filled in best-effort.
func (s *Service) render(w http.ResponseWriter, r *http.Request, page string, pd pageData) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, pd.Data)
		return
	}
	pd.Nav = s.navCounts(r.Context())
	pd = withFlash(r, pd)
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

// writeJSON renders v as the entire response. It encodes into a buffer first,
// like renderPage: encoding straight into w would commit the status line and
// then discover a failure mid-body, leaving the client a truncated document that
// is neither valid JSON nor an error it can detect. Buffering means the status
// is only chosen once the body is known to exist.
func writeJSON(w http.ResponseWriter, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, "internal error: encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}

// errorData is the payload for the styled in-console error page.
type errorData struct {
	Status  int
	Heading string
	Message string
}

// renderErrorPage renders a full, layout-wrapped error page (sidebar + a way
// back) at the given HTTP status, so a bad/stale URL keeps the observer inside
// the console instead of dropping them on a bare "404 page not found". Only for
// browsers; JSON/CLI callers are answered before reaching here. Falls back to
// plaintext if the template is somehow unavailable.
func (s *Service) renderErrorPage(w http.ResponseWriter, r *http.Request, status int, heading, msg string) {
	// A ?peek=1 fetch is injected verbatim into the in-page detail pane; a
	// layout-wrapped page there nests the whole console inside the pane. Answer
	// fragment fetches (e.g. a stale row whose entity is gone) with a
	// fragment-shaped error instead.
	if r.URL.Query().Get("peek") == "1" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w,
			`<article class="peek-entity"><div class="peek-head"><span class="badge danger">%d</span></div><h2 class="peek-title">%s</h2><p class="peek-desc">%s</p><p class="peek-note">The row may be stale &mdash; reload the list.</p></article>`,
			status, template.HTMLEscapeString(heading), template.HTMLEscapeString(msg))
		return
	}
	tmpl, ok := s.pages["error"]
	if !ok {
		http.Error(w, msg, status)
		return
	}
	pd := pageData{
		Title: heading,
		Nav:   s.navCounts(r.Context()),
		Data:  errorData{Status: status, Heading: heading, Message: msg},
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", pd); err != nil {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// badRequest reports a 400 in the caller's preferred format, for a strictly
// validated query param (an unknown enum value). The message names the bad
// param and lists the valid values so an agent driving the console by URL sees
// the fix rather than a silent default; a browser gets the styled error page.
func (s *Service) badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	s.renderErrorPage(w, r, http.StatusBadRequest, "Bad request", msg)
}

// notFound reports a 404 in the caller's preferred format, naming the missing
// entity so an agent driving the console by URL sees exactly what was not found;
// a browser gets the styled error page with a way back.
func (s *Service) notFound(w http.ResponseWriter, r *http.Request, msg string) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": msg})
		return
	}
	s.renderErrorPage(w, r, http.StatusNotFound, "Not found", msg)
}

// serverError logs and reports a 500 in the caller's preferred format. The
// browser page stays generic (the detail is in the log, not the response).
func (s *Service) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("console: request failed", "path", r.URL.Path, "error", err)
	if wantsJSON(r) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.renderErrorPage(w, r, http.StatusInternalServerError, "Something went wrong",
		"An internal error occurred and has been logged.")
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

// ts formats a timestamp for a title= tooltip: a clean minute-precision UTC
// stamp ("2026-07-14 07:29 UTC"), instead of Go's default time.Time String()
// with microseconds and a "+0000 UTC" suffix. Accepts time.Time or *time.Time;
// a nil or zero time renders "" so the attribute is simply empty.
func ts(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return ""
		}
		t = *x
	default:
		return ""
	}
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// copyBtn renders a small quiet copy-to-clipboard button carrying the FULL
// value in a data-copy attribute; the global handler in layout.html copies it
// on click. Pass the untruncated id/name/slug/path -- not the shortID display
// text. The value is HTML-attribute-escaped; an empty value renders nothing so
// callers can drop it inline next to conditionally-shown fields. It carries
// both the copy and check glyphs; CSS toggles them via the .copied class.
func copyBtn(value string) template.HTML {
	if value == "" {
		return ""
	}
	esc := template.HTMLEscapeString(value)
	return template.HTML(`<button type="button" class="copy-btn" data-copy="` + esc +
		`" aria-label="Copy" title="Copy">` + string(icon("copy")) + string(icon("check")) + `</button>`)
}

// shortID returns the last 8 chars of a ULID for compact display. The last 8
// distinguish ULIDs better than the first 8 (ULID prefixes are the timestamp,
// so recent ids share them); the Interactions client does the same (id.slice(-8))
// so a given id renders identically server-side and client-side.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// percent computes n/d as a rounded whole percentage (0 when d == 0).
func percent(n, d int) int {
	if d == 0 {
		return 0
	}
	return int(float64(n)/float64(d)*100 + 0.5)
}

// durUntil renders the remaining time until t as a compact "22m left" / "1h left".
// It is the countdown half of ago(): both the project workspace and the sessions
// screen render task leases with it.
func durUntil(t, now time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "expired"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm left", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh left", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd left", int(d.Hours()/24))
	}
}
