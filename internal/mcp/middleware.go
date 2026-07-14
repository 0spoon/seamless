package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/store"
)

// logMiddleware records one tool.call event per invocation for the console's
// Interactions feed: the tool name, its (truncated) arguments, the (truncated)
// result text or error, and the wall-clock duration. It runs INNER of
// authMiddleware -- registered after it in New(), and mcp-go applies middlewares
// in reverse registration order -- so an unauthorized call is rejected before it
// reaches here and is never logged. Attribution is read AFTER next() so that a
// session_start's own event carries the session it just bound. Logging is
// best-effort: a recorder failure never affects the tool's result.
func (s *Server) logMiddleware(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		result, err := next(ctx, req)
		durMS := time.Since(start).Milliseconds()

		// Attribution doubles as the session heartbeat: any tool call is proof the
		// agent is alive, so bump the attributed session's updated_at to keep the
		// idle reaper from expiring it. Covers bound (explicit session_start) and
		// ambient-fallback sessions alike; the active-only guard in TouchSession
		// makes it a no-op for a just-ended one. Best-effort.
		sessionID, project := s.attribution(ctx)
		if sessionID != "" {
			if terr := store.TouchSession(ctx, s.cfg.DB, sessionID, time.Now().UTC()); terr != nil {
				s.logger.Warn("mcp: session heartbeat", "session", sessionID, "error", terr)
			}
		}

		if s.cfg.Events == nil {
			return result, err
		}
		max := s.cfg.ToolEventMaxChars

		payload := map[string]any{
			"tool":        req.Params.Name,
			"duration_ms": durMS,
		}
		if args := truncateArgs(req.GetArguments(), max); len(args) > 0 {
			payload["args"] = args
		}
		switch {
		case err != nil:
			// Exceptional: a recovered panic surfaced as a Go error. Record the
			// error with no result body.
			payload["is_error"] = true
			payload["error"] = firstLine(err.Error())
		case result != nil:
			text := contentText(result)
			if t := events.Truncate(text, max); t != "" {
				payload["result"] = t
			}
			if result.IsError {
				payload["is_error"] = true
				payload["error"] = firstLine(text)
			}
		}
		s.record(ctx, core.EventToolCall, sessionID, project, "", payload)
		return result, err
	}
}

// attribution resolves the (sessionID, project) a logged tool.call should carry:
// the connection's binding if it has one, else a single unambiguous ambient
// session, else empty. It never errors -- an unattributable call (stateless
// transport, or before any session_start) simply lands in the feed's
// "unattributed" tab. It is read after the handler runs so session_start's event
// reflects the binding it just created.
func (s *Server) attribution(ctx context.Context) (sessionID, project string) {
	if b, ok := s.getBinding(ctx); ok {
		return b.sessionID, b.project
	}
	if sess, ok, _ := s.ambientFallback(ctx); ok {
		return sess.ID, sess.ProjectSlug
	}
	return "", ""
}

// truncateArgs copies args, truncating string values to max runes (keeping the
// stored JSON small yet valid) and leaving non-string values untouched. A nil or
// empty map yields nil.
func truncateArgs(args map[string]any, max int) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if sv, ok := v.(string); ok {
			out[k] = events.Truncate(sv, max)
		} else {
			out[k] = v
		}
	}
	return out
}

// contentText concatenates the text parts of a tool result (usually just one) --
// the request/response body the Interactions feed shows.
func contentText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// firstLine returns the first line of s, trimmed -- a compact error label for the
// feed's summary while the full text stays in the result field.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
