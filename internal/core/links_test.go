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
