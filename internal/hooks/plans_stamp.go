package hooks

// The plan-file vocabulary: which paths count as a plan, the correlation key
// derived from one, and the provenance stamp prepended to a captured body.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/validate"
)

// planFilePath validates that a tool-input path is a .md file directly under
// the plans dir (defense in depth behind the CLI pre-filter) and returns the
// cleaned absolute path.
func (h *Handler) planFilePath(raw string) (string, bool) {
	if h.plansDir == "" || raw == "" {
		return "", false
	}
	path := raw
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		path = filepath.Join(home, path[2:])
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) != filepath.Clean(h.plansDir) || filepath.Ext(path) != ".md" {
		return "", false
	}
	if validate.Name(planBasename(path)) != nil {
		return "", false
	}
	return path, true
}

// planBasename is the plan's correlation key: the file name without .md.
func planBasename(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

// planStamp is the provenance blockquote prepended to every captured plan body.
func planStamp(claudeSessionID, basename string, iter int, head string, now time.Time) string {
	return fmt.Sprintf("> captured from %s | %s.md | iter %d | git %s | %s",
		stampSession(claudeSessionID), basename, iter, shortHead(head), now.UTC().Format(time.RFC3339))
}

// stampSession names the capturing session in a stamp line. Plan capture is
// Claude Code-only (Codex registers no plan-capture hooks), so the session is
// always a cc/ ambient.
func stampSession(claudeSessionID string) string {
	if claudeSessionID == "" {
		return "cc/unknown"
	}
	return ambientName(ClientClaudeCode, claudeSessionID)
}

// shortHead abbreviates a commit hash for stamps ("unknown" when absent).
func shortHead(head string) string {
	if head == "" {
		return "unknown"
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

// firstHeading returns the text of the first "# " heading line, or "".
func firstHeading(content string) string {
	for line := range strings.Lines(content) {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
