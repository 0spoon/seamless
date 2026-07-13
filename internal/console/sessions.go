package console

import (
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/markdown"
	"github.com/0spoon/seamless/internal/store"
)

// sessionRow is a display projection of a session for the list page.
type sessionRow struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Project  string    `json:"project"`
	Status   string    `json:"status"`
	Source   string    `json:"source"`
	Ambient  bool      `json:"ambient"`
	Findings string    `json:"findings"`
	Updated  time.Time `json:"updated"`
}

// sessionsData is the payload for the sessions list page. Active/Completed count
// the sessions shown (windowed); Total is the all-time session count for context.
type sessionsData struct {
	Filter      string         `json:"filter"`
	Active      int            `json:"active"`
	Completed   int            `json:"completed"`
	Total       int            `json:"total"`
	Window      string         `json:"window"`
	WindowLabel string         `json:"windowLabel"`
	Windows     []windowOption `json:"-"`
	Sessions    []sessionRow   `json:"sessions"`
}

func (s *Service) sessionsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status") // "", active, completed
	var statusFilter core.SessionStatus
	switch filter {
	case string(core.SessionActive):
		statusFilter = core.SessionActive
	case string(core.SessionCompleted):
		statusFilter = core.SessionCompleted
	default:
		filter = ""
	}
	win := store.ResolveRetrievalWindow(r.URL.Query().Get("w"), time.Now())

	sessions, err := store.ListSessions(ctx, s.cfg.DB, statusFilter, win.Since, 200)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	counts, err := store.GetNavCounts(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	rows := make([]sessionRow, 0, len(sessions))
	active, completed := 0, 0
	for _, sess := range sessions {
		if sess.Status == core.SessionActive {
			active++
		} else {
			completed++
		}
		rows = append(rows, sessionRow{
			ID: sess.ID, Name: sess.Name, Project: sess.ProjectSlug,
			Status: string(sess.Status), Source: sess.Source, Ambient: sess.Ambient,
			Findings: snippet(markdown.PlainText(sess.Findings), 120), Updated: sess.UpdatedAt,
		})
	}
	s.render(w, r, "sessions", pageData{
		Title:  "Sessions",
		Active: "sessions",
		Data: sessionsData{
			Filter: filter, Active: active, Completed: completed, Total: counts.Sessions,
			Window: win.Key, WindowLabel: win.Label, Windows: windowOptions(win.Key),
			Sessions: rows,
		},
	})
}

// sessionDetail is the payload for a single session's page. Findings is the raw
// markdown (JSON output); FindingsHTML is the rendered, sanitized version the
// template shows.
type sessionDetail struct {
	Session      core.Session  `json:"session"`
	Findings     string        `json:"findings"`
	FindingsHTML template.HTML `json:"-"`
	Timeline     []eventRow    `json:"timeline"`
	ToolCalls    int           `json:"toolCalls"`
	Reads        int           `json:"memoryReads"`
	Writes       int           `json:"memoryWrites"`
	Injected     int           `json:"injectedItems"`
	ReadBack     int           `json:"readAfterInject"`
	ByKind       []kindCount   `json:"eventsByKind"`
}

func (s *Service) sessionDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	sess, ok, err := store.SessionByID(ctx, s.cfg.DB, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	var timeline []eventRow
	byKind := map[string]int{}
	toolCalls, reads, writes := 0, 0, 0
	injected := map[string]struct{}{}
	readItems := map[string]struct{}{}

	if s.cfg.Events != nil {
		evs, eerr := s.cfg.Events.BySession(ctx, id, 500)
		if eerr != nil {
			s.serverError(w, r, eerr)
			return
		}
		for _, e := range evs {
			timeline = append(timeline, toEventRow(e))
			byKind[string(e.Kind)]++
			switch e.Kind {
			case core.EventToolCall:
				toolCalls++
			case core.EventMemoryRead:
				reads++
				if e.ItemID != "" {
					readItems[e.ItemID] = struct{}{}
				}
			case core.EventMemoryWritten:
				writes++
			case core.EventInjected:
				for _, itemID := range injectedEventItemIDs(e) {
					injected[itemID] = struct{}{}
				}
			}
		}
	}
	readBack := 0
	for itemID := range injected {
		if _, ok := readItems[itemID]; ok {
			readBack++
		}
	}

	data := sessionDetail{
		Session: sess, Findings: sess.Findings, Timeline: timeline,
		ToolCalls: toolCalls, Reads: reads, Writes: writes,
		Injected: len(injected), ReadBack: readBack, ByKind: sortedKinds(byKind),
	}
	data.FindingsHTML = s.renderBody(ctx, sess.Findings, sess.ProjectSlug)
	if r.URL.Query().Get("peek") == "1" {
		s.renderFragment(w, r, "session", data)
		return
	}
	s.render(w, r, "session", pageData{
		Title:  "Session " + shortID(sess.ID),
		Active: "sessions",
		Data:   data,
	})
}

// injectedEventItemIDs pulls the item ids an injection event surfaced (recall
// records them in the payload's item_ids array; single-item events use ItemID).
func injectedEventItemIDs(e core.Event) []string {
	var ids []string
	if e.ItemID != "" {
		ids = append(ids, e.ItemID)
	}
	if raw, ok := e.Payload["item_ids"].([]any); ok {
		for _, v := range raw {
			if str, ok := v.(string); ok && str != "" {
				ids = append(ids, str)
			}
		}
	}
	return ids
}

// snippet trims s to a single line no longer than n runes, appending an ellipsis
// when it was cut.
func snippet(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// sortedKinds returns event-kind counts as ordered pairs (descending count).
func sortedKinds(byKind map[string]int) []kindCount {
	out := make([]kindCount, 0, len(byKind))
	for k, n := range byKind {
		out = append(out, kindCount{Kind: k, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}
