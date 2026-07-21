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
	mk := func(id string, secAgo int, kind core.EventKind) events.KindTick {
		return events.KindTick{ID: id, TS: now.Add(-time.Duration(secAgo) * time.Second), Kind: string(kind)}
	}
	// Newest-first (as KindTimeline returns). The 1h-old plan tick sits outside a
	// 5-minute window and must be dropped.
	ticks := []events.KindTick{
		mk("01TOOL", 1, core.EventToolCall),
		mk("01INJECT", 2, core.EventInjected),
		mk("01PROMPT", 3, core.EventHookPrompt),
		mk("01SESSION", 30, core.EventSessionStarted),
		mk("01PLAN", 3600, core.EventPlanApproved),
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
	require.Equal(t, "01TOOL", buckets[volBuckets-1].LatestID, "the newest event becomes the bucket click target")

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
		if b.N > 0 {
			require.NotEmpty(t, b.LatestID, "every non-empty bucket exposes an event detail target")
		}
	}
	require.Equal(t, 7, total)
}

// The shared renderer powers the live Interactions chart and the embedded
// project/session timelines. Pin the interaction affordances in one place so a
// later visual refactor cannot silently return the embedded charts to title-only
// hover or remove their event-detail action.
func TestInteractionsVolumeClient_InteractiveContract(t *testing.T) {
	js := string(interactionsJS)
	require.Contains(t, js, "fillVolumeTip(tip, b, range, action)", "non-empty buckets render a visible breakdown")
	require.Contains(t, js, "bar.onfocus = bar.onpointerenter", "keyboard focus gets the same readout as pointer hover")
	require.Contains(t, js, "'/console/events/' + encodeURIComponent(b.latestId)", "embedded charts link to the represented event")
	require.Contains(t, js, "bar.setAttribute('aria-pressed', 'false')", "live-feed filter bars expose their toggle state")
}
