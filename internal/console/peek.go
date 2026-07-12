package console

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"time"

	"github.com/0spoon/seamless/internal/markdown"
	"github.com/0spoon/seamless/internal/store"
)

// The peek handlers render a single entity three ways through renderDetail: JSON
// for the CLI, an HTML fragment for the drawer (?peek=1), and a full
// layout-wrapped page by default (a shareable, no-JS fallback). Every entity
// reference across the console links to one of these routes with data-peek, so
// the drawer can surface related items without a page navigation.

// memoryRef is a compact pointer to another memory (a supersession neighbor),
// enough to render a peek link.
type memoryRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
}

// memoryDetail is the payload for a single memory's peek/detail. Body is the
// rendered markdown HTML the template shows; BodyText is the raw source (JSON +
// the no-files fallback), so the two never double-escape each other.
type memoryDetail struct {
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Project      string        `json:"project"`
	Status       string        `json:"status"` // active|superseded|archived
	Body         template.HTML `json:"-"`
	BodyText     string        `json:"body"`
	BodyLoaded   bool          `json:"bodyAvailable"`
	Tags         []string      `json:"tags,omitempty"`
	Created      time.Time     `json:"created"`
	Updated      time.Time     `json:"updated"`
	Injects      int           `json:"injects"`
	Reads        int           `json:"reads"`
	LastInjected *time.Time    `json:"lastInjected,omitempty"`
	LastRead     *time.Time    `json:"lastRead,omitempty"`
	Source       string        `json:"sourceSession,omitempty"`   // session name
	SourceID     string        `json:"sourceSessionId,omitempty"` // resolved ULID, for a link
	ReplacedBy   string        `json:"replacedBy,omitempty"`      // name of the superseder
	ReplacedByID string        `json:"replacedById,omitempty"`
	Supersedes   []memoryRef   `json:"supersedes,omitempty"` // reverse: memories this replaced
	FilePath     string        `json:"filePath"`
	AbsPath      string        `json:"absPath"`
	EditURL      template.URL  `json:"-"`
	CanArchive   bool          `json:"-"`
}

func (s *Service) memoryDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	m, ok, err := store.MemoryByID(ctx, s.cfg.DB, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	status := "active"
	if !m.Active() {
		if m.SupersededBy != "" {
			status = "superseded"
		} else {
			status = "archived"
		}
	}
	abs, edit := absAndEditURL(s.cfg.DataDir, m.FilePath)
	d := memoryDetail{
		ID: m.ID, Kind: string(m.Kind), Name: m.Name, Description: m.Description,
		Project: m.Project, Status: status, Tags: m.Tags,
		Created: m.Created, Updated: m.Updated, Source: m.SourceSession,
		FilePath: m.FilePath, AbsPath: abs, EditURL: edit,
		CanArchive: s.cfg.Files != nil && m.Active(),
	}

	// Body lives in the file, not the index. A nil Files subsystem or a read
	// error leaves the metadata intact with the body simply unavailable.
	if s.cfg.Files != nil {
		if full, ferr := s.cfg.Files.Store().ReadMemory(m.FilePath); ferr != nil {
			s.logger.Warn("console: read memory body", "id", id, "error", ferr)
		} else {
			d.BodyText = full.Body
			d.BodyLoaded = true
			d.Body = s.renderBody(ctx, full.Body, m.Project)
		}
	}

	if stat, found, serr := store.GetRetrievalStat(ctx, s.cfg.DB, m.ID); serr != nil {
		s.logger.Warn("console: memory retrieval stat", "id", id, "error", serr)
	} else if found {
		d.Injects, d.Reads = stat.InjectCount, stat.ReadCount
		d.LastInjected, d.LastRead = stat.LastInjectedAt, stat.LastReadAt
	}

	// Provenance: SourceSession stores a session name, so resolve it to an id.
	if m.SourceSession != "" {
		if sess, found, serr := store.SessionByName(ctx, s.cfg.DB, m.SourceSession); serr != nil {
			s.logger.Warn("console: memory source session", "name", m.SourceSession, "error", serr)
		} else if found {
			d.SourceID = sess.ID
		}
	}

	// Supersession, both directions.
	if m.SupersededBy != "" {
		if by, found, serr := store.MemoryByID(ctx, s.cfg.DB, m.SupersededBy); serr != nil {
			s.logger.Warn("console: memory superseder", "id", m.SupersededBy, "error", serr)
		} else if found {
			d.ReplacedBy, d.ReplacedByID = by.Name, by.ID
		}
	}
	superseded, err := store.MemoriesSuperseding(ctx, s.cfg.DB, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, sm := range superseded {
		d.Supersedes = append(d.Supersedes, memoryRef{
			ID: sm.ID, Name: sm.Name, Kind: string(sm.Kind), Project: sm.Project,
		})
	}

	s.renderDetail(w, r, "memory", pageData{Title: "Memory " + m.Name, Active: "memories", Data: d})
}

// noteDetail is the payload for a single note's peek/detail.
type noteDetail struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Slug        string        `json:"slug"`
	Description string        `json:"description"`
	Project     string        `json:"project"`
	Body        template.HTML `json:"-"`
	BodyText    string        `json:"body"`
	BodyLoaded  bool          `json:"bodyAvailable"`
	Tags        []string      `json:"tags,omitempty"`
	SourceURL   string        `json:"sourceUrl,omitempty"`
	Created     time.Time     `json:"created"`
	Updated     time.Time     `json:"updated"`
	FilePath    string        `json:"filePath"`
	AbsPath     string        `json:"absPath"`
	EditURL     template.URL  `json:"-"`
}

func (s *Service) noteDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	n, ok, err := store.NoteByID(ctx, s.cfg.DB, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, edit := absAndEditURL(s.cfg.DataDir, n.FilePath)
	d := noteDetail{
		ID: n.ID, Title: n.Title, Slug: n.Slug, Description: n.Description,
		Project: n.Project, Tags: n.Tags, SourceURL: n.SourceURL,
		Created: n.Created, Updated: n.Updated,
		FilePath: n.FilePath, AbsPath: abs, EditURL: edit,
	}
	if s.cfg.Files != nil {
		if full, ferr := s.cfg.Files.Store().ReadNote(n.FilePath); ferr != nil {
			s.logger.Warn("console: read note body", "id", id, "error", ferr)
		} else {
			d.BodyText = full.Body
			d.BodyLoaded = true
			d.Body = s.renderBody(ctx, full.Body, n.Project)
		}
	}
	s.renderDetail(w, r, "note", pageData{Title: "Note " + n.Title, Active: "notes", Data: d})
}

// taskRef is a compact pointer to a related task (a dependency or a dependent).
type taskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// taskDetail is the payload for a single task's peek/detail. Unlike the Tasks
// list rows, it renders the task Body and resolves both dependency directions.
type taskDetail struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Status  string `json:"status"`
	// Body is the rendered markdown HTML the template shows; BodyText is the raw
	// source (JSON output), so the two never double-escape each other.
	Body        template.HTML `json:"-"`
	BodyText    string        `json:"body"`
	PlanSlug    string        `json:"planSlug,omitempty"`
	CreatedBy   string        `json:"createdBy,omitempty"`
	ClaimedBy   string        `json:"claimedBy,omitempty"`
	LeaseExpiry *time.Time    `json:"leaseExpiry,omitempty"`
	ClaimLive   bool          `json:"claimLive,omitempty"` // lease unexpired as of render time
	Deps        []taskRef     `json:"deps,omitempty"`      // tasks this one depends on
	Blocks      []taskRef     `json:"blocks,omitempty"`    // tasks that depend on this one
	Created     time.Time     `json:"created"`
	Updated     time.Time     `json:"updated"`
	Closed      *time.Time    `json:"closed,omitempty"`
}

func (s *Service) taskDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	t, err := store.TaskByID(ctx, s.cfg.DB, id)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, r, err)
		return
	}
	d := taskDetail{
		ID: t.ID, Title: t.Title, Project: t.ProjectSlug, Status: string(t.Status),
		BodyText: t.Body, PlanSlug: t.PlanSlug, CreatedBy: t.CreatedBy,
		ClaimedBy: t.ClaimedBy, LeaseExpiry: t.LeaseExpiresAt,
		ClaimLive: t.ClaimLive(time.Now().UTC()),
		Created:   t.CreatedAt, Updated: t.UpdatedAt, Closed: t.ClosedAt,
	}
	// Task bodies get the same markdown + wiki-link treatment as memory/note
	// bodies, scoped to the task's project.
	d.Body = s.renderBody(ctx, t.Body, t.ProjectSlug)
	for _, depID := range t.DependsOn {
		dep, derr := store.TaskByID(ctx, s.cfg.DB, depID)
		if derr != nil {
			if errors.Is(derr, store.ErrTaskNotFound) {
				continue // a dangling dep edge should not 500 the page
			}
			s.serverError(w, r, derr)
			return
		}
		d.Deps = append(d.Deps, taskRef{ID: dep.ID, Title: dep.Title, Status: string(dep.Status)})
	}
	blocks, err := store.TasksBlockedBy(ctx, s.cfg.DB, t.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, b := range blocks {
		d.Blocks = append(d.Blocks, taskRef{ID: b.ID, Title: b.Title, Status: string(b.Status)})
	}
	s.renderDetail(w, r, "task", pageData{Title: "Task " + shortID(t.ID), Active: "tasks", Data: d})
}

// projectDetail is the payload for a single project's peek/detail: its metadata
// plus live per-channel counts, each linking to the filtered screen.
type projectDetail struct {
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Memories    int       `json:"memories"`
	Sessions    int       `json:"sessions"`
	OpenTasks   int       `json:"openTasks"`
	Notes       int       `json:"notes"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

func (s *Service) projectDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	p, ok, err := store.ProjectBySlug(ctx, s.cfg.DB, slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	counts, err := store.GetProjectCounts(ctx, s.cfg.DB, slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := projectDetail{
		Slug: p.Slug, Name: p.Name, Description: p.Description,
		Memories: counts.Memories, Sessions: counts.Sessions,
		OpenTasks: counts.OpenTasks, Notes: counts.Notes,
		Created: p.CreatedAt, Updated: p.UpdatedAt,
	}
	s.renderDetail(w, r, "project", pageData{Title: "Project " + p.Slug, Active: "settings", Data: d})
}

// renderBody renders a memory/note/task body -- or session findings -- as
// sanitized markdown HTML for the peek panel and the no-JS detail page. [[name]]
// references resolve to memory peek links (data-peek), scoped to the item's
// project with a global fallback (the same rule recall uses); unresolved
// references stay as literal text. The raw source is kept separately in each
// detail's BodyText field, so rendered and raw never double-escape each other.
func (s *Service) renderBody(ctx context.Context, body, project string) template.HTML {
	return markdown.Render(body, s.wikiResolver(ctx, project))
}

// wikiResolver maps a normalized [[name]] reference to a memory peek link,
// resolving the name against the store scoped to project with a global fallback.
func (s *Service) wikiResolver(ctx context.Context, project string) markdown.WikiResolver {
	return func(name string) (string, bool) {
		m, ok, err := store.MemoryByName(ctx, s.cfg.DB, project, name)
		if err != nil {
			s.logger.Warn("console: resolve wiki-link", "name", name, "error", err)
			return "", false
		}
		if !ok && project != "" {
			m, ok, err = store.MemoryByName(ctx, s.cfg.DB, "", name)
			if err != nil {
				s.logger.Warn("console: resolve wiki-link (global)", "name", name, "error", err)
				return "", false
			}
		}
		if !ok {
			return "", false
		}
		return "/console/memories/" + m.ID, true
	}
}
