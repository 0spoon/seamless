package gardener

// Natural-language maintenance requests. The owner describes an organization
// problem ("these two are duplicates", "archive anything about the old port")
// and the gardener interprets it into PENDING proposals for review. Like every
// other pass it only ever writes gardener_proposals rows -- it never mutates a
// memory. Interpretation reuses the existing merge/archive apply paths verbatim.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// Sentinel errors for Request, checked with errors.Is.
var (
	ErrNoChat       = errors.New("gardener: natural-language requests need an LLM chat client")
	ErrEmptyRequest = errors.New("gardener: empty request")
	ErrUnparseable  = errors.New("gardener: could not parse the interpreter's output")
)

const (
	// requestTimeout bounds one interpretation. It is tighter than the llm
	// chatTimeout (60s) so a slow provider fails the console POST promptly rather
	// than hanging the request.
	requestTimeout = 45 * time.Second
	// maxRequestCandidates caps how many active memories are shown to the model,
	// bounding prompt size. A personal store holds tens per scope; this is a guard
	// against a pathologically large corpus, not an expected limit.
	maxRequestCandidates = 150
)

// CanRequest reports whether natural-language requests are available (a chat
// client is configured). The console and MCP gate their UI on it without needing
// to see the private chat field.
func (s *Service) CanRequest() bool { return s.chat != nil }

// RequestResult summarizes one interpretation pass.
type RequestResult struct {
	Created []string       `json:"created"` // proposal ids, in creation order
	ByKind  map[string]int `json:"byKind"`  // e.g. {"merge":2,"archive":1}
	Total   int            `json:"total"`
	Skipped []string       `json:"skipped"` // human-readable notes on dropped ops
	Summary string         `json:"summary"` // one-line summary
}

// Request interprets a single natural-language maintenance request against the
// active memories in scope and creates PENDING proposals for review. It never
// mutates a memory. projectScope: "" => all projects; "global" => globals only;
// any slug => that project plus globals.
func (s *Service) Request(ctx context.Context, text, projectScope string) (RequestResult, error) {
	if s.chat == nil {
		return RequestResult{}, ErrNoChat
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return RequestResult{}, ErrEmptyRequest
	}

	candidates, err := s.requestCandidates(ctx, projectScope)
	if err != nil {
		return RequestResult{}, fmt.Errorf("gardener.Request: %w", err)
	}
	if len(candidates) == 0 {
		return RequestResult{ByKind: map[string]int{}, Summary: "no active memories in scope"}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	out, err := s.chat.Complete(cctx, requestSystemPrompt, requestUserPrompt(text, candidates))
	if err != nil {
		return RequestResult{}, fmt.Errorf("gardener.Request: %w", err)
	}

	plan, err := parseRequestPlan(out)
	if err != nil {
		return RequestResult{}, err // already wraps ErrUnparseable
	}

	// Seed the dedup set from every existing proposal key (any status) so a
	// request never re-raises something already proposed, applied, or dismissed.
	seen, err := store.AllProposalKeys(ctx, s.db)
	if err != nil {
		return RequestResult{}, fmt.Errorf("gardener.Request: %w", err)
	}

	res := RequestResult{ByKind: map[string]int{}}
	for _, op := range plan.Ops {
		kind, key, payload, skip := mapRequestOp(op, candidates, text)
		if skip != "" {
			res.Skipped = append(res.Skipped, skip)
			continue
		}
		if _, dup := seen[key]; dup {
			res.Skipped = append(res.Skipped, kind+" is already proposed")
			continue
		}
		id, err := s.createProposal(ctx, kind, key, payload, seen)
		if err != nil {
			return res, fmt.Errorf("gardener.Request: %w", err)
		}
		res.Created = append(res.Created, id)
		res.ByKind[kind]++
		res.Total++
	}
	res.Summary = requestSummary(res)

	s.record(ctx, "", map[string]any{
		"action": "request", "text": truncateRunes(text, 200),
		"created": res.Total, "merges": res.ByKind[store.ProposalMerge],
		"archives": res.ByKind[store.ProposalArchive], "consolidations": res.ByKind[store.ProposalConsolidate],
	})
	return res, nil
}

// requestCandidates loads the active memories the interpreter may reference,
// scoped and capped. "" => every project; "global" => globals only; a slug =>
// that project plus globals.
func (s *Service) requestCandidates(ctx context.Context, projectScope string) ([]core.Memory, error) {
	var (
		mems []core.Memory
		err  error
	)
	if projectScope == "" {
		mems, err = store.AllActiveMemories(ctx, s.db)
	} else {
		scope := projectScope
		if scope == "global" {
			scope = "" // ActiveMemories("") returns only global memories
		}
		mems, err = store.ActiveMemories(ctx, s.db, scope)
	}
	if err != nil {
		return nil, err
	}
	if len(mems) > maxRequestCandidates {
		mems = mems[:maxRequestCandidates]
	}
	return mems, nil
}

const requestSystemPrompt = `You convert a single natural-language maintenance request into a set of PROPOSED, reviewable operations over an AI agent's memory store. You never modify anything yourself; a human reviews and applies each operation. Reference memories only by their candidate number.

Operations:
- merge: fold one memory into another EXISTING memory. {"op":"merge","keep":<n>,"drop":<m>}. keep is retained as-is; drop is superseded by keep (still readable, pointing at keep). Use this when one existing memory should simply absorb another.
- archive: retire a memory that is no longer relevant. {"op":"archive","target":<n>,"reason":"<short why>"}. The memory is marked invalid but stays readable.
- consolidate: replace several redundant memories with ONE new unified memory that you write. {"op":"consolidate","name":"<short-kebab-name>","kind":"<kind>","description":"<one line, <=150 chars>","body":"<full markdown body>","sources":[<n>,<m>,...]}. Every named source is superseded by the new memory. Use this (not merge) when the truth is spread across several memories and a fresh combined write is clearer than keeping any single one.

Rules:
- Reference memories ONLY by their [N] candidate number. Never invent numbers.
- For a merge, keep and drop must be different memories.
- For consolidate, kind is one of: constraint, runbook, protocol, gotcha, decision, refuted, reference, stage. name is a short kebab-case identifier. Write a genuine unified body from the sources; do not invent facts beyond them.
- Propose only what the request asks for. If nothing applies, return {"ops":[]}.
- Output ONLY a JSON object of the form {"ops":[...]} -- no prose, no markdown code fences.`

// requestUserPrompt renders the request plus a numbered candidate list. The
// model references memories by their [N] index, resolved back to ids server-side.
func requestUserPrompt(text string, mems []core.Memory) string {
	var b strings.Builder
	b.WriteString("Request:\n")
	b.WriteString(text)
	b.WriteString("\n\nCandidate memories (reference by [N]):\n")
	for i, m := range mems {
		fmt.Fprintf(&b, "[%d] %s (%s, %s) -- %s\n", i+1, m.Name, projectLabel(m.Project), m.Kind, m.Description)
	}
	return b.String()
}

func projectLabel(project string) string {
	if project == "" {
		return "global"
	}
	return project
}

// reqOp is one operation the interpreter emits. Candidate references are 1-based
// indices into the candidate list, so 0 is a clean "absent" sentinel.
type reqOp struct {
	Op     string `json:"op"`     // "merge" | "archive" | "consolidate"
	Keep   int    `json:"keep"`   // merge: candidate to retain
	Drop   int    `json:"drop"`   // merge: candidate to fold into keep
	Target int    `json:"target"` // archive: candidate to retire
	Reason string `json:"reason"` // archive rationale

	// consolidate: a new unified memory written from several sources.
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Sources     []int  `json:"sources"`
}

type reqPlan struct {
	Ops []reqOp `json:"ops"`
}

// parseRequestPlan extracts the {"ops":[...]} object from a completion, tolerating
// a code fence or surrounding prose. It never fabricates: a body that will not
// unmarshal yields ErrUnparseable so the caller creates nothing.
func parseRequestPlan(raw string) (reqPlan, error) {
	s := stripCodeFence(strings.TrimSpace(raw))
	if !strings.HasPrefix(s, "{") {
		if i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); i >= 0 && j > i {
			s = s[i : j+1]
		}
	}
	var plan reqPlan
	if err := json.Unmarshal([]byte(s), &plan); err != nil {
		return reqPlan{}, fmt.Errorf("%w: %w", ErrUnparseable, err)
	}
	return plan, nil
}

// stripCodeFence removes a leading ```/```json fence line and a trailing ``` if
// the model wrapped its JSON in a Markdown code block.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if _, rest, ok := strings.Cut(s, "\n"); ok {
		s = rest
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// mapRequestOp validates one op against the candidate list and produces the
// proposal (kind, dedup key, payload). A non-empty skip note means the op was
// invalid and is dropped rather than fabricated. Keys match the gardener's own
// passes (mergeKey / archive:<id>) so a request shares dedup state with them.
func mapRequestOp(op reqOp, candidates []core.Memory, text string) (kind, key string, payload map[string]any, skip string) {
	switch op.Op {
	case store.ProposalMerge:
		keep, ok := candidateAt(candidates, op.Keep)
		if !ok {
			return "", "", nil, fmt.Sprintf("merge references memory #%d, which is not in the candidate list", op.Keep)
		}
		drop, ok := candidateAt(candidates, op.Drop)
		if !ok {
			return "", "", nil, fmt.Sprintf("merge references memory #%d, which is not in the candidate list", op.Drop)
		}
		if keep.ID == drop.ID {
			return "", "", nil, "merge keep and drop are the same memory"
		}
		return store.ProposalMerge, mergeKey(keep.ID, drop.ID), map[string]any{
			"keep":         requestBrief(keep),
			"drop":         requestBrief(drop),
			"source":       "request",
			"request_text": text,
		}, ""
	case store.ProposalArchive:
		target, ok := candidateAt(candidates, op.Target)
		if !ok {
			return "", "", nil, fmt.Sprintf("archive references memory #%d, which is not in the candidate list", op.Target)
		}
		reason := strings.TrimSpace(op.Reason)
		if reason == "" {
			reason = "requested"
		}
		return store.ProposalArchive, "archive:" + target.ID, map[string]any{
			"id": target.ID, "name": target.Name, "project": target.Project,
			"kind": string(target.Kind), "description": target.Description,
			"reason": reason, "source": "request", "request_text": text,
		}, ""
	case store.ProposalConsolidate:
		name := strings.TrimSpace(op.Name)
		body := strings.TrimSpace(op.Body)
		if name == "" || body == "" {
			return "", "", nil, "consolidate op is missing a name or body"
		}
		var (
			srcObjs []map[string]any
			srcIDs  []string
			project string
		)
		for _, n := range op.Sources {
			m, ok := candidateAt(candidates, n)
			if !ok {
				continue // skip an out-of-range source reference
			}
			if project == "" {
				project = m.Project // the unified memory lives with its first source
			}
			srcObjs = append(srcObjs, map[string]any{"id": m.ID, "name": m.Name})
			srcIDs = append(srcIDs, m.ID)
		}
		if len(srcIDs) == 0 {
			return "", "", nil, "consolidate op references no valid source memories"
		}
		slices.Sort(srcIDs) // canonical key regardless of source order
		return store.ProposalConsolidate, "consolidate:" + strings.Join(srcIDs, "|"), map[string]any{
			"name": name, "kind": normalizeKind(op.Kind), "project": project,
			"description": strings.TrimSpace(op.Description), "body": body,
			"sources": srcObjs, "source": "request", "request_text": text,
		}, ""
	default:
		return "", "", nil, fmt.Sprintf("unknown operation %q", op.Op)
	}
}

// normalizeKind validates a memory kind for a consolidated memory, defaulting to
// reference when the model supplies an unknown or empty kind.
func normalizeKind(kind string) string {
	k := core.MemoryKind(strings.TrimSpace(kind))
	if slices.Contains(core.MemoryKinds, k) {
		return string(k)
	}
	return string(core.KindReference)
}

// candidateAt resolves a 1-based reference to a candidate memory. ok is false for
// an out-of-range index (including the 0 sentinel).
func candidateAt(candidates []core.Memory, n int) (core.Memory, bool) {
	if n < 1 || n > len(candidates) {
		return core.Memory{}, false
	}
	return candidates[n-1], true
}

// requestBrief is the compact memory descriptor embedded in a merge payload,
// matching the shape the apply path and console card read (see memoryBrief).
func requestBrief(m core.Memory) map[string]any {
	return map[string]any{
		"id": m.ID, "name": m.Name, "project": m.Project,
		"description": m.Description, "kind": string(m.Kind),
	}
}

func requestSummary(res RequestResult) string {
	if res.Total == 0 {
		return "no proposals matched"
	}
	var parts []string
	if n := res.ByKind[store.ProposalMerge]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d merge", n))
	}
	if n := res.ByKind[store.ProposalArchive]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d archive", n))
	}
	if n := res.ByKind[store.ProposalConsolidate]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d consolidate", n))
	}
	return "created " + strings.Join(parts, ", ") + " proposal(s)"
}

// truncateRunes bounds a string to n runes (for event payloads), never splitting
// a multi-byte rune.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
