package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWikiLinks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"none", "plain text with no links", nil},
		{"single", "see [[chroma-boot-race]] here", []string{"chroma-boot-race"}},
		{"project-qualified", "ref [[seam/backup-strategy]]", []string{"backup-strategy"}},
		{"alias-and-anchor", "[[name|Display]] and [[other#section]]", []string{"name", "other"}},
		{"dedup", "[[a]] then [[a]] again and [[b]]", []string{"a", "b"}},
		{"empty-ignored", "[[]] and [[ ]] and [[real]]", []string{"real"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, WikiLinks(tc.body))
		})
	}
}

func TestReplaceWikiLinks(t *testing.T) {
	// repl uppercases the resolved name and records the tokens it saw.
	upper := func(token, name string) string { return "<" + name + ">" }

	require.Equal(t, "see <chroma-boot-race> here",
		ReplaceWikiLinks("see [[chroma-boot-race]] here", upper))
	// project-qualified + alias normalize to the bare name for repl.
	require.Equal(t, "<backup-strategy> and <name>",
		ReplaceWikiLinks("[[seam/backup-strategy]] and [[name|Display]]", upper))
	// No links -> unchanged.
	require.Equal(t, "plain text", ReplaceWikiLinks("plain text", upper))
	// An empty inner reference is left as its literal token.
	require.Equal(t, "[[]] stays", ReplaceWikiLinks("[[]] stays", upper))
}
