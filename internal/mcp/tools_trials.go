package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// trialContextLimit is how many recent trials lab_open returns as context.
const trialContextLimit = 10

func labOpenTool() mcp.Tool {
	return mcp.NewTool("lab_open",
		mcp.WithDescription("Open a research lab and get its recent trial history for context. Binds the lab to this connection so later trial_record calls inherit it. A lab is just a label; opening a new one creates it implicitly on first trial_record."),
		mcp.WithString("lab", mcp.Required(), mcp.Description("lab name (a stable label for a line of investigation)")),
		mcp.WithString("goal", mcp.Description("optional note on what this lab is investigating")),
	)
}

func (s *Server) handleLabOpen(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	lab := argString(req, "lab")
	if lab == "" {
		return errResult("lab_open", errors.New("lab is required"))
	}
	s.setBindingLab(ctx, lab)
	trials, err := store.QueryTrials(ctx, s.cfg.DB, store.TrialFilter{Lab: lab, Limit: trialContextLimit})
	if err != nil {
		return errResult("lab_open", err)
	}
	recent := make([]map[string]any, 0, len(trials))
	for _, tr := range trials {
		recent = append(recent, trialJSON(tr))
	}
	return jsonResult(map[string]any{"lab": lab, "trial_count": len(trials), "recent_trials": recent})
}

func trialRecordTool() mcp.Tool {
	return mcp.NewTool("trial_record",
		mcp.WithDescription("Record one experiment in a research lab: what changed, expected vs actual, outcome, and optional structured metrics for later querying. Inherits the lab from lab_open unless you pass one."),
		mcp.WithString("title", mcp.Required(), mcp.Description("short trial title")),
		mcp.WithString("lab", mcp.Description("lab name; defaults to the lab opened on this connection")),
		mcp.WithString("changes", mcp.Description("what was changed for this trial")),
		mcp.WithString("expected", mcp.Description("expected result")),
		mcp.WithString("actual", mcp.Description("observed result")),
		mcp.WithString("outcome", mcp.Description("pass|fail|partial|inconclusive (free-form)")),
		mcp.WithString("metrics", mcp.Description(`optional JSON object of structured metrics, e.g. {"hz":497,"err_pct":0.2}`)),
	)
}

func (s *Server) handleTrialRecord(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := argString(req, "title")
	if title == "" {
		return errResult("trial_record", errors.New("title is required"))
	}
	lab := argString(req, "lab")
	if lab == "" {
		lab = s.boundLab(ctx)
	}
	if lab == "" {
		return errResult("trial_record", errors.New("no lab: call lab_open first or pass lab"))
	}
	metrics, err := parseMetrics(argRaw(req, "metrics"))
	if err != nil {
		return errResult("trial_record", err)
	}
	id, err := core.NewID()
	if err != nil {
		return errResult("trial_record", err)
	}
	project := s.resolveProject(ctx, argString(req, "project"))
	tr := core.Trial{
		ID: id, Lab: lab, Title: title,
		Changes:  argRaw(req, "changes"),
		Expected: argRaw(req, "expected"),
		Actual:   argRaw(req, "actual"),
		Outcome:  core.TrialOutcome(argString(req, "outcome")),
		Metrics:  metrics, SessionID: s.boundSession(ctx), ProjectSlug: project,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.CreateTrial(ctx, s.cfg.DB, tr); err != nil {
		return errResult("trial_record", err)
	}
	s.record(ctx, core.EventTrialRecorded, tr.SessionID, project, id,
		map[string]any{"lab": lab, "title": title, "outcome": string(tr.Outcome)})
	return jsonResult(trialJSON(tr))
}

func trialQueryTool() mcp.Tool {
	return mcp.NewTool("trial_query",
		mcp.WithDescription("Query recorded trials, filtered by lab, outcome, and/or an exact-match metrics filter (native structured query over the metrics recorded by trial_record)."),
		mcp.WithString("lab", mcp.Description("lab name; defaults to the lab opened on this connection")),
		mcp.WithString("outcome", mcp.Description("filter by outcome (e.g. fail)")),
		mcp.WithString("metrics_filter", mcp.Description(`optional JSON object; trials whose metrics equal every given key match, e.g. {"hz":497}`)),
		mcp.WithNumber("limit", mcp.Description("max results (default 20)")),
	)
}

func (s *Server) handleTrialQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	lab := argString(req, "lab")
	if lab == "" {
		lab = s.boundLab(ctx)
	}
	filter := store.TrialFilter{
		Lab:     lab,
		Outcome: argString(req, "outcome"),
		Limit:   argInt(req, "limit", 20),
	}
	metricsFilter, err := parseMetrics(argRaw(req, "metrics_filter"))
	if err != nil {
		return errResult("trial_query", err)
	}
	filter.MetricsEquals = metricsFilter

	trials, err := store.QueryTrials(ctx, s.cfg.DB, filter)
	if err != nil {
		return errResult("trial_query", err)
	}
	out := make([]map[string]any, 0, len(trials))
	for _, tr := range trials {
		out = append(out, trialJSON(tr))
	}
	return jsonResult(map[string]any{"lab": lab, "trials": out})
}

// parseMetrics decodes a JSON-object metrics argument, or returns nil for a
// blank argument. A non-object (or malformed) value is a user error.
func parseMetrics(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("metrics must be a JSON object: %w", err)
	}
	return m, nil
}

// trialJSON renders a trial for a tool response, omitting empty fields.
func trialJSON(tr core.Trial) map[string]any {
	j := map[string]any{"id": tr.ID, "lab": tr.Lab, "title": tr.Title}
	if tr.Changes != "" {
		j["changes"] = tr.Changes
	}
	if tr.Expected != "" {
		j["expected"] = tr.Expected
	}
	if tr.Actual != "" {
		j["actual"] = tr.Actual
	}
	if tr.Outcome != "" {
		j["outcome"] = string(tr.Outcome)
	}
	if len(tr.Metrics) > 0 {
		j["metrics"] = tr.Metrics
	}
	return j
}
