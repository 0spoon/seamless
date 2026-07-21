package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// Proposal statuses.
const (
	ProposalPending   = "pending"
	ProposalApplied   = "applied"
	ProposalDismissed = "dismissed"
)

// Proposal kinds (mirrors the gardener_proposals.kind CHECK constraint).
const (
	ProposalMerge        = "merge"
	ProposalArchive      = "archive"
	ProposalDigest       = "digest"
	ProposalConsolidate  = "consolidate"
	ProposalReproject    = "reproject"     // move one memory to another project
	ProposalSplit        = "split"         // set up child/shared projects + family for a project split
	ProposalAbandonPlan  = "abandon_plan"  // retag a never-approved captured plan plan-status:abandoned
	ProposalMemoryWanted = "memory_wanted" // agents repeatedly searched for knowledge that does not exist
	ProposalToolError    = "tool_error"    // agents keep hitting the same tool-call or hook-stage error
)

// ProposalKinds lists every valid proposal kind. This is the canonical set:
// derive the MCP schema's enum from it rather than transcribing, so a new kind
// cannot reach the store while staying invisible at the boundary.
var ProposalKinds = []string{
	ProposalMerge, ProposalArchive, ProposalDigest, ProposalConsolidate,
	ProposalReproject, ProposalSplit, ProposalAbandonPlan, ProposalMemoryWanted,
	ProposalToolError,
}

// Proposal is one gardener suggestion awaiting owner review. Payload carries the
// kind-specific detail; every payload includes a stable "key" string the
// gardener uses to avoid re-proposing the same thing on a later pass.
type Proposal struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Payload    map[string]any `json:"payload"`
	Status     string         `json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
	ResolvedAt *time.Time     `json:"resolvedAt,omitempty"`
}

// CreateProposal inserts a pending gardener proposal and returns it. The caller
// supplies the kind and payload; the id and created_at are stamped here.
func CreateProposal(ctx context.Context, db *sql.DB, kind string, payload map[string]any) (Proposal, error) {
	id, err := core.NewID()
	if err != nil {
		return Proposal{}, fmt.Errorf("store.CreateProposal: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Proposal{}, fmt.Errorf("store.CreateProposal: marshal payload: %w", err)
	}
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO gardener_proposals (id, kind, payload, status, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		id, kind, string(raw), ProposalPending, core.FormatTime(now))
	if err != nil {
		return Proposal{}, fmt.Errorf("store.CreateProposal: %w", err)
	}
	return Proposal{ID: id, Kind: kind, Payload: payload, Status: ProposalPending, CreatedAt: now}, nil
}

// PendingProposals returns pending proposals, newest first. kind filters by
// proposal kind when non-empty.
func PendingProposals(ctx context.Context, db *sql.DB, kind string) ([]Proposal, error) {
	q := `SELECT id, kind, payload, status, created_at, resolved_at
		FROM gardener_proposals WHERE status = ?`
	args := []any{ProposalPending}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store.PendingProposals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanProposals(rows)
}

// ProposalByID returns one proposal. found is false when absent.
func ProposalByID(ctx context.Context, db *sql.DB, id string) (Proposal, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, kind, payload, status, created_at, resolved_at
		FROM gardener_proposals WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return Proposal{}, false, fmt.Errorf("store.ProposalByID: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ps, err := scanProposals(rows)
	if err != nil {
		return Proposal{}, false, err
	}
	if len(ps) == 0 {
		return Proposal{}, false, nil
	}
	return ps[0], true, nil
}

// ResolveProposal marks a proposal applied or dismissed and stamps resolved_at.
// It errors if the proposal is missing or already resolved (not pending).
func ResolveProposal(ctx context.Context, db *sql.DB, id, status string, at time.Time) error {
	if status != ProposalApplied && status != ProposalDismissed {
		return fmt.Errorf("store.ResolveProposal: invalid status %q", status)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE gardener_proposals SET status = ?, resolved_at = ?
		WHERE id = ? AND status = ?`,
		status, core.FormatTime(at.UTC()), id, ProposalPending)
	if err != nil {
		return fmt.Errorf("store.ResolveProposal: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store.ResolveProposal: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store.ResolveProposal: no pending proposal with id %q", id)
	}
	return nil
}

// UpdateProposalPayload replaces a pending proposal's payload (e.g. retargeting a
// reproject to a different project before applying). It errors if the proposal is
// missing or not pending, so a resolved proposal is never silently rewritten.
func UpdateProposalPayload(ctx context.Context, db *sql.DB, id string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("store.UpdateProposalPayload: marshal: %w", err)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE gardener_proposals SET payload = ?
		WHERE id = ? AND status = ?`, string(raw), id, ProposalPending)
	if err != nil {
		return fmt.Errorf("store.UpdateProposalPayload: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store.UpdateProposalPayload: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store.UpdateProposalPayload: no pending proposal with id %q", id)
	}
	return nil
}

// AllProposalKeys returns the set of payload "key" values across proposals of
// EVERY status. The gardener consults it before proposing, so a suggestion the
// owner already applied or dismissed is never raised again.
func AllProposalKeys(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT payload FROM gardener_proposals`)
	if err != nil {
		return nil, fmt.Errorf("store.AllProposalKeys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	keys := make(map[string]struct{})
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("store.AllProposalKeys: scan: %w", err)
		}
		var p struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err == nil && p.Key != "" {
			keys[p.Key] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.AllProposalKeys: rows: %w", err)
	}
	return keys, nil
}

func scanProposals(rows *sql.Rows) ([]Proposal, error) {
	var out []Proposal
	for rows.Next() {
		var (
			p            Proposal
			raw, created string
			resolved     sql.NullString
		)
		if err := rows.Scan(&p.ID, &p.Kind, &raw, &p.Status, &created, &resolved); err != nil {
			return nil, fmt.Errorf("store: scan proposal: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &p.Payload); err != nil {
			return nil, fmt.Errorf("store: proposal payload: %w", err)
		}
		var err error
		if p.CreatedAt, err = core.ParseTime(created); err != nil {
			return nil, fmt.Errorf("store: proposal created_at: %w", err)
		}
		if p.ResolvedAt, err = nullTimePtr(resolved); err != nil {
			return nil, fmt.Errorf("store: proposal resolved_at: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.scanProposals: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Gardener support queries (dedup / staleness / digest inputs)
// ---------------------------------------------------------------------------

// MemoryVector pairs an active memory's index metadata with its stored embedding
// vector, for the gardener's pairwise dedup scan.
type MemoryVector struct {
	ID          string
	Project     string
	Name        string
	Description string
	Kind        string
	UpdatedAt   time.Time
	Vec         []float32
}

// ActiveMemoryVectors returns the vectors of all active memories embedded under
// model, oldest-created first (stable ordering for deterministic pairing). It
// joins the embeddings table, so a memory with no vector for that model is
// omitted (the gardener simply cannot dedup it semantically).
func ActiveMemoryVectors(ctx context.Context, db *sql.DB, model string) ([]MemoryVector, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, m.project, m.name, m.description, m.kind, m.updated_at, e.vec
		FROM memories_index m
		JOIN embeddings e ON e.item_id = m.id
		WHERE m.invalid_at IS NULL AND e.kind = 'memory' AND e.model = ?
		ORDER BY m.created_at ASC, m.id ASC`, model)
	if err != nil {
		return nil, fmt.Errorf("store.ActiveMemoryVectors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MemoryVector
	for rows.Next() {
		var (
			mv      MemoryVector
			updated string
			blob    []byte
		)
		if err := rows.Scan(&mv.ID, &mv.Project, &mv.Name, &mv.Description, &mv.Kind, &updated, &blob); err != nil {
			return nil, fmt.Errorf("store.ActiveMemoryVectors: scan: %w", err)
		}
		var err error
		if mv.UpdatedAt, err = core.ParseTime(updated); err != nil {
			return nil, fmt.Errorf("store.ActiveMemoryVectors: updated_at: %w", err)
		}
		mv.Vec = DecodeVector(blob)
		out = append(out, mv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ActiveMemoryVectors: rows: %w", err)
	}
	return out, nil
}

// AllActiveMemories returns every active memory across all projects (index rows,
// no body), newest-updated first. It backs the gardener's reference scan, which
// reads each file's body to find [[name]] links.
func AllActiveMemories(ctx context.Context, db *sql.DB) ([]core.Memory, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+memoryCols+`
		FROM memories_index WHERE invalid_at IS NULL
		ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store.AllActiveMemories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mems, err := scanMemories(rows)
	if err != nil {
		return nil, fmt.Errorf("store.AllActiveMemories: %w", err)
	}
	return mems, nil
}

// CompletedSessionsSince returns completed sessions with non-empty findings
// updated on or after since, across all projects, newest first. It feeds the
// gardener's monthly digest pass.
func CompletedSessionsSince(ctx context.Context, db *sql.DB, since time.Time) ([]core.Session, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+sessionCols+`
		FROM sessions
		WHERE status = 'completed' AND findings <> '' AND updated_at >= ?
		ORDER BY updated_at DESC, id DESC`, core.FormatTime(since.UTC()))
	if err != nil {
		return nil, fmt.Errorf("store.CompletedSessionsSince: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []core.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store.CompletedSessionsSince: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
