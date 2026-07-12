package retrieve

import (
	"context"
	"fmt"
	"strings"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// BriefingInput carries the SessionStart hook fields the briefing depends on.
type BriefingInput struct {
	CWD       string // agent working directory; resolved to a project slug
	Source    string // startup|resume|clear|compact
	AgentType string // non-empty => subagent => constraints-only briefing
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
	project, err := store.ResolveProjectForCWD(ctx, s.db, in.CWD)
	if err != nil {
		return "", nil, err
	}
	mems, err := store.ActiveMemories(ctx, s.db, project)
	if err != nil {
		return "", nil, err
	}
	var constraints, index, stageMems []core.Memory
	for _, m := range mems {
		switch m.Kind {
		case core.KindConstraint:
			constraints = append(constraints, m)
		case core.KindStage:
			stageMems = append(stageMems, m)
		default:
			index = append(index, m)
		}
	}

	// Subagents get constraints only (they inherit the parent's task context).
	if in.AgentType != "" {
		text, ids := s.assembleSubagent(project, constraints)
		return text, ids, nil
	}

	findings, err := store.RecentFindings(ctx, s.db, project, 3)
	if err != nil {
		return "", nil, err
	}
	ready, err := store.ReadyTasks(ctx, s.db, project)
	if err != nil {
		return "", nil, err
	}
	siblings, err := s.siblingFindings(ctx, project)
	if err != nil {
		return "", nil, err
	}
	stages := s.pinnedStages(stageMems)
	if len(constraints) == 0 && len(index) == 0 && len(findings) == 0 &&
		len(ready) == 0 && len(siblings) == 0 && len(stages) == 0 {
		return "", nil, nil
	}
	text, ids := s.assembleBriefing(project, in.Source, briefingSections{
		constraints: constraints, index: index, findings: findings,
		ready: ready, siblings: siblings, stages: stages,
	})
	return text, ids, nil
}

// siblingFindings gathers up to two recent findings from a project's family
// members (see store.SiblingProjects/SiblingFindings), for the briefing's
// "Sibling projects" section. It is a no-op for the global scope or a project
// with no configured family.
func (s *Service) siblingFindings(ctx context.Context, project string) ([]core.Session, error) {
	siblings, err := store.SiblingProjects(ctx, s.db, project)
	if err != nil || len(siblings) == 0 {
		return nil, err
	}
	return store.SiblingFindings(ctx, s.db, siblings, 2)
}

// RegisterProjectForCWD resolves cwd to a project slug and, for a not-yet-mapped
// repo, grows the repo->project map (see store.RegisterProjectForCWD). The
// session-start hook calls it before assembling the briefing so an agent working
// in a new repo is bound to a freshly registered project. It is failure-soft:
// resolution errors degrade to the global scope rather than blocking the agent.
func (s *Service) RegisterProjectForCWD(ctx context.Context, cwd string) string {
	slug, err := store.RegisterProjectForCWD(ctx, s.db, cwd)
	if err != nil {
		s.logger.Warn("retrieve: register project for cwd", "cwd", cwd, "error", err)
		return ""
	}
	return slug
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
	constraints []core.Memory
	index       []core.Memory
	findings    []core.Session
	ready       []core.Task
	siblings    []core.Session // recent findings from family-member projects
	stages      []stageLine    // non-done stage memories, pinned after constraints
}

// assembleBriefing packs the sections against the token budget. Constraints, the
// header, and the trailer are counted first and never dropped; the memory index,
// sibling findings, and findings are packed against the soft budget, then the
// whole is hard-capped. Section order: constraints > memory index > sibling
// findings > recent findings > ready tasks. The second return value is the ids of
// the memories actually rendered (constraints, pinned stages, and the index lines
// that survived budgeting) -- for retrieval instrumentation. Findings, siblings,
// and ready tasks are sessions/tasks, not memory_read-able memories, so they are
// omitted from the funnel.
func (s *Service) assembleBriefing(project, source string, sec briefingSections) (string, []string) {
	constraints, index := sec.constraints, sec.index
	findings, ready := sec.findings, sec.ready
	label := projectLabel(project)
	budget := s.budgets.MaxBriefingTokens
	if budget <= 0 {
		budget = 1500
	}
	hardCap := budget * 2

	ids := make([]string, 0, len(constraints)+len(sec.stages)+len(index))

	var head strings.Builder
	head.WriteString("<seam-briefing>\n")
	fmt.Fprintf(&head, "Seam project: %s -- %d constraints, %d memories, %d recent findings.\n",
		sanitizeField(label, 80), len(constraints), len(index), len(findings))
	for _, c := range constraints {
		head.WriteString("CONSTRAINT: " + sanitizeField(c.Name, 80) + ": " + sanitizeField(c.Description, 160) + "\n")
		ids = append(ids, c.ID)
	}
	// Pinned stages sit right after constraints and, like them, are never dropped
	// for budget -- a gated stage's status is load-bearing for the whole session.
	head.WriteString(stageHead(sec.stages))
	for _, st := range sec.stages {
		ids = append(ids, st.id)
	}

	var tail strings.Builder
	tail.WriteString("Recall on demand with recall; read a memory with memory_read.\n")
	if source == "compact" || source == "resume" {
		tail.WriteString("(resumed session -- earlier context may be summarized; recall to re-ground.)\n")
	}
	tail.WriteString("</seam-briefing>")

	used := estTokens(head.String()) + estTokens(tail.String())

	var body strings.Builder
	dropped := 0
	if len(index) > 0 {
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
		if dropped > 0 {
			extra := fmt.Sprintf("- (+%d older -- use recall)\n", dropped)
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
				line := "- " + sanitizeField(f.ProjectSlug, 60) + " (" + humanAge(f.UpdatedAt) + "): " + sanitizeField(f.Findings, 150) + "\n"
				if used+estTokens(line) > budget {
					break
				}
				body.WriteString(line)
				used += estTokens(line)
			}
		}
	}

	if len(findings) > 0 {
		lead := "\nRecent findings:\n"
		if used+estTokens(lead) <= budget {
			body.WriteString(lead)
			used += estTokens(lead)
			for _, f := range findings {
				line := "- " + sanitizeField(f.Name, 80) + " (" + humanAge(f.UpdatedAt) + "): " + sanitizeField(f.Findings, 200) + "\n"
				if used+estTokens(line) > budget {
					break
				}
				body.WriteString(line)
				used += estTokens(line)
			}
		}
	}

	// Ready tasks is the last body section, so its cost is only checked against
	// the budget, not accumulated (nothing follows it).
	if line := readyTasksLine(ready); line != "" && used+estTokens(line) <= budget {
		body.WriteString(line)
	}

	return hardTruncate(head.String()+body.String()+tail.String(), hardCap), ids
}

// readyTasksLine renders the briefing's ready-queue line ("Ready tasks: N -- t1;
// t2; t3"), naming up to the three oldest ready tasks, or "" when none are ready.
// The ordering matches store.ReadyTasks (oldest first), which the CLI shares.
func readyTasksLine(ready []core.Task) string {
	if len(ready) == 0 {
		return ""
	}
	titles := make([]string, 0, 3)
	for _, t := range ready {
		if len(titles) == 3 {
			break
		}
		titles = append(titles, sanitizeField(t.Title, 60))
	}
	return fmt.Sprintf("\nReady tasks: %d -- %s\n", len(ready), strings.Join(titles, "; "))
}

// assembleSubagent renders a constraints-only briefing for a subagent, or "" if
// there are no constraints in scope. The second return value is the ids of the
// rendered constraints, for retrieval instrumentation.
func (s *Service) assembleSubagent(project string, constraints []core.Memory) (string, []string) {
	if len(constraints) == 0 {
		return "", nil
	}
	label := projectLabel(project)
	ids := make([]string, 0, len(constraints))
	var b strings.Builder
	b.WriteString("<seam-briefing>\n")
	fmt.Fprintf(&b, "Seam project: %s -- %d constraints (subagent scope).\n", sanitizeField(label, 80), len(constraints))
	for _, c := range constraints {
		b.WriteString("CONSTRAINT: " + sanitizeField(c.Name, 80) + ": " + sanitizeField(c.Description, 160) + "\n")
		ids = append(ids, c.ID)
	}
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
	if len(body) > maxChars-len(closeTag) {
		body = body[:maxChars-len(closeTag)] + "..."
	}
	return body + closeTag
}
