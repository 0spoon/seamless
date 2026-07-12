package mcp

import (
	"context"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/retrieve"
)

func recallTool() mcp.Tool {
	return mcp.NewTool("recall",
		mcp.WithDescription("Search memories and notes by meaning and keyword (fused), scoped to the current project plus global items. This is the single search entry point."),
		mcp.WithString("query", mcp.Required(), mcp.Description("what you are looking for")),
		mcp.WithString("scope", mcp.Enum("all", "memories", "notes"), mcp.Description("what to search (default all)")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
		mcp.WithNumber("limit", mcp.Description("maximum results (default 10)")),
	)
}

func (s *Server) handleRecall(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query := argString(req, "query")
	if query == "" {
		return errResult("recall", errors.New("query is required"))
	}
	project, err := s.resolveReadScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("recall", err)
	}
	hits, err := s.cfg.Retrieve.Recall(ctx, retrieve.RecallInput{
		Query: query, Project: project, Scope: argString(req, "scope"), Limit: argInt(req, "limit", 10),
	})
	if err != nil {
		return errResult("recall", err)
	}
	// Record what was surfaced so read-after-inject stats can be derived later.
	if len(hits) > 0 {
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.ID
		}
		s.record(ctx, core.EventInjected, s.boundSession(ctx), project, "",
			map[string]any{"query": query, "item_ids": ids, "source": "recall"})
	}
	return jsonResult(map[string]any{"hits": hits})
}
