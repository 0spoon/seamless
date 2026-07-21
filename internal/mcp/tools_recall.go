package mcp

import (
	"context"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/events"
	"github.com/0spoon/seamless/internal/retrieve"
)

// recallMissQueryMax bounds the query text stored on a recall.miss event; the
// miss log is transport-class telemetry, not a transcript.
const recallMissQueryMax = 500

func recallTool() mcp.Tool {
	return mcp.NewTool("recall",
		mcp.WithDescription("Search memories and notes by meaning and keyword (fused), scoped to the current project plus global items. This is the single search entry point."),
		mcp.WithString("query", mcp.Required(), mcp.Description("what you are looking for")),
		mcp.WithString("scope", enumOf(retrieve.RecallScopes), mcp.Description("what to search (default all)")),
		mcp.WithString("project", mcp.Description("project slug; defaults to the bound session's project")),
		mcp.WithNumber("limit", mcp.Min(1), mcp.Description("maximum results (default 10)")),
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
	// item_scores rides along (parallel to item_ids, which is already in rank
	// order) so future scoring can weigh how confidently each hit ranked --
	// dropping the score here would lose it forever, the log being append-only.
	if len(hits) > 0 {
		ids := make([]string, len(hits))
		scores := make([]float64, len(hits))
		for i, h := range hits {
			ids[i] = h.ID
			scores[i] = h.Score
		}
		s.record(ctx, core.EventInjected, s.boundSession(ctx), project, "",
			map[string]any{"query": query, "item_ids": ids, "item_scores": scores, "source": "recall"})
	} else {
		// A zero-hit recall is demand for knowledge that does not exist -- the
		// signal the gardener's memory-wanted pass clusters into proposals.
		s.record(ctx, core.EventRecallMiss, s.boundSession(ctx), project, "",
			map[string]any{"query": events.Truncate(query, recallMissQueryMax), "scope": argString(req, "scope"),
				"limit": argInt(req, "limit", 10), "source": "recall"})
	}
	return jsonResult(map[string]any{"hits": hits})
}
