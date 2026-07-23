package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// The corpus deliberately contains a name that is a prefix of another
// (chroma-boot vs chroma-boot-race): the boundary rule must keep the shorter
// slug from false-matching inside the longer one.
func TestMishapMemoryIDs(t *testing.T) {
	corpus := []core.Memory{
		{ID: "M1", Name: "chroma-boot-race"},
		{ID: "M2", Name: "chroma-boot"},
		{ID: "M3", Name: "pkill-target-check"},
	}
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"name mid-text", "violated pkill-target-check: pkill -f hit the daemon", []string{"M3"}},
		{"no name present", "wiped the wrong scratch directory", nil},
		{"multiple names in one text", "ignored chroma-boot-race and then pkill-target-check", []string{"M1", "M3"}},
		{"prefix slug does not match inside a longer slug", "raced chroma-boot-race again", []string{"M1"}},
		{"shorter slug still matches on its own", "raced chroma-boot on startup", []string{"M2"}},
		{"slug embedded in a longer token does not match", "ranchroma-boot-racer overflowed", nil},
		{"slug extended by a suffix does not match", "a pkill-target-checklist is not that memory", nil},
		{"punctuation delimits", "see `chroma-boot-race` (again)", []string{"M1"}},
		{"whole text is the slug", "chroma-boot-race", []string{"M1"}},
		{"match is case-sensitive (exact slug)", "violated Chroma-Boot-Race", nil},
		{"multibyte neighbors delimit", "échroma-boot-raceé", []string{"M1"}},
		{"empty text", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, mishapMemoryIDs(tt.text, corpus))
		})
	}
}

// A memory row without a name or id (defensive: never produced by the store)
// must not match everything or emit empty ids.
func TestMishapMemoryIDs_SkipsBlankRows(t *testing.T) {
	corpus := []core.Memory{{ID: "M1", Name: ""}, {ID: "", Name: "chroma-boot"}}
	require.Nil(t, mishapMemoryIDs("chroma-boot text", corpus))
}
