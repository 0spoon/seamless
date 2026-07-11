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
	"login", "overview", "sessions", "session", "memories", "retrieval",
	"tasks", "gardener", "settings",
}

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
	"ago":       ago,
	"shortID":   shortID,
	"pct":       func(n, d int) int { return percent(n, d) },
	"add":       func(a, b int) int { return a + b },
	"hasPrefix": strings.HasPrefix,
}

// parseTemplates parses the layout + every page into its own template set.
func parseTemplates() (map[string]*template.Template, error) {
	out := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t := template.New("layout").Funcs(funcs)
		t, err := t.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("console: parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
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
