package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// unsetEnv removes keys for the duration of the test and restores them after:
// t.Setenv registers the restore, the Unsetenv makes the key truly absent
// (an empty value would be a present, unparseable one).
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

func TestUpdateDefaults(t *testing.T) {
	u := Defaults().Update
	require.Nil(t, u.Check, "unset: the build decides, not config")
	require.Equal(t, DefaultUpdateCheckInterval, u.CheckInterval.Std())
	require.Equal(t, 6*time.Hour, u.CheckInterval.Std())
	require.NoError(t, u.Validate())
}

func TestUpdateValidate(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		wantErr  bool
	}{
		{"zero is out of range, not the default", 0, true},
		{"just under the floor", 59 * time.Minute, true},
		{"the floor", time.Hour, false},
		{"the default", 6 * time.Hour, false},
		{"the ceiling", 720 * time.Hour, false},
		{"just over the ceiling", 721 * time.Hour, true},
		{"negative", -time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Update{CheckInterval: Duration(tt.interval)}.Validate()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "config: update.check_interval: ")
			require.ErrorContains(t, err, "outside [1h, 720h]")
		})
	}

	// Validate is wired into the whole-config check.
	c := Defaults()
	c.Update.CheckInterval = Duration(59 * time.Minute)
	require.ErrorContains(t, c.Validate(), "update.check_interval")
}

func TestLoadFrom_UpdateCheckFile(t *testing.T) {
	unsetEnv(t, "SEAMLESS_UPDATE_CHECK", "SEAMLESS_UPDATE_CHECK_INTERVAL")

	tests := []struct {
		name string
		body string
		want *bool // nil = unset
	}{
		{"block absent", "addr: 127.0.0.1:8081\n", nil},
		{"key absent", "update:\n  check_interval: 12h\n", nil},
		{"explicit false", "update:\n  check: false\n", new(false)},
		{"explicit true", "update:\n  check: true\n", new(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadFrom(writeConfig(t, tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Update.Check)
		})
	}
}

func TestLoadFrom_UpdateCheckEnv(t *testing.T) {
	falseFile := writeConfig(t, "update:\n  check: false\n")
	tests := []struct {
		name    string
		file    string // "" = no file
		env     string
		want    *bool
		wantErr bool
	}{
		{name: "true", env: "true", want: new(true)},
		{name: "false", env: "false", want: new(false)},
		{name: "ParseBool spelling, trimmed", env: " 1 ", want: new(true)},
		{name: "env wins over file", file: falseFile, env: "true", want: new(true)},
		{name: "garbage is an error, never false", env: "nope", wantErr: true},
		{name: "set but empty is an error, never unset", env: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SEAMLESS_UPDATE_CHECK", tt.env)
			cfg, err := LoadFrom(tt.file)
			if tt.wantErr {
				require.ErrorContains(t, err, "config: env SEAMLESS_UPDATE_CHECK: ")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Update.Check)
		})
	}

	t.Run("unset env leaves the file value", func(t *testing.T) {
		unsetEnv(t, "SEAMLESS_UPDATE_CHECK")
		cfg, err := LoadFrom(falseFile)
		require.NoError(t, err)
		require.Equal(t, new(false), cfg.Update.Check)

		cfg, err = LoadFrom("")
		require.NoError(t, err)
		require.Nil(t, cfg.Update.Check)
	})
}

func TestLoadFrom_UpdateCheckIntervalEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr string
	}{
		{name: "hours", env: "12h", want: 12 * time.Hour},
		{name: "compound, trimmed", env: " 1h30m ", want: 90 * time.Minute},
		{name: "the ceiling", env: "720h", want: 720 * time.Hour},
		{name: "bare number needs a unit", env: "30", wantErr: `config: env SEAMLESS_UPDATE_CHECK_INTERVAL: invalid duration "30": needs a unit`},
		{name: "garbage", env: "soon", wantErr: "config: env SEAMLESS_UPDATE_CHECK_INTERVAL: "},
		{name: "set but empty", env: "", wantErr: "config: env SEAMLESS_UPDATE_CHECK_INTERVAL: "},
		{name: "negative", env: "-6h", wantErr: "must not be negative"},
		// Parses, then fails the range check.
		{name: "below the floor", env: "30m", wantErr: "config: update.check_interval: 30m is outside [1h, 720h]"},
		{name: "bare zero parses, then fails the range", env: "0", wantErr: "config: update.check_interval: 0s is outside"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SEAMLESS_UPDATE_CHECK_INTERVAL", tt.env)
			cfg, err := LoadFrom("")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Update.CheckInterval.Std())
		})
	}

	t.Run("env wins over file", func(t *testing.T) {
		t.Setenv("SEAMLESS_UPDATE_CHECK_INTERVAL", "24h")
		cfg, err := LoadFrom(writeConfig(t, "update:\n  check_interval: 12h\n"))
		require.NoError(t, err)
		require.Equal(t, 24*time.Hour, cfg.Update.CheckInterval.Std())
	})
}

func TestLoadFrom_UpdateFileRejects(t *testing.T) {
	unsetEnv(t, "SEAMLESS_UPDATE_CHECK", "SEAMLESS_UPDATE_CHECK_INTERVAL")

	tests := []struct {
		name string
		body string
		want []string // every fragment must appear
	}{
		// Leaving the key out is how check says "unset"; null is not a second spelling.
		{"explicit null check", "update:\n  check: null\n", []string{"config.update.check must not be null"}},
		{"implicit null check", "update:\n  check:\n", []string{"config.update.check must not be null"}},
		{"null block", "update: null\n", []string{"config.update must not be null"}},
		{"misspelled key", "update:\n  chek: true\n", []string{"field chek not found"}},
		{"misspelled interval", "update:\n  interval: 6h\n", []string{"field interval not found"}},
		{"non-bool check", "update:\n  check: sometimes\n", []string{"cannot unmarshal", "sometimes"}},
		{"bare int interval", "update:\n  check_interval: 30\n", []string{"line 2", `invalid duration "30": needs a unit`}},
		{"float interval", "update:\n  check_interval: 1.5\n", []string{"line 2", "is not a duration"}},
		{"zero interval", "update:\n  check_interval: 0\n", []string{"config: update.check_interval: 0s is outside [1h, 720h]"}},
		{"interval below the floor", "update:\n  check_interval: 59m\n", []string{"config: update.check_interval: 59m is outside"}},
		{"interval above the ceiling", "update:\n  check_interval: 721h\n", []string{"config: update.check_interval: 721h is outside"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFrom(writeConfig(t, tt.body))
			for _, frag := range tt.want {
				require.ErrorContains(t, err, frag)
			}
		})
	}
}

func TestLoadFrom_UpdateIntervalFile(t *testing.T) {
	unsetEnv(t, "SEAMLESS_UPDATE_CHECK", "SEAMLESS_UPDATE_CHECK_INTERVAL")

	cfg, err := LoadFrom(writeConfig(t, "update:\n  check_interval: 1h30m\n"))
	require.NoError(t, err)
	require.Equal(t, 90*time.Minute, cfg.Update.CheckInterval.Std())
	require.Nil(t, cfg.Update.Check, "setting the interval leaves check unset")

	// An absent interval keeps the default, even when the block is present.
	cfg, err = LoadFrom(writeConfig(t, "update:\n  check: true\n"))
	require.NoError(t, err)
	require.Equal(t, DefaultUpdateCheckInterval, cfg.Update.CheckInterval.Std())
}
