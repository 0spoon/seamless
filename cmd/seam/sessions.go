package main

// seam sessions -- session list/detail via the console JSON endpoint.

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

func runSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	status := fs.String("status", "", "filter: active|completed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return sessionDetail(cfg, fs.Arg(0))
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
	if *status != "" {
		path += "&status=" + *status
	}
	if err := consoleJSON(cfg, path, &data); err != nil {
		return err
	}
	fmt.Printf("%d sessions (%d active)\n", data.Total, data.Active)
	for _, s := range data.Sessions {
		name := s.Name
		if name == "" {
			name = shortID(s.ID)
		}
		amb := ""
		if s.Ambient {
			amb = " (ambient)"
		}
		fmt.Printf("  %-20s %-10s %-9s %s%s\n", name, orDash(s.Project), s.Status, agoShort(s.Updated), amb)
	}
	return nil
}

func sessionDetail(cfg config.Config, id string) error {
	var d struct {
		Session struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			ProjectSlug string `json:"projectSlug"`
			Status      string `json:"status"`
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
	fmt.Printf("%s  [%s]  %s\n", name, d.Session.Status, orDash(d.Session.ProjectSlug))
	fmt.Printf("tool calls: %d  writes: %d  reads: %d  read-after-inject: %d/%d\n",
		d.ToolCalls, d.Writes, d.Reads, d.ReadBack, d.Injected)
	if strings.TrimSpace(d.Findings) != "" {
		fmt.Printf("\nfindings:\n%s\n", d.Findings)
	}
	return nil
}
