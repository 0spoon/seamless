// Isolation fences for the cross-project passes. The gardener is propose-only,
// but a proposal that names an isolated project's memory alongside another
// project's is already a leak -- the payload carries names and descriptions
// into a review surface, and applying it would move knowledge across the fence.
// So each cross-project pass asks store.CanRead/CanWrite instead of re-deriving
// the matrix, from the scope it actually runs as.
package gardener

import (
	"context"
	"database/sql"

	"github.com/0spoon/seamless/internal/store"
)

// scopeGate answers "may this pass touch that project?" for one pass, caching
// per project. The cache is what makes the O(n^2) dedup scan affordable: the
// verdict depends only on the two projects' states, so re-querying the projects
// table for every candidate pair would be thousands of identical reads.
type scopeGate struct {
	db     *sql.DB
	caller string
	read   map[string]bool
	write  map[string]bool
}

// newScopeGate builds a gate for a pass running as caller ("" is the global
// scope: an unbound whole-machine pass sees everything except what a fenced
// project keeps to itself).
func newScopeGate(db *sql.DB, caller string) *scopeGate {
	return &scopeGate{db: db, caller: caller, read: map[string]bool{}, write: map[string]bool{}}
}

// readable reports whether the pass may include project's knowledge.
func (g *scopeGate) readable(ctx context.Context, project string) (bool, error) {
	if ok, done := g.read[project]; done {
		return ok, nil
	}
	ok, err := store.CanRead(ctx, g.db, g.caller, project)
	if err != nil {
		return false, err
	}
	g.read[project] = ok
	return ok, nil
}

// writable reports whether the pass may propose moving knowledge INTO project.
// It is the reproject/split target test: inbound to a confidential project is
// allowed (relocating knowledge into the fence is how it gets there), inbound to
// a sealed one is not.
func (g *scopeGate) writable(ctx context.Context, project string) (bool, error) {
	if ok, done := g.write[project]; done {
		return ok, nil
	}
	ok, err := store.CanWrite(ctx, g.db, g.caller, project)
	if err != nil {
		return false, err
	}
	g.write[project] = ok
	return ok, nil
}

// pairable reports whether two memories' scopes may be proposed against each
// other. A merge crosses in both directions -- the drop is superseded by the
// keep, and the payload names both -- so a cross-project pair needs each side
// readable from the pass's scope. An isolated memory is therefore only ever
// paired with its own project's.
func (g *scopeGate) pairable(ctx context.Context, a, b string) (bool, error) {
	if a == b {
		return true, nil
	}
	okA, err := g.readable(ctx, a)
	if err != nil || !okA {
		return false, err
	}
	return g.readable(ctx, b)
}
