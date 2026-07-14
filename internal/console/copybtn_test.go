package console

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// TestCopyButton_HelperEmitsFullValue unit-tests the copyBtn helper directly: it
// carries the full value in data-copy (not a truncated display string), escapes
// it for the attribute context, and renders nothing for an empty value.
func TestCopyButton_HelperEmitsFullValue(t *testing.T) {
	full := "01ABCDEFGHIJKLMNOPQRSTUVWX"
	out := string(copyBtn(full))
	require.Contains(t, out, `class="copy-btn"`)
	require.Contains(t, out, `data-copy="`+full+`"`, "must carry the full untruncated value")
	require.Contains(t, out, `aria-label="Copy"`)
	require.Contains(t, out, "ico-copy", "carries the copy glyph")
	require.Contains(t, out, "ico-check", "carries the check glyph for the copied flash")

	// Empty value renders nothing so callers can drop it inline unconditionally.
	require.Equal(t, "", string(copyBtn("")))

	// A quote/ampersand in a value is escaped for the attribute context.
	esc := string(copyBtn(`a"&b`))
	require.NotContains(t, esc, `data-copy="a"&b"`)
	require.Contains(t, esc, "&#34;")
	require.Contains(t, esc, "&amp;")
}

// TestCopyButton_RendersFullValueAcrossSurfaces proves the button reaches both a
// peek fragment (so it works after the detail pane swaps innerHTML) and a full
// page, always carrying the full id/path rather than the shortID display text.
func TestCopyButton_RendersFullValueAcrossSurfaces(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A long ULID-like id + long path: shortID would truncate to 8 chars, so a
	// data-copy carrying the whole string proves we copy the full value.
	const sessID = "01ABCDEFGHIJKLMNOPQRSTUVWX"
	const cwd = "/Users/someone/repos/very/long/path/to/a/working/directory"
	require.NoError(t, store.CreateSession(ctx, db, core.Session{
		ID: sessID, Name: "cc/peek1234", ProjectSlug: "seamless", Status: core.SessionActive,
		Source: "startup", CWD: cwd, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}))

	// Full page: the metadata card copies the full session id and cwd.
	full := getPeek(t, mux, "/console/sessions/"+sessID).Body.String()
	require.Contains(t, full, `data-copy="`+sessID+`"`)
	require.Contains(t, full, `data-copy="`+cwd+`"`)
	require.NotContains(t, full, `data-copy="`+shortID(sessID)+`"`, "must copy the full id, not the shortID")

	// Peek fragment (pane body only, no layout): still carries the copy button,
	// which the document-level delegated handler drives after the innerHTML swap.
	frag := getPeek(t, mux, "/console/sessions/"+sessID+"?peek=1").Body.String()
	require.NotContains(t, frag, "<html")
	require.Contains(t, frag, `class="copy-btn"`)
	require.Contains(t, frag, `data-copy="`+sessID+`"`)
}

// TestCopyButton_ListRowsCarryFullIDs checks the dense list surfaces (tasks) put
// a copy button on each row carrying the full task id, not the truncated one.
func TestCopyButton_ListRowsCarryFullIDs(t *testing.T) {
	db, mux := newConsole(t)
	ctx := context.Background()
	const taskID = "01TASKFEDCBA9876543210ZYXW"
	require.NoError(t, store.CreateTask(ctx, db, core.Task{
		ID: taskID, ProjectSlug: "seamless", Title: "a ready task", Status: core.TaskOpen,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))

	body := getPeek(t, mux, "/console/tasks").Body.String()
	require.Contains(t, body, `data-copy="`+taskID+`"`, "ready row copies the full task id")
}

// TestCopyButton_ServedCSSAndJS confirms the reusable core (the .copy-btn rule
// and the delegated click handler) is actually served to the browser.
func TestCopyButton_ServedCSSAndJS(t *testing.T) {
	_, mux := newConsole(t)

	css := getPeek(t, mux, "/console/static/console.css").Body.String()
	require.Contains(t, css, ".copy-btn")
	require.Contains(t, css, ".copy-btn.copied")

	// The global handler ships in every page's layout.
	page := getPeek(t, mux, "/console/settings").Body.String()
	require.Contains(t, page, ".copy-btn[data-copy]", "delegated click handler present")
}
