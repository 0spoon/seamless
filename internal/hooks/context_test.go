package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

func TestPrepareHookContext_ClientAwareThreshold(t *testing.T) {
	tests := []struct {
		name      string
		client    Client
		content   string
		truncated bool
	}{
		{
			name:    "codex just below",
			client:  ClientCodex,
			content: strings.Repeat("a", (codexContextMaxTokens-1)*4),
		},
		{
			name:      "codex just above",
			client:    ClientCodex,
			content:   strings.Repeat("a", codexContextMaxTokens*4+1),
			truncated: true,
		},
		{
			name:    "normal 1500 token briefing unchanged",
			client:  ClientCodex,
			content: strings.Repeat("b", 1500*4),
		},
		{
			name:    "claude remains uncapped",
			client:  ClientClaudeCode,
			content: strings.Repeat("c", (codexContextMaxTokens+100)*4),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prepareHookContext(tt.client, tt.content)
			require.Equal(t, retrieve.EstimateTokens(tt.content), got.originalEstimatedTokens)
			require.Equal(t, tt.truncated, got.truncated)
			require.Equal(t, retrieve.EstimateTokens(got.content), got.emittedEstimatedTokens)
			if tt.truncated {
				require.LessOrEqual(t, got.emittedEstimatedTokens, codexContextMaxTokens)
				require.NotEqual(t, tt.content, got.content)
				return
			}
			require.Equal(t, tt.content, got.content)
		})
	}
}

func TestPrepareHookContext_PreservesPriorityUTF8AndTags(t *testing.T) {
	const identity = "Seam session: cx/019f7291-46ec71e628fd86c6 (ambient)"
	content := "<seam-briefing>\n" +
		"Seam project: demo -- pinned context.\n" +
		"CONSTRAINT: exact-telemetry: the emitted bytes are authoritative\n" +
		"PLAN: codex-hardening -- 1/11 done, 5 claimable, 1 in flight\n" +
		strings.Repeat("- optional-memory: 界界界 optional detail that drops first\n", 500) +
		identity + "\n</seam-briefing>"

	got := prepareHookContext(ClientCodex, content)
	require.True(t, got.truncated)
	require.Greater(t, got.originalEstimatedTokens, codexContextMaxTokens)
	require.LessOrEqual(t, got.emittedEstimatedTokens, codexContextMaxTokens)
	require.True(t, utf8.ValidString(got.content))
	require.True(t, strings.HasPrefix(got.content, "<seam-briefing>\n"))
	require.Contains(t, got.content, "CONSTRAINT: exact-telemetry")
	require.Contains(t, got.content, "PLAN: codex-hardening")
	require.Contains(t, got.content, contextTruncationMarker)
	require.Contains(t, got.content, identity)
	require.True(t, strings.HasSuffix(got.content, "</seam-briefing>"))
}

func TestWriteContextResponse_CodexEventsShareCapAndExactTelemetry(t *testing.T) {
	tests := []struct {
		event   string
		hook    string
		content string
	}{
		{
			event: "SessionStart",
			hook:  "session-start",
			content: "<seam-briefing>\nCONSTRAINT: keep-me: pinned\n" +
				strings.Repeat("session optional detail\n", 1200) +
				"Seam session: cx/019f7291-46ec71e628fd86c6 (ambient)\n</seam-briefing>",
		},
		{
			event: "UserPromptSubmit",
			hook:  "user-prompt-submit",
			content: "<seam-recall>possibly relevant:\n" +
				strings.Repeat("- oversized recall detail 界\n", 1200) +
				"</seam-recall>",
		},
		{
			event: "SubagentStart",
			hook:  "subagent-start",
			content: "<seam-briefing>\nCONSTRAINT: child-scope: keep constraints\n" +
				strings.Repeat("CONSTRAINT: bounded-child: 界界界\n", 1200) +
				"</seam-briefing>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			h := NewHandler(Config{DB: db, Events: events.NewRecorder(db)})
			rr := httptest.NewRecorder()

			h.writeContextResponse(context.Background(), rr, tt.event, tt.hook,
				ClientCodex, "codex-session", "prompt", tt.content, true, []string{"01A"})

			require.Equal(t, http.StatusOK, rr.Code)
			var response hookResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&response))
			require.NotNil(t, response.HookSpecificOutput)
			emitted := response.HookSpecificOutput.AdditionalContext
			require.LessOrEqual(t, retrieve.EstimateTokens(emitted), codexContextMaxTokens)
			require.True(t, utf8.ValidString(emitted))

			injected := eventsOfKind(t, events.NewRecorder(db), core.EventInjected)
			require.Len(t, injected, 1)
			require.Equal(t, emitted, injected[0].Payload["content"],
				"telemetry content must be byte-for-byte the response content")
			require.Equal(t, tt.hook, injected[0].Payload["hook"])
			require.Equal(t, true, injected[0].Payload["truncated"])
			require.Equal(t, float64(retrieve.EstimateTokens(tt.content)),
				injected[0].Payload["original_estimated_tokens"])
			require.Equal(t, float64(retrieve.EstimateTokens(emitted)),
				injected[0].Payload["emitted_estimated_tokens"])
			require.Equal(t, false, injected[0].Payload["item_ids_exact"])
			// Truncation makes the list a superset, not a reason to drop it:
			// item_ids is the only source of last_injected_at, so discarding it
			// would make an injected memory read as stale-and-archivable.
			require.Equal(t, []any{"01A"}, injected[0].Payload["item_ids"])
			require.Equal(t, float64(1), injected[0].Payload["original_item_count"])
		})
	}
}

func TestSessionStart_CodexCapsPinnedContextAfterAmbientLine(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "seam.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db,
		store.SettingRepoProjectMap, `{"/work/demo":"demo"}`))

	for i := range 25 {
		insertMemory(t, db, fmt.Sprintf("01C%03d", i), "constraint",
			fmt.Sprintf("pinned-constraint-%03d", i), strings.Repeat("bounded pinned detail ", 12), "demo")
	}
	now := time.Now().UTC()
	for i := range 80 {
		require.NoError(t, store.CreateTask(ctx, db, core.Task{
			ID:          fmt.Sprintf("01T%03d", i),
			ProjectSlug: "demo",
			Title:       fmt.Sprintf("plan step %03d", i),
			Status:      core.TaskOpen,
			PlanSlug:    fmt.Sprintf("codex-context-plan-%03d-%s", i, strings.Repeat("x", 32)),
			CreatedAt:   now,
			UpdatedAt:   now,
		}))
	}

	ret := retrieve.New(db, nil, config.Budgets{
		MaxBriefingTokens:  1500,
		RecallBudgetTokens: 1000,
	}, nil)
	h := NewHandler(Config{
		DB: db, Retrieve: ret, Events: events.NewRecorder(db), APIKey: testKey,
	})
	mux := http.NewServeMux()
	h.Register(mux)
	body, err := json.Marshal(map[string]any{
		"session_id": "019f7291-40f1-7311-8997-0d497579d27b",
		"cwd":        "/work/demo",
		"source":     "startup",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost,
		"/api/hooks/session-start?client=codex", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var response hookResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&response))
	require.NotNil(t, response.HookSpecificOutput)
	emitted := response.HookSpecificOutput.AdditionalContext
	require.LessOrEqual(t, retrieve.EstimateTokens(emitted), codexContextMaxTokens)
	require.Contains(t, emitted, "CONSTRAINT: pinned-constraint-000")
	require.Contains(t, emitted, "PLAN: codex-context-plan-000")
	require.Contains(t, emitted, "Seam session: cx/019f7291-")
	require.True(t, strings.HasSuffix(emitted, "</seam-briefing>"))

	injected := eventsOfKind(t, events.NewRecorder(db), core.EventInjected)
	require.Len(t, injected, 1)
	require.Equal(t, emitted, injected[0].Payload["content"])
	require.Equal(t, true, injected[0].Payload["truncated"])
	require.Equal(t, false, injected[0].Payload["item_ids_exact"])
	// The truncated briefing still credits the constraints it injected, so they
	// do not accumulate a null last_injected_at and become archive proposals.
	require.NotEmpty(t, injected[0].Payload["item_ids"])
	require.Greater(t, injected[0].Payload["original_estimated_tokens"].(float64),
		float64(codexContextMaxTokens))
}
