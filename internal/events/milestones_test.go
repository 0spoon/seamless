package events

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func TestRecord_ArmedRecorderMintsMilestoneOnce(t *testing.T) {
	r := newRecorder(t)
	ctx := context.Background()
	r.SetFeatures(config.Features{Momentum: true})

	for i := range 100 {
		_, err := r.Record(ctx, core.Event{
			Kind: core.EventMemoryWritten, SessionID: "sess-1",
			ProjectSlug: "demo", ItemID: "mem-" + strconv.Itoa(i),
		})
		require.NoError(t, err)
	}
	got, err := r.ByKinds(ctx, []core.EventKind{store.EventMilestoneReached}, "", "", 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "the 100th write mints the milestone exactly once")
	m := got[0]
	require.Equal(t, "memories-100:demo", m.ItemID)
	require.Equal(t, "demo", m.ProjectSlug)
	require.Equal(t, "sess-1", m.SessionID)
	require.Equal(t, "100 memories written in demo", m.Payload["claim"])
	require.EqualValues(t, 100, m.Payload["count"])

	// The 101st write re-checks but the latch holds.
	_, err = r.Record(ctx, core.Event{Kind: core.EventMemoryWritten, ProjectSlug: "demo", ItemID: "mem-100"})
	require.NoError(t, err)
	got, err = r.ByKinds(ctx, []core.EventKind{store.EventMilestoneReached}, "", "", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestRecord_UnarmedRecorderMintsNothing(t *testing.T) {
	r := newRecorder(t) // SetFeatures never called: behavior as before
	ctx := context.Background()

	for i := range 100 {
		_, err := r.Record(ctx, core.Event{
			Kind: core.EventMemoryWritten, ProjectSlug: "demo", ItemID: "mem-" + strconv.Itoa(i),
		})
		require.NoError(t, err)
	}
	got, err := r.ByKinds(ctx, []core.EventKind{store.EventMilestoneReached}, "", "", 10)
	require.NoError(t, err)
	require.Empty(t, got)
}
