package console

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/markdown"
	"github.com/0spoon/seamless/internal/store"
)

// sessionSortKeys are the accepted ?sort values on the sessions list.
var sessionSortKeys = []string{"recent", "name"}

// sessionRow is a display projection of a session for the list page. Live is true
// only for an active session whose last activity is within core.SessionIdleTTL;
// an active-but-stale (idle) row has Live false and is awaiting the reaper.
type sessionRow struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Project  string    `json:"project"`
	Status   string    `json:"status"`
	Source   string    `json:"source"`
	Ambient  bool      `json:"ambient"`
	Live     bool      `json:"live"`
	Findings string    `json:"findings"`
	Updated  time.Time `json:"updated"`
}

// sessionsData is the payload for the sessions list page. Active counts genuinely
// live sessions (active + heartbeated within the idle TTL); Idle counts active
// sessions gone quiet past it (the reaper will expire them); Completed and Expired
// count those terminal states. All are windowed; Total is the all-time count.
type sessionsData struct {
	Filter      string         `json:"filter"`
	Query       string         `json:"query,omitempty"`
	Sort        string         `json:"sort"`
	Active      int            `json:"active"`
	Idle        int            `json:"idle"`
	Completed   int            `json:"completed"`
	Expired     int            `json:"expired"`
	Total       int            `json:"total"`
	Window      string         `json:"window"`
	WindowLabel string         `json:"windowLabel"`
	Windows     []windowOption `json:"-"`
	Sessions    []sessionRow   `json:"sessions"`
}

func (s *Service) sessionsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status") // "", active, completed, expired
	var statusFilter core.SessionStatus
	switch filter {
	case string(core.SessionActive):
		statusFilter = core.SessionActive
	case string(core.SessionCompleted):
		statusFilter = core.SessionCompleted
	case string(core.SessionExpired):
		statusFilter = core.SessionExpired
	default:
		filter = ""
	}
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "recent"
	}
	if !slices.Contains(sessionSortKeys, sortKey) {
		s.badRequest(w, r, fmt.Sprintf("invalid sort %q: valid values are %s", sortKey, strings.Join(sessionSortKeys, ", ")))
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	q := strings.ToLower(query)
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

	now := time.Now()
	rows := make([]sessionRow, 0, len(sessions))
	active, idle, completed, expired := 0, 0, 0, 0
	for _, sess := range sessions {
		plain := markdown.PlainText(sess.Findings)
		if !sessionMatches(sess.Name, sess.ProjectSlug, plain, sess.ID, q) {
			continue
		}
		live := sess.LiveAsOf(now)
		switch sess.Status {
		case core.SessionActive:
			if live {
				active++
			} else {
				idle++
			}
		case core.SessionExpired:
			expired++
		default:
			completed++
		}
		rows = append(rows, sessionRow{
			ID: sess.ID, Name: sess.Name, Project: sess.ProjectSlug,
			Status: string(sess.Status), Source: sess.Source, Ambient: sess.Ambient,
			Live: live, Findings: snippet(plain, 120), Updated: sess.UpdatedAt,
		})
	}
	if sortKey == "name" {
		sort.SliceStable(rows, func(i, j int) bool {
			return sessionSortName(rows[i]) < sessionSortName(rows[j])
		})
	}
	s.render(w, r, "sessions", pageData{
		Title:  "Sessions",
		Active: "sessions",
		Data: sessionsData{
			Filter: filter, Query: query, Sort: sortKey,
			Active: active, Idle: idle, Completed: completed, Expired: expired,
			Total:  counts.Sessions,
			Window: win.Key, WindowLabel: win.Label, Windows: windowOptions(win.Key),
			Sessions: rows,
		},
	})
}

// sessionMatches reports whether a session row satisfies the ?q text filter
// (empty q matches all): a case-insensitive substring of name, project, findings,
// or id.
func sessionMatches(name, project, findings, id, q string) bool {
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), q) ||
		strings.Contains(strings.ToLower(project), q) ||
		strings.Contains(strings.ToLower(findings), q) ||
		strings.Contains(strings.ToLower(id), q)
}

// sessionSortName is the key for the "name" sort: the display name, or the id
// when a session is unnamed, lowercased.
func sessionSortName(row sessionRow) string {
	if row.Name != "" {
		return strings.ToLower(row.Name)
	}
	return strings.ToLower(row.ID)
}

// sessionDetail is the payload for a single session's page. Findings is the raw
// markdown (JSON output); FindingsHTML is the rendered, sanitized version the
// template shows.
type sessionDetail struct {
	Session      core.Session     `json:"session"`
	Findings     string           `json:"findings"`
	FindingsHTML template.HTML    `json:"-"`
	Timeline     []eventRow       `json:"timeline"`
	Interactions []interactionRow `json:"interactions"` // the timeline as shared IX feed rows
	IxVolumeJSON string           `json:"-"`            // session-scoped volume buckets for IX.mountVolume
	ToolCalls    int              `json:"toolCalls"`
	Reads        int              `json:"memoryReads"`
	Writes       int              `json:"memoryWrites"`
	Injected     int              `json:"injectedItems"`
	ReadBack     int              `json:"readAfterInject"`
	ByKind       []kindCount      `json:"eventsByKind"`
	ClaimedTasks []claimedTaskVM  `json:"claimedTasks"`
	Memories     []sessMemVM      `json:"memoriesWritten"`
}

// claimedTaskVM is a task the session currently holds (a live claim), shown on
// the session detail's right rail.
type claimedTaskVM struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	PlanSlug  string `json:"planSlug,omitempty"`
	LeaseLeft string `json:"leaseLeft,omitempty"`
}

// sessMemVM is a memory the session produced (its source_session is the session
// name), shown on the session detail's "Memories written" list.
type sessMemVM struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
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
		s.notFound(w, r, "No session with id "+id+".")
		return
	}

	var timeline []eventRow
	var interactions []interactionRow
	var ixVolume string
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
		// Interactions surface: the session's events projected into the shared IX
		// feed rows (newest first) plus a session-scoped volume histogram, built
		// in-Go from the same fetch (no extra query). Scoped to the feed's
		// interaction kinds so the rows, histogram, and kind filter all agree; the
		// right-rail cards cover the non-interaction detail (reads/writes, produced
		// memories, claimed tasks). BySession returns oldest-first, so we walk it in
		// reverse to build both newest-first.
		namer := func(string) (string, bool) { return sess.Name, sess.Ambient }
		var ticks []events.KindTick
		for i := len(evs) - 1; i >= 0; i-- {
			e := evs[i]
			if !isInteraction(e) || skipInteraction(e) {
				continue
			}
			interactions = append(interactions, toInteractionRow(e, namer))
			ticks = append(ticks, events.KindTick{TS: e.TS, Kind: string(e.Kind)})
		}
		if len(ticks) > 0 {
			// Span the session's own activity (first -> last event), not up to now,
			// so a short historical session isn't crushed into a single bucket.
			newest, oldest := ticks[0].TS, ticks[len(ticks)-1].TS
			if vol := buildVolume(ticks, newest.Sub(oldest), newest); len(vol) > 0 {
				if b, jerr := json.Marshal(vol); jerr == nil {
					ixVolume = string(b)
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
		Interactions: interactions, IxVolumeJSON: ixVolume,
		ToolCalls: toolCalls, Reads: reads, Writes: writes,
		Injected: len(injected), ReadBack: readBack, ByKind: sortedKinds(byKind),
	}
	data.FindingsHTML = s.renderBody(ctx, sess.Findings, sess.ProjectSlug)

	// Claimed tasks (live claims this session holds) + memories it produced --
	// the T2b relation joins. Best-effort: a legacy non-ULID session id or empty
	// name (the joins guard against mis-keyed calls) leaves the panel empty
	// rather than failing the whole detail page.
	now := time.Now().UTC()
	if held, herr := store.TasksClaimedBy(ctx, s.cfg.DB, sess.ID); herr == nil {
		for _, t := range held {
			if !t.ClaimLive(now) {
				continue
			}
			vm := claimedTaskVM{ID: t.ID, Title: t.Title, PlanSlug: t.PlanSlug}
			if t.LeaseExpiresAt != nil {
				vm.LeaseLeft = durUntil(*t.LeaseExpiresAt, now)
			}
			data.ClaimedTasks = append(data.ClaimedTasks, vm)
		}
	} else {
		s.logger.Warn("console: session claimed tasks", "session", sess.ID, "error", herr)
	}
	if sess.Name != "" {
		if mems, merr := store.MemoriesForSession(ctx, s.cfg.DB, sess.Name); merr == nil {
			for _, m := range mems {
				data.Memories = append(data.Memories, sessMemVM{ID: m.ID, Name: m.Name, Kind: string(m.Kind)})
			}
		} else {
			s.logger.Warn("console: session memories written", "session", sess.Name, "error", merr)
		}
	}

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
