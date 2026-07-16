package core

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestTruncateWords(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"fits unchanged", "short and sweet", 40, "short and sweet"},
		{"exact fit unchanged", "twelve chars", 12, "twelve chars"},
		{"cuts on word boundary", "the quick brown fox jumps", 16, "the quick brown…"},
		{"never splits the last word", "authentication is not a vulnerability", 30, "authentication is not a…"},
		{"trims trailing separator before ellipsis", "gates tools/call only, so an", 24, "gates tools/call only…"},
		{"trims dangling em-dash half", "design -- auth gates", 12, "design…"},
		{"single long token hard-cuts", "supercalifragilisticexpialidocious", 10, "supercali…"},
		{"disabled at maxRunes 1", "anything at all", 1, "anything at all"},
		{"multibyte stays valid", "café société normande", 12, "café…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateWords(tc.in, tc.max)
			require.Equal(t, tc.want, got)
			require.True(t, utf8.ValidString(got), "result must be valid UTF-8")
			if utf8.RuneCountInString(tc.in) > tc.max && tc.max > 1 {
				require.LessOrEqual(t, utf8.RuneCountInString(got), tc.max,
					"truncated result (ellipsis included) must fit the budget")
			}
		})
	}
}
