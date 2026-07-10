package events

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func newRecorder(t *testing.T) *Recorder {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewRecorder(db)
}

func TestRecord_StampsIDAndTimeAndPayload(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()

	id, err := r.Record(ctx, core.Event{
		Kind:        core.EventMemoryWritten,
		ProjectSlug: "seam",
		ItemID:      "gotcha/chroma-boot-race",
		Payload:     map[string]any{"name": "chroma-boot-race", "score": 0.93},
	})
	require.NoError(t, err)
	require.Len(t, id, 26, "id should be a stamped ULID")

	got, err := r.Recent(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)

	e := got[0]
	require.Equal(t, id, e.ID)
	require.Equal(t, core.EventMemoryWritten, e.Kind)
	require.Equal(t, "seam", e.ProjectSlug)
	require.Equal(t, "gotcha/chroma-boot-race", e.ItemID)
	require.False(t, e.TS.IsZero())
	require.Equal(t, "chroma-boot-race", e.Payload["name"])
	require.InEpsilon(t, 0.93, e.Payload["score"], 1e-9)
}

func TestRecord_PreservesExplicitIDAndTS(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()

	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	_, err := r.Record(ctx, core.Event{ID: "01EXPLICITID0000000000000A", TS: ts, Kind: core.EventSessionStarted})
	require.NoError(t, err)

	got, err := r.Recent(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "01EXPLICITID0000000000000A", got[0].ID)
	require.True(t, ts.Equal(got[0].TS))
}

func TestRecord_EmptyKindRejected(t *testing.T) {
	r := newRecorder(t)
	_, err := r.Record(context.Background(), core.Event{ProjectSlug: "seam"})
	require.Error(t, err)
}

func TestRecent_NewestFirstAndLimit(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()

	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		_, err := r.Record(ctx, core.Event{
			TS:   base.Add(time.Duration(i) * time.Minute),
			Kind: core.EventInjected,
			// distinct item so we can assert ordering
			ItemID: string(rune('a' + i)),
		})
		require.NoError(t, err)
	}

	got, err := r.Recent(ctx, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Newest first: minute 4, 3, 2.
	require.Equal(t, "e", got[0].ItemID)
	require.Equal(t, "d", got[1].ItemID)
	require.Equal(t, "c", got[2].ItemID)
}
