package retrieve

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// fakeBodyReader maps a memory file path to its body, standing in for files.Store
// in briefing tests.
type fakeBodyReader map[string]string

func (f fakeBodyReader) ReadMemory(relPath string) (core.Memory, error) {
	body, ok := f[relPath]
	if !ok {
		return core.Memory{}, fmt.Errorf("fakeBodyReader: no body for %q", relPath)
	}
	return core.Memory{Body: body}, nil
}

func TestBriefingPinsStages(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/seam":"seam"}`))
	insMem(t, db, "01A", "constraint", "no-force-push", "never force push", "seam")
	insMem(t, db, "01S1", "stage", "f5-ssh-signing", "ssh signing stage", "seam")
	insMem(t, db, "01S2", "stage", "f6-old", "finished stage", "seam")

	reader := fakeBodyReader{
		"memory/x/f5-ssh-signing.md": "Status: in_progress\nGate: human\n\ndetails",
		"memory/x/f6-old.md":         "Status: done\nGate: ai",
	}
	svc := New(db, nil, budgets(), nil)
	svc.SetBodyReader(reader)

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "STAGE: f5-ssh-signing -- in_progress, gate human")
	require.NotContains(t, b, "f6-old", "done stages are not pinned")
	// The pinned stage's id is injected; the done (unpinned) stage's id is not.
	require.Subset(t, ids, []string{"01A", "01S1"})
	require.NotContains(t, ids, "01S2")

	// Without a body reader, the stage section is omitted (degrades cleanly).
	plain := New(db, nil, budgets(), nil)
	b2, ids2, err := plain.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.NotContains(t, b2, "STAGE:")
	require.Contains(t, ids2, "01A")     // constraint still injected
	require.NotContains(t, ids2, "01S1") // no stage section => no stage ids
}

// A stage whose Status header does not parse to a live gate holds its pin only
// through the StageUnknownMaxAgeDays grace window; a live gate pins at any age.
func TestBriefingAgesOutGatelessStages(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/seam":"seam"}`))
	now := time.Now()
	insMemAt(t, db, "01S1", "stage", "old-no-header", "milestone breadcrumb", "seam", now.AddDate(0, 0, -30))
	insMemAt(t, db, "01S2", "stage", "fresh-no-header", "just written", "seam", now.AddDate(0, 0, -2))
	insMemAt(t, db, "01S3", "stage", "old-live-gate", "genuinely gated", "seam", now.AddDate(0, 0, -30))
	insMemAt(t, db, "01S4", "stage", "old-odd-status", "heading token status", "seam", now.AddDate(0, 0, -30))

	reader := fakeBodyReader{
		"memory/x/old-no-header.md":   "landed 2026-07-01, all green",
		"memory/x/fresh-no-header.md": "landed yesterday, all green",
		"memory/x/old-live-gate.md":   "Status: blocked\nGate: human\n\nwaiting on hardware",
		"memory/x/old-odd-status.md":  "## Status: P6 cutover COMPLETE\n\nnarrative",
	}
	svc := New(db, nil, budgets(), nil)
	svc.SetBodyReader(reader)
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.StageUnknownMaxAgeDays = 7 }))

	b, ids, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.NotContains(t, b, "old-no-header", "gateless stage past the window is not pinned")
	require.Contains(t, b, "STAGE: fresh-no-header -- status unknown (no Status: header)", "grace window keeps a fresh gateless stage visible, with the missing header named")
	require.Contains(t, b, "STAGE: old-live-gate -- blocked, gate human", "a live gate pins at any age")
	require.NotContains(t, b, "old-odd-status", "an unrecognized status token is not a live gate")
	require.NotContains(t, ids, "01S1")
	require.Subset(t, ids, []string{"01S2", "01S3"})

	// 0 disables the age-out: every non-done stage pins, the historical behavior.
	svc.SetBriefingConfig(briefingWith(func(b *config.Briefing) { b.StageUnknownMaxAgeDays = 0 }))
	b0, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b0, "STAGE: old-no-header -- status unknown (no Status: header)")
	require.Contains(t, b0, "STAGE: old-odd-status -- p6")
}
