package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// An unparseable ?volume= used to fall back to 0, which is not a default but a
// distinct, wider query -- the client asked for a bounded window and silently got
// all of history back, labelled Window: 0. Only a value that cannot mean a window
// is rejected; 0 itself is the legitimate "all time" option the UI offers.
func TestInteractionsVolumeWindowParsing(t *testing.T) {
	mux := newTestMux(t)

	get := func(vs string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/console/interactions?volume="+vs, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Accept", "application/json")
		return do(mux, req)
	}

	for _, bad := range []string{"abc", "3600x", "1.5", "-60"} {
		rr := get(bad)
		require.Equal(t, http.StatusBadRequest, rr.Code, "volume=%s must not be read as a window", bad)
	}

	// 0 = all time, and a normal window: both still answered.
	for _, ok := range []string{"0", "3600"} {
		rr := get(ok)
		require.Equal(t, http.StatusOK, rr.Code, "volume=%s is a valid window", ok)
		var data interactionsData
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &data))
	}
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
