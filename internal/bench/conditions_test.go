package bench

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultConditions(t *testing.T) {
	defs := DefaultConditions()
	require.Len(t, defs, len(Profiles))
	for i, c := range defs {
		require.Equal(t, string(Profiles[i]), c.Name)
		require.Equal(t, Profiles[i], c.Profile)
		require.Equal(t, ClientClaude, c.Client)
		require.NoError(t, c.Validate())
	}
}

func TestParseCondition(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    Condition
		wantErr string
	}{
		{"name only defaults profile and client", "vanilla",
			Condition{Name: "vanilla", Profile: ProfileVanilla, Client: ClientClaude}, ""},
		{"explicit profile", "study:mechanism",
			Condition{Name: "study", Profile: ProfileMechanism, Client: ClientClaude}, ""},
		{"explicit client", "arm1:full:claude",
			Condition{Name: "arm1", Profile: ProfileFull, Client: ClientClaude}, ""},
		{"whitespace stripped", " vanilla ",
			Condition{Name: "vanilla", Profile: ProfileVanilla, Client: ClientClaude}, ""},
		{"name not a profile", "candidate",
			Condition{}, "unknown profile"},
		{"uppercase name", "Vanilla",
			Condition{}, "bad condition name"},
		{"empty name", ":mechanism",
			Condition{}, "bad condition name"},
		{"unknown profile", "x:turbo",
			Condition{}, "unknown profile"},
		{"unknown client", "x:full:gemini",
			Condition{}, "unknown client"},
		{"codex is design-only", "x:full:codex",
			Condition{}, "design-only"},
		{"too many fields", "a:b:c:d",
			Condition{}, "want name[:profile[:client]]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCondition(tt.spec)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseConditions(t *testing.T) {
	got, err := ParseConditions("vanilla,mechanism,full")
	require.NoError(t, err)
	require.Equal(t, DefaultConditions(), got)

	got, err = ParseConditions("vanilla,,full,")
	require.NoError(t, err)
	require.Len(t, got, 2)

	_, err = ParseConditions("vanilla,vanilla")
	require.ErrorContains(t, err, "duplicate condition name")

	_, err = ParseConditions("")
	require.ErrorContains(t, err, "selected no arms")

	_, err = ParseConditions(",,")
	require.ErrorContains(t, err, "selected no arms")
}

func TestConditionSpecRoundTrip(t *testing.T) {
	for _, c := range DefaultConditions() {
		parsed, err := ParseCondition(c.Spec())
		require.NoError(t, err)
		require.Equal(t, c, parsed)
	}
}

func TestConditionValidate_NameGrammar(t *testing.T) {
	for _, bad := range []string{"", "-lead", "has space", "UPPER", "dot.name"} {
		c := Condition{Name: bad, Profile: ProfileVanilla, Client: ClientClaude}
		require.ErrorContains(t, c.Validate(), "bad condition name", "name %q", bad)
	}
	c := Condition{Name: "ok-name_2", Profile: ProfileVanilla, Client: ClientClaude}
	require.NoError(t, c.Validate())
}
