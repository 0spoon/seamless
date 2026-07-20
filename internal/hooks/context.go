package hooks

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/0spoon/seamless/internal/retrieve"
)

// codexContextMaxTokens stays below Codex's approximately 2,500-token hook
// output spill threshold. It applies to every Codex additionalContext response,
// including SessionStart, UserPromptSubmit, and SubagentStart.
const codexContextMaxTokens = 2400

const contextTruncationMarker = "... [context truncated]"

// preparedHookContext is the single value used for both response serialization
// and injection telemetry. Keeping the emitted bytes and cap metadata together
// prevents telemetry from claiming that pre-cap content reached the model.
type preparedHookContext struct {
	content                 string
	originalEstimatedTokens int
	emittedEstimatedTokens  int
	truncated               bool
}

// prepareHookContext applies the client-aware output policy after callers have
// finished adding content. Claude Code remains uncapped; every Codex context is
// capped, so a newly supported Codex hook event cannot accidentally bypass the
// ceiling by falling outside an event-name allowlist.
func prepareHookContext(client Client, content string) preparedHookContext {
	originalTokens := retrieve.EstimateTokens(content)
	emitted := content
	if client == ClientCodex && originalTokens > codexContextMaxTokens {
		emitted = truncateCodexContext(content, codexContextMaxTokens)
	}
	return preparedHookContext{
		content:                 emitted,
		originalEstimatedTokens: originalTokens,
		emittedEstimatedTokens:  retrieve.EstimateTokens(emitted),
		truncated:               emitted != content,
	}
}

// writeContextResponse is the common model-visible hook path. The cap runs
// before both recordInjection and JSON serialization, and both consume the same
// prepared value.
func (h *Handler) writeContextResponse(
	ctx context.Context,
	w http.ResponseWriter,
	event, hook string,
	client Client,
	externalSessionID, prompt, content string,
	record bool,
	itemIDs []string,
) {
	prepared := prepareHookContext(client, content)
	if record && prepared.content != "" {
		h.recordInjection(ctx, hook, client, externalSessionID, prompt, prepared, itemIDs)
	}
	writePreparedHookResponse(w, event, prepared)
}

// truncateCodexContext preserves generated Seamless tags and, for briefings,
// the ambient identity line. The source briefing already orders pinned
// constraints and plans first, so trimming the middle/tail removes optional
// material before those priority lines. If priority content alone exceeds the
// ceiling, its leading portion survives deterministically.
func truncateCodexContext(content string, maxTokens int) string {
	maxBytes := maxTokens * 4
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}

	for _, closeTag := range []string{
		"</seam-briefing>",
		"</seam-recall>",
		"</seam-plan-context>",
	} {
		if strings.HasSuffix(content, closeTag) {
			return truncateTaggedContext(content, closeTag, maxBytes)
		}
	}

	marker := contextTruncationMarker
	if len(marker) >= maxBytes {
		return runeSafePrefix(marker, maxBytes)
	}
	return runeSafePrefix(content, maxBytes-len(marker)) + marker
}

func truncateTaggedContext(content, closeTag string, maxBytes int) string {
	body := strings.TrimSuffix(content, closeTag)
	body = strings.TrimRight(body, "\r\n")
	suffix := closeTag

	// The ambient identity is appended immediately before the briefing's closing
	// tag. Preserve it as a suffix while the main prefix keeps constraints/plans.
	if closeTag == "</seam-briefing>" {
		if i := strings.LastIndex(body, "\nSeam session: "); i >= 0 {
			suffix = body[i+1:] + "\n" + closeTag
			body = strings.TrimRight(body[:i], "\r\n")
		}
	}

	separator := "\n" + contextTruncationMarker + "\n"
	available := maxBytes - len(separator) - len(suffix)
	if available <= 0 {
		// The production ceiling has ample room for the fixed tags and bounded
		// ambient name. Keep a defensive rune-safe fallback for direct unit use.
		return runeSafePrefix(content, maxBytes)
	}
	prefix := runeSafePrefix(body, available)
	if len(prefix) < len(body) {
		// Generated contexts are line-oriented. Avoid leaving a partial optional
		// line when a nearby complete line fits, while retaining enough prefix to
		// preserve the opening tag and priority section.
		if i := strings.LastIndex(prefix, "\n"); i > len(prefix)/2 {
			prefix = prefix[:i]
		}
	}
	prefix = strings.TrimRight(prefix, "\r\n")
	return prefix + separator + suffix
}

func runeSafePrefix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
