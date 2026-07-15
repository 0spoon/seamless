package console

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/gardener"
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

// reprojectView is the reproject-specific projection of a proposal card: a memory
// moved from one project to another.
type reprojectView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	From      string `json:"from"`
	To        string `json:"to"`
	Rationale string `json:"rationale,omitempty"`
}

// splitView is the split-setup projection: the source project and the child /
// shared projects it will be split into.
type splitView struct {
	Source       string       `json:"source"`
	Children     []projectOpt `json:"children"`
	Shared       *projectOpt  `json:"shared,omitempty"`
	Family       string       `json:"family,omitempty"`
	RetireSource bool         `json:"retireSource"`
}

// proposalCard is a display projection of one gardener proposal.
type proposalCard struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source,omitempty"` // "request" => owner asked for it
	Plan      string    `json:"plan,omitempty"`   // plan slug => part of a reviewed batch (e.g. a split)
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

	// Reproject (move a memory across projects) and Split (set up a project split).
	Reproject *reprojectView `json:"reproject,omitempty"`
	Split     *splitView     `json:"split,omitempty"`

	// AbandonPlan (retag a never-approved captured plan).
	AbandonPlan *abandonPlanView `json:"abandonPlan,omitempty"`
}

// abandonPlanView is the abandon_plan projection: the stale captured plan.
type abandonPlanView struct {
	NoteID  string `json:"noteId"`
	Slug    string `json:"slug"` // plan:<slug> composition key
	Title   string `json:"title"`
	Project string `json:"project,omitempty"`
	Status  string `json:"status"` // draft | presented
}

// planGroup collects the pending proposals of one plan (a split batch) so the
// console reviews them together: the setup card first, then the per-memory
// reproject cards, with Targets listing the projects a reproject may retarget to.
type planGroup struct {
	Slug    string         `json:"slug"`
	Cards   []proposalCard `json:"cards"`
	Targets []projectOpt   `json:"targets"`
}

// projectOpt is one entry in the request-scope selector.
type projectOpt struct {
	Slug  string `json:"slug"`
	Label string `json:"label"`
}

// gardenerData is the payload for the Gardener page. Groups holds plan-batched
// proposals (a split's setup + reproject cards); Cards holds the rest.
type gardenerData struct {
	Groups     []planGroup    `json:"groups,omitempty"`
	Cards      []proposalCard `json:"cards"`
	CanAct     bool           `json:"-"`
	CanRequest bool           `json:"canRequest"` // an LLM chat client is configured
	Projects   []projectOpt   `json:"projects,omitempty"`
	Scope      string         `json:"scope,omitempty"`    // selected request scope (project slug)
	SplitReq   string         `json:"splitReq,omitempty"` // split request awaiting a source project (renders the picker follow-up)
	Notice     string         `json:"notice,omitempty"`   // positive flash
	Error      string         `json:"error,omitempty"`    // failure flash
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
	groups, ungrouped := groupByPlan(cards)
	s.render(w, r, "gardener", pageData{
		Title:  "Gardener",
		Active: "gardener",
		Data: gardenerData{
			Groups:     groups,
			Cards:      ungrouped,
			CanAct:     s.cfg.Gardener != nil,
			CanRequest: s.cfg.Gardener != nil && s.cfg.Gardener.CanRequest(),
			Projects:   s.projectOptions(ctx),
			Scope:      r.URL.Query().Get("project"),
			SplitReq:   r.URL.Query().Get("split"),
			Notice:     r.URL.Query().Get("notice"),
			Error:      r.URL.Query().Get("error"),
		},
	})
}

// groupByPlan partitions cards into plan groups (those carrying a plan slug) and
// the ungrouped remainder. Within a group the split setup card sorts first, then
// reprojects in creation order; Targets lists the projects a reproject may
// retarget to (the split's children + shared parent, or the distinct targets
// already chosen when the setup card has been applied). Group order follows first
// appearance so the newest plan (newest proposals first) leads.
func groupByPlan(cards []proposalCard) (groups []planGroup, ungrouped []proposalCard) {
	idx := map[string]int{} // plan slug -> position in groups
	for _, c := range cards {
		if c.Plan == "" {
			ungrouped = append(ungrouped, c)
			continue
		}
		i, ok := idx[c.Plan]
		if !ok {
			idx[c.Plan] = len(groups)
			groups = append(groups, planGroup{Slug: c.Plan})
			i = idx[c.Plan]
		}
		groups[i].Cards = append(groups[i].Cards, c)
	}
	for i := range groups {
		sortGroupCards(groups[i].Cards)
		groups[i].Targets = planTargets(groups[i].Cards)
	}
	return groups, ungrouped
}

// sortGroupCards stable-orders a group's cards: the split setup first, then the
// rest in their existing (newest-first) order.
func sortGroupCards(cards []proposalCard) {
	sort.SliceStable(cards, func(i, j int) bool {
		return cards[i].Kind == store.ProposalSplit && cards[j].Kind != store.ProposalSplit
	})
}

// planTargets derives the retarget options for a plan group: the split setup's
// children + shared parent when present, else the distinct targets already chosen
// by the reproject cards (so retargeting still works after the setup is applied).
func planTargets(cards []proposalCard) []projectOpt {
	for _, c := range cards {
		if c.Split != nil {
			out := append([]projectOpt{}, c.Split.Children...)
			if c.Split.Shared != nil {
				out = append(out, *c.Split.Shared)
			}
			return out
		}
	}
	seen := map[string]bool{}
	var out []projectOpt
	for _, c := range cards {
		if c.Reproject == nil || c.Reproject.To == "" || seen[c.Reproject.To] {
			continue
		}
		seen[c.Reproject.To] = true
		out = append(out, projectOpt{Slug: c.Reproject.To, Label: c.Reproject.To})
	}
	return out
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
	c := proposalCard{
		ID: p.ID, Kind: p.Kind, CreatedAt: p.CreatedAt,
		Source: payloadStr(p.Payload, "source"), Plan: payloadStr(p.Payload, "plan"),
	}
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
	case store.ProposalReproject:
		c.Reproject = &reprojectView{
			ID: payloadStr(p.Payload, "id"), Name: payloadStr(p.Payload, "name"),
			From: payloadStr(p.Payload, "from"), To: payloadStr(p.Payload, "to"),
			Rationale: payloadStr(p.Payload, "rationale"),
		}
	case store.ProposalAbandonPlan:
		c.AbandonPlan = &abandonPlanView{
			NoteID: payloadStr(p.Payload, "id"), Slug: payloadStr(p.Payload, "slug"),
			Title: payloadStr(p.Payload, "title"), Project: payloadStr(p.Payload, "project"),
			Status: payloadStr(p.Payload, "plan_status"),
		}
		c.Reason = payloadStr(p.Payload, "reason")
	case store.ProposalSplit:
		sv := &splitView{
			Source:       payloadStr(p.Payload, "source_project"),
			Family:       payloadStr(p.Payload, "family"),
			RetireSource: payloadBool(p.Payload, "retire_source"),
		}
		for _, ch := range payloadList(p.Payload, "children") {
			sv.Children = append(sv.Children, projectOpt{Slug: payloadStr(ch, "slug"), Label: payloadStr(ch, "label")})
		}
		if sh := payloadMap(p.Payload, "shared"); sh != nil {
			if slug := payloadStr(sh, "slug"); slug != "" {
				sv.Shared = &projectOpt{Slug: slug, Label: payloadStr(sh, "label")}
			}
		}
		c.Split = sv
	}
	return c
}

// payloadBool reads a boolean field from a payload map (false if absent).
func payloadBool(p map[string]any, key string) bool {
	if p == nil {
		return false
	}
	v, _ := p[key].(bool)
	return v
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
	redirectNotice(w, r, "Applied the proposal.")
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
	redirectNotice(w, r, "Dismissed the proposal.")
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
	// The scope select offers "All projects" as its empty option, so an empty
	// project here is a choice the owner made from a closed list, not an absent
	// argument -- the console has no session to infer from and nothing to be
	// ambiguous about. Saying so explicitly is what lets the gardener stop reading
	// "" as "everything", which on the agent surface was a silent whole-machine
	// scan rather than a deliberate one.
	scope := gardener.RequestScope{Project: r.PostFormValue("project")}
	scope.AllProjects = scope.Project == ""

	res, err := s.cfg.Gardener.Request(r.Context(), text, scope)
	if err != nil {
		s.logger.Warn("console: gardener request", "error", err)
		redirectFlash(w, r, err.Error())
		return
	}
	// A recognized split of a known project chains straight into split planning:
	// both steps only ever create reviewable proposals, so there is nothing to
	// confirm in between and no reason to make the owner retype the request.
	if res.SplitSource != "" {
		sres, err := s.cfg.Gardener.Split(r.Context(), res.SplitSource, text)
		if err != nil {
			s.logger.Warn("console: gardener request split", "source", res.SplitSource, "error", err)
			redirectFlash(w, r, err.Error())
			return
		}
		if sres.Total == 0 {
			redirectNotice(w, r, fmt.Sprintf("Recognized a split of %s, but no proposals matched -- try naming the child projects.", res.SplitSource))
			return
		}
		redirectNotice(w, r, fmt.Sprintf("Recognized a split of %s: %d proposal(s) below.", res.SplitSource, sres.Total))
		return
	}
	// Split intent without a known source: bounce the request text back so the
	// page renders the inline project picker follow-up under the ask box.
	if res.SplitIntent {
		http.Redirect(w, r, "/console/gardener?split="+url.QueryEscape(text), http.StatusSeeOther)
		return
	}
	if res.Total == 0 {
		redirectNotice(w, r, "No proposals matched that request -- try rephrasing.")
		return
	}
	redirectNotice(w, r, fmt.Sprintf("Generated %d proposal(s) -- review below.", res.Total))
}

// gardenerSplit interprets a project-split request into a plan batch of pending
// proposals (one split setup + one reproject per memory) and redirects back with
// the outcome. Like every request it only ever creates proposals.
func (s *Service) gardenerSplit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gardener == nil {
		s.serverError(w, r, errNoGardener)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		redirectFlash(w, r, "could not read the request")
		return
	}
	source := strings.TrimSpace(r.PostFormValue("source"))
	if source == "" {
		redirectFlash(w, r, "choose a project to split")
		return
	}
	res, err := s.cfg.Gardener.Split(r.Context(), source, r.PostFormValue("instruction"))
	if err != nil {
		s.logger.Warn("console: gardener split", "source", source, "error", err)
		redirectFlash(w, r, err.Error())
		return
	}
	if res.Total == 0 {
		redirectNotice(w, r, "No split proposals matched -- try naming the child projects.")
		return
	}
	redirectNotice(w, r, fmt.Sprintf("Planned split of %s: %d proposal(s) below.", source, res.Total))
}

// gardenerRetarget rewrites a pending reproject proposal's destination project
// before it is applied, so the owner can correct a mis-classified memory without
// dismissing and re-asking. It only touches reproject proposals.
func (s *Service) gardenerRetarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	to := strings.TrimSpace(r.PostFormValue("to"))
	if to == "" {
		redirectFlash(w, r, "choose a target project")
		return
	}
	p, ok, err := store.ProposalByID(r.Context(), s.cfg.DB, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok || p.Kind != store.ProposalReproject {
		redirectFlash(w, r, "not a reproject proposal")
		return
	}
	p.Payload["to"] = to
	if err := store.UpdateProposalPayload(r.Context(), s.cfg.DB, id, p.Payload); err != nil {
		s.logger.Warn("console: gardener retarget", "id", id, "error", err)
		redirectFlash(w, r, err.Error())
		return
	}
	redirectNotice(w, r, fmt.Sprintf("Retargeted %s to %s.", payloadStr(p.Payload, "name"), to))
}

// gardenerApplyPlan applies every pending proposal in a plan, setup (split) first
// so the child/shared projects and family exist before the memories move. It is
// best-effort: it applies what it can and reports how many landed, leaving any
// that error still pending.
func (s *Service) gardenerApplyPlan(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Gardener == nil {
		s.serverError(w, r, errNoGardener)
		return
	}
	slug := r.PathValue("slug")
	proposals, err := store.PendingProposals(r.Context(), s.cfg.DB, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Setup (split) proposals first, then the rest, so targets exist before moves.
	var batch []store.Proposal
	for _, p := range proposals {
		if payloadStr(p.Payload, "plan") == slug && p.Kind == store.ProposalSplit {
			batch = append(batch, p)
		}
	}
	for _, p := range proposals {
		if payloadStr(p.Payload, "plan") == slug && p.Kind != store.ProposalSplit {
			batch = append(batch, p)
		}
	}
	if len(batch) == 0 {
		redirectFlash(w, r, "no pending proposals in that plan")
		return
	}
	applied, firstErr := 0, ""
	for _, p := range batch {
		if _, aerr := s.cfg.Gardener.Apply(r.Context(), p.ID); aerr != nil {
			if firstErr == "" {
				firstErr = aerr.Error()
			}
			continue
		}
		applied++
	}
	if firstErr != "" {
		redirectFlash(w, r, fmt.Sprintf("applied %d of %d -- first error: %s", applied, len(batch), firstErr))
		return
	}
	redirectNotice(w, r, fmt.Sprintf("Applied all %d proposal(s) in %s.", applied, slug))
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
