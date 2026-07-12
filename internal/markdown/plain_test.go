package markdown

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlainText(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", ""},
		{"blank", "  \n\t", ""},
		{"heading stripped", "# Title", "Title"},
		{"list flattened", "- one\n- two", "one two"},
		{"emphasis stripped", "**bold** and _italic_", "bold and italic"},
		{"inline code kept", "run `go test` now", "run go test now"},
		{"fenced code kept", "text\n\n```\ncode line\n```", "text code line"},
		{"wiki alias", "see [[proj/known|The Thing]] end", "see The Thing end"},
		{"wiki bare", "see [[known]] end", "see known end"},
		{"paragraphs collapse", "one\n\ntwo", "one two"},
		{"whitespace collapses", "a    b\tc", "a b c"},
		{"link text kept url dropped", "go to [the site](https://example.com)", "go to the site"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, PlainText(tc.body))
		})
	}
}
