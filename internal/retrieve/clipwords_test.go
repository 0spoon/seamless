package retrieve

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

func TestClipWords(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short unchanged", "fits fine", 20, "fits fine"},
		{"exact fit unchanged", "abcde fghij", 11, "abcde fghij"},
		{"cuts at word boundary", "alpha bravo charlie", 14, "alpha bravo" + core.Ellipsis},
		{"drops dangling separator", "alpha bravo, charlie", 14, "alpha bravo" + core.Ellipsis},
		{"single long token hard-cuts", "abcdefghijklmnop", 8, "abcdefg" + core.Ellipsis},
		{"cap disabled at 1", "alpha bravo", 1, "alpha bravo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clipWords(tt.in, tt.max)
			require.Equal(t, tt.want, got)
			if tt.max > 1 {
				require.LessOrEqual(t, utf8.RuneCountInString(got), tt.max)
			}
		})
	}
}

// clippedSegment extracts the text after "): " on the briefing line starting
// with prefix, failing the test when the line is missing.
func clippedSegment(t *testing.T, briefing, prefix string) string {
	t.Helper()
	for line := range strings.SplitSeq(briefing, "\n") {
		if strings.HasPrefix(line, prefix) {
			_, seg, ok := strings.Cut(line, "): ")
			require.True(t, ok, "line %q has no finding segment", line)
			return seg
		}
	}
	t.Fatalf("no briefing line starts with %q:\n%s", prefix, briefing)
	return ""
}

// A recent-findings line built from an over-long finding clips at a word
// boundary inside its 200-rune budget: the text before the ellipsis is a whole
// source word, never a fragment.
func TestBriefingFindingsClippedAtWordBoundary(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/w":"p"}`))
	insMem(t, db, "01A", "gotcha", "some-memory", "still relevant", "p")

	long := strings.TrimSpace(strings.Repeat("abcdefgh ", 40)) // 359 runes of one known word
	ts := time.Now().Add(-time.Minute)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "01S1", Name: "cc/aa", ProjectSlug: "p", Status: core.SessionCompleted,
		Findings: long, CreatedAt: ts, UpdatedAt: ts,
	}))

	svc := New(db, nil, budgets(), nil)
	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/w", Source: "startup"})
	require.NoError(t, err)

	seg := clippedSegment(t, b, "- cc/aa (")
	require.LessOrEqual(t, utf8.RuneCountInString(seg), 200)
	require.True(t, strings.HasSuffix(seg, core.Ellipsis), "clipped finding must end with an ellipsis: %q", seg)
	words := strings.Fields(strings.TrimSuffix(seg, core.Ellipsis))
	require.NotEmpty(t, words)
	require.Equal(t, "abcdefgh", words[len(words)-1], "the cut must land on a word boundary, not mid-word")
}

// The sibling-findings line gets the same word-boundary clip at its tighter
// 150-rune budget.
func TestBriefingSiblingFindingsClippedAtWordBoundary(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/app":"app"}`))
	require.NoError(t, store.SetSetting(ctx, db, store.SettingProjectFamilies, `{"product":["app","backend"]}`))
	insMem(t, db, "01A", "constraint", "no-force-push", "never force push", "app")

	long := strings.TrimSpace(strings.Repeat("abcdefgh ", 40))
	ts := time.Now().Add(-5 * time.Minute)
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: "01S1", Name: "cc/bb", ProjectSlug: "backend", Status: core.SessionCompleted,
		Findings: long, CreatedAt: ts, UpdatedAt: ts,
	}))

	svc := New(db, nil, budgets(), nil)
	b, _, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/app", Source: "startup"})
	require.NoError(t, err)

	seg := clippedSegment(t, b, "- backend (")
	require.LessOrEqual(t, utf8.RuneCountInString(seg), 150)
	require.True(t, strings.HasSuffix(seg, core.Ellipsis), "clipped sibling finding must end with an ellipsis: %q", seg)
	words := strings.Fields(strings.TrimSuffix(seg, core.Ellipsis))
	require.NotEmpty(t, words)
	require.Equal(t, "abcdefgh", words[len(words)-1], "the cut must land on a word boundary, not mid-word")
}
