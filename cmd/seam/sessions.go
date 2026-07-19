package main

// seam sessions -- session list/detail via the console JSON endpoint.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
)

// The list and the detail view are one command, which is what `between` exists
// for: `seam sessions` lists, `seam sessions <id>` shows one.
var sessionsCmd = spec("sessions", groupObservability, "list sessions, or show one by id",
	between(0, 1, "id"), bindSessions, runSessions)

type sessionsOpts struct {
	status *string
}

func bindSessions(fs *flag.FlagSet) *sessionsOpts {
	return &sessionsOpts{
		// Derived from core.SessionStatuses, and the derivation is load-bearing: the
		// string this replaced advertised "active|completed", omitting the "expired"
		// the console has always accepted. Transcribing the help text would have
		// shipped that omission as an enforced rule.
		status: enumFlag(fs, "status", "", "filter by `STATUS`", enumOf(core.SessionStatuses)),
	}
}

func runSessions(_ context.Context, e *env, o *sessionsOpts, pos []string) error {
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return sessionDetail(e.stdout, cfg, pos[0])
	}

	var data struct {
		Total    int `json:"total"`
		Active   int `json:"active"`
		Sessions []struct {
			ID       string    `json:"id"`
			Name     string    `json:"name"`
			Project  string    `json:"project"`
			Status   string    `json:"status"`
			Ambient  bool      `json:"ambient"`
			Findings string    `json:"findings"`
			Updated  time.Time `json:"updated"`
		} `json:"sessions"`
	}
	path := "/console/sessions?format=json"
	if *o.status != "" {
		path += "&status=" + url.QueryEscape(*o.status)
	}
	if err := consoleJSON(cfg, path, &data); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "%d sessions (%d active)\n", data.Total, data.Active)
	for _, s := range data.Sessions {
		name := s.Name
		if name == "" {
			name = shortID(s.ID)
		}
		amb := ""
		if s.Ambient {
			amb = " (ambient)"
		}
		fmt.Fprintf(e.stdout, "  %-20s %-10s %-9s %s%s\n", name, orDash(s.Project), s.Status, agoShort(s.Updated), amb)
	}
	return nil
}

func sessionDetail(out io.Writer, cfg config.Config, id string) error {
	var d struct {
		Session struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			ProjectSlug string `json:"projectSlug"`
			Status      string `json:"status"`
			Model       string `json:"model"`
		} `json:"session"`
		Findings  string `json:"findings"`
		ToolCalls int    `json:"toolCalls"`
		Reads     int    `json:"memoryReads"`
		Writes    int    `json:"memoryWrites"`
		Injected  int    `json:"injectedItems"`
		ReadBack  int    `json:"readAfterInject"`
	}
	if err := consoleJSON(cfg, "/console/sessions/"+id+"?format=json", &d); err != nil {
		return err
	}
	name := d.Session.Name
	if name == "" {
		name = shortID(d.Session.ID)
	}
	fmt.Fprintf(out, "%s  [%s]  %s\n", name, d.Session.Status, orDash(d.Session.ProjectSlug))
	if d.Session.Model != "" {
		fmt.Fprintf(out, "model: %s\n", d.Session.Model)
	}
	fmt.Fprintf(out, "tool calls: %d  writes: %d  reads: %d  read-after-inject: %d/%d\n",
		d.ToolCalls, d.Writes, d.Reads, d.ReadBack, d.Injected)
	if strings.TrimSpace(d.Findings) != "" {
		fmt.Fprintf(out, "\nfindings:\n%s\n", d.Findings)
	}
	return nil
}
