//go:build linux

package agentproc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStat(t *testing.T) {
	// 52 fields as the kernel writes them; starttime (field 22) is 98765.
	const tail = " S 4242 100 100 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000000 200 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0 0 0 0 0 0 0 0 0"
	for _, tc := range []struct {
		name string
		line string
		want procInfo
		ok   bool
	}{
		{"plain", "777 (claude)" + tail, procInfo{ppid: 4242, name: "claude", start: 98765}, true},
		{"comm with spaces and parens", "777 (a (b) c)" + tail, procInfo{ppid: 4242, name: "a (b) c", start: 98765}, true},
		{"truncated", "777 (claude) S 4242", procInfo{}, false},
		{"no comm", "777 claude S 4242", procInfo{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStat(tc.line)
			if !tc.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
