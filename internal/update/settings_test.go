package update

import (
	"fmt"
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

func TestEffective_Auto(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name                        string
		baseCheck, baseAuto         *bool
		overrideCheck, overrideAuto *bool
		release                     bool
		wantAuto                    bool
		wantSource                  string
		wantLocked                  bool
	}{
		// Nothing set: on wherever checks are on.
		{"release build, nothing set", nil, nil, nil, nil, true, true, SourceDefault, false},
		{"source build, nothing set: checks are off", nil, nil, nil, nil, false, false, SourceCheck, false},
		// Unset auto is the owner's permission on every build; Detect is what
		// keeps a source build from acting on it.
		{"source build told to check", &yes, nil, nil, nil, false, true, SourceDefault, false},
		{"source build checking from the console", nil, nil, &yes, nil, false, true, SourceDefault, false},
		// Key unset: the console is free either way.
		{"console turns auto off", nil, nil, nil, &no, true, false, SourceConsole, false},
		{"console turns auto on", nil, nil, nil, &yes, true, true, SourceConsole, false},
		// An explicit file/env true can still be turned off from the console.
		{"config true", nil, &yes, nil, nil, true, true, SourceConfig, false},
		{"config true, console off", nil, &yes, nil, &no, true, false, SourceConsole, false},
		{"config true, console on", nil, &yes, nil, &yes, true, true, SourceConsole, false},
		// An explicit file/env false is final and locks the toggle.
		{"config false", nil, &no, nil, nil, true, false, SourceConfig, true},
		{"config false beats a console on", nil, &no, nil, &yes, true, false, SourceConfig, true},
		{"config false, console off", nil, &no, nil, &no, true, false, SourceConfig, true},
		// Its own false is named before checks being off: it alone survives
		// checks coming back on.
		{"config false while checks are locked off", &no, &no, nil, nil, true, false, SourceConfig, true},
		{"config false while the console has checks off", nil, &no, &no, nil, true, false, SourceConfig, true},
		// Checks off: nothing can be fetched to install, whatever auto says.
		// Locked exactly when the check is.
		{"checks locked off", &no, nil, nil, nil, true, false, SourceCheck, true},
		{"checks locked off beat a console on", &no, nil, nil, &yes, true, false, SourceCheck, true},
		{"checks locked off beat a config true", &no, &yes, nil, nil, true, false, SourceCheck, true},
		{"checks off in the console", nil, nil, &no, nil, true, false, SourceCheck, false},
		{"checks off in the console beat a console on", nil, nil, &no, &yes, true, false, SourceCheck, false},
		{"checks off in the console beat a config true", nil, &yes, &no, nil, true, false, SourceCheck, false},
		{"checks on again: the console's auto applies", nil, nil, &yes, &no, true, false, SourceConsole, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := config.Update{Check: tt.baseCheck, Auto: tt.baseAuto}
			got := Effective(base, config.UpdateOverride{Check: tt.overrideCheck, Auto: tt.overrideAuto}, tt.release)
			require.Equal(t, tt.wantAuto, got.Auto)
			require.Equal(t, tt.wantSource, got.AutoSource)
			require.Equal(t, tt.wantLocked, got.AutoLocked)
		})
	}
}

// TestEffective_AutoInvariants walks every combination of the four switches
// and both builds, and checks the rules every surface relies on.
func TestEffective_AutoInvariants(t *testing.T) {
	states := []*bool{nil, new(true), new(false)}
	isFalse := func(b *bool) bool { return b != nil && !*b }
	for _, baseCheck := range states {
		for _, baseAuto := range states {
			for _, overrideCheck := range states {
				for _, overrideAuto := range states {
					for _, release := range []bool{true, false} {
						base := config.Update{Check: baseCheck, Auto: baseAuto}
						override := config.UpdateOverride{Check: overrideCheck, Auto: overrideAuto}
						got := Effective(base, override, release)
						name := fmt.Sprintf("base{check:%s auto:%s} override{check:%s auto:%s} release:%t",
							optBoolString(baseCheck), optBoolString(baseAuto), optBoolString(overrideCheck), optBoolString(overrideAuto), release)

						require.False(t, got.Auto && !got.Check, "%s: no automatic update without update traffic", name)
						require.False(t, got.Auto && got.AutoLocked, "%s: a locked toggle is a locked-off toggle", name)
						require.Equal(t, isFalse(baseAuto) || isFalse(baseCheck), got.AutoLocked,
							"%s: only a file/env false locks auto", name)
						if isFalse(baseAuto) {
							require.Equal(t, SourceConfig, got.AutoSource, "%s: a file/env false is final", name)
						}
						require.Equal(t, !got.Check && !isFalse(baseAuto), got.AutoSource == SourceCheck, name)

						// Auto never feeds back into the check.
						noAuto := Effective(config.Update{Check: baseCheck}, config.UpdateOverride{Check: overrideCheck}, release)
						require.Equal(t, noAuto.Check, got.Check, name)
						require.Equal(t, noAuto.CheckSource, got.CheckSource, name)
						require.Equal(t, noAuto.CheckLocked, got.CheckLocked, name)
					}
				}
			}
		}
	}
}

func optBoolString(b *bool) string {
	if b == nil {
		return "unset"
	}
	return fmt.Sprint(*b)
}

func TestEffective_Durations(t *testing.T) {
	base := config.Update{
		CheckInterval: config.Duration(3 * time.Hour),
		MaxDefer:      config.Duration(2 * time.Hour),
		MinAge:        config.Duration(90 * time.Minute),
	}
	got := Effective(base, config.UpdateOverride{}, true)
	require.Equal(t, 3*time.Hour, got.CheckInterval)
	require.Equal(t, 2*time.Hour, got.MaxDefer)
	require.Equal(t, 90*time.Minute, got.MinAge)

	// A configured zero soak is kept: no soak, never the default.
	base.MinAge = 0
	require.Zero(t, Effective(base, config.UpdateOverride{}, true).MinAge)

	// The durations hold whatever the switches say.
	off := base
	off.Check, off.Auto = new(false), new(false)
	got = Effective(off, config.UpdateOverride{}, true)
	require.Equal(t, 3*time.Hour, got.CheckInterval)
	require.Equal(t, 2*time.Hour, got.MaxDefer)

	// The loaded defaults come through as they are.
	got = Effective(config.Defaults().Update, config.UpdateOverride{}, true)
	require.Equal(t, config.DefaultUpdateCheckInterval, got.CheckInterval)
	require.Equal(t, config.DefaultUpdateMaxDefer, got.MaxDefer)
	require.Equal(t, config.DefaultUpdateMinAge, got.MinAge)
}

func TestEffective_ZeroIntervalFallsBackToDefault(t *testing.T) {
	// A zero-value config (a caller that never loaded one) still schedules,
	// and a pending automatic update still has a deadline. Its soak is zero:
	// MinAge has no fallback, since zero is a valid soak.
	got := Effective(config.Update{}, config.UpdateOverride{}, true)
	require.Equal(t, config.DefaultUpdateCheckInterval, got.CheckInterval)
	require.Equal(t, config.DefaultUpdateMaxDefer, got.MaxDefer)
	require.Zero(t, got.MinAge)
}
