package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/gardener"
	"github.com/0spoon/seamless/internal/store"
)

func gardenerProposalsTool() mcp.Tool {
	return mcp.NewTool("gardener_proposals",
		mcp.WithDescription("List pending gardener proposals (merge/consolidate duplicate memories, archive stale memories, write a monthly session digest, reproject a memory to another project, set up a project split, or abandon a never-approved captured plan). Review, then apply or dismiss each with gardener_apply. Read-only."),
		mcp.WithString("kind", enumOf(store.ProposalKinds), mcp.Description("filter by proposal kind (default: all pending)")),
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
		mcp.WithString("project", mcp.Description("scope candidate memories: a project slug (its memories + globals), \"global\" for globals only, or \"all\" for every project on the machine. Omit to use the session's project.")),
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
	scope, err := s.gardenerRequestScope(ctx, argString(req, "project"))
	if err != nil {
		return errResult("gardener_request", err)
	}
	res, err := s.cfg.Gardener.Request(ctx, text, scope)
	if err != nil {
		return errResult("gardener_request", err)
	}
	return jsonResult(res)
}

// gardenerRequestScope resolves gardener_request's project argument, the last
// tool argument that reached a service without passing the scope guards.
//
// Every failure it now reports used to be a success. project="_global" was not
// normalized, so ActiveMemories("_global") matched no rows and answered "no
// active memories in scope"; a typo'd slug said the same thing, indistinguishable
// from a genuinely empty project; and an omitted project scanned every project on
// the machine with no ambiguity check at all -- the standing "no automatic
// fallbacks for ambiguous requests" directive, violated in the one place left
// that could still violate it.
func (s *Server) gardenerRequestScope(ctx context.Context, explicit string) (gardener.RequestScope, error) {
	// The widening token is read before the guards: it is not a slug, and
	// normalizeProject would hand it through as one.
	if explicit == allProjectsToken {
		return gardener.RequestScope{AllProjects: true}, nil
	}
	// A read, so resolveReadScope, not Write: a genuine global read is ("", nil)
	// and only ambiguity errors.
	project, err := s.resolveReadScope(ctx, explicit)
	if err != nil {
		return gardener.RequestScope{}, err
	}
	// Existence is checked only for a slug the caller named, and only here.
	//
	// validateProjectArg checks slug SHAPE, so "typoed" passes it and lands on the
	// same silent empty success this tool exists to stop returning. But an
	// inferred project comes from a live session rather than a typo, and a
	// candidate memory may legitimately reference a project that was never
	// registered (see requestCandidates' callers) -- so this stays a
	// gardener_request rule and does not move into the shared guards, where
	// memory_read and tasks_list would start rejecting unregistered-but-real slugs.
	if explicit != "" && project != "" {
		_, found, err := store.ProjectBySlug(ctx, s.cfg.DB, project)
		if err != nil {
			return gardener.RequestScope{}, err
		}
		if !found {
			return gardener.RequestScope{}, fmt.Errorf("unknown project %q", project)
		}
	}
	return gardener.RequestScope{Project: project}, nil
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
