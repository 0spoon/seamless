package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// recordingWriter captures the memory handed to WriteMemory.
type recordingWriter struct{ last core.Memory }

func (w *recordingWriter) WriteMemory(_ context.Context, m core.Memory) (core.Memory, error) {
	w.last = m
	return m, nil
}

func TestArchive(t *testing.T) {
	w := &recordingWriter{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mem := core.Memory{
		ID: "01ARCHIVE", Kind: core.KindGotcha, Name: "old", Project: "demo",
		Body: "body line", ValidFrom: now.Add(-time.Hour),
	}

	got, err := Archive(context.Background(), w, mem, "gardener staleness", now)
	require.NoError(t, err)
	require.NotNil(t, got.InvalidAt)
	require.Equal(t, now, *got.InvalidAt)
	require.Empty(t, got.SupersededBy, "an archive has no successor")
	require.Equal(t, now, got.Updated)
	require.Contains(t, got.Body, "Archived (gardener staleness) on 2026-07-10")
	require.True(t, strings.HasPrefix(strings.Split(got.Body, "\n")[0], "body line"))
	require.False(t, got.Active())
}

func TestArchive_NoReason(t *testing.T) {
	w := &recordingWriter{}
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	got, err := Archive(context.Background(), w, core.Memory{Name: "x", Body: "b"}, "", now)
	require.NoError(t, err)
	require.Contains(t, got.Body, "Archived on 2026-07-10")
	require.NotContains(t, got.Body, "()")
}

func TestArchive_AlreadyInvalidRejected(t *testing.T) {
	w := &recordingWriter{}
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	_, err := Archive(context.Background(), w, core.Memory{Name: "x", Body: "b", InvalidAt: &earlier}, "", now)
	require.ErrorIs(t, err, ErrAlreadyInvalid)
	require.Empty(t, w.last.Name, "an already-invalid memory must not be rewritten")
}

func TestSupersede_InvariantGuards(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	active := core.Memory{ID: "01OLD", Name: "old", Body: "b"}
	replacement := core.Memory{ID: "01NEW", Name: "new"}

	t.Run("self-supersession rejected", func(t *testing.T) {
		w := &recordingWriter{}
		_, err := Supersede(ctx, w, active, active, now)
		require.ErrorIs(t, err, ErrSelfSupersede)
		require.Empty(t, w.last.Name)
	})

	t.Run("already-invalid old rejected", func(t *testing.T) {
		w := &recordingWriter{}
		invalid := active
		invalid.InvalidAt = &earlier
		invalid.SupersededBy = "01ELSEWHERE"
		_, err := Supersede(ctx, w, invalid, replacement, now)
		require.ErrorIs(t, err, ErrAlreadyInvalid)
		require.Empty(t, w.last.Name, "supersession history must not be rewritten")
	})

	t.Run("invalid replacement rejected", func(t *testing.T) {
		w := &recordingWriter{}
		stale := replacement
		stale.InvalidAt = &earlier
		_, err := Supersede(ctx, w, active, stale, now)
		require.ErrorIs(t, err, ErrInvalidReplacement)
	})

	t.Run("replacement without id rejected", func(t *testing.T) {
		w := &recordingWriter{}
		_, err := Supersede(ctx, w, active, core.Memory{Name: "new"}, now)
		require.ErrorIs(t, err, ErrInvalidReplacement)
	})
}
