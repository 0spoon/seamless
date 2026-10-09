package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// The session= handle: a session binding carried by the call instead of the
// connection.
//
// session_start binds a session to the connection, keyed by Mcp-Session-Id
// (setBinding). The stateless MCP revision (2026-07-28, SEP-2567) has no such
// id: every request runs in a throwaway session, so that binding lasts one call.
// What replaces it is what mcp-go's migration guide prescribes -- an explicit
// handle the model threads back as an argument. Seamless already has one: the
// session name session_start returns. Every tool but session_start accepts it as
// session=, and a call naming a session is bound to it exactly as a session_start
// binding would bind it (getBinding consults the handle first): its project
// scopes the call, the isolation fence judges against it, and the call is
// credited to it. That makes the handle no stronger than session_start, which
// can already resume any named session.
//
// The handle works on every revision, not only the stateless one, so a caller
// can also use it to act as a session other than its connection's. Until a
// client we serve stops negotiating down, the transport serves only the session
// revisions (Handler), and the handle is a convenience rather than the only
// binding a connection has.

// sessionHandleArgDesc describes the session= handle on the tools that did not
// already declare a session argument of their own.
const sessionHandleArgDesc = "the session to act as: the name session_start returned, the cc/<id> or cx/<id> on your " +
	"briefing's 'Seam session' line, or a session ULID. It scopes and credits this call. Defaults to this " +
	"connection's session; pass it on every call when session_start reports the connection as stateless"

// withSessionHandle declares the session= handle on t. session_start is
// exempt: it names the session it binds with name=, and resuming one through a
// second argument would be two ways to say one thing. A tool that declares its
// own session argument (session_update/end, the claim-aware task tools) keeps
// its description -- each already means "the session to act as".
func withSessionHandle(t mcp.Tool) mcp.Tool {
	if t.Name == "session_start" {
		return t
	}
	if _, declared := t.InputSchema.Properties["session"]; declared {
		return t
	}
	if t.InputSchema.Properties == nil {
		t.InputSchema.Properties = map[string]any{}
	}
	t.InputSchema.Properties["session"] = map[string]any{"type": "string", "description": sessionHandleArgDesc}
	if t.InputSchema.PropertyOrder != nil {
		t.InputSchema.PropertyOrder = append(t.InputSchema.PropertyOrder, "session")
	}
	return t
}

// sessionHandleSlotKey keys the per-call slot logMiddleware plants and
// sessionHandleMiddleware fills.
type sessionHandleSlotKey struct{}

// sessionHandleSlot holds the binding a call's session= handle resolved to. It
// is planted by logMiddleware and filled in place further in, the same hand-off
// as attributionSlot, so the attribution logMiddleware reads after the handler
// sees the handle too.
type sessionHandleSlot struct {
	b  binding
	ok bool
}

// sessionHandleMiddleware resolves a call's session= handle and binds the call
// to it. A handle that names no session is refused before the handler runs:
// naming a bad identity must fail, never fall through to whatever binding the
// connection happens to have (the same rule resolveActor applies).
func (s *Server) sessionHandleMiddleware(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref := argString(req, "session")
		if ref == "" || s.cfg.DB == nil {
			return next(ctx, req)
		}
		sess, ok, err := sessionByRef(ctx, s.cfg.DB, ref)
		if err != nil {
			return errResult(req.Params.Name, err)
		}
		if !ok {
			return errResult(req.Params.Name, errSessionRefNotFound(ref))
		}
		b := binding{sessionID: sess.ID, project: sess.ProjectSlug}
		if slot, planted := ctx.Value(sessionHandleSlotKey{}).(*sessionHandleSlot); planted {
			slot.b, slot.ok = b, true
		} else {
			ctx = context.WithValue(ctx, sessionHandleSlotKey{}, &sessionHandleSlot{b: b, ok: true})
		}
		return next(ctx, req)
	}
}

// handleBinding returns the binding this call's session= handle resolved to.
func handleBinding(ctx context.Context) (binding, bool) {
	slot, ok := ctx.Value(sessionHandleSlotKey{}).(*sessionHandleSlot)
	if !ok || !slot.ok {
		return binding{}, false
	}
	return slot.b, true
}

// statelessConnection reports whether this call's connection keeps no MCP
// session, so nothing setBinding or setBindingLab records outlives the call.
func (s *Server) statelessConnection(ctx context.Context) bool {
	return s.mcpSessionID(ctx) == ""
}

// withStatelessHint tells a session_start caller on a stateless connection how
// to keep the binding it just made: thread the session name back as session= on
// every call, plus project= when the binding's project is not the session's own
// (a resumed session moved to another project). A connection that keeps its
// binding gets the result unchanged.
func (s *Server) withStatelessHint(ctx context.Context, out map[string]any, name, project, sessionProject string) map[string]any {
	if !s.statelessConnection(ctx) {
		return out
	}
	hint := "This connection keeps no MCP session, so the binding above lasts only for this call: pass session=" + name
	if project != sessionProject {
		named := project
		if named == "" {
			named = "global"
		}
		hint += " and project=" + named
	}
	out["stateless"] = hint + " on every later call."
	return out
}
