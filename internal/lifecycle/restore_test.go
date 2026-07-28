package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// Archive then Unarchive is a round trip: the memory is active again and its
// body is byte-identical to what it was before the archive.
func TestUnarchive_RoundTripsArchive(t *testing.T) {
	ctx := context.Background()
	w := &recordingWriter{}
	archivedAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	restoredAt := archivedAt.Add(time.Hour)
	body := "body line\n\nmore body\n"
	mem := core.Memory{
		ID: "01ARCHIVE", Kind: core.KindGotcha, Name: "old", Project: "demo",
		Body: body, ValidFrom: archivedAt.Add(-time.Hour),
	}

	archived, err := Archive(ctx, w, mem, "gardener staleness", archivedAt)
	require.NoError(t, err)
	require.False(t, archived.Active())

	restored, err := Unarchive(ctx, w, archived, restoredAt)
	require.NoError(t, err)
	require.True(t, restored.Active())
	require.Nil(t, restored.InvalidAt)
	require.Empty(t, restored.SupersededBy)
	require.Equal(t, restoredAt, restored.Updated)
	require.Equal(t, body, restored.Body, "the archive tombstone is gone and nothing else changed")
	require.NotContains(t, restored.Body, "Archived")
}

// Supersede then Unsupersede is a round trip, and the superseded_by edge is
// cleared along with the tombstone that recorded it.
func TestUnsupersede_RoundTripsSupersede(t *testing.T) {
	ctx := context.Background()
	w := &recordingWriter{}
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	body := "the older take\n"
	old := core.Memory{ID: "01OLD", Name: "old", Project: "demo", Body: body}
	replacement := core.Memory{ID: "01NEW", Name: "new", Project: "demo"}

	superseded, err := Supersede(ctx, w, old, replacement, at)
	require.NoError(t, err)
	require.Equal(t, "01NEW", superseded.SupersededBy)

	restored, err := Unsupersede(ctx, w, superseded, at.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, restored.Active())
	require.Empty(t, restored.SupersededBy, "the superseded_by edge is cleared")
	require.Equal(t, body, restored.Body)
	require.NotContains(t, restored.Body, "Superseded by")
}

// Each inverse refuses the states it does not own, so an undo can never quietly
// turn a supersession into a plain archive (or vice versa).
func TestRestore_RefusesMismatchedStates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)

	active := core.Memory{ID: "01A", Name: "active", Body: "b"}
	archived := core.Memory{ID: "01B", Name: "archived", Body: "b", InvalidAt: &earlier}
	superseded := core.Memory{ID: "01C", Name: "superseded", Body: "b", InvalidAt: &earlier, SupersededBy: "01NEW"}

	t.Run("unarchive refuses an active memory", func(t *testing.T) {
		w := &recordingWriter{}
		_, err := Unarchive(ctx, w, active, now)
		require.ErrorIs(t, err, ErrNotArchived)
		require.Empty(t, w.last.Name)
	})
	t.Run("unarchive refuses a superseded memory", func(t *testing.T) {
		w := &recordingWriter{}
		_, err := Unarchive(ctx, w, superseded, now)
		require.ErrorIs(t, err, ErrNotArchived)
		require.Empty(t, w.last.Name, "the superseded_by edge must not be dropped by an unarchive")
	})
	t.Run("unsupersede refuses an active memory", func(t *testing.T) {
		w := &recordingWriter{}
		_, err := Unsupersede(ctx, w, active, now)
		require.ErrorIs(t, err, ErrNotSuperseded)
		require.Empty(t, w.last.Name)
	})
	t.Run("unsupersede refuses a plainly archived memory", func(t *testing.T) {
		w := &recordingWriter{}
		_, err := Unsupersede(ctx, w, archived, now)
		require.ErrorIs(t, err, ErrNotSuperseded)
		require.Empty(t, w.last.Name)
	})
}

func TestStripTombstone(t *testing.T) {
	for name, tc := range map[string]struct{ body, prefix, want string }{
		"trailing archive line": {
			body: "text\n\n> Archived on 2026-07-10\n", prefix: "> Archived", want: "text\n",
		},
		"trailing supersede line": {
			body:   "text\n\n> Superseded by demo/new (01NEW) on 2026-07-10\n",
			prefix: "> Superseded by", want: "text\n",
		},
		"body quoting a tombstone earlier stays intact": {
			body:   "> Archived on 2020-01-01 is what it used to say\n\nreal content\n",
			prefix: "> Archived", want: "> Archived on 2020-01-01 is what it used to say\n\nreal content\n",
		},
		"older tombstone from a prior retirement is kept": {
			body:   "text\n\n> Archived on 2025-01-01\n\n> Archived on 2026-07-10\n",
			prefix: "> Archived", want: "text\n\n> Archived on 2025-01-01\n",
		},
		"wrong prefix leaves the body alone": {
			body: "text\n\n> Archived on 2026-07-10\n", prefix: "> Superseded by",
			want: "text\n\n> Archived on 2026-07-10\n",
		},
		"tombstone-only body empties": {
			body: "> Archived on 2026-07-10\n", prefix: "> Archived", want: "",
		},
		"empty body": {body: "", prefix: "> Archived", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, stripTombstone(tc.body, tc.prefix))
		})
	}
}
