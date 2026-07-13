package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/store"
)

func gardenerProposalsTool() mcp.Tool {
	return mcp.NewTool("gardener_proposals",
		mcp.WithDescription("List pending gardener proposals (merge/consolidate duplicate memories, archive stale memories, write a monthly session digest, reproject a memory to another project, or set up a project split). Review, then apply or dismiss each with gardener_apply. Read-only."),
		mcp.WithString("kind", mcp.Enum("merge", "archive", "digest", "consolidate", "reproject", "split"), mcp.Description("filter by proposal kind (default: all pending)")),
	)
}

func (s *Server) handleGardenerProposals(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	kind := argString(req, "kind")
	proposals, err := store.PendingProposals(ctx, s.cfg.DB, kind)
	if err != nil {
		return errResult("gardener_proposals", err)
	}
	return jsonResult(map[string]any{"proposals": proposals, "count": len(proposals)})
}

func gardenerRequestTool() mcp.Tool {
	return mcp.NewTool("gardener_request",
		mcp.WithDescription("The natural-language entry point for REORGANIZING memory. Describe the change in plain language and it returns reviewable pending proposals -- fold duplicates together (\"these two memories are duplicates -- keep the newer\"), retire stale memories (\"archive anything about the old port 8080\"), synthesize several into one (\"combine the three auth-flow notes\"), or move a mis-filed memory to another EXISTING project (\"the iOS DFU memory belongs in arctop-ios\"). Use this whenever the user describes how they want their knowledge organized; if the intended change is ambiguous, ask them a clarifying question first. It NEVER mutates memories: it only creates pending proposals -- review with gardener_proposals, resolve with gardener_apply. If the request is to split one project into NEW child projects, it recognizes that and returns guidance (splitSource) pointing you at gardener_split instead. Needs an LLM chat client."),
		mcp.WithString("request", mcp.Required(), mcp.Description("the reorganization request in plain language")),
		mcp.WithString("project", mcp.Description("scope candidate memories: a project slug (its memories + globals), \"global\" for globals only, or omit for all projects")),
	)
}

func (s *Server) handleGardenerRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.cfg.Gardener == nil {
		return errResult("gardener_request", errors.New("gardener is not configured on this server"))
	}
	text := argString(req, "request")
	if text == "" {
		return errResult("gardener_request", errors.New("request is required"))
	}
	res, err := s.cfg.Gardener.Request(ctx, text, argString(req, "project"))
	if err != nil {
		return errResult("gardener_request", err)
	}
	return jsonResult(res)
}

func gardenerSplitTool() mcp.Tool {
	return mcp.NewTool("gardener_split",
		mcp.WithDescription("Plan a project SPLIT: divide one existing project into two or more NEW child projects, keeping cross-platform memories in a shared parent (e.g. split arctop-app into arctop-ios + arctop-android with shared arctop-mobile-apps). Use this when the user wants to break one project into several -- gardener_request points you here (via splitSource) when it detects that intent. It NEVER creates a project or moves a memory: it only creates reviewable pending proposals -- one 'split' setup proposal plus one 'reproject' per memory, all under plan 'split-<source>'. Review with gardener_proposals, then apply each with gardener_apply (or retarget a memory first in the console). Needs an LLM chat client and a known source project slug (see project_list)."),
		mcp.WithString("source", mcp.Required(), mcp.Description("the project slug to split (its own memories are classified into the children/shared parent)")),
		mcp.WithString("instruction", mcp.Description("optional guidance: which children, what stays shared")),
	)
}

func (s *Server) handleGardenerSplit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.cfg.Gardener == nil {
		return errResult("gardener_split", errors.New("gardener is not configured on this server"))
	}
	source := argString(req, "source")
	if source == "" {
		return errResult("gardener_split", errors.New("source is required"))
	}
	res, err := s.cfg.Gardener.Split(ctx, source, argString(req, "instruction"))
	if err != nil {
		return errResult("gardener_split", err)
	}
	return jsonResult(res)
}

func gardenerApplyTool() mcp.Tool {
	return mcp.NewTool("gardener_apply",
		mcp.WithDescription("Resolve a gardener proposal. action=apply carries out the effect (archive -> retire the memory; merge -> supersede the older by the newer; consolidate -> write a unified memory superseding its sources; digest -> save the summary as a note; reproject -> move the memory to another project; split -> create the child/shared projects, link the family, parent the children, retire the source); action=dismiss discards it. A dismissed proposal is never re-raised."),
		mcp.WithString("id", mcp.Required(), mcp.Description("proposal id (ULID)")),
		mcp.WithString("action", mcp.Enum("apply", "dismiss"), mcp.Description("apply (default) or dismiss")),
	)
}

func (s *Server) handleGardenerApply(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "id")
	if id == "" {
		return errResult("gardener_apply", errors.New("id is required"))
	}
	if s.cfg.Gardener == nil {
		return errResult("gardener_apply", errors.New("gardener is not configured on this server"))
	}
	action := argString(req, "action")
	if action == "" {
		action = "apply"
	}
	switch action {
	case "apply":
		result, err := s.cfg.Gardener.Apply(ctx, id)
		if err != nil {
			return errResult("gardener_apply", err)
		}
		return jsonResult(result)
	case "dismiss":
		if err := s.cfg.Gardener.Dismiss(ctx, id); err != nil {
			return errResult("gardener_apply", err)
		}
		return jsonResult(map[string]any{"id": id, "status": "dismissed"})
	default:
		return errResult("gardener_apply", fmt.Errorf("unknown action %q (want apply|dismiss)", action))
	}
}
