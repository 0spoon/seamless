package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/plans"
)

// placeSubagentTranscript copies the fixture transcript into the verified
// layout (<main transcript minus .jsonl>/subagents/agent-<id>.jsonl) and
// returns the main transcript path the hook payload would carry.
func placeSubagentTranscript(t *testing.T, agentID string) string {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("testdata", "agent-transcript.jsonl"))
	require.NoError(t, err)
	main := filepath.Join(t.TempDir(), "session.jsonl")
	sub := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-"+agentID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sub), 0o755))
	require.NoError(t, os.WriteFile(sub, fixture, 0o644))
	return main
}

func (e *captureEnv) subagentStop(t *testing.T, body map[string]any) {
	t.Helper()
	e.postHook(t, "subagent-stop", body)
}

func TestSubagentCaptureDuringPlanning(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	// An active draft plan gates the capture on.
	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nDo it.\n")
	e.planWrite(t, "Write", path)

	main := placeSubagentTranscript(t, "abc123")
	e.subagentStop(t, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "permission_mode": "default",
		"transcript_path": main, "agent_id": "abc123", "agent_type": "Explore",
	})

	note, found := e.loadNote(t, "cc-agent-abc123")
	require.True(t, found)
	require.Equal(t, "[Explore] Explore the gardener package and report its structure", note.Title)
	require.Contains(t, note.Tags, "plan:my-plan")
	require.Contains(t, note.Tags, "agent-cache")
	require.Contains(t, note.Tags, "agent:Explore")
	require.Contains(t, note.Tags, "created-by:agent")
	require.Contains(t, note.Body,
		"> captured from "+ambientName(ClientClaudeCode, testSID)+" | agent abc123 | git ")
	require.Contains(t, note.Body, "## Prompt\n\nExplore the gardener package and report its structure")
	require.Contains(t, note.Body, "## Report\n\nFinal report: the gardener has three passes.")

	evs := eventsOfKind(t, e.rec, core.EventSubagentCaptured)
	require.Len(t, evs, 1)
	require.Equal(t, note.ID, evs[0].ItemID)
	require.Equal(t, "abc123", evs[0].Payload["agent_id"])
	require.Equal(t, "my-plan", evs[0].Payload["plan_slug"])
	require.Equal(t, "Final report: the gardener has three passes.", payloadString(evs[0].Payload["content"]))
	require.Contains(t, payloadString(evs[0].Payload["prompt"]), "Explore the gardener package")
}

func TestSubagentCaptureGatedOutsidePlanning(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	main := placeSubagentTranscript(t, "abc123")
	e.subagentStop(t, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "permission_mode": "default",
		"transcript_path": main, "agent_id": "abc123", "agent_type": "Explore",
	})

	_, found := e.loadNote(t, "cc-agent-abc123")
	require.False(t, found)
	require.Empty(t, eventsOfKind(t, e.rec, core.EventSubagentCaptured))
}

func TestSubagentCapturePlanModeGate(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	// permission_mode=="plan" gates the capture on without any captured plan;
	// the payload names the subagent transcript directly (the other resolution
	// branch). No plan slug is known, so no plan: tag.
	main := placeSubagentTranscript(t, "def456")
	sub := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-def456.jsonl")
	e.subagentStop(t, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "permission_mode": "plan",
		"transcript_path": sub, "agent_id": "def456", "agent_type": "general-purpose",
	})

	note, found := e.loadNote(t, "cc-agent-def456")
	require.True(t, found)
	require.Contains(t, note.Tags, "agent-cache")
	require.Contains(t, note.Tags, "agent:general-purpose")
	for _, tag := range note.Tags {
		require.False(t, strings.HasPrefix(tag, "plan:"), "no plan tag without a correlated plan, got %q", tag)
	}
}

func TestSubagentBeforePlanAdoptedIntoComposition(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	// Explore-first pattern: the subagent completes before any plan file exists,
	// so no plan slug can be minted yet.
	main := placeSubagentTranscript(t, "early99")
	e.subagentStop(t, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "permission_mode": "plan",
		"transcript_path": main, "agent_id": "early99", "agent_type": "Explore",
	})

	note, found := e.loadNote(t, "cc-agent-early99")
	require.True(t, found)
	require.Empty(t, plans.SlugFromTags(note.Tags))
	require.Equal(t, []any{"cc-agent-early99"}, e.sessionPlanMeta(t)["pending_agents"],
		"the parked note slug rides on the session until a plan exists")

	// The first plan capture mints the slug and adopts the parked agent note.
	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nDo it.\n")
	e.planWrite(t, "Write", path)

	note, found = e.loadNote(t, "cc-agent-early99")
	require.True(t, found)
	require.Contains(t, note.Tags, "plan:my-plan")
	require.Contains(t, note.Tags, "agent-cache")
	require.Nil(t, e.sessionPlanMeta(t)["pending_agents"], "pending list drains at adoption")

	evs := eventsOfKind(t, e.rec, core.EventPlanCaptured)
	require.Len(t, evs, 1)
	require.Equal(t, float64(1), evs[0].Payload["adopted_agents"])

	// A later subagent (slug now known) is tagged directly, no pending detour.
	main2 := placeSubagentTranscript(t, "late42")
	e.subagentStop(t, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "permission_mode": "plan",
		"transcript_path": main2, "agent_id": "late42", "agent_type": "Explore",
	})
	note, found = e.loadNote(t, "cc-agent-late42")
	require.True(t, found)
	require.Contains(t, note.Tags, "plan:my-plan")
	require.Nil(t, e.sessionPlanMeta(t)["pending_agents"])
}

func TestSubagentCaptureMissingTranscriptFailsOpen(t *testing.T) {
	e := newCaptureEnv(t, config.PlanCapture{Enabled: true, AutoTask: true})
	e.startSession(t)

	path := e.writePlanFile(t, "clever-stallman", "# My Plan\n\nDo it.\n")
	e.planWrite(t, "Write", path)

	e.subagentStop(t, map[string]any{
		"session_id": testSID, "cwd": "/work/demo", "permission_mode": "default",
		"transcript_path": filepath.Join(t.TempDir(), "nope.jsonl"), "agent_id": "ghost1", "agent_type": "Explore",
	})

	_, found := e.loadNote(t, "cc-agent-ghost1")
	require.False(t, found)
	require.Empty(t, eventsOfKind(t, e.rec, core.EventSubagentCaptured))
}

// TestSubagentTranscriptPathRejectsTraversalAgentID pins the agent-id guard:
// the id is a filename fragment, so one carrying separators or ".." must not be
// joined into the transcript path (it could point the read anywhere on disk).
func TestSubagentTranscriptPathRejectsTraversalAgentID(t *testing.T) {
	base := toolPayload{TranscriptPath: "/tmp/sess.jsonl"}

	for _, id := range []string{"../../../etc/passwd", "a/b", `a\b`, "a..b"} {
		p := base
		p.AgentID = id
		require.Empty(t, subagentTranscriptPath(p), "agent id %q must be rejected", id)
	}

	ok := base
	ok.AgentID = "agent01"
	require.Equal(t, filepath.Join("/tmp/sess", "subagents", "agent-agent01.jsonl"),
		subagentTranscriptPath(ok))
}

func TestParseSubagentTranscript(t *testing.T) {
	prompt, report := parseSubagentTranscript(filepath.Join("testdata", "agent-transcript.jsonl"))
	require.Equal(t, "Explore the gardener package and report its structure", prompt,
		"prompt is the FIRST user message, not later tool feedback")
	require.Equal(t, "Final report: the gardener has three passes.", report,
		"report is the LAST assistant text")

	prompt, report = parseSubagentTranscript("")
	require.Empty(t, prompt)
	require.Empty(t, report)
}
