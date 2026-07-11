// Package lifecycle carries a memory's bi-temporal lifecycle: supersession (one
// memory replacing another) and provenance. A superseded memory is not deleted;
// it is stamped invalid_at + superseded_by and keeps a tombstone line in its
// file body, so the on-disk source of truth stays honest while the memory leaves
// every active index (briefing, prompt corpus, recall).
package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/files"
)

// MemoryWriter is the subset of files.Manager that Supersede needs, so the flow
// is testable against a real files.Manager without importing the whole world.
type MemoryWriter interface {
	WriteMemory(ctx context.Context, mem core.Memory) (core.Memory, error)
}

// Supersede marks old as replaced by replacement: it sets old.InvalidAt = now and
// old.SupersededBy = replacement.ID, appends a tombstone line to old's body, and
// rewrites old's file (which re-indexes it out of the active set). old must carry
// its full Body (read the file first, since index rows have no body). It returns
// the updated old memory.
//
// old and replacement must be distinct memories with distinct file paths;
// same-name replacement is handled as an in-place update by memory_write and must
// not reach here (guarded by the caller).
func Supersede(ctx context.Context, w MemoryWriter, old, replacement core.Memory, now time.Time) (core.Memory, error) {
	at := now.UTC()
	old.InvalidAt = &at
	old.SupersededBy = replacement.ID
	old.Updated = at
	old.Body = strings.TrimRight(old.Body, "\n") + "\n\n" + tombstone(replacement, at) + "\n"
	return w.WriteMemory(ctx, old)
}

// tombstone is the body line appended to a superseded memory, keeping the file
// truthful about what replaced it.
func tombstone(replacement core.Memory, at time.Time) string {
	return fmt.Sprintf("> Superseded by %s (%s) on %s",
		MemoryRef(replacement.Project, replacement.Name), replacement.ID, at.Format("2006-01-02"))
}

// MemoryRef renders a project-qualified memory reference (project/name), or just
// name for a global memory. Used in tombstones and read warnings.
func MemoryRef(project, name string) string {
	if project == "" {
		return name
	}
	return project + "/" + name
}

// compile-time assurance that *files.Manager satisfies MemoryWriter.
var _ MemoryWriter = (*files.Manager)(nil)
