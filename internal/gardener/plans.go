package gardener

// Stale-plan pass: a captured Claude Code plan that was never approved
// (plan-status draft/presented) past StalePlanDays is proposed for settlement.
// Which settlement depends on what the repo's history says happened after the
// capture: when commits landed after the capture's git stamp whose reflog
// messages match the plan (shared tokens with its title, headings, or
// mentioned files), the pass proposes marking it shipped -- the work happened,
// just without the approval ceremony -- otherwise it proposes abandonment,
// with whatever partial evidence it found folded into the reason. Applying
// retags the cc-plan note (plan-status:shipped or plan-status:abandoned),
// which removes it from the briefing's awaiting-approval lines; the note
// itself stays readable, as always.
//
// The evidence is read straight out of .git (internal/gitread) -- the daemon
// never execs git -- from the repos the repo_project_map ties to the note's
// project. Everything is best-effort: no stamp, no mapped repo, or no reflog
// simply means no ship evidence, and the pass proposes abandonment exactly as
// it did before the fork existed. The gardener stays propose-only either way;
// the owner judges the evidence at the review gate.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/gitread"
	"github.com/0spoon/seamless/internal/plans"
	"github.com/0spoon/seamless/internal/store"
)

// maxShipCommits caps how many matching commits a ship proposal's payload
// lists (the counts still cover everything).
const maxShipCommits = 8

// proposeStalePlans proposes settling captured plans still unapproved after
// StalePlanDays -- shipped when the repo evidences the work landed, abandoned
// otherwise. 0 disables the pass.
func (s *Service) proposeStalePlans(ctx context.Context, seen seenKeys) (int, error) {
	if s.cfg.StalePlanDays <= 0 {
		return 0, nil
	}
	cutoff := s.now().UTC().Add(-time.Duration(s.cfg.StalePlanDays) * 24 * time.Hour)
	notes, err := store.NotesByTag(ctx, s.db, "", plans.TagPlan)
	if err != nil {
		return 0, err
	}
	var roots map[string][]string // project -> mapped repo roots, loaded on first stale hit
	created := 0
	for _, n := range notes {
		switch plans.StatusFromTags(n.Tags) {
		case plans.StatusDraft, plans.StatusPresented:
		default:
			continue // approved, abandoned, and shipped plans are settled
		}
		if n.Updated.After(cutoff) {
			continue
		}
		if roots == nil {
			if roots, err = projectRepoRoots(ctx, s.db); err != nil {
				return created, err
			}
		}
		ev := s.shipEvidence(n, roots[n.Project])

		// The two keys are deliberately distinct: an abandon proposed before the
		// work landed does not stop a later pass from surfacing the ship evidence.
		kind, key := store.ProposalAbandonPlan, "abandon_plan:"+n.ID
		if ev.shipped() {
			kind, key = store.ProposalShipPlan, "ship_plan:"+n.ID
		}
		if seen.blocked(key) {
			continue
		}
		payload := map[string]any{
			"id": n.ID, "slug": plans.SlugFromTags(n.Tags), "note_slug": n.Slug,
			"title": n.Title, "project": n.Project, "plan_status": plans.StatusFromTags(n.Tags),
			"reason":        ev.reason(s.cfg.StalePlanDays),
			"last_activity": core.FormatTime(n.Updated),
		}
		if ev.shipped() {
			payload["repo"] = ev.repo
			payload["stamp"] = ev.stamp
			payload["commits_since"] = ev.commitsSince
			payload["matched_count"] = ev.matchedCount
			payload["commits"] = ev.matched
		}
		if _, err := s.createProposal(ctx, kind, key, payload, seen); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// applySettlePlan retags the plan note with a terminal status (abandoned or
// shipped). A plan that was approved after the proposal was raised is left
// alone (error keeps the proposal pending, so the owner sees why and
// dismisses it).
func (s *Service) applySettlePlan(ctx context.Context, p store.Proposal, status string, now time.Time) (map[string]any, error) {
	id := payloadString(p.Payload, "id")
	idx, ok, err := store.NoteByID(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("plan note %q no longer exists", id)
	}
	// Retag under the note's lock. The capture hook rewrites this file on every
	// plan-file save and the console's approve hatch on every approval, so the
	// settlement reads the tags it is judging inside the lock -- an approval
	// landing between an unlocked read and the write would be silently retagged
	// away, which is exactly the state this apply refuses to overwrite.
	written, err := s.files.MutateNote(ctx, idx.FilePath, func(_ context.Context, note core.Note) (core.Note, error) {
		if plans.StatusFromTags(note.Tags) == plans.StatusApproved {
			return core.Note{}, fmt.Errorf("plan %q was approved since this was proposed", note.Slug)
		}
		basename := plans.Basename(note.Slug)
		note.Tags = plans.SetStatusTag(note.Tags, status)
		note.Description = plans.NoteDescription(basename, plans.NoteIteration(note), status)
		note.Updated = now
		return note, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		status: written.Slug, "plan": plans.SlugFromTags(written.Tags), "project": written.Project,
	}, nil
}

// shipEvidence is what the repo's history says happened after a plan capture.
// The zero value means nothing could be established (no stamp, no mapped repo,
// no reflog).
type shipEvidence struct {
	repo          string   // the repo root the evidence came from
	stamp         string   // the capture's short git head
	stampFound    bool     // the stamp was located in a mapped repo's reflog
	repoUnchanged bool     // HEAD still sits on the stamp: nothing landed at all
	commitsSince  int      // commits landed after the stamp
	matchedCount  int      // how many of them match the plan
	matched       []string // "sha message" display lines, capped at maxShipCommits
}

// shipped reports whether the evidence supports settling the plan as shipped.
func (e shipEvidence) shipped() bool { return e.matchedCount > 0 }

// reason renders the settlement rationale, folding in whatever the repo
// established so an abandon never hides that commits were checked.
func (e shipEvidence) reason(days int) string {
	base := "never approved in " + strconv.Itoa(days) + "d"
	switch {
	case e.shipped():
		return fmt.Sprintf("unapproved for %dd, but %d of %d commit(s) since its capture match the plan -- the work appears to have shipped",
			days, e.matchedCount, e.commitsSince)
	case e.repoUnchanged:
		return base + "; repo unchanged since the capture"
	case e.stampFound && e.commitsSince > 0:
		return fmt.Sprintf("%s; %d commit(s) since the capture, none matching the plan", base, e.commitsSince)
	default:
		return base
	}
}

// shipEvidence gathers the git evidence for one stale captured plan: it reads
// the full note for the capture stamp, then walks the HEAD reflog of each repo
// mapped to the note's project for commits that landed after the stamp and
// match the plan's tokens. Best-effort throughout -- any gap yields weaker
// evidence, never an error.
func (s *Service) shipEvidence(n core.Note, roots []string) shipEvidence {
	full, err := s.files.Store().ReadNote(n.FilePath)
	if err != nil {
		return shipEvidence{}
	}
	stamp := plans.StampHead(full.Body)
	if stamp == "" || stamp == "unknown" {
		return shipEvidence{}
	}
	tokens := planTokens(full.Title, full.Body)
	sort.Strings(roots)
	for _, root := range roots {
		if _, err := os.Lstat(root); err != nil {
			continue
		}
		entries := gitread.ReflogHEAD(root)
		last := -1
		for i, e := range entries {
			if strings.HasPrefix(e.New, stamp) {
				last = i // the last time HEAD landed on the stamped commit
			}
		}
		if last == -1 {
			continue
		}
		ev := shipEvidence{repo: root, stamp: stamp, stampFound: true}
		counted := map[string]struct{}{}
		for _, e := range entries[last+1:] {
			if !landedCommit(e.Message) {
				continue
			}
			if _, dup := counted[e.New]; dup {
				continue // a reset/checkout revisiting the same commit
			}
			counted[e.New] = struct{}{}
			ev.commitsSince++
			if tokenOverlap(tokens, e.Message) >= 2 {
				ev.matchedCount++
				if len(ev.matched) < maxShipCommits {
					ev.matched = append(ev.matched, shortHash(e.New)+" "+e.Message)
				}
			}
		}
		ev.repoUnchanged = ev.commitsSince == 0
		return ev
	}
	// The stamp is in no mapped repo's reflog. A HEAD still sitting on the
	// stamp is conclusive anyway: nothing landed.
	for _, root := range roots {
		if head := gitread.Head(root); head != "" && strings.HasPrefix(head, stamp) {
			return shipEvidence{repo: root, stamp: stamp, repoUnchanged: true}
		}
	}
	return shipEvidence{stamp: stamp}
}

// projectRepoRoots inverts the repo_project_map: every mapped repo path per
// project slug (a project can own several -- the main checkout plus
// out-of-tree worktrees).
func projectRepoRoots(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	m, err := store.RepoProjectMap(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(m))
	for path, slug := range m {
		out[slug] = append(out[slug], path)
	}
	return out, nil
}

// landedCommitPrefixes are the reflog actions that add commits to the current
// branch; everything else (checkout, reset, branch) only moves HEAD around.
var landedCommitPrefixes = []string{"commit", "merge", "pull", "cherry-pick", "rebase (finish)"}

// landedCommit reports whether a reflog message records a commit landing.
func landedCommit(msg string) bool {
	for _, p := range landedCommitPrefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// planTokens extracts the tokens a commit message could plausibly echo from a
// captured plan: words from its title and markdown headings, plus words from
// the file paths its body mentions. Deliberately not the whole body -- prose
// overlaps everything and would turn every active repo's history into ship
// evidence.
func planTokens(title, body string) map[string]struct{} {
	tokens := map[string]struct{}{}
	addWordTokens(tokens, title)
	for line := range strings.Lines(body) {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			addWordTokens(tokens, strings.TrimLeft(t, "# "))
		}
	}
	for f := range strings.FieldsSeq(body) {
		f = strings.Trim(f, "`*(),.;:'\"")
		if strings.ContainsRune(f, '/') || hasCodeExt(f) {
			addWordTokens(tokens, f)
		}
	}
	return tokens
}

// tokenOverlap counts the distinct plan tokens a commit message shares.
func tokenOverlap(tokens map[string]struct{}, msg string) int {
	shared := map[string]struct{}{}
	for _, w := range wordTokens(msg) {
		if _, ok := tokens[w]; ok {
			shared[w] = struct{}{}
		}
	}
	return len(shared)
}

// matchStopwords are words too common in both plan prose and commit subjects
// to evidence a connection: the conventional-commit types that open nearly
// every subject line, and the reflog action words ("commit:", "merge <x>:")
// that appear in every landed entry.
var matchStopwords = map[string]struct{}{
	"this": {}, "that": {}, "with": {}, "from": {}, "into": {}, "then": {},
	"when": {}, "what": {}, "have": {}, "will": {}, "make": {}, "made": {},
	"plan": {}, "plans": {}, "step": {}, "steps": {}, "should": {}, "would": {},
	"feat": {}, "docs": {}, "chore": {}, "test": {}, "tests": {}, "refactor": {},
	"perf": {}, "build": {}, "style": {},
	"commit": {}, "commits": {}, "merge": {}, "merges": {}, "pull": {},
	"rebase": {}, "cherry": {}, "pick": {},
}

// addWordTokens folds s's significant words into tokens.
func addWordTokens(tokens map[string]struct{}, s string) {
	for _, w := range wordTokens(s) {
		tokens[w] = struct{}{}
	}
}

// wordTokens lowercases s and splits it into words of four or more letters or
// digits, dropping the stopwords.
func wordTokens(s string) []string {
	var out []string
	for w := range strings.FieldsFuncSeq(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if len(w) < 4 {
			continue
		}
		if _, stop := matchStopwords[w]; stop {
			continue
		}
		out = append(out, w)
	}
	return out
}

// codeExts are the file extensions that mark a bare token as a mentioned file.
var codeExts = []string{
	".go", ".md", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".swift", ".kt",
	".java", ".c", ".h", ".cpp", ".yaml", ".yml", ".json", ".sql", ".html",
	".css", ".sh", ".proto",
}

// hasCodeExt reports whether a token ends in a code-ish file extension.
func hasCodeExt(s string) bool {
	for _, ext := range codeExts {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

// shortHash abbreviates a commit hash for display.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
