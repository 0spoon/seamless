package console

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
)

func TestVolCategory(t *testing.T) {
	require.Equal(t, "tool", volCategory("tool.call"))
	require.Equal(t, "inject", volCategory("retrieval.injected"))
	require.Equal(t, "prompt", volCategory("hook.prompt"))
	require.Equal(t, "session", volCategory("session.started"))
	require.Equal(t, "session", volCategory("session.ended"))
	require.Equal(t, "plan", volCategory("plan.approved"))
	require.Equal(t, "plan", volCategory("subagent.captured"))
	require.Equal(t, "", volCategory("memory.written"))
}

func TestBuildVolume(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	mk := func(secAgo int, kind core.EventKind) events.KindTick {
		return events.KindTick{TS: now.Add(-time.Duration(secAgo) * time.Second), Kind: string(kind)}
	}
	// Newest-first (as KindTimeline returns). The 1h-old plan tick sits outside a
	// 5-minute window and must be dropped.
	ticks := []events.KindTick{
		mk(1, core.EventToolCall),
		mk(2, core.EventInjected),
		mk(3, core.EventHookPrompt),
		mk(30, core.EventSessionStarted),
		mk(3600, core.EventPlanApproved),
	}

	buckets := buildVolume(ticks, 300*time.Second, now)
	require.Len(t, buckets, volBuckets)

	var total, tool, inject, prompt, session, plan int
	for _, b := range buckets {
		total += b.N
		tool += b.Tool
		inject += b.Inject
		prompt += b.Prompt
		session += b.Session
		plan += b.Plan
	}
	require.Equal(t, 4, total, "the 1h-old tick is outside the 300s window")
	require.Equal(t, 1, tool)
	require.Equal(t, 1, inject)
	require.Equal(t, 1, prompt)
	require.Equal(t, 1, session)
	require.Equal(t, 0, plan)
	require.Positive(t, buckets[volBuckets-1].N, "the most recent ticks land in the last bucket")

	require.Nil(t, buildVolume(nil, 300*time.Second, now), "empty input yields nil")
}

func TestInteractions_VolumeEndpoint(t *testing.T) {
	_, mux, _ := newConsoleWithSession(t, "cc/vol")

	var data interactionsData
	getJSON(t, mux, "/console/interactions?volume=0", &data)

	require.Empty(t, data.Rows, "volume mode returns no feed rows")
	require.Len(t, data.Volume, volBuckets)
	// Every interaction-kind seed event is counted (volume does not drop the recall
	// twin the feed hides): 01ERR, 01SES, 01TC1, 01TC2, 01INJ, 01RCL, 01HKP.
	var total int
	for _, b := range data.Volume {
		total += b.N
	}
	require.Equal(t, 7, total)
}
