package update

import (
	"testing"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/stretchr/testify/require"
)

func TestEffective(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name       string
		base       *bool
		override   *bool
		release    bool
		wantCheck  bool
		wantSource string
		wantLocked bool
	}{
		// Nothing set: the build decides.
		{"release build, nothing set", nil, nil, true, true, SourceDefault, false},
		{"source build, nothing set", nil, nil, false, false, SourceDefault, false},
		// Key unset: the console is free either way, on any build.
		{"console turns a release build off", nil, &no, true, false, SourceConsole, false},
		{"console turns a source build on", nil, &yes, false, true, SourceConsole, false},
		// An explicit file/env true can still be turned off from the console.
		{"config true", &yes, nil, false, true, SourceConfig, false},
		{"config true, console off", &yes, &no, true, false, SourceConsole, false},
		{"config true, console on", &yes, &yes, true, true, SourceConsole, false},
		// An explicit file/env false is final and locks the toggle.
		{"config false", &no, nil, true, false, SourceConfig, true},
		{"config false beats a console on", &no, &yes, true, false, SourceConfig, true},
		{"config false, console off", &no, &no, false, false, SourceConfig, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := config.Update{Check: tt.base, CheckInterval: config.Duration(3 * time.Hour)}
			got := Effective(base, config.UpdateOverride{Check: tt.override}, tt.release)
			require.Equal(t, tt.wantCheck, got.Check)
			require.Equal(t, tt.wantSource, got.CheckSource)
			require.Equal(t, tt.wantLocked, got.CheckLocked)
			require.Equal(t, 3*time.Hour, got.CheckInterval)
		})
	}
}

func TestEffective_ZeroIntervalFallsBackToDefault(t *testing.T) {
	// A zero-value config (a caller that never loaded one) still schedules.
	got := Effective(config.Update{}, config.UpdateOverride{}, true)
	require.Equal(t, config.DefaultUpdateCheckInterval, got.CheckInterval)
}
