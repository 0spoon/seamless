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

func mustRecord(t *testing.T, r *Recorder, e core.Event) {
	t.Helper()
	_, err := r.Record(context.Background(), e)
	require.NoError(t, err)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"disabled-zero", "hello", 0, "hello"},
		{"disabled-negative", "hello", -3, "hello"},
		{"under", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"over", "hello world", 5, "hello" + truncationMarker},
		{"multibyte-not-split", "héllo wörld", 5, "héllo" + truncationMarker},
		{"multibyte-exact-runes", "héllo", 5, "héllo"}, // 6 bytes, 5 runes -> unchanged
		{"empty", "", 4, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Truncate(tt.in, tt.max))
		})
	}
}

// interactionKinds used by the query tests below.
var testInteractionKinds = []core.EventKind{core.EventToolCall, core.EventHookPrompt}

// seedTie records four events: two tool.call sharing base's ts (ids 01A<01B), a
// later hook.prompt (01C), and a memory.written (01D) that the interaction
// queries must exclude.
func seedTie(t *testing.T, r *Recorder, base time.Time) {
	t.Helper()
	mustRecord(t, r, core.Event{ID: "01A", TS: base, Kind: core.EventToolCall, ItemID: "t1"})
	mustRecord(t, r, core.Event{ID: "01B", TS: base, Kind: core.EventToolCall, ItemID: "t2"})
	mustRecord(t, r, core.Event{ID: "01C", TS: base.Add(time.Minute), Kind: core.EventHookPrompt, ItemID: "h1"})
	mustRecord(t, r, core.Event{ID: "01D", TS: base.Add(2 * time.Minute), Kind: core.EventMemoryWritten, ItemID: "m1"})
}

func TestByKinds_FilterAndCursorAcrossTie(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	seedTie(t, r, base)

	// Newest first, limit 2: hook.prompt (01C) then the higher-id tie sibling (01B).
	// memory.written (01D) is filtered out entirely.
	page1, err := r.ByKinds(ctx, testInteractionKinds, "", "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.Equal(t, "01C", page1[0].ID)
	require.Equal(t, "01B", page1[1].ID)

	// Page from the last cursor: yields only 01A (the other tie sibling), never
	// re-emitting 01B nor skipping 01A.
	last := page1[len(page1)-1]
	page2, err := r.ByKinds(ctx, testInteractionKinds, core.FormatTime(last.TS), last.ID, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Equal(t, "01A", page2[0].ID)
}

func TestByKinds_EmptyKindsAndDefaultLimit(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	got, err := r.ByKinds(ctx, nil, "", "", 0)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestByKindsSince_StrictlyNewerOldestFirstAcrossTie(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	seedTie(t, r, base)

	// Cursor at the earliest tie sibling (01A, ts=base): strictly-newer means the
	// same-ts higher-id sibling (01B) plus the later hook.prompt (01C), oldest
	// first.
	got, err := r.ByKindsSince(ctx, testInteractionKinds, core.FormatTime(base), "01A", 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "01B", got[0].ID)
	require.Equal(t, "01C", got[1].ID)
}

func TestRecentExcluding_OmitsGivenKinds(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	mustRecord(t, r, core.Event{ID: "01A", TS: base, Kind: core.EventToolCall})
	mustRecord(t, r, core.Event{ID: "01B", TS: base.Add(time.Minute), Kind: core.EventMemoryWritten})
	mustRecord(t, r, core.Event{ID: "01C", TS: base.Add(2 * time.Minute), Kind: core.EventHookPrompt})
	mustRecord(t, r, core.Event{ID: "01D", TS: base.Add(3 * time.Minute), Kind: core.EventSessionStarted})

	got, err := r.RecentExcluding(ctx, 10, core.EventToolCall, core.EventHookPrompt)
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Newest first, transport kinds omitted.
	require.Equal(t, core.EventSessionStarted, got[0].Kind)
	require.Equal(t, core.EventMemoryWritten, got[1].Kind)

	// No exclusions behaves like Recent.
	all, err := r.RecentExcluding(ctx, 10)
	require.NoError(t, err)
	require.Len(t, all, 4)
}

func TestPruneKinds_OnlyGivenKindsBeforeCutoff(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	mustRecord(t, r, core.Event{ID: "01A", TS: base, Kind: core.EventToolCall})                    // old transport -> pruned
	mustRecord(t, r, core.Event{ID: "01B", TS: base, Kind: core.EventHookPrompt})                  // old transport -> pruned
	mustRecord(t, r, core.Event{ID: "01C", TS: base, Kind: core.EventMemoryWritten})               // old domain -> kept
	mustRecord(t, r, core.Event{ID: "01D", TS: base.Add(2 * time.Hour), Kind: core.EventToolCall}) // fresh transport -> kept

	cutoff := base.Add(time.Hour)
	n, err := r.PruneKinds(ctx, testInteractionKinds, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	got, err := r.Recent(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	kinds := map[core.EventKind]bool{}
	for _, e := range got {
		kinds[e.Kind] = true
	}
	require.True(t, kinds[core.EventMemoryWritten], "domain event must survive")
	require.True(t, kinds[core.EventToolCall], "fresh transport event must survive")

	// Empty kinds / zero cutoff are no-ops.
	n0, err := r.PruneKinds(ctx, nil, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(0), n0)
	n1, err := r.PruneKinds(ctx, testInteractionKinds, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(0), n1)
}

func TestSubscribe_ReceivesPublishedEvent(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	id, err := r.Record(ctx, core.Event{Kind: core.EventMemoryWritten, ItemID: "m1"})
	require.NoError(t, err)

	select {
	case e := <-ch:
		require.Equal(t, id, e.ID)
		require.Equal(t, core.EventMemoryWritten, e.Kind)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive published event")
	}
}

func TestUnsubscribe_StopsDeliveryAndIsIdempotent(t *testing.T) {
	r := newRecorder(t)
	ch, unsubscribe := r.Subscribe()
	unsubscribe()
	unsubscribe() // idempotent -- must not panic

	// Channel is closed; a receive returns the zero value with ok=false.
	_, open := <-ch
	require.False(t, open)

	// Recording after unsubscribe must not block or panic.
	_, err := r.Record(context.Background(), core.Event{Kind: core.EventMemoryRead, ItemID: "m2"})
	require.NoError(t, err)
}

func TestPublish_DropsWhenSubscriberFull(t *testing.T) {
	r := newRecorder(t)
	_, unsubscribe := r.Subscribe() // never drained
	defer unsubscribe()

	// Far more than subBuffer events; publish must never block on the full channel.
	for range subBuffer * 3 {
		_, err := r.Record(context.Background(), core.Event{Kind: core.EventMemoryRead, ItemID: "x"})
		require.NoError(t, err)
	}
}
