package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// Archive marks a memory inactive without a replacement: it sets invalid_at = now
// (leaving superseded_by empty), appends an archive tombstone to the body, and
// rewrites the file (which re-indexes it out of the active set). Unlike Supersede
// there is no successor to point at -- the memory is simply retired. old must
// carry its full Body (read the file first; index rows have no body). It returns
// the updated memory.
func Archive(ctx context.Context, w MemoryWriter, old core.Memory, reason string, now time.Time) (core.Memory, error) {
	if old.InvalidAt != nil {
		return core.Memory{}, fmt.Errorf("lifecycle.Archive: %s: %w", old.Name, ErrAlreadyInvalid)
	}
	at := now.UTC()
	old.InvalidAt = &at
	old.SupersededBy = ""
	old.Updated = at
	old.Body = strings.TrimRight(old.Body, "\n") + "\n\n" + archiveTombstone(reason, at) + "\n"
	return w.WriteMemory(ctx, old)
}

// archiveTombstone is the body line appended to an archived memory.
func archiveTombstone(reason string, at time.Time) string {
	label := "Archived"
	if reason != "" {
		label = "Archived (" + reason + ")"
	}
	return fmt.Sprintf("> %s on %s", label, at.Format("2006-01-02"))
}
