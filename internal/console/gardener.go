package console

import (
	"errors"
	"net/http"
	"net/url"
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
	CreatedAt time.Time `json:"createdAt"`

	// Archive
	Archive *memBrief `json:"archive,omitempty"`
	Reason  string    `json:"reason,omitempty"`

	// Merge
	Score float64   `json:"score,omitempty"`
	Keep  *memBrief `json:"keep,omitempty"`
	Drop  *memBrief `json:"drop,omitempty"`

	// Digest
	Project      string `json:"project,omitempty"`
	Month        string `json:"month,omitempty"`
	SessionCount int    `json:"sessionCount,omitempty"`
	Title        string `json:"title,omitempty"`
	Preview      string `json:"preview,omitempty"`
}

// gardenerData is the payload for the Gardener page.
type gardenerData struct {
	Cards  []proposalCard `json:"cards"`
	CanAct bool           `json:"-"`
	Error  string         `json:"error,omitempty"`
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
		cards = append(cards, toProposalCard(p))
	}
	s.render(w, r, "gardener", pageData{
		Title:  "Gardener",
		Active: "gardener",
		Data: gardenerData{
			Cards:  cards,
			CanAct: s.cfg.Gardener != nil,
			Error:  r.URL.Query().Get("error"),
		},
	})
}

func toProposalCard(p store.Proposal) proposalCard {
	c := proposalCard{ID: p.ID, Kind: p.Kind, CreatedAt: p.CreatedAt}
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
		c.Preview = snippet(payloadStr(p.Payload, "body"), 600)
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

func redirectFlash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/console/gardener?error="+url.QueryEscape(msg), http.StatusSeeOther)
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
