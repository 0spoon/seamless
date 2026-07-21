package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// TestSessionEndHarvestsClaudeTokens: a Claude Code SessionEnd parses the
// transcript's per-message usage (deduped by message id) and lands the real token
// totals on the ambient session -- alongside the findings harvest.
func TestSessionEndHarvestsClaudeTokens(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()
	claudeID := "abcdef12-7777"

	_, _ = post(t, ts.URL+"/api/hooks/session-start", testKey, map[string]any{
		"session_id": claudeID, "cwd": "/work/demo", "source": "startup",
	})

	// A transcript where msg_A spans two lines (repeated usage) and msg_B is one.
	transcript := writeTranscript(t, strings.Join([]string{
		aLine("msg_A", 100, 50, 1000, 200),
		aLine("msg_A", 100, 50, 1000, 200),
		aLine("msg_B", 20, 10, 0, 0),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`,
	}, "\n"))
	_, _ = post(t, ts.URL+"/api/hooks/session-end", testKey, map[string]any{
		"session_id": claudeID, "transcript_path": transcript, "reason": "clear",
	})

	got, _, err := store.SessionByID(ctx, db, requireAmbientSession(t, db, ClientClaudeCode, claudeID).ID)
	require.NoError(t, err)
	require.Equal(t, 120, got.Tokens.Input, "msg_A + msg_B, each once")
	require.Equal(t, 60, got.Tokens.Output)
	require.Equal(t, 1000, got.Tokens.Cached)
	require.Equal(t, 200, got.Tokens.CacheCreation)
	require.Equal(t, 120+60+1000+200, got.Tokens.Total)
}

// TestCodexStopHarvestsTokens: Codex has no SessionEnd, so every Stop re-reads the
// rollout's latest cumulative token_count and overwrites the ambient session's
// totals -- idempotent across turns.
func TestCodexStopHarvestsTokens(t *testing.T) {
	ts, db := newHandlerServer(t)
	ctx := context.Background()
	codexID := "01999999-1111-2222-3333-444444444444"

	_, _ = post(t, ts.URL+"/api/hooks/session-start?client=codex", testKey, map[string]any{
		"session_id": codexID, "cwd": "/work/demo", "source": "startup",
	})

	// Turn 1: a rollout with one cumulative token_count.
	rollout := writeRollout(t, strings.Join([]string{
		tokenCountLine(5000, 4000, 30),
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"turn one"}}`,
	}, "\n"))
	_, _ = post(t, ts.URL+"/api/hooks/stop?client=codex", testKey, map[string]any{
		"session_id": codexID, "transcript_path": rollout, "last_assistant_message": "turn one",
	})

	sess := requireAmbientSession(t, db, ClientCodex, codexID)
	got, _, err := store.SessionByID(ctx, db, sess.ID)
	require.NoError(t, err)
	require.Equal(t, 5000-4000, got.Tokens.Input)
	require.Equal(t, 5030, got.Tokens.Total)

	// Turn 2: the rollout grows; the newer cumulative token_count OVERWRITES (not
	// accumulates), so the stored total is the latest figure, never the sum.
	rollout2 := writeRollout(t, strings.Join([]string{
		tokenCountLine(5000, 4000, 30),
		tokenCountLine(12655, 9600, 75),
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"turn two"}}`,
	}, "\n"))
	_, _ = post(t, ts.URL+"/api/hooks/stop?client=codex", testKey, map[string]any{
		"session_id": codexID, "transcript_path": rollout2, "last_assistant_message": "turn two",
	})

	got, _, err = store.SessionByID(ctx, db, sess.ID)
	require.NoError(t, err)
	require.Equal(t, 12655-9600, got.Tokens.Input)
	require.Equal(t, 9600, got.Tokens.Cached)
	require.Equal(t, 75, got.Tokens.Output)
	require.Equal(t, 12730, got.Tokens.Total, "latest cumulative, not turn1 + turn2")
}
