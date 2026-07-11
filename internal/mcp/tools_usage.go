package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/store"
)

func usageSummaryTool() mcp.Tool {
	return mcp.NewTool("usage_summary",
		mcp.WithDescription("Report a roll-up of activity: memory/note/session/task counts, retrieval totals with the most-injected memories, pending gardener proposals, and events by kind. Read-only."),
	)
}

func (s *Server) handleUsageSummary(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Refresh the retrieval_stats projection so injection/read totals are current
	// even between gardener passes.
	if err := store.RebuildRetrievalStats(ctx, s.cfg.DB); err != nil {
		s.logger.Warn("usage_summary: rebuild retrieval stats", "error", err)
	}
	summary, err := store.GetUsageSummary(ctx, s.cfg.DB)
	if err != nil {
		return errResult("usage_summary", err)
	}
	return jsonResult(summary)
}
