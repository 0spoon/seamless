package mcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// The MCP input-boundary matrix: what the server does with an argument it cannot
// read the way the caller meant it.
//
// The rule under test, from argspec.go: absent -> default; present but
// uninterpretable -> error. Before validateMiddleware, GetString/GetInt
// collapsed absent, wrong-type, and typo'd-name into the same zero value, so
// every case below produced a confident, plausible, wrong answer instead of a
// fix -- tasks_add{despends_on:...} created a task with no blockers and reported
// success. These tests exist because that failure is invisible: nothing errors,
// nothing logs, and the agent is told it worked.

func TestArgsRejectUnknownParam(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// A near-miss names the parameter the caller meant. This is the canonical
	// case: "despends_on" used to be dropped in silence, and the task came back
	// ready with no blockers.
	isErr, txt := callErr(t, ctx, cli, "tasks_add", map[string]any{"title": "t", "despends_on": "x"})
	require.True(t, isErr, "an unknown parameter must be rejected, not dropped")
	require.Contains(t, txt, `unknown parameter "despends_on"`)
	require.Contains(t, txt, `did you mean "depends_on"?`)
	require.Contains(t, txt, "valid parameters are:", "the error lists what the tool accepts")

	// A case-only difference resolves to the declared name.
	isErr, txt = callErr(t, ctx, cli, "recall", map[string]any{"query": "q", "Project": "demo"})
	require.True(t, isErr)
	require.Contains(t, txt, `did you mean "project"?`)

	// Nothing close enough: rejected, but with no misleading suggestion.
	isErr, txt = callErr(t, ctx, cli, "notes_create", map[string]any{"title": "t", "body": "b", "zzz": "1"})
	require.True(t, isErr)
	require.Contains(t, txt, `unknown parameter "zzz"`)
	require.NotContains(t, txt, "did you mean", "a far-off name must not get a made-up suggestion")
}

func TestArgsRejectWrongType(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// A number title is not silently rendered as "42". Before this, it became ""
	// and the handler answered "title is required" -- a lie about which of the two
	// problems occurred.
	isErr, txt := callErr(t, ctx, cli, "tasks_add", map[string]any{"title": 42})
	require.True(t, isErr)
	require.Contains(t, txt, "invalid title: expected a string, got number")
	require.NotContains(t, txt, "title is required", "the error must name the real problem")

	isErr, txt = callErr(t, ctx, cli, "recall", map[string]any{"query": "q", "limit": "abc"})
	require.True(t, isErr)
	require.Contains(t, txt, `invalid limit: expected a number, got "abc"`)

	isErr, txt = callErr(t, ctx, cli, "trial_record", map[string]any{
		"title": "t", "lab": "L", "metrics": "not json",
	})
	require.True(t, isErr)
	require.Contains(t, txt, "invalid metrics: expected a JSON object")
}

// TestToolArgsCoerceLegacyForms is the back-compat pin. The strictness above
// must not break the forms this repo's own clients emit: seam sends depends_on
// as CSV and lease_seconds as a numeric string, and an agent that stringifies
// its arguments sends metrics as a JSON string. Each must mean exactly what its
// native form means -- not merely "also work".
//
// argspec_test's TestArgsCoerceLegacyForms pins the same equivalence on
// normalizeArgs directly. This one pins it through a real tool call, which is
// what catches the half of the bug a unit test cannot see: a schema and a
// handler that each look right but read the parameter as different types.
func TestToolArgsCoerceLegacyForms(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// The seam CLI shape: every optional param sent, blank (cmd/seam/task.go:88).
	// Blank must read as absent, or every CLI-created task would carry an empty
	// blocker and a blank plan.
	cliShape := callJSON(t, ctx, cli, "tasks_add", map[string]any{
		"title": "cli shape", "body": "", "project": "", "depends_on": "", "plan": "",
	})
	require.Nil(t, cliShape["depends_on"], `depends_on:"" is absent, not a blocker`)

	b1 := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "blocker one"})
	b2 := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "blocker two"})
	id1, _ := b1["id"].(string)
	id2, _ := b2["id"].(string)
	require.NotEmpty(t, id1)
	require.NotEmpty(t, id2)

	// THE pin: the CSV and array forms are the same call. This is the assertion
	// that proves the original bug is dead -- an array used to yield zero blockers.
	viaCSV := callJSON(t, ctx, cli, "tasks_add", map[string]any{
		"title": "via csv", "depends_on": id1 + "," + id2,
	})
	viaArray := callJSON(t, ctx, cli, "tasks_add", map[string]any{
		"title": "via array", "depends_on": []any{id1, id2},
	})
	// ElementsMatch, not Equal: dependsOnOf returns deps ordered by ULID (ORDER BY
	// depends_on ASC), and core.NewID is not monotonic within a millisecond, so
	// id1/id2's relative ULID order is random -- asserting a fixed order flakes. The
	// pin is that BOTH ids survive (the bug yielded zero); the CSV-equals-array
	// assertion below carries the order-stability guarantee.
	require.ElementsMatch(t, []any{id1, id2}, viaArray["depends_on"], "an array of ids must survive")
	require.Equal(t, viaCSV["depends_on"], viaArray["depends_on"], "CSV and array must be identical")

	// Whitespace and blanks normalize the same across both forms, so the legacy
	// path is lossless rather than a second, subtly different parse.
	viaMessyCSV := callJSON(t, ctx, cli, "tasks_add", map[string]any{
		"title": "messy csv", "depends_on": " " + id1 + " , , " + id2 + " ",
	})
	require.Equal(t, viaArray["depends_on"], viaMessyCSV["depends_on"])

	// lease_seconds: "60" and 60 mean the same 60 -- and neither is the 900
	// default, which is what a silently-dropped number used to produce.
	leaseOf := func(taskID string, lease any) time.Duration {
		t.Helper()
		claimed := callJSON(t, ctx, cli, "tasks_claim", map[string]any{"id": taskID, "lease_seconds": lease})
		require.Equal(t, "in_progress", claimed["status"])
		exp, err := time.Parse(time.RFC3339Nano, claimed["lease_expires_at"].(string))
		require.NoError(t, err)
		return time.Until(exp)
	}
	fromString := leaseOf(id1, "60")
	fromNumber := leaseOf(id2, 60)
	require.InDelta(t, fromString.Seconds(), fromNumber.Seconds(), 5,
		`lease_seconds:"60" and lease_seconds:60 must mean the same lease`)
	require.Less(t, fromNumber.Seconds(), 120.0, "60 must not silently become the 900s default")

	// metrics: the object and its stringified form record identically, and a
	// filter finds both. A dropped metrics used to record the trial with none.
	callJSON(t, ctx, cli, "lab_open", map[string]any{"lab": "L"})
	viaObject := callJSON(t, ctx, cli, "trial_record", map[string]any{
		"title": "metrics via object", "metrics": map[string]any{"hz": 497},
	})
	viaString := callJSON(t, ctx, cli, "trial_record", map[string]any{
		"title": "metrics via string", "metrics": `{"hz":497}`,
	})
	require.Equal(t, map[string]any{"hz": float64(497)}, viaObject["metrics"])
	require.Equal(t, viaObject["metrics"], viaString["metrics"])

	found := callJSON(t, ctx, cli, "trial_query", map[string]any{"metrics_filter": map[string]any{"hz": 497}})
	require.Len(t, found["trials"], 2, "an object metrics_filter must match both recorded forms")

	// tags: CSV and array agree (created-by:agent is appended either way).
	tagsOf := func(title string, tags any) []any {
		t.Helper()
		n := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": title, "body": "b", "tags": tags})
		return callJSON(t, ctx, cli, "notes_read", map[string]any{"id": n["id"]})["tags"].([]any)
	}
	require.Equal(t, tagsOf("csv tags", "a,b"), tagsOf("array tags", []any{"a", "b"}))
	require.ElementsMatch(t, []any{"a", "b", "created-by:agent"}, tagsOf("csv tags 2", "a,b"))
}

func TestArgsRejectUncoercible(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{
			"a number is neither an array nor a CSV string",
			"tasks_add", map[string]any{"title": "t", "depends_on": 5},
			"invalid depends_on: expected an array of strings or a comma-separated string, got number",
		},
		{
			"a bad element names its index, not just the parameter",
			"tasks_add", map[string]any{"title": "t", "depends_on": []any{"a", 2}},
			"invalid depends_on[1]: expected a string, got number",
		},
		{
			"a JSON array is not a JSON object",
			"trial_record", map[string]any{"title": "t", "lab": "L", "metrics": "[]"},
			"invalid metrics: expected a JSON object",
		},
		{
			"a number is not an object",
			"trial_record", map[string]any{"title": "t", "lab": "L", "metrics": 5},
			"invalid metrics: expected an object, got number",
		},
		{
			"the literal null string is not an empty object",
			"trial_record", map[string]any{"title": "t", "lab": "L", "metrics": "null"},
			"invalid metrics: expected a JSON object",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isErr, txt := callErr(t, ctx, cli, tc.tool, tc.args)
			require.True(t, isErr, "%s: must be rejected", tc.name)
			require.Contains(t, txt, tc.want, txt)
		})
	}
}

func TestArgsEnforceEnums(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	task := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "enum subject"})
	taskID, _ := task["id"].(string)

	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{
			// Used to hit scopeKinds' bare default: both kinds searched, success reported.
			"a typo'd scope does not silently search everything",
			"recall", map[string]any{"query": "q", "scope": "memoires"},
			"valid values are all, memories, notes",
		},
		{
			"a hyphen is not an underscore",
			"tasks_update", map[string]any{"id": taskID, "status": "in-progress"},
			"valid values are open, in_progress, done, dropped",
		},
		{
			"a typo'd memory kind is rejected",
			"memory_write", map[string]any{
				"name": "k", "kind": "gotchya", "description": "d", "body": "b", "project": "global",
			},
			"valid values are constraint, runbook, protocol, gotcha, decision, refuted, reference, stage",
		},
		{
			// Used to become SQL `AND kind = 'merged'` -> {"proposals":[],"count":0}
			// as SUCCESS, indistinguishable from a real empty result.
			"a typo'd proposal kind is not an empty result",
			"gardener_proposals", map[string]any{"kind": "merged"},
			"valid values are merge, archive, digest, consolidate, reproject, split, abandon_plan",
		},
		{
			// briefing.go branches on "compact"/"resume", so a near-miss silently
			// produced the wrong briefing shape and stored garbage.
			"a near-miss session source is rejected",
			"session_start", map[string]any{"cwd": "/work/demo", "source": "compacted"},
			"valid values are startup, resume, clear, compact, explicit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isErr, txt := callErr(t, ctx, cli, tc.tool, tc.args)
			require.True(t, isErr, "%s: must be rejected", tc.name)
			require.Contains(t, txt, tc.want, txt)
		})
	}

	// The drift-regression pin: abandon_plan is real everywhere (migration 005,
	// the gardener, the applier, the console) and always worked, but the
	// hand-written enum omitted it. Enforcing THAT list would have broken this.
	isErr, txt := callErr(t, ctx, cli, "gardener_proposals", map[string]any{"kind": "abandon_plan"})
	require.False(t, isErr, "abandon_plan must stay queryable: %s", txt)

	// Every canonical source still starts a session (the enum enforces, it does
	// not narrow), and an absent source still defaults.
	for _, src := range append(append([]string{}, core.SessionSources...), "") {
		args := map[string]any{"cwd": "/work/demo"}
		if src != "" {
			args["source"] = src
		}
		isErr, txt := callErr(t, ctx, dialClient(t, ctx, url, testKey), "session_start", args)
		require.False(t, isErr, "source %q must be accepted: %s", src, txt)
	}
}

// TestToolArgsAliasGroup is argspec_test's TestArgsAliasGroup through a real tool
// call: the aliases have to survive the schema's required check and reach the
// handler under the canonical name, which is where they were resolved four
// different ways before.
func TestToolArgsAliasGroup(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// An alias satisfies a required parameter: the required check must run on the
	// canonical name AFTER the collapse, or memory_write{content:...} -- ~11% of
	// real agent tool failures -- would report "body is required" while holding it.
	callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "aliased-write", "kind": "reference", "description": "d",
		"content": "via the content alias", "project": "global",
	})
	r := callJSON(t, ctx, cli, "memory_read", map[string]any{"name": "aliased-write", "project": "global"})
	require.Contains(t, r["body"], "via the content alias")

	note := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "alias note", "body": "seed"})
	noteID, _ := note["id"].(string)
	isErr, txt := callErr(t, ctx, cli, "notes_append", map[string]any{"id": noteID, "content": "appended via content"})
	require.False(t, isErr, txt)

	// Two names, one value: agreement collapses.
	isErr, txt = callErr(t, ctx, cli, "notes_append", map[string]any{
		"id": noteID, "body": "same", "content": "same",
	})
	require.False(t, isErr, "aliases that agree must collapse: %s", txt)

	// Two names, two values: neither may be silently picked. The helpers this
	// replaced disagreed about which one won.
	isErr, txt = callErr(t, ctx, cli, "notes_append", map[string]any{
		"id": noteID, "body": "a", "content": "b",
	})
	require.True(t, isErr, "conflicting aliases must be rejected, not resolved by luck")
	require.Contains(t, txt, "conflicting values for body")
	require.Contains(t, txt, "pass exactly one of body, content, text")

	// An empty body is a real value on a free-form string: it blanks the field.
	// (Only an interpreted type -- enum, array, number, object -- reads "" as absent.)
	callJSON(t, ctx, cli, "notes_update", map[string]any{"id": noteID, "body": ""})
	r = callJSON(t, ctx, cli, "notes_read", map[string]any{"id": noteID})
	require.Equal(t, "", r["body"], `body:"" must blank the body, not be dropped as absent`)

	// The aliases belong to the property, not to the server: recall has no body
	// concept, so "content" is simply unknown there.
	isErr, txt = callErr(t, ctx, cli, "recall", map[string]any{"query": "q", "content": "x"})
	require.True(t, isErr)
	require.Contains(t, txt, `unknown parameter "content"`)
}

func TestArgsMissingRequired(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	isErr, txt := callErr(t, ctx, cli, "recall", map[string]any{})
	require.True(t, isErr)
	require.Contains(t, txt, `missing required parameter "query"`)

	// A required parameter with aliases names all of them, so a caller that sent
	// none of the accepted names learns every one it could have used.
	note := callJSON(t, ctx, cli, "notes_create", map[string]any{"title": "req note", "body": "seed"})
	isErr, txt = callErr(t, ctx, cli, "notes_append", map[string]any{"id": note["id"]})
	require.True(t, isErr)
	require.Contains(t, txt, `missing required parameter "body" (aliases: content, text)`)

	// An empty enum/array/number reads as absent, so a required one sent blank
	// reports itself missing rather than invalid.
	isErr, txt = callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "n", "kind": "", "description": "d", "body": "b", "project": "global",
	})
	require.True(t, isErr)
	require.Contains(t, txt, `missing required parameter "kind"`)
}

func TestArgsLimitZeroRejected(t *testing.T) {
	ctx := context.Background()
	url, _ := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	// limit:0 is PRESENT. There is no reading of "zero" under which "here are ten"
	// is the right answer, so the boundary refuses rather than clamping.
	for _, lim := range []any{0, -5} {
		isErr, txt := callErr(t, ctx, cli, "recall", map[string]any{"query": "q", "limit": lim})
		require.True(t, isErr, "limit %v must be rejected", lim)
		require.Contains(t, txt, "invalid limit: must be >= 1", txt)
	}
	isErr, txt := callErr(t, ctx, cli, "trial_query", map[string]any{"lab": "L", "limit": 0})
	require.True(t, isErr)
	require.Contains(t, txt, "invalid limit: must be >= 1")

	// Absent still means the library default -- the clamp stays where a zero value
	// legitimately means "field unset".
	isErr, txt = callErr(t, ctx, cli, "recall", map[string]any{"query": "q"})
	require.False(t, isErr, txt)
}

// TestValidationRejectionIsLogged pins two decisions at once: validate sits
// INSIDE log (so a rejected call is still evidence in the Interactions feed), and
// it REPLACES the arguments rather than mutating them (so the feed shows what the
// agent actually sent). If the feed showed normalized arguments, "this agent
// still emits CSV" would be undiagnosable from it.
func TestValidationRejectionIsLogged(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)
	callJSON(t, ctx, cli, "session_start", map[string]any{"cwd": "/work/demo", "source": "startup"})

	isErr, _ := callErr(t, ctx, cli, "tasks_add", map[string]any{"title": "t", "despends_on": "x"})
	require.True(t, isErr)

	e := findToolCall(t, db, "tasks_add")
	require.Equal(t, true, e.Payload["is_error"], "a rejected call is still logged")
	require.Contains(t, e.Payload["error"], "unknown parameter")

	args, ok := e.Payload["args"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "x", args["despends_on"], "the feed records the RAW args the agent sent")
}

// TestValidationSkippedWhenUnauthorized pins the middleware order from the other
// side: auth is outermost, so a bad key gets no schema feedback -- an unauthorized
// caller must not be able to probe the tools' parameters through error messages.
func TestValidationSkippedWhenUnauthorized(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, "wrong-key")

	isErr, txt := callErr(t, ctx, cli, "tasks_add", map[string]any{"despends_on": 5, "zzz": "garbage"})
	require.True(t, isErr)
	require.Contains(t, txt, "unauthorized")
	require.NotContains(t, txt, "unknown parameter", "an unauthorized caller learns nothing about the schema")
	require.NotContains(t, txt, "valid parameters are")

	require.Empty(t, toolCallEvents(t, db), "an unauthorized call is not logged")
}
