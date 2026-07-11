package retrieve

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// fakeBodyReader maps a memory file path to its body, standing in for files.Store
// in briefing tests.
type fakeBodyReader map[string]string

func (f fakeBodyReader) ReadMemory(relPath string) (core.Memory, error) {
	body, ok := f[relPath]
	if !ok {
		return core.Memory{}, fmt.Errorf("fakeBodyReader: no body for %q", relPath)
	}
	return core.Memory{Body: body}, nil
}

func TestParseStageHeader(t *testing.T) {
	cases := []struct {
		name, body, status, gate string
	}{
		{"basic", "Status: in_progress\nGate: human\n\nthe body", "in_progress", "human"},
		{"reversed", "Gate: ai\nStatus: blocked", "blocked", "ai"},
		{"list-prefixed", "- Status: open\n- Gate: human", "open", "human"},
		{"status only", "Status: open\n\ndetails", "open", ""},
		{"case insensitive", "STATUS: Done", "done", ""},
		{"trailing prose", "Status: blocked waiting on hardware", "blocked", ""},
		{"none", "just some prose\nno header here", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, gate := ParseStageHeader(c.body)
			require.Equal(t, c.status, status)
			require.Equal(t, c.gate, gate)
		})
	}
}

func TestBriefingPinsStages(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/seam":"seam"}`))
	insMem(t, db, "01A", "constraint", "no-force-push", "never force push", "seam")
	insMem(t, db, "01S1", "stage", "f5-ssh-signing", "ssh signing stage", "seam")
	insMem(t, db, "01S2", "stage", "f6-old", "finished stage", "seam")

	reader := fakeBodyReader{
		"memory/x/f5-ssh-signing.md": "Status: in_progress\nGate: human\n\ndetails",
		"memory/x/f6-old.md":         "Status: done\nGate: ai",
	}
	svc := New(db, nil, budgets(), nil)
	svc.SetBodyReader(reader)

	b, err := svc.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.Contains(t, b, "STAGE: f5-ssh-signing -- in_progress, gate human")
	require.NotContains(t, b, "f6-old", "done stages are not pinned")

	// Without a body reader, the stage section is omitted (degrades cleanly).
	plain := New(db, nil, budgets(), nil)
	b2, err := plain.Briefing(ctx, BriefingInput{CWD: "/work/seam", Source: "startup"})
	require.NoError(t, err)
	require.NotContains(t, b2, "STAGE:")
}
