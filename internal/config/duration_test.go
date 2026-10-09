package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr string // "" = no error
	}{
		{in: "6h", want: 6 * time.Hour},
		{in: "90m", want: 90 * time.Minute},
		{in: "1h30m", want: 90 * time.Minute},
		{in: "0s", want: 0},
		{in: " 6h ", want: 6 * time.Hour},
		{in: "\t720h\n", want: 720 * time.Hour},
		{in: "1.5h", want: 90 * time.Minute},
		// A bare zero is the same in every unit, so it alone may go bare.
		{in: "0", want: 0},
		{in: " 0 ", want: 0},
		{in: "00", want: 0},
		{in: "-0", want: 0},

		{in: "", wantErr: "invalid duration"},
		{in: "   ", wantErr: "invalid duration"},
		{in: "30", wantErr: `invalid duration "30": needs a unit, e.g. 24h`},
		{in: "-30", wantErr: "needs a unit"},
		{in: "+6", wantErr: "needs a unit"},
		{in: "007", wantErr: "needs a unit"},
		{in: "99999999999999999999999", wantErr: "needs a unit"},
		{in: "-1h", wantErr: `invalid duration "-1h": must not be negative`},
		{in: "6x", wantErr: `invalid duration "6x"`},
		{in: "6H", wantErr: `invalid duration "6H"`},
		{in: "abc", wantErr: `invalid duration "abc"`},
		{in: "0.0", wantErr: `invalid duration "0.0"`},
		{in: "99999999999h", wantErr: `invalid duration "99999999999h"`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Std())
		})
	}
}

func TestDurationString(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{6 * time.Hour, "6h"},
		{720 * time.Hour, "720h"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Minute, "2m"},
		{10 * time.Minute, "10m"},
		{90 * time.Second, "1m30s"},
		{30 * time.Second, "30s"},
		{time.Hour + 30*time.Second, "1h0m30s"},
		{1500 * time.Millisecond, "1.5s"},
		{100 * time.Millisecond, "100ms"},
		{0, "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			d := Duration(tt.in)
			require.Equal(t, tt.want, d.String())
			// The compact form is still a duration ParseDuration reads back.
			back, err := ParseDuration(d.String())
			require.NoError(t, err)
			require.Equal(t, d, back)
		})
	}
}

func TestDurationUnmarshalYAML(t *testing.T) {
	type doc struct {
		D Duration `yaml:"d"`
	}
	tests := []struct {
		name    string
		body    string
		want    time.Duration
		wantErr []string // every fragment must appear; nil = no error
	}{
		{name: "string", body: "d: 6h\n", want: 6 * time.Hour},
		{name: "compound string", body: "d: 1h30m\n", want: 90 * time.Minute},
		{name: "quoted string", body: `d: "90m"` + "\n", want: 90 * time.Minute},
		{name: "zero string", body: "d: 0s\n", want: 0},
		{name: "bare zero", body: "d: 0\n", want: 0},
		{name: "quoted zero", body: `d: "0"` + "\n", want: 0},
		{name: "hex zero is still zero", body: "d: 0x0\n", want: 0},

		{name: "bare int", body: "d: 30\n", wantErr: []string{"line 1", `invalid duration "30": needs a unit`}},
		{name: "quoted bare int", body: `d: "30"` + "\n", wantErr: []string{"line 1", "needs a unit"}},
		{name: "negative bare int", body: "d: -5\n", wantErr: []string{"line 1", "needs a unit"}},
		{name: "int on a later line", body: "\n\nd: 7\n", wantErr: []string{"line 3", "needs a unit"}},
		{name: "float", body: "d: 1.5\n", wantErr: []string{"line 1", "!!float 1.5 is not a duration"}},
		{name: "bool", body: "d: true\n", wantErr: []string{"line 1", "!!bool true is not a duration"}},
		{name: "sequence", body: "d: [1]\n", wantErr: []string{"line 1", "a sequence is not a duration"}},
		{name: "mapping", body: "d: {h: 6}\n", wantErr: []string{"line 1", "a mapping is not a duration"}},
		{name: "timestamp", body: "d: 2026-10-09\n", wantErr: []string{"line 1", "!!timestamp", "is not a duration"}},
		{name: "negative string", body: "d: -1h\n", wantErr: []string{"line 1", "must not be negative"}},
		{name: "unknown unit", body: "d: 6x\n", wantErr: []string{"line 1", `invalid duration "6x"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got doc
			err := yaml.Unmarshal([]byte(tt.body), &got)
			if tt.wantErr != nil {
				for _, frag := range tt.wantErr {
					require.ErrorContains(t, err, frag)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got.D.Std())
		})
	}
}

func TestDurationMarshalYAMLRoundTrip(t *testing.T) {
	type doc struct {
		D Duration `yaml:"d"`
	}
	out, err := yaml.Marshal(doc{D: Duration(90 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, "d: 1h30m\n", string(out))

	var back doc
	require.NoError(t, yaml.Unmarshal(out, &back))
	require.Equal(t, Duration(90*time.Minute), back.D)
}
