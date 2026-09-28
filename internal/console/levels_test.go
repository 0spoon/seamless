package console

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// A level change reads as one ledger line in Activity, like a features toggle;
// a reset names the level it fell back to, and a thin payload says less rather
// than inventing a level.
func TestEventSummary_LevelChanged(t *testing.T) {
	for _, tc := range []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"from": "basic", "to": "standard", "by": "console"}, "console level basic -> standard"},
		{map[string]any{"to": "advanced", "by": "console"}, "console level set to advanced"},
		{map[string]any{"from": "advanced", "to": "basic", "reset": true}, "console level reset to the file configuration (basic)"},
		{map[string]any{"reset": true}, "console level reset to the file configuration"},
		{nil, "console level changed"},
	} {
		require.Equal(t, tc.want, eventSummary(core.Event{Kind: eventLevelChanged, Payload: tc.payload}))
	}
	require.Equal(t, "Console level changed", evtLabel(string(eventLevelChanged)))
}
