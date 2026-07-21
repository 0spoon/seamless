package files

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// A starred memory round-trips through render/parse losslessly, and an
// unstarred one never emits the key at all (existing files must not churn).
func TestMemoryFavoriteRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	mem := core.Memory{
		ID: "01FAV", Kind: core.KindGotcha, Name: "starred-gotcha",
		Description: "d", Project: "seam", Body: "body",
		Created: now, Updated: now, ValidFrom: now, Favorite: true,
	}
	content, err := RenderMemory(mem)
	require.NoError(t, err)
	require.Contains(t, content, "favorite: true")

	parsed, err := ParseMemory(content, "memory/seam/starred-gotcha.md")
	require.NoError(t, err)
	require.True(t, parsed.Favorite)

	mem.Favorite = false
	content, err = RenderMemory(mem)
	require.NoError(t, err)
	require.NotContains(t, content, "favorite")
	parsed, err = ParseMemory(content, "memory/seam/starred-gotcha.md")
	require.NoError(t, err)
	require.False(t, parsed.Favorite)
}

// A hand-written `favorite: false` parses as unstarred and normalizes to an
// absent key on the next render.
func TestMemoryFavoriteFalseNormalizesToAbsent(t *testing.T) {
	content := strings.Join([]string{
		"---",
		"id: 01FAV2",
		"kind: gotcha",
		"name: unstarred",
		"description: d",
		"created: 2026-07-21T10:00:00Z",
		"updated: 2026-07-21T10:00:00Z",
		"valid_from: 2026-07-21T10:00:00Z",
		"favorite: false",
		"---",
		"body",
	}, "\n") + "\n"
	parsed, err := ParseMemory(content, "memory/_global/unstarred.md")
	require.NoError(t, err)
	require.False(t, parsed.Favorite)

	rendered, err := RenderMemory(parsed)
	require.NoError(t, err)
	require.NotContains(t, rendered, "favorite")
}

func TestNoteFavoriteRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	note := core.Note{
		ID: "01NFAV", Title: "Starred note", Slug: "starred-note",
		Project: "seam", Body: "body", Created: now, Updated: now, Favorite: true,
	}
	content, err := RenderNote(note)
	require.NoError(t, err)
	require.Contains(t, content, "favorite: true")

	parsed, err := ParseNote(content, "notes/seam/starred-note.md")
	require.NoError(t, err)
	require.True(t, parsed.Favorite)

	note.Favorite = false
	content, err = RenderNote(note)
	require.NoError(t, err)
	require.NotContains(t, content, "favorite")
}
