package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestReadAfterInjectFunnel_SegmentsBySurface(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// session-start pushes A+B; subagent-start pushes B+C.
	insertEvent(t, db, core.EventInjected, "", `{"hook":"session-start","item_ids":["A","B"]}`, base)
	insertEvent(t, db, core.EventInjected, "", `{"hook":"subagent-start","item_ids":["B","C"]}`, base)
	// B is explicitly read; C comes back as a recall hit. Both within the follow
	// window, so each converts for every surface that pushed it.
	insertEvent(t, db, core.EventMemoryRead, "B", "{}", base.Add(time.Hour))
	insertEvent(t, db, core.EventInjected, "", `{"source":"recall","item_ids":["C"]}`, base.Add(2*time.Hour))
	// A read of an item nothing injected converts nothing.
	insertEvent(t, db, core.EventMemoryRead, "D", "{}", base.Add(time.Hour))
	// A prompt-recall match is an injection, not a pull: A stays unconverted.
	insertEvent(t, db, core.EventInjected, "", `{"hook":"user-prompt-submit","item_ids":["A"]}`, base.Add(time.Hour))

	out, err := ReadAfterInjectFunnel(ctx, db, time.Time{}, 0)
	require.NoError(t, err)
	require.Len(t, out, 2)

	require.Equal(t, "session-start", out[0].Surface)
	require.Equal(t, 2, out[0].Injections)
	require.Equal(t, 2, out[0].Items)
	require.Equal(t, 1, out[0].ItemsRead, "only B converts: A's prompt match is not a pull")
	require.Equal(t, 50, out[0].ReadRate)

	require.Equal(t, "subagent-start", out[1].Surface)
	require.Equal(t, 2, out[1].Injections)
	require.Equal(t, 2, out[1].Items)
	require.Equal(t, 2, out[1].ItemsRead, "B via read, C via recall hit")
	require.Equal(t, 100, out[1].ReadRate)
}

func TestReadAfterInjectFunnel_WindowExcludesEarlierInjections(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// Injection before the window, read inside it: the injection is not seeded,
	// so the read is a pull with no preceding in-window inject -- no conversion.
	insertEvent(t, db, core.EventInjected, "", `{"hook":"session-start","item_ids":["A"]}`, base)
	insertEvent(t, db, core.EventMemoryRead, "A", "{}", base.Add(2*time.Hour))

	out, err := ReadAfterInjectFunnel(ctx, db, base.Add(time.Hour), 0)
	require.NoError(t, err)
	require.Len(t, out, 2)
	for _, f := range out {
		require.Zero(t, f.Injections, f.Surface)
		require.Zero(t, f.Items, f.Surface)
		require.Zero(t, f.ItemsRead, f.Surface)
		require.Zero(t, f.ReadRate, f.Surface)
	}
}

func TestReadAfterInjectFunnel_FollowWindowBoundsConversion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	insertEvent(t, db, core.EventInjected, "", `{"hook":"subagent-start","item_ids":["E"]}`, base)
	insertEvent(t, db, core.EventMemoryRead, "E", "{}", base.Add(30*time.Hour))

	// Default 24h follow: the 30h-later read is not this injection's conversion.
	out, err := ReadAfterInjectFunnel(ctx, db, time.Time{}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, out[1].Items)
	require.Zero(t, out[1].ItemsRead)
	require.Zero(t, out[1].ReadRate)

	// A wider follow window admits it.
	out, err = ReadAfterInjectFunnel(ctx, db, time.Time{}, 48*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 1, out[1].ItemsRead)
	require.Equal(t, 100, out[1].ReadRate)
}

func TestReadAfterInjectFunnel_ReadBeforeInjectDoesNotConvert(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	insertEvent(t, db, core.EventMemoryRead, "F", "{}", base)
	insertEvent(t, db, core.EventInjected, "", `{"hook":"session-start","item_ids":["F"]}`, base.Add(time.Hour))

	out, err := ReadAfterInjectFunnel(ctx, db, time.Time{}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, out[0].Items)
	require.Zero(t, out[0].ItemsRead, "a read preceding the injection is not a conversion")
	require.Zero(t, out[0].ReadRate)
}
