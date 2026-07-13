package console

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/store"
)

// errNoGardener is returned when a gardener action is requested but the console
// was built without a gardener service.
var errNoGardener = errors.New("console: gardener unavailable")

// memBrief is a compact memory descriptor inside a proposal card.
type memBrief struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Project     string `json:"project"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// proposalCard is a display projection of one gardener proposal.
type proposalCard struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source,omitempty"` // "request" => owner asked for it
	CreatedAt time.Time `json:"createdAt"`

	// Archive
	Archive *memBrief `json:"archive,omitempty"`
	Reason  string    `json:"reason,omitempty"`

	// Merge
	Score float64   `json:"score,omitempty"`
	Keep  *memBrief `json:"keep,omitempty"`
	Drop  *memBrief `json:"drop,omitempty"`

	// Digest. Preview is the raw body text (JSON); Body is the full rendered
	// markdown the card shows.
	Project      string        `json:"project,omitempty"`
	Month        string        `json:"month,omitempty"`
	SessionCount int           `json:"sessionCount,omitempty"`
	Title        string        `json:"title,omitempty"`
	Preview      string        `json:"preview,omitempty"`
	Body         template.HTML `json:"-"`

	// Consolidate: a new unified memory (NewName/NewKind/NewDesc + rendered Body,
	// which reuses the digest's Body field) that supersedes Sources.
	NewName string     `json:"newName,omitempty"`
	NewKind string     `json:"newKind,omitempty"`
	NewDesc string     `json:"newDesc,omitempty"`
	Sources []memBrief `json:"sources,omitempty"`
}

// projectOpt is one entry in the request-scope selector.
type projectOpt struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

// gardenerData is the payload for the Gardener page.
type gardenerData struct {
	Cards      []proposalCard `json:"cards"`
	CanAct     bool           `json:"-"`
	CanRequest bool           `json:"canRequest"` // an LLM chat client is configured
	Projects   []projectOpt   `json:"projects,omitempty"`
	Scope      string         `json:"scope,omitempty"`  // selected request scope (project slug)
	Notice     string         `json:"notice,omitempty"` // positive flash
	Error      string         `json:"error,omitempty"`  // failure flash
}

func (s *Service) gardenerPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	proposals, err := store.PendingProposals(ctx, s.cfg.DB, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards := make([]proposalCard, 0, len(proposals))
	for _, p := range proposals {
		cards = append(cards, s.toProposalCard(ctx, p))
	}
	s.render(w, r, "gardener", pageData{
		Title:  "Gardener",
		Active: "gardener",
		Data: gardenerData{
			Cards:      cards,
			CanAct:     s.cfg.Gardener != nil,
			CanRequest: s.cfg.Gardener != nil && s.cfg.Gardener.CanRequest(),
			Projects:   s.projectOptions(ctx),
			Scope:      r.URL.Query().Get("project"),
			Notice:     r.URL.Query().Get("notice"),
			Error:      r.URL.Query().Get("error"),
		},
	})
}

// projectOptions lists projects for the request-scope selector, best-effort: a
// query error yields no options rather than failing the page.
func (s *Service) projectOptions(ctx context.Context) []projectOpt {
	projs, err := store.ListProjects(ctx, s.cfg.DB)
	if err != nil {
		s.logger.Warn("console: list projects", "error", err)
		return nil
	}
	out := make([]projectOpt, 0, len(projs))
	for _, p := range projs {
		label := p.Name
		if label == "" {
			label = p.Slug
		}
		out = append(out, projectOpt{Slug: p.Slug, Label: label})
	}
	return out
}

func (s *Service) toProposalCard(ctx context.Context, p store.Proposal) proposalCard {
	c := proposalCard{ID: p.ID, Kind: p.Kind, CreatedAt: p.CreatedAt, Source: payloadStr(p.Payload, "source")}
	switch p.Kind {
	case store.ProposalArchive:
		c.Archive = &memBrief{
			ID: payloadStr(p.Payload, "id"), Name: payloadStr(p.Payload, "name"),
			Project: payloadStr(p.Payload, "project"), Kind: payloadStr(p.Payload, "kind"),
			Description: payloadStr(p.Payload, "description"),
		}
		c.Reason = payloadStr(p.Payload, "reason")
	case store.ProposalMerge:
		c.Score = payloadFloat(p.Payload, "score")
		c.Keep = briefFrom(payloadMap(p.Payload, "keep"))
		c.Drop = briefFrom(payloadMap(p.Payload, "drop"))
	case store.ProposalDigest:
		c.Project = payloadStr(p.Payload, "project")
		c.Month = payloadStr(p.Payload, "month")
		c.SessionCount = int(payloadFloat(p.Payload, "session_count"))
		c.Title = payloadStr(p.Payload, "title")
		body := payloadStr(p.Payload, "body")
		c.Preview = snippet(body, 600) // raw, for JSON
		c.Body = s.renderBody(ctx, body, c.Project)
	case store.ProposalConsolidate:
		c.NewName = payloadStr(p.Payload, "name")
		c.NewKind = payloadStr(p.Payload, "kind")
		c.NewDesc = payloadStr(p.Payload, "description")
		c.Project = payloadStr(p.Payload, "project")
		body := payloadStr(p.Payload, "body")
		c.Preview = snippet(body, 600) // raw, for JSON
		c.Body = s.renderBody(ctx, body, c.Project)
		for _, src := range payloadList(p.Payload, "sources") {
			c.Sources = append(c.Sources, memBrief{ID: payloadStr(src, "id"), Name: payloadStr(src, "name")})
		}
	}
	return c
}

func briefFrom(m map[string]any) *memBrief {
	if m == nil {
		return nil
	}
	return &memBrief{
		ID: payloadStr(m, "id"), Name: payloadStr(m, "name"), Project: payloadStr(m, "project"),
		Kind: payloadStr(m, "kind"), Description: payloadStr(m, "description"),
	}
}

// gardenerApply carries out a proposal and redirects back, surfacing an error via
// a flash query param when the effect could not be applied.
func (s *Service) gardenerApply(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gardener == nil {
		s.serverError(w, r, errNoGardener)
		return
	}
	id := r.PathValue("id")
	if _, err := s.cfg.Gardener.Apply(r.Context(), id); err != nil {
		s.logger.Warn("console: gardener apply", "id", id, "error", err)
		redirectFlash(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/console/gardener", http.StatusSeeOther)
}

func (s *Service) gardenerDismiss(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gardener == nil {
		s.serverError(w, r, errNoGardener)
		return
	}
	id := r.PathValue("id")
	if err := s.cfg.Gardener.Dismiss(r.Context(), id); err != nil {
		s.logger.Warn("console: gardener dismiss", "id", id, "error", err)
		redirectFlash(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/console/gardener", http.StatusSeeOther)
}

// gardenerRequest interprets a natural-language maintenance request into pending
// proposals and redirects back to the page, flashing the outcome. The request
// never mutates a memory -- it only ever creates proposals for review.
func (s *Service) gardenerRequest(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gardener == nil {
		s.serverError(w, r, errNoGardener)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		redirectFlash(w, r, "could not read the request")
		return
	}
	text := strings.TrimSpace(r.PostFormValue("request"))
	if text == "" {
		redirectFlash(w, r, "enter a request")
		return
	}
	res, err := s.cfg.Gardener.Request(r.Context(), text, r.PostFormValue("project"))
	if err != nil {
		s.logger.Warn("console: gardener request", "error", err)
		redirectFlash(w, r, err.Error())
		return
	}
	if res.Total == 0 {
		redirectNotice(w, r, "No proposals matched that request -- try rephrasing.")
		return
	}
	redirectNotice(w, r, fmt.Sprintf("Generated %d proposal(s) -- review below.", res.Total))
}

func redirectFlash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/gardener?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

func redirectNotice(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/gardener?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

// payloadFloat reads a numeric field from a payload map (0 if absent).
func payloadFloat(p map[string]any, key string) float64 {
	if p == nil {
		return 0
	}
	if v, ok := p[key].(float64); ok {
		return v
	}
	return 0
}
