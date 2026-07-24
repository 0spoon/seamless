package retrieve

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0spoon/seamless/internal/agentguide"
	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// BriefingInput carries the SessionStart hook fields the briefing depends on.
type BriefingInput struct {
	CWD       string // agent working directory; resolved to a project slug
	Source    string // startup|resume|clear|compact
	AgentType string // non-empty => subagent => constraints-only briefing
	// Prompt is the child's spawn prompt, resolved best-effort at SubagentStart
	// (empty on the main-session path and whenever resolution fails).
	// Intentionally UNREAD for now: it is staged for the RELEVANT-section step
	// of plan:subagent-briefing, which will match it against project memories.
	// Until that step consumes it, briefing output must be byte-identical
	// whether this field is set or empty (pinned by
	// TestSubagentBriefing_PromptFieldUnread).
	Prompt string
}

// Briefing assembles the SessionStart briefing for an agent: constraints (always
// included), a newest-first memory index, and recent sibling findings, budgeted
// by estimated tokens and wrapped in <seam-briefing> tags. It returns "" when
// there is nothing worth injecting (unmapped cwd with no global memories), which
// the hook forwards as an empty additionalContext. The second return value is the
// ids of the memories actually rendered (after budget dropping), so the caller
// can record them as a retrieval.injected event and feed the read-after-inject
// funnel -- the same telemetry the recall tool emits.
func (s *Service) Briefing(ctx context.Context, in BriefingInput) (string, []string, error) {
	cfg := s.effectiveBriefing(ctx)
	project, err := store.ResolveProjectForCWD(ctx, s.db, in.CWD)
	if err != nil {
		return "", nil, err
	}
	// A child project (one with a parent) sees its shared parent's active memories
	// in-briefing too, so cross-platform knowledge kept in the parent surfaces in
	// each child without being duplicated (see the arctop-app split).
	extra, err := s.familyMemoryScope(ctx, project, cfg.IncludeParentMemories)
	if err != nil {
		return "", nil, err
	}
	mems, err := store.ActiveMemoriesForScope(ctx, s.db, project, extra)
	if err != nil {
		return "", nil, err
	}
	var constraints, stageMems, favorites []core.Memory
	index := make([]core.Memory, 0, len(mems))
	for _, m := range mems {
		switch {
		case m.Kind == core.KindConstraint:
			constraints = append(constraints, m)
		case m.Kind == core.KindStage:
			stageMems = append(stageMems, m)
		case m.Favorite:
			// Starred memories are pinned like constraints/stages: pulled out of
			// the index before the recency/count trims and never budget-dropped.
			// A starred constraint or stage keeps its existing pinned section
			// (the cases above win), so it is never rendered twice.
			favorites = append(favorites, m)
		default:
			index = append(index, m)
		}
	}

	// Constraints are ranked ahead of the tier split (ConstraintMaxFull):
	// starred first, then constraints a recent mishap referenced (most recent
	// mishap first), then the blended recency+utility order. With tiering
	// disabled (0) the order stays untouched -- the legacy all-full rendering.
	constraints = s.rankConstraints(ctx, project, constraints, cfg)

	// Subagents get constraints only (they inherit the parent's task context).
	if in.AgentType != "" {
		text, ids := s.assembleSubagent(project, constraints, cfg.ConstraintMaxFull)
		return text, ids, nil
	}

	// Utility blend runs after the partition (pinned sections are already out)
	// and before the trims, so the MemoryMaxItems head-slice and the budget
	// drop order both follow the blended ranking rather than raw recency.
	index = s.utilityRankedMemories(ctx, project, index, cfg)

	// Recency/count trims run AFTER the kind partition above, so constraints and
	// pinned stages are exempt (the never-drop invariant).
	index, omitted := trimMemoryIndex(index, cfg, time.Now())

	var findings []core.Session
	if cfg.FindingsCount > 0 {
		findings, err = store.RecentFindings(ctx, s.db, project, cfg.FindingsCount)
		if err != nil {
			return "", nil, err
		}
		findings = filterFindingsByAge(findings, cfg.FindingsMaxAgeDays, time.Now())
	}
	var ready []core.Task
	if cfg.ReadyTasksShown > 0 {
		ready, err = store.ReadyTasks(ctx, s.db, project)
		if err != nil {
			return "", nil, err
		}
	}
	siblings, err := s.siblingFindings(ctx, project, cfg.SiblingFindingsCount)
	if err != nil {
		return "", nil, err
	}
	siblingMems, err := s.siblingMemories(ctx, project, extra, cfg)
	if err != nil {
		return "", nil, err
	}
	rollups, err := store.ActivePlans(ctx, s.db, project)
	if err != nil {
		return "", nil, err
	}
	pending, err := s.pendingPlans(ctx, project, cfg.PendingPlanMaxDays)
	if err != nil {
		return "", nil, err
	}
	stages := s.pinnedStages(stageMems, cfg.StageUnknownMaxAgeDays, time.Now())
	if len(constraints) == 0 && len(favorites) == 0 && len(index) == 0 && omitted == 0 &&
		len(findings) == 0 && len(ready) == 0 && len(siblings) == 0 && len(siblingMems) == 0 &&
		len(stages) == 0 && len(rollups) == 0 && len(pending) == 0 {
		return "", nil, nil
	}
	text, ids := s.assembleBriefing(project, in.Source, briefingSections{
		constraints: constraints, favorites: favorites, index: index, indexOmitted: omitted,
		findings: findings, ready: ready, siblings: siblings,
		siblingMems: siblingMems, stages: stages, plans: rollups,
		pendingPlans: pending,
	}, cfg)
	return text, ids, nil
}

// effectiveBriefing resolves the briefing knobs for one assembly: the file/env
// base layered with the console's runtime override row, so a console save takes
// effect on the next session start without a daemon restart. Failure-soft: an
// unreadable override degrades to the base config rather than blocking the
// agent's session start.
func (s *Service) effectiveBriefing(ctx context.Context) config.Briefing {
	cfg, _, err := store.BriefingConfig(ctx, s.db, s.briefing)
	if err != nil {
		s.logger.Warn("retrieve: briefing override unavailable, using base config", "error", err)
		return s.briefing
	}
	return cfg
}

// briefingRecencyHalfLifeDays is the half-life of the recency term in the
// blended index sort key. It matches the utility score's own half-life (store
// side) so the two terms decay on the same clock.
const briefingRecencyHalfLifeDays = 14.0

// utilityRankedMemories re-orders a briefing memory slice (the index, and the
// constraints ahead of their tier split) by the blended recency+utility key
// when utility ranking is active for the project (mode "on", or "auto" with
// the gardener's readiness latch / owner force). Both state and score reads
// are failure-soft: the briefing must never fail -- or change composition --
// because a stats read did. With weight 0 or inactive state the slice is
// returned untouched (the legacy recency order).
func (s *Service) utilityRankedMemories(ctx context.Context, project string, mems []core.Memory, cfg config.Briefing) []core.Memory {
	if cfg.UtilityWeight <= 0 || len(mems) < 2 {
		return mems
	}
	activation, err := store.GetUtilityActivation(ctx, s.db)
	if err != nil {
		s.logger.Warn("retrieve: utility activation unavailable, briefing keeps recency order", "error", err)
		return mems
	}
	if !activation.Active(project, cfg.UtilityMode) {
		return mems
	}
	utility, err := store.UtilityScores(ctx, s.db)
	if err != nil {
		s.logger.Warn("retrieve: utility scores unavailable, briefing keeps recency order", "error", err)
		return mems
	}
	return rankMemories(mems, utility, cfg.UtilityWeight, time.Now())
}

// rankMemories sorts memories by (1-w)*recency + w*utility, both in [0,1)
// (recency decays with briefingRecencyHalfLifeDays). Ties break Updated desc
// then ID desc, replicating ActiveMemoriesForScope's SQL order so w=0 or
// all-zero utility reproduces the legacy ordering exactly.
func rankMemories(index []core.Memory, utility map[string]float64, weight float64, now time.Time) []core.Memory {
	key := func(m core.Memory) float64 {
		days := now.Sub(m.Updated).Hours() / 24
		if days < 0 {
			days = 0
		}
		recency := math.Exp2(-days / briefingRecencyHalfLifeDays)
		return (1-weight)*recency + weight*utility[m.ID]
	}
	out := make([]core.Memory, len(index))
	copy(out, index)
	sort.Slice(out, func(i, j int) bool {
		ki, kj := key(out[i]), key(out[j])
		if ki != kj {
			return ki > kj
		}
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// mishapPinWindowDays bounds the briefing's mishap promotion: a constraint
// referenced by an agent.mishap event recorded within this many days is
// promoted to the head of the full tier (store.RecentMishapItemIDs supplies
// the references). Older mishaps stop promoting -- the signal is "this rule
// was violated recently", not a permanent demerit.
const mishapPinWindowDays = 30

// recentMishapRefs reads the project's mishap-referenced memory ids inside the
// promotion window. Failure-soft, the utilityRankedMemories posture: an
// unreadable query costs the promotion, never the briefing. Promotion is a
// briefing-side ordering signal only, like favorites -- it never feeds utility
// scores, recall, or search (the closed-loop-utility-signal-contract).
func (s *Service) recentMishapRefs(ctx context.Context, project string) map[string]time.Time {
	mishaps, err := store.RecentMishapItemIDs(ctx, s.db, project, mishapPinWindowDays*24*time.Hour)
	if err != nil {
		s.logger.Warn("retrieve: recent mishaps unavailable, briefing skips promotion", "error", err)
		return nil
	}
	return mishaps
}

// rankConstraints orders constraints for the tiered rendering: priority class
// first (constraintPriority -- starred constraints claim full-tier slots
// before mishap-promoted ones, which claim them before anything else), then
// within a class the blended recency+utility order -- the same
// utilityRankedMemories gate and key as the memory index, degrading to pure
// recency (the SQL updated_at DESC order) when utility is inactive or weighted
// 0. The mishap class alone re-sorts internally, most recent mishap first.
// ConstraintMaxFull 0 disables tiering, so the input order is returned
// untouched: the legacy all-full rendering stays exactly as it was, favorites
// and mishaps included -- promotion is part of the tier ranking only, and the
// mishap query is skipped entirely.
func (s *Service) rankConstraints(ctx context.Context, project string, constraints []core.Memory, cfg config.Briefing) []core.Memory {
	if cfg.ConstraintMaxFull <= 0 || len(constraints) < 2 {
		return constraints
	}
	mishaps := s.recentMishapRefs(ctx, project)
	ranked := s.utilityRankedMemories(ctx, project, constraints, cfg)
	out := make([]core.Memory, len(ranked))
	copy(out, ranked)
	slices.SortStableFunc(out, func(a, b core.Memory) int {
		ca, cb := constraintPriority(a, mishaps), constraintPriority(b, mishaps)
		if c := cmp.Compare(ca, cb); c != 0 {
			return c
		}
		if ca == constraintClassMishap {
			// Most recent mishap first; equal stamps keep the blended order
			// (the sort is stable).
			return mishaps[b.ID].Compare(mishaps[a.ID])
		}
		return 0
	})
	return out
}

// Constraint priority classes for the tiered ordering: a lower class claims
// full-tier slots first, and the blended order (rankConstraints) is preserved
// within a class -- except the mishap class, which orders by most recent
// mishap. A star outranks a mishap deliberately: the star is an explicit
// owner signal, the mishap an implicit one, so a starred constraint sorts as
// a favorite even when a recent mishap also references it.
const (
	constraintClassFavorite = iota // starred by the owner or an agent
	constraintClassMishap          // referenced by an agent.mishap event within the window
	constraintClassBlended         // everything else: the blended recency+utility order
)

// constraintPriority is a constraint's class in the tiered ordering. mishaps
// maps memory ids to their most recent referencing agent.mishap timestamp
// (store.RecentMishapItemIDs); nil or empty means no promotion.
func constraintPriority(m core.Memory, mishaps map[string]time.Time) int {
	if m.Favorite {
		return constraintClassFavorite
	}
	if _, ok := mishaps[m.ID]; ok {
		return constraintClassMishap
	}
	return constraintClassBlended
}

// constraintTiers splits ranked constraints at the full-tier boundary: the top
// maxFull render as full CONSTRAINT lines, the rest collapse into the compact
// "Also binding" line. maxFull 0 disables tiering (everything renders full,
// the legacy behavior).
func constraintTiers(constraints []core.Memory, maxFull int) (full, compact []core.Memory) {
	if maxFull <= 0 || len(constraints) <= maxFull {
		return constraints, nil
	}
	return constraints[:maxFull], constraints[maxFull:]
}

// alsoBindingLine collapses the constraints past the full tier into one
// compact pinned line that still names every one -- the name is what
// memory_read takes, so the never-drop invariant holds in compact form.
// Returns "" when the compact tier is empty.
func alsoBindingLine(compact []core.Memory) string {
	if len(compact) == 0 {
		return ""
	}
	names := make([]string, 0, len(compact))
	for _, c := range compact {
		names = append(names, sanitizeField(c.Name, 80))
	}
	return fmt.Sprintf("Also binding (%d): %s -- memory_read a name before working near it.\n",
		len(compact), strings.Join(names, ", "))
}

// trimMemoryIndex applies the recency filter (MemoryMaxAgeDays) and item cap
// (MemoryMaxItems) to the briefing's memory index, returning the kept lines and
// how many were omitted. The omitted count folds into the assembler's
// "(+N older -- use recall)" trailer so trimmed knowledge stays discoverable.
// Zero-valued knobs disable their trim (the default: budget-only).
func trimMemoryIndex(index []core.Memory, cfg config.Briefing, now time.Time) ([]core.Memory, int) {
	kept := index
	if cfg.MemoryMaxAgeDays > 0 {
		cutoff := now.AddDate(0, 0, -cfg.MemoryMaxAgeDays)
		fresh := make([]core.Memory, 0, len(kept))
		for _, m := range kept {
			if m.Updated.After(cutoff) {
				fresh = append(fresh, m)
			}
		}
		kept = fresh
	}
	if cfg.MemoryMaxItems > 0 && len(kept) > cfg.MemoryMaxItems {
		kept = kept[:cfg.MemoryMaxItems]
	}
	return kept, len(index) - len(kept)
}

// filterFindingsByAge drops findings older than maxAgeDays (0 = keep all).
// Findings arrive newest-first, so only the tail is ever dropped.
func filterFindingsByAge(findings []core.Session, maxAgeDays int, now time.Time) []core.Session {
	if maxAgeDays <= 0 {
		return findings
	}
	cutoff := now.AddDate(0, 0, -maxAgeDays)
	kept := make([]core.Session, 0, len(findings))
	for _, f := range findings {
		if f.UpdatedAt.After(cutoff) {
			kept = append(kept, f)
		}
	}
	return kept
}

// pendingPlans returns the project's recently captured, not-yet-approved
// Claude Code plans (plan-status draft/presented updated within maxDays days;
// 0 = no age cutoff), newest first. Anything older is presumed stale (the
// gardener proposes abandoning it) and stops earning briefing space. These earn
// budget-participating "awaiting approval" lines -- unlike the pinned
// active-plan rollups, a pending plan is a hint, not a commitment.
func (s *Service) pendingPlans(ctx context.Context, project string, maxDays int) ([]core.Note, error) {
	if project == "" {
		return nil, nil // the global scope aggregates too much to be useful
	}
	notes, err := store.NotesByTag(ctx, s.db, project, plans.TagPlan)
	if err != nil {
		return nil, err
	}
	var cutoff time.Time
	if maxDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -maxDays)
	}
	var out []core.Note
	for _, n := range notes {
		switch plans.StatusFromTags(n.Tags) {
		case plans.StatusDraft, plans.StatusPresented:
			if n.Updated.After(cutoff) {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// familyMemoryScope returns the extra project slugs whose active memories should
// be folded into project's briefing: its shared parent, when set and the
// IncludeParentMemories knob is on (the default). It is a no-op (nil) for the
// global scope or a project with no parent. Kept small on purpose -- a child
// inherits its parent's shared memories, not its siblings' platform-specific
// ones (those cross over only via siblingFindings/siblingMemories).
func (s *Service) familyMemoryScope(ctx context.Context, project string, includeParent bool) ([]string, error) {
	if project == "" || !includeParent {
		return nil, nil
	}
	p, ok, err := store.ProjectBySlug(ctx, s.db, project)
	if err != nil || !ok || p.ParentSlug == "" {
		return nil, err
	}
	return []string{p.ParentSlug}, nil
}

// siblingFindings gathers up to limit recent findings from a project's family
// members (see store.SiblingProjects/SiblingFindings), for the briefing's
// "Sibling projects" section. It is a no-op for the global scope, a project
// with no configured family, or limit 0 (the section disabled).
func (s *Service) siblingFindings(ctx context.Context, project string, limit int) ([]core.Session, error) {
	if limit <= 0 {
		return nil, nil
	}
	siblings, err := store.SiblingProjects(ctx, s.db, project)
	if err != nil || len(siblings) == 0 {
		return nil, err
	}
	return store.SiblingFindings(ctx, s.db, siblings, limit)
}

// siblingMemories gathers family members' active memories for the opt-in
// "Sibling memories" cross-over (IncludeSiblingMemories, default off).
// Constraints and stages are excluded -- a sibling's gates bind its own
// sessions, not this one -- and slugs already folded into the main scope (the
// shared parent) are skipped so nothing renders twice. The memory recency
// filter applies here too; the item cap does not (the section already packs
// after the own-project index, so budget drops it first).
func (s *Service) siblingMemories(ctx context.Context, project string, already []string, cfg config.Briefing) ([]core.Memory, error) {
	if !cfg.IncludeSiblingMemories {
		return nil, nil
	}
	siblings, err := store.SiblingProjects(ctx, s.db, project)
	if err != nil || len(siblings) == 0 {
		return nil, err
	}
	skip := make(map[string]bool, len(already))
	for _, slug := range already {
		skip[slug] = true
	}
	fetch := make([]string, 0, len(siblings))
	for _, slug := range siblings {
		if !skip[slug] {
			fetch = append(fetch, slug)
		}
	}
	mems, err := store.ActiveMemoriesForProjects(ctx, s.db, fetch)
	if err != nil {
		return nil, err
	}
	var cutoff time.Time
	if cfg.MemoryMaxAgeDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -cfg.MemoryMaxAgeDays)
	}
	var out []core.Memory
	for _, m := range mems {
		if m.Kind == core.KindConstraint || m.Kind == core.KindStage {
			continue
		}
		if !m.Updated.After(cutoff) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func projectLabel(project string) string {
	if project == "" {
		return "(global)"
	}
	return project
}

// briefingSections carries the assembled, pre-filtered content of a full
// briefing (subagents use assembleSubagent instead). Grouping the sections keeps
// the assembler signature stable as new sections are added.
type briefingSections struct {
	constraints  []core.Memory // ranked (rankConstraints); tier-split by ConstraintMaxFull at render
	favorites    []core.Memory // starred non-constraint/stage memories, pinned after plans
	index        []core.Memory
	indexOmitted int // index lines cut by the recency/count trims, for the "+N older" trailer
	findings     []core.Session
	ready        []core.Task
	siblings     []core.Session     // recent findings from family-member projects
	siblingMems  []core.Memory      // family members' memories (opt-in cross-over)
	stages       []stageLine        // non-done stage memories, pinned after constraints
	plans        []store.PlanRollup // active plans (a plan-tagged task set), pinned after stages
	pendingPlans []core.Note        // captured, not-yet-approved CC plans (budget-participating)
}

// assembleBriefing packs the sections against the token budget. Constraints
// (both the full tier and the compact "Also binding" line), stages, plan
// rollups, favorites, the header, and the trailer are counted first and never
// dropped. Body sections then pack in render order --
// situation before library: pending plans > recent findings > ready tasks >
// memory index > sibling findings > sibling memories -- so budget priority and
// render priority agree: a fat memory index can no longer evict the findings
// that render above it, and the sibling sections, last, are dropped first. The
// header counts the findings actually rendered (headLine defers for that), and
// findings cut by budget leave a "+N more" trailer, mirroring the memory
// index. The whole is hard-capped. The second return value is the ids of the
// memories actually rendered (constraints, pinned stages, favorites, and the
// index/sibling lines that survived budgeting) -- for retrieval
// instrumentation. Findings, siblings, and ready tasks are sessions/tasks, not
// memory_read-able memories, so they are omitted from the funnel.
func (s *Service) assembleBriefing(project, source string, sec briefingSections, cfg config.Briefing) (string, []string) {
	constraints, index := sec.constraints, sec.index
	findings, ready := sec.findings, sec.ready
	label := projectLabel(project)
	budget := s.budgets.MaxBriefingTokens
	if budget <= 0 {
		budget = 1500
	}
	mult := cfg.HardCapMultiplier
	if mult <= 0 {
		mult = 2
	}
	hardCap := budget * mult

	ids := make([]string, 0, len(constraints)+len(sec.stages)+len(index))

	// The header line carries the count of findings that actually render, which
	// is only known after the findings section packs below; headLine defers its
	// composition. Estimating the budget with the pre-packing count is fine --
	// the two strings differ by at most a digit.
	//
	// Constraints ARE memories (kind=constraint), just pinned and rendered first,
	// so the header reports one memory total with constraints called out as its
	// subset. Reporting them as two disjoint pools ("6 constraints, 76 memories")
	// contradicts the body, where 6 CONSTRAINT lines + the index + the "+N older"
	// trailer sum to the total, not to the index count alone.
	totalMems := len(constraints) + len(sec.favorites) + len(index)
	headLine := func(renderedFindings int) string {
		return fmt.Sprintf("<seam-briefing>\nSeam project: %s -- %d memories (%d constraints), %d recent findings.\n",
			sanitizeField(label, 80), totalMems, len(constraints), renderedFindings)
	}

	var head strings.Builder
	full, compact := constraintTiers(constraints, cfg.ConstraintMaxFull)
	for _, c := range full {
		head.WriteString("CONSTRAINT: " + sanitizeField(c.Name, 80) + ": " + sanitizeField(c.Description, 160) + "\n")
		ids = append(ids, c.ID)
	}
	// The compact tier is part of the same pinned head -- budget can drop
	// neither tier -- and its ids are recorded like the full lines' so the
	// read-after-inject funnel keeps seeing every constraint.
	head.WriteString(alsoBindingLine(compact))
	for _, c := range compact {
		ids = append(ids, c.ID)
	}
	// Pinned stages sit right after constraints and, like them, are never dropped
	// for budget -- a gated stage's status is load-bearing for the whole session.
	head.WriteString(stageHead(sec.stages))
	for _, st := range sec.stages {
		ids = append(ids, st.id)
	}
	// Active-plan rollups follow stages, also pinned: a plan's claimable/in-flight
	// counts tell the next agent what work is available to pick up right now.
	head.WriteString(planHead(sec.plans))
	// Starred memories close the pinned head: the owner (or an agent) marked
	// them important enough that every session should see them, so like
	// constraints they are exempt from the trims and the budget loop.
	for _, m := range sec.favorites {
		head.WriteString("FAVORITE: " + sanitizeField(m.Name, 80) + ": " + sanitizeField(m.Description, 160) + "\n")
		ids = append(ids, m.ID)
	}

	var tail strings.Builder
	tail.WriteString(agentguide.BriefingFooter)
	if source == "compact" || source == "resume" {
		tail.WriteString("(resumed session -- earlier context may be summarized; recall to re-ground.)\n")
	}
	tail.WriteString("</seam-briefing>")

	used := estTokens(headLine(len(findings))) + estTokens(head.String()) + estTokens(tail.String())

	var body strings.Builder
	// Pending (unapproved) plan lines come first in the body so they read as a
	// continuation of the pinned PLAN rollups, but unlike those they compete
	// for budget. Packing first gives them budget priority too -- deliberate:
	// they are few and PendingPlanMaxDays-bounded, so they cannot crowd out
	// what follows the way a fat index could.
	for _, n := range sec.pendingPlans {
		line := fmt.Sprintf("PLAN (awaiting approval): %s -- %s (%s, %s)\n",
			sanitizeField(plans.SlugFromTags(n.Tags), 80), sanitizeField(n.Title, 80),
			plans.StatusFromTags(n.Tags), humanAge(n.Updated))
		if used+estTokens(line) > budget {
			break
		}
		body.WriteString(line)
		used += estTokens(line)
	}

	// Recent findings pack before the memory index: they are few
	// (FindingsCount-capped) and say what just happened here, so a fat index
	// can neither out-render nor out-budget them.
	findingsRendered := 0
	if len(findings) > 0 {
		lead := "\nRecent findings:\n"
		if used+estTokens(lead) <= budget {
			body.WriteString(lead)
			used += estTokens(lead)
			for _, f := range findings {
				line := "- " + sanitizeField(f.Name, 80) + " (" + humanAge(f.UpdatedAt) + "): " + clipWords(sanitizeField(f.Findings, 0), 200) + "\n"
				if used+estTokens(line) > budget {
					break
				}
				body.WriteString(line)
				used += estTokens(line)
				findingsRendered++
			}
			if cut := len(findings) - findingsRendered; cut > 0 {
				extra := fmt.Sprintf("- (+%d more -- use recall)\n", cut)
				body.WriteString(extra)
				used += estTokens(extra)
			}
		}
	}

	// The ready-tasks line follows the findings: the queue is part of the
	// situation, not the library. Sections still pack after it, so its cost
	// accumulates like any other line.
	if line := readyTasksLine(ready, cfg.ReadyTasksShown); line != "" && used+estTokens(line) <= budget {
		body.WriteString(line)
		used += estTokens(line)
	}

	dropped := 0
	if len(index) > 0 || sec.indexOmitted > 0 {
		lead := "\nMemories (" + sanitizeField(label, 80) + "):\n"
		body.WriteString(lead)
		used += estTokens(lead)
		for i, m := range index {
			line := "- " + sanitizeField(m.Name, 80) + ": " + sanitizeField(m.Description, 160) + "\n"
			if used+estTokens(line) > budget && i > 0 {
				dropped = len(index) - i
				break
			}
			body.WriteString(line)
			used += estTokens(line)
			ids = append(ids, m.ID)
		}
		// The trailer counts both budget-dropped lines and the recency/count trims,
		// so filtered-out knowledge stays discoverable via recall.
		if dropped+sec.indexOmitted > 0 {
			extra := fmt.Sprintf("- (+%d older -- use recall)\n", dropped+sec.indexOmitted)
			body.WriteString(extra)
			used += estTokens(extra)
		}
	}

	if len(sec.siblings) > 0 {
		lead := "\n## Sibling projects\n"
		if used+estTokens(lead) <= budget {
			body.WriteString(lead)
			used += estTokens(lead)
			for _, f := range sec.siblings {
				line := "- " + sanitizeField(f.ProjectSlug, 60) + " (" + humanAge(f.UpdatedAt) + "): " + clipWords(sanitizeField(f.Findings, 0), 150) + "\n"
				if used+estTokens(line) > budget {
					break
				}
				body.WriteString(line)
				used += estTokens(line)
			}
		}
	}

	// Sibling memories pack right after the sibling findings: durable cross-over
	// knowledge, deliberately below the own-project index so budget drops it first.
	if len(sec.siblingMems) > 0 {
		lead := "\nSibling memories:\n"
		if used+estTokens(lead) <= budget {
			body.WriteString(lead)
			used += estTokens(lead)
			for _, m := range sec.siblingMems {
				line := "- " + sanitizeField(m.Project, 60) + "/" + sanitizeField(m.Name, 80) + ": " + sanitizeField(m.Description, 160) + "\n"
				if used+estTokens(line) > budget {
					break
				}
				body.WriteString(line)
				used += estTokens(line)
				ids = append(ids, m.ID)
			}
		}
	}

	return hardTruncate(headLine(findingsRendered)+head.String()+body.String()+tail.String(), hardCap), ids
}

// planHead renders one pinned line per active plan
// ("PLAN: <slug> -- X/Y done, Z claimable, W in flight"), or "" when there are
// none. A trailing reminder of the plan:<slug> convention is appended once so
// agents discover how to attach step tasks and supporting notes to a plan.
func planHead(plans []store.PlanRollup) string {
	if len(plans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range plans {
		fmt.Fprintf(&b, "PLAN: %s -- %d/%d done, %d claimable, %d in flight\n",
			sanitizeField(p.Slug, 80), p.Done, p.Total, p.Claimable, p.InFlight)
	}
	b.WriteString("(claim a step with tasks_claim; attach notes/tasks to a plan with the plan:<slug> convention)\n")
	return b.String()
}

// readyTasksLine renders the briefing's ready-queue line ("Ready tasks: N -- t1;
// t2; t3"), naming up to shown oldest ready tasks, or "" when none are ready or
// the line is disabled (shown 0). The ordering matches store.ReadyTasks (oldest
// first), which the CLI shares.
func readyTasksLine(ready []core.Task, shown int) string {
	if len(ready) == 0 || shown <= 0 {
		return ""
	}
	titles := make([]string, 0, min(shown, len(ready)))
	for _, t := range ready {
		if len(titles) == shown {
			break
		}
		titles = append(titles, sanitizeField(t.Title, 60))
	}
	return fmt.Sprintf("\nReady tasks: %d -- %s\n", len(ready), strings.Join(titles, "; "))
}

// subagentFooter is the always-on tool-guidance line closing every non-empty
// subagent briefing: the first sentence of agentguide.BriefingFooter (a test
// pins the prefix relation so the vocabulary cannot drift). Without it a child
// in a project with few constraints would get no hint that recall/memory_read
// exist -- alsoBindingLine names memory_read only past the tier cap. The mild
// redundancy between the two lines when both render is accepted by design.
const subagentFooter = "Recall on demand with recall; read a memory with memory_read.\n"

// assembleSubagent renders a constraints-only briefing for a subagent, or "" if
// there are no constraints in scope. Constraints arrive already ranked
// (rankConstraints) and get the same maxFull tier split as the full briefing --
// a subagent drowns in an all-full wall just the same. Whenever the briefing
// renders it closes with subagentFooter, independent of the tier split. The
// second return value is the ids of the rendered constraints (both tiers), for
// retrieval instrumentation.
func (s *Service) assembleSubagent(project string, constraints []core.Memory, maxFull int) (string, []string) {
	if len(constraints) == 0 {
		return "", nil
	}
	label := projectLabel(project)
	ids := make([]string, 0, len(constraints))
	var b strings.Builder
	b.WriteString("<seam-briefing>\n")
	fmt.Fprintf(&b, "Seam project: %s -- %d constraints (subagent scope).\n", sanitizeField(label, 80), len(constraints))
	full, compact := constraintTiers(constraints, maxFull)
	for _, c := range full {
		b.WriteString("CONSTRAINT: " + sanitizeField(c.Name, 80) + ": " + sanitizeField(c.Description, 160) + "\n")
		ids = append(ids, c.ID)
	}
	b.WriteString(alsoBindingLine(compact))
	for _, c := range compact {
		ids = append(ids, c.ID)
	}
	b.WriteString(subagentFooter)
	b.WriteString("</seam-briefing>")
	return b.String(), ids
}

// hardTruncate caps s at hardCapTokens estimated tokens while preserving the
// closing </seam-briefing> tag, so a truncated briefing is still well-formed.
func hardTruncate(s string, hardCapTokens int) string {
	if estTokens(s) <= hardCapTokens {
		return s
	}
	const closeTag = "\n</seam-briefing>"
	maxChars := hardCapTokens * 4
	if maxChars <= len(closeTag) {
		return s
	}
	body := strings.TrimSuffix(s, "</seam-briefing>")
	body = strings.TrimRight(body, "\n")
	if cut := maxChars - len(closeTag); len(body) > cut {
		// Back off to a rune boundary so truncation never emits invalid UTF-8.
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + "..."
	}
	return body + closeTag
}
