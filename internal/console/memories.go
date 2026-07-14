package console

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/lifecycle"
	"github.com/0spoon/seamless/internal/store"
)

// memorySortKeys are the accepted ?sort values on the memories list. name is the
// default (preserving the by-name order within each kind group); recent orders by
// last-updated, reach by injection count.
var memorySortKeys = []string{"name", "recent", "reach"}

// errNoFiles is returned when a write action needs the files subsystem but the
// console was built without it (should not happen in serve).
var errNoFiles = errors.New("console: files subsystem unavailable")

// memoryRow is a display projection of a memory for the browser.
type memoryRow struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Project      string       `json:"project"`
	FilePath     string       `json:"filePath"`
	Updated      time.Time    `json:"updated"`
	Status       string       `json:"status"` // active|superseded|archived
	ReplacedBy   string       `json:"replacedBy,omitempty"`
	ReplacedByID string       `json:"replacedById,omitempty"`
	Injects      int          `json:"injects"`
	Reads        int          `json:"reads"`
	LastInjected *time.Time   `json:"lastInjected,omitempty"`
	AbsPath      string       `json:"absPath"`
	EditURL      template.URL `json:"-"` // vscode://file link; template.URL so it survives sanitization
}

type kindGroup struct {
	Kind     string      `json:"kind"`
	Memories []memoryRow `json:"memories"`
}

type projectGroup struct {
	Project string      `json:"project"`
	Count   int         `json:"count"`
	Kinds   []kindGroup `json:"kinds"`
}

// memoriesData is the payload for the Memories browser.
type memoriesData struct {
	Groups        []projectGroup `json:"groups"`
	Inactive      []memoryRow    `json:"inactive"`
	ActiveCount   int            `json:"activeCount"`
	InactiveCount int            `json:"inactiveCount"`
	Query         string         `json:"query,omitempty"`
	Sort          string         `json:"sort"`
	CanArchive    bool           `json:"-"`
}

func (s *Service) memoriesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "name"
	}
	if !slices.Contains(memorySortKeys, sortKey) {
		s.badRequest(w, r, fmt.Sprintf("invalid sort %q: valid values are %s", sortKey, strings.Join(memorySortKeys, ", ")))
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	q := strings.ToLower(query)
	if err := store.RebuildRetrievalStats(ctx, s.cfg.DB); err != nil {
		s.logger.Warn("console: rebuild retrieval stats", "error", err)
	}
	mems, err := store.AllMemoriesIncludingInvalid(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stats, err := store.AllRetrievalStats(ctx, s.cfg.DB)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	nameByID := make(map[string]string, len(mems))
	for _, m := range mems {
		nameByID[m.ID] = m.Name
	}

	// project -> kind -> rows (active only); inactive collected separately. The
	// ?q filter applies to both sets.
	active := map[string]map[string][]memoryRow{}
	var inactive []memoryRow
	activeCount := 0
	for _, m := range mems {
		if !memoryMatches(m, q) {
			continue
		}
		row := toMemoryRow(m, stats[m.ID], nameByID, s.cfg.DataDir)
		if m.Active() {
			activeCount++
			if active[m.Project] == nil {
				active[m.Project] = map[string][]memoryRow{}
			}
			active[m.Project][string(m.Kind)] = append(active[m.Project][string(m.Kind)], row)
		} else {
			inactive = append(inactive, row)
		}
	}

	data := memoriesData{
		Groups:        buildGroups(active, sortKey),
		Inactive:      inactive,
		ActiveCount:   activeCount,
		InactiveCount: len(inactive),
		Query:         query,
		Sort:          sortKey,
		CanArchive:    s.cfg.Files != nil,
	}
	s.render(w, r, "memories", pageData{Title: "Memories", Active: "memories", Data: data})
}

// memoryMatches reports whether a memory satisfies the ?q text filter (empty q
// matches all): a case-insensitive substring of name, description, kind, or any
// tag.
func memoryMatches(m core.Memory, q string) bool {
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(m.Name), q) ||
		strings.Contains(strings.ToLower(m.Description), q) ||
		strings.Contains(strings.ToLower(string(m.Kind)), q) {
		return true
	}
	for _, t := range m.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func toMemoryRow(m core.Memory, stat store.RetrievalStat, nameByID map[string]string, dataDir string) memoryRow {
	status := "active"
	if !m.Active() {
		if m.SupersededBy != "" {
			status = "superseded"
		} else {
			status = "archived"
		}
	}
	abs, edit := absAndEditURL(dataDir, m.FilePath)
	return memoryRow{
		ID: m.ID, Kind: string(m.Kind), Name: m.Name, Description: m.Description,
		Project: m.Project, FilePath: m.FilePath, Updated: m.Updated, Status: status,
		ReplacedBy: nameByID[m.SupersededBy], ReplacedByID: m.SupersededBy,
		Injects: stat.InjectCount, Reads: stat.ReadCount, LastInjected: stat.LastInjectedAt,
		AbsPath: abs, EditURL: edit,
	}
}

// absAndEditURL resolves a data-dir-relative file path to its absolute path and a
// vscode:// editor link. An empty dataDir (or an already-absolute path) leaves
// the path as given. template.URL bypasses sanitization so the custom scheme
// survives; the input is a server-controlled file path, never user text.
func absAndEditURL(dataDir, relPath string) (string, template.URL) {
	abs := relPath
	if dataDir != "" && !filepath.IsAbs(abs) {
		abs = filepath.Join(dataDir, relPath)
	}
	return abs, template.URL("vscode://file" + abs)
}

// buildGroups turns the project->kind->rows map into an ordered slice: global
// ("") first then projects alphabetically, kinds in canonical order.
func buildGroups(active map[string]map[string][]memoryRow, sortKey string) []projectGroup {
	projects := make([]string, 0, len(active))
	for p := range active {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		if (projects[i] == "") != (projects[j] == "") {
			return projects[i] == "" // global first
		}
		return projects[i] < projects[j]
	})

	var groups []projectGroup
	for _, p := range projects {
		byKind := active[p]
		var kinds []kindGroup
		count := 0
		for _, k := range core.MemoryKinds {
			rows := byKind[string(k)]
			if len(rows) == 0 {
				continue
			}
			sortMemoryRows(rows, sortKey)
			kinds = append(kinds, kindGroup{Kind: string(k), Memories: rows})
			count += len(rows)
		}
		groups = append(groups, projectGroup{Project: p, Count: count, Kinds: kinds})
	}
	return groups
}

// sortMemoryRows orders the rows within a kind group per the ?sort key: name
// (A-Z, the default), recent (newest-updated first), or reach (most-injected
// first). Ties fall back to name for a stable, readable order.
func sortMemoryRows(rows []memoryRow, sortKey string) {
	switch sortKey {
	case "recent":
		sort.SliceStable(rows, func(i, j int) bool {
			if !rows[i].Updated.Equal(rows[j].Updated) {
				return rows[i].Updated.After(rows[j].Updated)
			}
			return rows[i].Name < rows[j].Name
		})
	case "reach":
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Injects != rows[j].Injects {
				return rows[i].Injects > rows[j].Injects
			}
			return rows[i].Name < rows[j].Name
		})
	default: // name
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	}
}

// memoryArchive retires an active memory (the console's one write to memory
// state), then redirects back to the browser.
func (s *Service) memoryArchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.cfg.Files == nil {
		s.serverError(w, r, errNoFiles)
		return
	}
	id := r.PathValue("id")
	idx, ok, err := store.MemoryByID(ctx, s.cfg.DB, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.notFound(w, r, "No memory with id "+id+".")
		return
	}
	if idx.InvalidAt != nil {
		// Already inactive; nothing to do.
		http.Redirect(w, r, "/console/memories", http.StatusSeeOther)
		return
	}
	mem, err := s.cfg.Files.Store().ReadMemory(idx.FilePath)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	updated, err := lifecycle.Archive(ctx, s.cfg.Files, mem, "archived from console", time.Now().UTC())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.cfg.Events != nil {
		if _, err := s.cfg.Events.Record(ctx, core.Event{
			Kind: core.EventMemoryArchived, ProjectSlug: updated.Project, ItemID: updated.ID,
			Payload: map[string]any{"name": updated.Name, "by": "console"},
		}); err != nil {
			s.logger.Warn("console: record archive event", "error", err)
		}
	}
	http.Redirect(w, r, "/console/memories?notice="+url.QueryEscape("Archived "+updated.Name+"."), http.StatusSeeOther)
}
