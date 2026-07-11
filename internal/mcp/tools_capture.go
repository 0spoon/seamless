package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
)

// captureBodyMaxRunes caps a captured page's stored body so one huge page cannot
// bloat a note file (and its embedding).
const captureBodyMaxRunes = 50000

func captureURLTool() mcp.Tool {
	return mcp.NewTool("capture_url",
		mcp.WithDescription("Fetch a web page (SSRF-guarded: private/loopback addresses are rejected) and save its readable content as a note. Returns the new note's id."),
		mcp.WithString("url", mcp.Required(), mcp.Description("http(s) URL to capture")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project (empty = inbox)")),
	)
}

func (s *Server) handleCaptureURL(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rawURL := argString(req, "url")
	if rawURL == "" {
		return errResult("capture_url", errors.New("url is required"))
	}
	content, err := s.fetcher.FetchURL(ctx, rawURL)
	if err != nil {
		return errResult("capture_url", err)
	}

	body := fmt.Sprintf("Source: %s\n\n---\n\n%s", content.URL, content.Body)
	if r := []rune(body); len(r) > captureBodyMaxRunes {
		body = string(r[:captureBodyMaxRunes]) + "\n\n[content truncated]"
	}

	project := s.resolveProject(ctx, argString(req, "project"))
	id, err := core.NewID()
	if err != nil {
		return errResult("capture_url", err)
	}
	now := time.Now().UTC()
	note := core.Note{
		ID: id, Title: content.Title, Slug: slugify(content.Title), Description: "Captured from " + content.URL,
		Project: project, Body: body, Tags: []string{"created-by:agent", "captured-url"},
		SourceURL: content.URL, Created: now, Updated: now,
	}
	written, err := s.cfg.Files.WriteNote(ctx, note)
	if err != nil {
		return errResult("capture_url", err)
	}
	s.record(ctx, core.EventNoteWritten, s.boundSession(ctx), project, written.ID,
		map[string]any{"title": content.Title, "source_url": content.URL})
	return jsonResult(map[string]any{
		"id": written.ID, "slug": written.Slug, "title": written.Title,
		"project": project, "source_url": content.URL,
	})
}
