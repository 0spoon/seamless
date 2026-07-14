package main

// Authenticated HTTP to the console's JSON surface, for the owner-only actions
// that live there rather than on the MCP tool surface (force-releasing a task
// lock, approving a plan). The MCP path is dial + callTool in main.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

// consoleJSON fetches a console page as JSON, authenticating with the bearer key.
func consoleJSON(cfg config.Config, path string, v any) error {
	req, err := http.NewRequest(http.MethodGet, mcpBase(cfg)+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MCP.APIKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("console unreachable at %s: %w", mcpBase(cfg), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("console returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("unreadable console response from %s: %w", path, err)
	}
	return nil
}

// consolePOST performs an authenticated POST to a console action endpoint and
// decodes the JSON response into v (v may be nil to ignore the body). It backs
// owner-only overrides (e.g. force-releasing a task lock) that live on the
// console surface rather than the MCP tools.
func consolePOST(cfg config.Config, path string, v any) error {
	req, err := http.NewRequest(http.MethodPost, mcpBase(cfg)+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MCP.APIKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("console unreachable at %s: %w", mcpBase(cfg), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		// Surface the handler's error message when it sent one (e.g. the task is
		// not claimed), falling back to the bare status.
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("console returned %s", resp.Status)
	}
	if v == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("unreadable console response from %s: %w", path, err)
	}
	return nil
}
