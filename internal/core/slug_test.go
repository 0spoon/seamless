package core

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already a slug", "backup-strategy", "backup-strategy"},
		{"dots and spaces", "sync.sh Windows SSH pitfalls", "sync-sh-windows-ssh-pitfalls"},
		{"colon", "deploy-runner: parallel-agent worktree pitfalls", "deploy-runner-parallel-agent-worktree-pitfalls"},
		{"digits kept, decimal split", "Widget9 2.4GHz antenna", "widget9-2-4ghz-antenna"},
		{"punctuation only", "!!!", "untitled"},
		{"empty", "", "untitled"},
		{"whitespace only", "   \t\n ", "untitled"},
		{"dash runs collapse", "a  --  b", "a-b"},
		{"edges trimmed", "--hello--", "hello"},
		{"underscores are separators", "foo_bar", "foo-bar"},
		// Unicode letters/numbers survive rather than collapsing to "untitled";
		// note slugs become filenames and notes_create does not disambiguate a
		// collision, so an ASCII-only filter would clobber non-Latin notes.
		{"accents kept", "Café Notes", "café-notes"},
		{"non-latin kept", "日本語ノート", "日本語ノート"},
		{"emoji stripped as non-letter", "ship it 🚀 now", "ship-it-now"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Slugify(tc.in))
		})
	}
}

func TestSlugifyIsIdempotent(t *testing.T) {
	for _, in := range []string{"backup-strategy", "Café Notes", "日本語ノート", "!!!", "Widget9 2.4GHz antenna"} {
		once := Slugify(in)
		require.Equal(t, once, Slugify(once), "slugifying a slug must be a no-op: %q", in)
	}
}

func TestSlugifyCapsAtRuneBoundary(t *testing.T) {
	const veryLong = "this is an extremely long memory name that keeps going well past the eighty character filesystem-safe limit we impose"
	got := Slugify(veryLong)
	require.LessOrEqual(t, utf8.RuneCountInString(got), SlugMaxRunes)
	require.NotEqual(t, "-", got[len(got)-1:], "cap must not leave a trailing dash")

	// The cap counts runes, so multi-byte input is never sliced mid-rune (a byte
	// cap here would emit invalid UTF-8).
	multi := Slugify(strings.Repeat("日", SlugMaxRunes*2))
	require.True(t, utf8.ValidString(multi))
	require.Equal(t, SlugMaxRunes, utf8.RuneCountInString(multi))
}
