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
