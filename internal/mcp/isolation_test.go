package mcp_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
	mcpserver "github.com/0spoon/seamless/internal/mcp"
	"github.com/0spoon/seamless/internal/retrieve"
	"github.com/0spoon/seamless/internal/store"
)

// isolate registers a project and sets its isolation state, the state the
// console's Overview control writes.
func isolate(t *testing.T, ctx context.Context, db *sql.DB, slug string, state core.Isolation) {
	t.Helper()
	_, err := store.EnsureProject(ctx, db, slug, slug)
	require.NoError(t, err)
	require.NoError(t, store.SetProjectIsolation(ctx, db, slug, state))
}

// bindClient dials a connection and binds it to project through a named
// session -- the cwd-free equivalent of an agent working in a mapped repo, and
// the only thing the fence accepts as proof of which project a caller is in.
// The seeded session is deliberately NOT ambient, so it cannot also become some
// other connection's ambient fallback.
func bindClient(t *testing.T, ctx context.Context, db *sql.DB, url, project string) *mcpclient.Client {
	t.Helper()
	id, err := core.NewID()
	require.NoError(t, err)
	name := "sess/" + project
	now := time.Now().UTC()
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: id, Name: name, ProjectSlug: project, Status: core.SessionActive,
		CreatedAt: now, UpdatedAt: now,
	}))
	cli := dialClient(t, ctx, url, testKey)
	start := callJSON(t, ctx, cli, "session_start", map[string]any{"name": name})
	require.Equal(t, project, start["project"])
	return cli
}

// createArgs returns one call per durable create, targeting project. capture_url
// is included: its scope resolves before the SSRF fetch, so a refusal never
// reaches the network (which is also why only its rejection is exercised here).
func createArgs(project, tag string) []struct {
	tool string
	args map[string]any
} {
	return []struct {
		tool string
		args map[string]any
	}{
		{"memory_write", map[string]any{
			"name": "fenced-" + tag, "kind": "reference", "description": "d",
			"body": "b\n", "project": project,
		}},
		{"notes_create", map[string]any{"title": "note " + tag, "body": "b", "project": project}},
		{"tasks_add", map[string]any{"title": "task " + tag, "project": project}},
		{"capture_url", map[string]any{"url": "https://example.com/" + tag, "project": project}},
		{"trial_record", map[string]any{"title": "trial " + tag, "lab": "fence-lab", "project": project}},
	}
}

// byIDMutations returns one call per by-id MUTATION of an existing note or
// task. These are the paths that resolve no write scope at all -- the item's
// ULID is the whole address -- so the fence is the only thing between an outside
// caller and another project's note or task. notes_delete is last because it
// destroys the target the earlier calls need.
func byIDMutations(noteID, taskID string) []struct {
	tool string
	args map[string]any
} {
	return []struct {
		tool string
		args map[string]any
	}{
		{"notes_update", map[string]any{"id": noteID, "description": "edited from outside"}},
		{"notes_append", map[string]any{"id": noteID, "body": "appended from outside"}},
		{"tasks_update", map[string]any{"id": taskID, "title": "retitled from outside"}},
		{"notes_delete", map[string]any{"id": noteID}},
	}
}

// TestWriteFenceByIDSealedRefusesConfidentialAdmits is the asymmetry itself, on
// the by-id write paths: a sealed project admits nothing from outside, while a
// confidential one stays writable from outside because relocating knowledge INTO
// the fence -- and maintaining it once there -- is how it gets there.
//
// Before this fence a ULID was the whole address: an outside caller could edit,
// append to, or outright DELETE a note in a sealed project.
func TestWriteFenceByIDSealedRefusesConfidentialAdmits(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)

	seed := func(project string) (*mcpclient.Client, string, string) {
		cli := bindClient(t, ctx, db, url, project)
		noteID := callJSON(t, ctx, cli, "notes_create", map[string]any{
			"title": "Fenced note in " + project, "body": "the client's deployment topology",
		})["id"].(string)
		taskID := callJSON(t, ctx, cli, "tasks_add", map[string]any{"title": "fenced task in " + project})["id"].(string)
		return cli, noteID, taskID
	}
	vault, vaultNote, vaultTask := seed("vault")
	_, clientNote, clientTask := seed("client-work")

	outside := bindClient(t, ctx, db, url, "demo")

	for _, tc := range byIDMutations(vaultNote, vaultTask) {
		isErr, txt := callErr(t, ctx, outside, tc.tool, tc.args)
		require.True(t, isErr, "%s by id must be refused against a sealed project", tc.tool)
		require.Contains(t, txt, "project vault is sealed: writes into it require a session bound to it",
			"%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "deployment topology",
			"%s must not echo the content it withheld: %s", tc.tool, txt)
	}
	// Nothing landed: the note still exists, unedited, and the task keeps its title.
	still := callJSON(t, ctx, vault, "notes_read", map[string]any{"id": vaultNote})
	require.Contains(t, still["body"], "the client's deployment topology")
	require.NotContains(t, still["body"], "appended from outside", "a refused append must not have landed")
	require.Empty(t, still["description"], "a refused update must not have landed")
	task := callJSON(t, ctx, vault, "tasks_list", map[string]any{"id": vaultTask})["tasks"].([]any)[0].(map[string]any)
	require.Equal(t, "fenced task in vault", task["title"])

	// The other side of the asymmetry: the same four calls land on a confidential
	// project, which fences outbound only.
	for _, tc := range byIDMutations(clientNote, clientTask) {
		out := callJSON(t, ctx, outside, tc.tool, tc.args)
		require.NotEmpty(t, out["id"], "%s must still land on a confidential project", tc.tool)
	}
}

// withheldMarker reads the trim marker off a success payload: the reason copy
// and the field names policy removed. It fails the test when the marker is
// missing, because "no marker" and "nothing withheld" must never look alike.
func withheldMarker(t *testing.T, out map[string]any) (reason string, fields []string) {
	t.Helper()
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	w, ok := out["withheld"].(map[string]any)
	require.True(t, ok, "response carries no withheld marker: %s", raw)
	reason, ok = w["reason"].(string)
	require.True(t, ok, "withheld marker carries no reason: %s", raw)
	list, ok := w["fields"].([]any)
	require.True(t, ok, "withheld marker names no fields: %s", raw)
	for _, f := range list {
		name, ok := f.(string)
		require.True(t, ok, "withheld field is not a name: %s", raw)
		fields = append(fields, name)
	}
	return reason, fields
}

// TestWriteFenceConfidentialResponseWithholdsContent closes the leak the write
// fence alone cannot: a confidential project takes writes from outside on
// purpose, and those writes answer with the item they just touched -- so a ULID
// plus an edit any outside caller is entitled to make WAS a read of the fenced
// task's body. The write still lands; only the answer is trimmed.
func TestWriteFenceConfidentialResponseWithholdsContent(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)

	inside := bindClient(t, ctx, db, url, "client-work")
	noteID := callJSON(t, ctx, inside, "notes_create", map[string]any{
		"title": "Deployment topology", "body": "the client's deployment topology",
	})["id"].(string)
	taskID := callJSON(t, ctx, inside, "tasks_add", map[string]any{
		"title": "rotate the client's keys", "body": "the client's deployment topology",
	})["id"].(string)

	outside := bindClient(t, ctx, db, url, "demo")
	for _, tc := range []struct {
		tool string
		args map[string]any
		// content names the fields the marker must claim, and that the payload
		// must not carry. tasks_update passes a title of its own: the trim is
		// uniform rather than clever about which text the caller already knew,
		// because a rule that returns SOME titles is one an agent cannot reason
		// about -- and getting it wrong returns the note's title, not the edit's.
		content []string
	}{
		{"notes_update", map[string]any{"id": noteID, "description": "edited from outside"}, []string{"title"}},
		{"notes_append", map[string]any{"id": noteID, "body": "appended from outside"}, []string{"title"}},
		{"tasks_update", map[string]any{"id": taskID, "title": "retitled from outside"}, []string{"title", "body"}},
		{"tasks_claim", map[string]any{"id": taskID}, []string{"title", "body"}},
		{"tasks_release", map[string]any{"id": taskID}, []string{"title", "body"}},
	} {
		out := callJSON(t, ctx, outside, tc.tool, tc.args)
		raw, err := json.Marshal(out)
		require.NoError(t, err)

		// The asymmetry is intact: the write landed and the caller keeps its handle.
		require.Equal(t, tc.args["id"], out["id"], "%s must still land on a confidential project: %s", tc.tool, raw)
		require.NotContains(t, string(raw), "deployment topology",
			"%s answered with the content it must withhold: %s", tc.tool, raw)
		require.NotContains(t, string(raw), "rotate the client's keys",
			"%s answered with the content it must withhold: %s", tc.tool, raw)

		reason, named := withheldMarker(t, out)
		require.Contains(t, reason, "project client-work is confidential", "%s: %s", tc.tool, raw)
		require.Contains(t, reason, "the write landed", "%s must not read as a failed write: %s", tc.tool, raw)
		for _, f := range tc.content {
			require.Contains(t, named, f, "%s: the marker must name %q: %s", tc.tool, f, raw)
			require.NotContains(t, out, f, "%s: %q must be absent, not empty: %s", tc.tool, f, raw)
		}
	}

	// Every one of those writes really happened -- the trim is about the answer,
	// never about the effect.
	still := callJSON(t, ctx, inside, "notes_read", map[string]any{"id": noteID})
	require.Equal(t, "edited from outside", still["description"])
	require.Contains(t, still["body"], "appended from outside")
	task := callJSON(t, ctx, inside, "tasks_list", map[string]any{"id": taskID})["tasks"].([]any)[0].(map[string]any)
	require.Equal(t, "retitled from outside", task["title"])
	require.Equal(t, "open", task["status"], "the outside claim was released again")

	// Sealed is untouched by all of this: it refuses the write outright rather
	// than answering with a trimmed success. Trimming is for writes that landed.
	isolate(t, ctx, db, "vault", core.IsolationSealed)
	vault := bindClient(t, ctx, db, url, "vault")
	vaultNote := callJSON(t, ctx, vault, "notes_create", map[string]any{
		"title": "Vault note", "body": "the client's deployment topology",
	})["id"].(string)
	isErr, txt := callErr(t, ctx, outside, "notes_append", map[string]any{"id": vaultNote, "body": "x"})
	require.True(t, isErr, "a sealed project must still refuse the write outright")
	require.Contains(t, txt, "project vault is sealed: writes into it require a session bound to it", txt)
	require.NotContains(t, txt, "withheld", "a refusal is a refusal, not a trimmed success: %s", txt)
}

// TestWriteFenceOpenProjectResponseUnchanged pins the other side byte for byte:
// the trim must be invisible to everyone it is not aimed at, so an open
// project's caller sees exactly the payload it saw before the fence existed.
// Asserted as whole-payload equality rather than a spot check, because a stray
// extra key is precisely what an unconditional marker would add.
func TestWriteFenceOpenProjectResponseUnchanged(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)

	owner := bindClient(t, ctx, db, url, "open-proj")
	noteID := callJSON(t, ctx, owner, "notes_create", map[string]any{
		"title": "Open note", "body": "open body",
	})["id"].(string)
	taskID := callJSON(t, ctx, owner, "tasks_add", map[string]any{
		"title": "open task", "body": "task body",
	})["id"].(string)

	outside := bindClient(t, ctx, db, url, "demo")

	require.Equal(t, map[string]any{"id": noteID, "title": "Open note"},
		callJSON(t, ctx, outside, "notes_update", map[string]any{"id": noteID, "description": "one line"}))
	require.Equal(t, map[string]any{"id": noteID, "title": "Open note"},
		callJSON(t, ctx, outside, "notes_append", map[string]any{"id": noteID, "body": "appended"}))

	open := map[string]any{
		"id": taskID, "title": "retitled", "body": "task body", "status": "open",
		"project": "open-proj", "created_by": "sess/open-proj",
	}
	require.Equal(t, open,
		callJSON(t, ctx, outside, "tasks_update", map[string]any{"id": taskID, "title": "retitled"}))

	claim := callJSON(t, ctx, outside, "tasks_claim", map[string]any{"id": taskID})
	// The holder id and the lease stamp are this call's own output (a fresh ULID
	// and a wall clock), so they are asserted present and dropped before the
	// exact comparison rather than hard-coded.
	require.NotEmpty(t, claim["claimed_by"])
	require.NotEmpty(t, claim["lease_expires_at"])
	delete(claim, "claimed_by")
	delete(claim, "lease_expires_at")
	claimed := map[string]any{
		"id": taskID, "title": "retitled", "body": "task body", "status": "in_progress",
		"project": "open-proj", "created_by": "sess/open-proj",
	}
	require.Equal(t, claimed, claim)
	require.Equal(t, open, callJSON(t, ctx, outside, "tasks_release", map[string]any{"id": taskID}))
}

// stubEmbedder answers every text with one fixed vector, so a seeded memory is a
// perfect cosine match for any proposed write. memory_write's dedup hint needs a
// model to fire at all, and the leak below needs the hint.
type stubEmbedder struct{}

func (stubEmbedder) Model() string { return "test-embed" }

func (stubEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0, 0}, nil }

// TestWriteFenceMemoryWriteWithholdsDedupHint covers the leak that was not on
// the by-id list: memory_write answers a NEW name with the nearest existing
// memory -- its name and its description, the one line every index shows. A
// confidential project takes writes from outside, so an outsider could write
// throwaway memories into it and harvest what is already there, one probe per
// write, without ever being allowed to read the project.
func TestWriteFenceMemoryWriteWithholdsDedupHint(t *testing.T) {
	ctx := context.Background()
	url, db := newServerCfg(t, func(cfg *mcpserver.Config) {
		cfg.Retrieve = retrieve.New(cfg.DB, stubEmbedder{},
			config.Budgets{MaxBriefingTokens: 1500, RecallBudgetTokens: 1000}, nil)
	})
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)

	inside := bindClient(t, ctx, db, url, "client-work")
	memID := callJSON(t, ctx, inside, "memory_write", map[string]any{
		"name": "client-topology", "kind": "reference",
		"description": "the client's deployment topology", "body": "b\n",
	})["id"].(string)
	// The files layer has no embedder in tests, so the vector is seeded directly.
	require.NoError(t, store.UpsertEmbedding(ctx, db, memID, "memory", "test-embed", []float32{1, 0, 0}))

	// Control first: the hint fires for the project's own agent. Without this the
	// assertion below would pass just as well against a payload that never
	// carried a hint at all.
	in := callJSON(t, ctx, inside, "memory_write", map[string]any{
		"name": "inside-second-memory", "kind": "reference", "description": "d", "body": "b\n",
	})
	sim, ok := in["similar"].(map[string]any)
	require.True(t, ok, "the dedup hint must fire inside, or this test proves nothing: %v", in)
	require.Equal(t, "client-topology", sim["name"])
	require.NotContains(t, in, "withheld", "a project's own agent gets its payload untrimmed")

	outside := bindClient(t, ctx, db, url, "demo")
	out := callJSON(t, ctx, outside, "memory_write", map[string]any{
		"name": "relocated-from-outside", "kind": "reference", "description": "d", "body": "b\n",
		"project": "client-work",
	})
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.Equal(t, "client-work", out["project"], "confidential still admits the write: %s", raw)
	require.NotEmpty(t, out["id"])
	require.Equal(t, false, out["updated"], "the write's own result stays: create vs. replace is the caller's action")
	require.NotContains(t, out, "similar")
	require.NotContains(t, string(raw), "deployment topology", "the hint's description leaked: %s", raw)
	require.NotContains(t, string(raw), "client-topology", "the hint's name leaked: %s", raw)

	reason, named := withheldMarker(t, out)
	require.Contains(t, reason, "project client-work is confidential")
	require.Contains(t, named, "similar")
}

// TestWriteFenceSessionFindings records the step-9 decision that a session is
// not merely caller identity. Findings are project knowledge -- they persist
// into the session's project and its future briefings -- so writing them through
// an explicit session_id is a write into THAT project, and takes the same fence
// a note or a task does. Both directions bite, and favorite_set kind=session
// already fenced on this exact project: overwriting a session's findings is
// strictly more consequential than starring it.
func TestWriteFenceSessionFindings(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)

	vault := bindClient(t, ctx, db, url, "vault")
	conf := bindClient(t, ctx, db, url, "client-work")
	outside := bindClient(t, ctx, db, url, "demo")

	sessionID := func(name string) string {
		sess, ok, err := store.SessionByName(ctx, db, name)
		require.NoError(t, err)
		require.True(t, ok)
		return sess.ID
	}
	vaultSession, confSession, demoSession := sessionID("sess/vault"), sessionID("sess/client-work"), sessionID("sess/demo")

	// Inbound into a sealed project: a briefing an outside agent can write into
	// is not a clean room, and an end would also release that project's claims.
	for _, tool := range []string{"session_update", "session_end"} {
		isErr, txt := callErr(t, ctx, outside, tool, map[string]any{
			"findings": "injected from outside", "session_id": vaultSession,
		})
		require.True(t, isErr, "%s must not reach a sealed project's session by id", tool)
		require.Contains(t, txt, "project vault is sealed: writes into it require a session bound to it",
			"%s: %s", tool, txt)
	}
	sess, ok, err := store.SessionByID(ctx, db, vaultSession)
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, sess.Findings, "a refused session_update must not have landed")
	require.Equal(t, core.SessionActive, sess.Status, "a refused session_end must not have completed the session")

	// Outbound: a confidential project's own agent may not park its findings in
	// another project's session -- project=global wearing a session id.
	isErr, txt := callErr(t, ctx, conf, "session_update", map[string]any{
		"findings": "the client's deployment topology", "session_id": demoSession,
	})
	require.True(t, isErr)
	require.Contains(t, txt,
		"this session is bound to confidential project client-work: writes outside it are disabled", txt)
	require.NotContains(t, txt, "deployment topology", txt)

	// The asymmetry holds here too: confidential admits inbound, so an outside
	// caller may still record findings on one of its sessions...
	upd := callJSON(t, ctx, outside, "session_update", map[string]any{
		"findings": "relocated from outside", "session_id": confSession,
	})
	require.Equal(t, confSession, upd["session_id"])
	// ... and there is nothing to trim on the way back: the session tools answer
	// with identity and counters, never with the findings they just stored.
	require.NotContains(t, upd, "findings")
	require.NotContains(t, upd, "withheld")

	// Each project's own agent is untouched by any of it.
	own := callJSON(t, ctx, vault, "session_update", map[string]any{"findings": "inside progress"})
	require.Equal(t, vaultSession, own["session_id"])
}

// TestWriteFenceByIDRelocationLeavesNothingBehind covers the direction the plain
// write fence cannot see: a project change is a read of the SOURCE plus a write
// into the target, so an outside caller may edit a confidential item in place
// (inbound is deliberately open) but may not carry it out of the fence.
func TestWriteFenceByIDRelocationLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)

	inside := bindClient(t, ctx, db, url, "client-work")
	noteID := callJSON(t, ctx, inside, "notes_create", map[string]any{
		"title": "Deployment topology", "body": "the client's deployment topology",
	})["id"].(string)
	taskID := callJSON(t, ctx, inside, "tasks_add", map[string]any{"title": "rotate the client's keys"})["id"].(string)

	outside := bindClient(t, ctx, db, url, "demo")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"notes_update", map[string]any{"id": noteID, "project": "demo"}},
		{"tasks_update", map[string]any{"id": taskID, "project": "demo"}},
	} {
		isErr, txt := callErr(t, ctx, outside, tc.tool, tc.args)
		require.True(t, isErr, "%s must not move an item out of a confidential project", tc.tool)
		require.Contains(t, txt, "project client-work is confidential: reads require a session bound to it",
			"%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "deployment topology", "%s: %s", tc.tool, txt)
	}

	// The in-place edit the move fence must not cost.
	upd := callJSON(t, ctx, outside, "notes_update", map[string]any{"id": noteID, "description": "one line"})
	require.Equal(t, noteID, upd["id"])

	// And the sanctioned direction: relocating an open project's note INTO the fence.
	openNote := callJSON(t, ctx, outside, "notes_create", map[string]any{"title": "Open note", "body": "b"})["id"].(string)
	moved := callJSON(t, ctx, outside, "notes_update", map[string]any{"id": openNote, "project": "client-work"})
	require.Equal(t, openNote, moved["id"])
}

// TestWriteFenceByIDClaimAndFavorite covers the by-id mutations beyond the four
// obvious ones. A claim locks a task to its holder AND returns its body, and a
// star pins a memory into briefings and boosts it in recall, so both are writes
// that a ULID alone must not buy across a fence.
func TestWriteFenceByIDClaimAndFavorite(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)

	inside := bindClient(t, ctx, db, url, "vault")
	noteID := callJSON(t, ctx, inside, "notes_create", map[string]any{
		"title": "Vault note", "body": "the client's deployment topology",
	})["id"].(string)
	taskID := callJSON(t, ctx, inside, "tasks_add", map[string]any{"title": "vault task"})["id"].(string)

	outside := bindClient(t, ctx, db, url, "demo")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"tasks_claim", map[string]any{"id": taskID}},
		{"tasks_release", map[string]any{"id": taskID}},
		{"favorite_set", map[string]any{"kind": "task", "id": taskID, "favorite": true}},
		{"favorite_set", map[string]any{"kind": "note", "id": noteID, "favorite": true}},
	} {
		isErr, txt := callErr(t, ctx, outside, tc.tool, tc.args)
		require.True(t, isErr, "%s must be refused across a sealed fence", tc.tool)
		require.Contains(t, txt, "project vault is sealed: writes into it require a session bound to it",
			"%s %v: %s", tc.tool, tc.args, txt)
		require.NotContains(t, txt, "deployment topology", "%s: %s", tc.tool, txt)
	}

	// Neither the lock nor the star landed, and the project's own agent is unaffected.
	task := callJSON(t, ctx, inside, "tasks_list", map[string]any{"id": taskID})["tasks"].([]any)[0].(map[string]any)
	require.Equal(t, "open", task["status"], "a refused claim must not have locked the task")
	require.Nil(t, task["favorite"])
	claimed := callJSON(t, ctx, inside, "tasks_claim", map[string]any{"id": taskID})
	require.Equal(t, "in_progress", claimed["status"])
}

// TestReadFenceSealedCallerHasNoGlobalFallback closes the implicit twin of
// project=global: every by-name lookup retries the global scope when the project
// scope misses, and that retry resolves no scope of its own -- so a sealed
// session read (and could delete) the global memories its fence exists to remove
// from its world, without ever naming the scope its explicit form is refused.
func TestReadFenceSealedCallerHasNoGlobalFallback(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)

	open := bindClient(t, ctx, db, url, "demo")
	callJSON(t, ctx, open, "memory_write", map[string]any{
		"name": "global-convention", "kind": "convention", "description": "d",
		"body": "the owner's machine-wide convention\n", "project": "global",
	})
	noteSlug := callJSON(t, ctx, open, "notes_create", map[string]any{
		"title": "Global Note", "body": "global note body", "project": "global",
	})["slug"].(string)

	// The fallback itself, unchanged for everyone it is not aimed at.
	require.Equal(t, "", callJSON(t, ctx, open, "memory_read", map[string]any{"name": "global-convention"})["project"])
	require.Equal(t, "", callJSON(t, ctx, open, "notes_read", map[string]any{"slug": noteSlug})["project"])

	sealed := bindClient(t, ctx, db, url, "vault")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_read", map[string]any{"name": "global-convention"}},
		{"memory_append", map[string]any{"name": "global-convention", "body": "x"}},
		{"memory_delete", map[string]any{"name": "global-convention"}},
		{"notes_read", map[string]any{"slug": noteSlug}},
		{"favorite_set", map[string]any{"kind": "memory", "id": "global-convention", "favorite": true}},
	} {
		isErr, txt := callErr(t, ctx, sealed, tc.tool, tc.args)
		require.True(t, isErr, "%s must not reach the global scope from a sealed session", tc.tool)
		require.Contains(t, txt, "this session is sealed, so the global scope is not searched",
			"%s: %s", tc.tool, txt)
		// A refusal must not offer the escape hatch the same fence closes, and the
		// message must not claim to have searched a scope it never touched.
		require.NotContains(t, txt, "project=global", "%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "also searched global", "%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "machine-wide convention", "%s: %s", tc.tool, txt)
	}

	// The global memory is untouched by the refused append and delete.
	r := callJSON(t, ctx, open, "memory_read", map[string]any{"name": "global-convention"})
	require.Equal(t, "the owner's machine-wide convention\n", r["body"])

	// Confidential is the deliberate contrast: it fences outbound only, so its
	// agents still inherit the owner's global knowledge.
	conf := bindClient(t, ctx, db, url, "client-work")
	require.Equal(t, "", callJSON(t, ctx, conf, "memory_read", map[string]any{"name": "global-convention"})["project"])
	require.Equal(t, "", callJSON(t, ctx, conf, "notes_read", map[string]any{"slug": noteSlug})["project"])

	// ... but a confidential session still may not WRITE to what it may read:
	// an append into the global scope is the outbound leak, not an inbound one.
	isErr, txt := callErr(t, ctx, conf, "memory_append", map[string]any{"name": "global-convention", "body": "x"})
	require.True(t, isErr)
	require.Contains(t, txt, "this session is bound to confidential project client-work: writes outside it are disabled", txt)
}

// TestProjectCreateIsolationRoundTrips is the agent-facing half of the surface:
// project_create takes the state at creation and project_list reports it, so an
// agent can both set a fence up front and discover one before a cross-project
// call. No new tool does either -- ToolCount is unchanged.
func TestProjectCreateIsolationRoundTrips(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := dialClient(t, ctx, url, testKey)

	for _, state := range core.Isolations {
		slug := "proj-" + string(state)
		out := callJSON(t, ctx, cli, "project_create", map[string]any{
			"name": "Project " + string(state), "slug": slug, "isolation": string(state),
		})
		require.Equal(t, string(state), out["isolation"])
		got, err := store.IsolationOf(ctx, db, slug)
		require.NoError(t, err)
		require.Equal(t, state, got, "the created project must hold the state it was given")
	}

	// Absent -> the default. Never a guess, and never blank.
	out := callJSON(t, ctx, cli, "project_create", map[string]any{"name": "Plain", "slug": "plain"})
	require.Equal(t, string(core.IsolationOpen), out["isolation"])

	// Present but uninterpretable -> an error naming the accepted values, and no
	// project: a rejected argument must not half-create the thing it named.
	isErr, txt := callErr(t, ctx, cli, "project_create", map[string]any{
		"name": "Bad", "slug": "bad-iso", "isolation": "private",
	})
	require.True(t, isErr)
	require.Contains(t, txt, `invalid isolation "private"`)
	require.Contains(t, txt, "open, confidential, sealed")
	_, found, err := store.ProjectBySlug(ctx, db, "bad-iso")
	require.NoError(t, err)
	require.False(t, found, "a rejected isolation must not create the project")

	// project_list carries every project's state, open included -- a missing
	// field would be indistinguishable from a server that does not fence at all.
	states := map[string]string{}
	for _, p := range callJSON(t, ctx, cli, "project_list", map[string]any{})["projects"].([]any) {
		row := p.(map[string]any)
		states[row["slug"].(string)] = row["isolation"].(string)
	}
	require.Equal(t, "sealed", states["proj-sealed"])
	require.Equal(t, "confidential", states["proj-confidential"])
	require.Equal(t, "open", states["proj-open"])
	require.Equal(t, "open", states["plain"])
}

// TestWriteFenceConfidentialCallerWritesNowhereElse is the outbound half of the
// fence at its single funnel: a session bound to a fenced project may write only
// into that project, so global, a sibling slug, and a brand-new slug are all
// refused across every durable create.
func TestWriteFenceConfidentialCallerWritesNowhereElse(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "secret", core.IsolationConfidential)
	_, err := store.EnsureProject(ctx, db, "other", "other")
	require.NoError(t, err)

	cli := bindClient(t, ctx, db, url, "secret")

	for _, target := range []string{"global", "other", "brand-new-slug"} {
		for _, tc := range createArgs(target, target) {
			isErr, txt := callErr(t, ctx, cli, tc.tool, tc.args)
			require.True(t, isErr, "%s into %q must be refused", tc.tool, target)
			require.Contains(t, txt,
				"this session is bound to confidential project secret: writes outside it are disabled",
				"%s into %q: %s", tc.tool, target, txt)
			// The scope help promises that an unknown slug creates that project.
			// That promise does not hold for a fenced caller, so the refusal must
			// not carry it (write-scope-registers-the-project-it-names).
			require.NotContains(t, txt, "CREATES that project", "%s into %q: %s", tc.tool, target, txt)
		}
	}

	// The refused new slug was never registered: the fence runs before the
	// auto-create, so a rejected write leaves no project behind.
	_, found, err := store.ProjectBySlug(ctx, db, "brand-new-slug")
	require.NoError(t, err)
	require.False(t, found, "a refused write must not register the project it named")
}

// TestWriteFenceConfidentialCallerWritesItsOwnProject pins the other side: the
// fence costs a fenced agent nothing inside its own project, whether it names it
// or inherits it from the binding.
func TestWriteFenceConfidentialCallerWritesItsOwnProject(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "secret", core.IsolationSealed)
	cli := bindClient(t, ctx, db, url, "secret")

	for _, tc := range createArgs("secret", "own") {
		if tc.tool == "capture_url" {
			continue // an accepted capture would hit the network
		}
		out := callJSON(t, ctx, cli, tc.tool, tc.args)
		require.NotEmpty(t, out["id"], "%s into its own project must succeed", tc.tool)
	}

	// The everyday path: no project argument at all, inherited from the binding.
	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "inherited", "kind": "reference", "description": "d", "body": "b\n",
	})
	require.Equal(t, "secret", w["project"])
}

// TestWriteFenceOpenCallerUnchanged verifies the fence is inert for everyone
// else: an open project's agent still writes to global, to a sibling, and to a
// new slug that registers itself.
func TestWriteFenceOpenCallerUnchanged(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	cli := bindClient(t, ctx, db, url, "demo")

	for _, target := range []string{"global", "other", "fresh-slug"} {
		for _, tc := range createArgs(target, target) {
			if tc.tool == "capture_url" {
				continue // an accepted capture would hit the network
			}
			out := callJSON(t, ctx, cli, tc.tool, tc.args)
			require.NotEmpty(t, out["id"], "%s into %q must still succeed", tc.tool, target)
		}
	}
	_, found, err := store.ProjectBySlug(ctx, db, "fresh-slug")
	require.NoError(t, err)
	require.True(t, found, "an open caller's new slug still registers")
}

// TestWriteFenceSealedTargetRefusesOutsideWrites covers the inbound half, and
// the deliberate asymmetry beside it: sealed admits nothing from outside, while
// confidential stays writable from outside -- relocating knowledge INTO the
// fence is how it gets there.
func TestWriteFenceSealedTargetRefusesOutsideWrites(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)
	isolate(t, ctx, db, "client-work", core.IsolationConfidential)
	cli := bindClient(t, ctx, db, url, "demo")

	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "outside-in", "kind": "reference", "description": "d",
		"body": "b\n", "project": "vault",
	})
	require.True(t, isErr)
	require.Contains(t, txt, "project vault is sealed: writes into it require a session bound to it", txt)

	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "relocated", "kind": "reference", "description": "d",
		"body": "b\n", "project": "client-work",
	})
	require.Equal(t, "client-work", w["project"], "confidential does not fence inbound")
}

// TestReadFenceExplicitProject covers the read funnel: naming a fenced project
// is exactly how a session bound elsewhere would read it, so an explicit
// project= is denied unless the caller IS that project.
func TestReadFenceExplicitProject(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "secret", core.IsolationConfidential)

	inside := bindClient(t, ctx, db, url, "secret")
	callJSON(t, ctx, inside, "memory_write", map[string]any{
		"name": "inside-knowledge", "kind": "reference",
		"description": "the client's deployment topology", "body": "body\n",
	})

	outside := bindClient(t, ctx, db, url, "demo")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_read", map[string]any{"name": "inside-knowledge", "project": "secret"}},
		{"recall", map[string]any{"query": "deployment topology", "project": "secret"}},
		{"tasks_list", map[string]any{"project": "secret"}},
		{"tasks_ready", map[string]any{"project": "secret"}},
		{"notes_read", map[string]any{"slug": "anything", "project": "secret"}},
	} {
		isErr, txt := callErr(t, ctx, outside, tc.tool, tc.args)
		require.True(t, isErr, "%s project=secret must be refused", tc.tool)
		require.Contains(t, txt, "project secret is confidential: reads require a session bound to it",
			"%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "deployment topology",
			"%s must not echo the content it withheld: %s", tc.tool, txt)
	}

	// The bound session reads its own project through the same funnel.
	r := callJSON(t, ctx, inside, "memory_read", map[string]any{
		"name": "inside-knowledge", "project": "secret",
	})
	require.Equal(t, "secret", r["project"])

	// An open project is unaffected: the outside caller still reads it by name.
	callJSON(t, ctx, outside, "memory_write", map[string]any{
		"name": "open-knowledge", "kind": "reference", "description": "d", "body": "b\n",
	})
	open := callJSON(t, ctx, inside, "memory_read", map[string]any{
		"name": "open-knowledge", "project": "demo",
	})
	require.Equal(t, "demo", open["project"], "a confidential caller still reads open projects")
}

// TestReadFenceByID closes the by-id bypass: a ULID is globally unique, so
// memory_read/notes_read/tasks_list by id resolve no scope at all and would
// otherwise hand any caller any project's item.
func TestReadFenceByID(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "secret", core.IsolationConfidential)

	inside := bindClient(t, ctx, db, url, "secret")
	memID := callJSON(t, ctx, inside, "memory_write", map[string]any{
		"name": "by-id-memory", "kind": "reference", "description": "d", "body": "secret body\n",
	})["id"].(string)
	noteID := callJSON(t, ctx, inside, "notes_create", map[string]any{
		"title": "By id note", "body": "secret body",
	})["id"].(string)
	taskID := callJSON(t, ctx, inside, "tasks_add", map[string]any{"title": "By id task"})["id"].(string)

	outside := bindClient(t, ctx, db, url, "demo")
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_read", map[string]any{"id": memID}},
		{"notes_read", map[string]any{"id": noteID}},
		{"tasks_list", map[string]any{"id": taskID}},
	} {
		isErr, txt := callErr(t, ctx, outside, tc.tool, tc.args)
		require.True(t, isErr, "%s by id must be refused across the fence", tc.tool)
		require.Contains(t, txt, "project secret is confidential: reads require a session bound to it",
			"%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "secret body", "%s leaked the content it refused: %s", tc.tool, txt)
	}

	// The bound session reads the same ids.
	require.Equal(t, memID, callJSON(t, ctx, inside, "memory_read", map[string]any{"id": memID})["id"])
	require.Equal(t, noteID, callJSON(t, ctx, inside, "notes_read", map[string]any{"id": noteID})["id"])
	require.Len(t, callJSON(t, ctx, inside, "tasks_list", map[string]any{"id": taskID})["tasks"], 1)

	// An open project's items stay readable by id from anywhere.
	openID := callJSON(t, ctx, outside, "memory_write", map[string]any{
		"name": "open-by-id", "kind": "reference", "description": "d", "body": "b\n",
	})["id"].(string)
	require.Equal(t, openID, callJSON(t, ctx, inside, "memory_read", map[string]any{"id": openID})["id"])
}

// TestTrialFenceAcrossLabs covers the one read that saw no project at all:
// trials are queried by lab, and a lab spans projects on purpose, so a fenced
// project's trials must drop out of an outside caller's results while the open
// projects in the same lab stay.
func TestTrialFenceAcrossLabs(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "secret", core.IsolationConfidential)

	inside := bindClient(t, ctx, db, url, "secret")
	callJSON(t, ctx, inside, "trial_record", map[string]any{
		"title": "secret-trial", "lab": "shared-lab", "outcome": "pass",
	})

	outside := bindClient(t, ctx, db, url, "demo")
	callJSON(t, ctx, outside, "trial_record", map[string]any{
		"title": "open-trial", "lab": "shared-lab", "outcome": "fail",
	})

	titles := func(res map[string]any, key string) []string {
		out := []string{}
		for _, tr := range res[key].([]any) {
			out = append(out, tr.(map[string]any)["title"].(string))
		}
		return out
	}

	q := callJSON(t, ctx, outside, "trial_query", map[string]any{"lab": "shared-lab"})
	require.Equal(t, []string{"open-trial"}, titles(q, "trials"))
	opened := callJSON(t, ctx, outside, "lab_open", map[string]any{"lab": "shared-lab"})
	require.Equal(t, []string{"open-trial"}, titles(opened, "recent_trials"))

	// The fenced project's own agent still sees both: confidential fences
	// outbound only, so the open project's trials still reach it.
	q = callJSON(t, ctx, inside, "trial_query", map[string]any{"lab": "shared-lab"})
	require.ElementsMatch(t, []string{"secret-trial", "open-trial"}, titles(q, "trials"))
}

// TestTrialFenceSealedCallerSeesOnlyItself is the inbound half of the trial
// fence: sealed admits nothing, so the shared lab collapses to the caller's own
// project.
func TestTrialFenceSealedCallerSeesOnlyItself(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)

	outside := bindClient(t, ctx, db, url, "demo")
	callJSON(t, ctx, outside, "trial_record", map[string]any{
		"title": "open-trial", "lab": "shared-lab", "outcome": "pass",
	})

	inside := bindClient(t, ctx, db, url, "vault")
	callJSON(t, ctx, inside, "trial_record", map[string]any{
		"title": "vault-trial", "lab": "shared-lab", "outcome": "pass",
	})

	q := callJSON(t, ctx, inside, "trial_query", map[string]any{"lab": "shared-lab"})
	trials := q["trials"].([]any)
	require.Len(t, trials, 1)
	require.Equal(t, "vault-trial", trials[0].(map[string]any)["title"])
}

// TestAmbientFallbackNeverResolvesIntoIsolatedProject is the hardening the
// fence depends on: the ambient fallback GUESSES a caller's project from
// whichever agent last touched a hook session, and a guess must never be what
// grants access to a fenced project. The remedy is an explicit binding.
func TestAmbientFallbackNeverResolvesIntoIsolatedProject(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "vault", core.IsolationSealed)
	seedAmbient(t, ctx, db, "cc/vault0000", "vault")

	cli := dialClient(t, ctx, url, testKey)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_write", map[string]any{"name": "x", "kind": "reference", "description": "d", "body": "b"}},
		{"notes_create", map[string]any{"title": "a note", "body": "b"}},
		{"tasks_add", map[string]any{"title": "a task"}},
		{"recall", map[string]any{"query": "anything"}},
		{"memory_read", map[string]any{"name": "x"}},
		{"tasks_list", map[string]any{}},
	} {
		isErr, txt := callErr(t, ctx, cli, tc.tool, tc.args)
		require.True(t, isErr, "%s must not inherit an isolated project by inference", tc.tool)
		require.Contains(t, txt, "project vault is sealed: start a session bound to it", "%s: %s", tc.tool, txt)
		require.NotContains(t, txt, "CREATES that project", "%s: %s", tc.tool, txt)
	}

	// Naming the project is not the remedy either -- an argument cannot stand in
	// for a binding, or the fence would be a suggestion.
	isErr, txt := callErr(t, ctx, cli, "memory_write", map[string]any{
		"name": "x", "kind": "reference", "description": "d", "body": "b", "project": "vault",
	})
	require.True(t, isErr)
	require.Contains(t, txt, "project vault is sealed: writes into it require a session bound to it", txt)

	// An explicit binding is: the same connection works once bound.
	bound := bindClient(t, ctx, db, url, "vault")
	w := callJSON(t, ctx, bound, "memory_write", map[string]any{
		"name": "bound-write", "kind": "reference", "description": "d", "body": "b\n",
	})
	require.Equal(t, "vault", w["project"])
}

// TestAmbientFallbackOpenProjectUnchanged pins the single-agent ergonomic the
// hardening must not cost: an open project's ambient session is still inherited
// by an unbound connection.
func TestAmbientFallbackOpenProjectUnchanged(t *testing.T) {
	ctx := context.Background()
	url, db := newServer(t)
	isolate(t, ctx, db, "demo", core.IsolationOpen)
	seedAmbient(t, ctx, db, "cc/demo00000", "demo")

	cli := dialClient(t, ctx, url, testKey)
	w := callJSON(t, ctx, cli, "memory_write", map[string]any{
		"name": "ambient-write", "kind": "reference", "description": "d", "body": "b\n",
	})
	require.Equal(t, "demo", w["project"])
}
