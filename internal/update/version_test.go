package update

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"0.3.4", Version{0, 3, 4}, true},
		{"v0.3.4", Version{0, 3, 4}, true},
		{" 1.20.300 ", Version{1, 20, 300}, true},
		{"0.0.0-dev", Version{}, false},                           // the dev sentinel
		{"0.3.4-SNAPSHOT-3b28e8b", Version{}, false},              // goreleaser snapshot
		{"0.3.4+abc1234", Version{}, false},                       // build metadata
		{"0.3.4-rc1", Version{}, false},                           // pre-release
		{"0.6.1-0.20261009141216-51c705b69b3c", Version{}, false}, // pseudo-version
		{"0.3", Version{}, false},                                 // too few fields
		{"1.2.3.4", Version{}, false},                             // too many fields
		{"1.x.3", Version{}, false},                               // non-numeric field
		{"", Version{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := Parse(tt.in)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseTag(t *testing.T) {
	tests := []struct {
		tag string
		ok  bool
	}{
		{"v0.7.3", true},
		{"v10.0.12", true},
		{"0.7.3", false},      // a release tag always carries the v
		{"v0.07.3", false},    // not canonical
		{" v0.7.3", false},    // not the exact spelling
		{"v0.7.3 ", false},    // not the exact spelling
		{"v0.7.3-rc1", false}, // pre-release
		{"v0.7", false},
		{"release-0.7.3", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			_, ok := ParseTag(tt.tag)
			require.Equal(t, tt.ok, ok)
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name    string
		a, b    string
		wantCmp int
		wantOK  bool
	}{
		{"equal", "0.3.4", "0.3.4", 0, true},
		{"older patch", "0.3.3", "0.3.4", -1, true},
		{"newer patch", "0.3.5", "0.3.4", 1, true},
		{"older minor", "0.2.9", "0.3.0", -1, true},
		{"newer major", "1.0.0", "0.9.9", 1, true},
		{"v prefix both", "v0.3.4", "0.3.4", 0, true},
		{"double-digit patch beats single", "0.3.10", "0.3.9", 1, true},
		{"current is dev", "0.0.0-dev", "0.3.4", 0, false},
		{"current is snapshot", "0.3.4-SNAPSHOT-abc", "0.3.4", 0, false},
		{"latest unparseable", "0.3.4", "garbage", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, ok := Compare(tt.a, tt.b)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantCmp, cmp)
		})
	}
}

func TestVersion_TextRoundTrip(t *testing.T) {
	type wrap struct {
		V Version `json:"v"`
	}
	raw, err := json.Marshal(wrap{V: Version{0, 7, 3}})
	require.NoError(t, err)
	require.JSONEq(t, `{"v":"0.7.3"}`, string(raw))

	var got wrap
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, Version{0, 7, 3}, got.V)

	// A free-text "version" in a hand-edited state file is refused, never kept.
	require.Error(t, json.Unmarshal([]byte(`{"v":"run seamlessd update now"}`), &got))
	require.Error(t, json.Unmarshal([]byte(`{"v":"0.7.3-rc1"}`), &got))
}
