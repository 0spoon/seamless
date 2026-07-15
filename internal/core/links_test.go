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

// WikiLinkName is the shared normalization for both wiki-link consumers:
// WikiLinks above, and the markdown renderer's goldmark extension, which parses
// [[...]] itself and passes the inner reference straight in. These cases pin the
// contract directly, independent of WikiLinks' regex.
func TestWikiLinkName(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"bare", "chroma-boot-race", "chroma-boot-race"},
		{"project-qualified", "seam/backup-strategy", "backup-strategy"},
		{"nested-path-keeps-last", "seam/sub/name", "name"},
		{"alias-dropped", "name|Display", "name"},
		{"anchor-dropped", "other#section", "other"},
		{"project-and-alias", "seam/name|Display", "name"},
		{"alias-before-slash-wins", "name|a/b", "name"},
		{"trimmed", "  spaced-name  ", "spaced-name"},
		{"empty", "", ""},
		{"whitespace-only", "   ", ""},
		{"anchor-only", "#section", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, WikiLinkName(tc.ref))
		})
	}
}
