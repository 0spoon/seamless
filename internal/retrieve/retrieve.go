// Package retrieve assembles what an agent sees from its memory: the
// session-start briefing, the user-prompt-submit recall block, and the recall
// tool's fused search. It reads the store index and (when an embedder is set)
// the vector store, budgets output by estimated tokens, and sanitizes every
// interpolated field against prompt injection before it reaches an agent.
package retrieve

import (
	"database/sql"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/llm"
)

// Service assembles briefings, prompt recall, and fused recall over one store.
type Service struct {
	db       *sql.DB
	embedder llm.Embedder // nil => lexical-only (FTS); recall degrades gracefully
	budgets  config.Budgets
	logger   *slog.Logger

	corpus *corpusCache // prompt-matcher IDF corpus, cached per project scope
}

// New builds a retrieval Service. embedder may be nil, in which case recall uses
// FTS only and the semantic paths are skipped.
func New(db *sql.DB, embedder llm.Embedder, budgets config.Budgets, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db:       db,
		embedder: embedder,
		budgets:  budgets,
		logger:   logger,
		corpus:   newCorpusCache(),
	}
}

// injectionRe strips imperative prompt-injection phrases from any field lifted
// out of stored content and shown to an agent as trusted context.
var injectionRe = regexp.MustCompile(`(?i)\b(ignore|disregard|from now on|you must|override)\b[^\n]*`)

// sanitizeField scrubs a single field for safe interpolation into a briefing:
// newlines flattened, injection phrases removed, whitespace collapsed, and the
// result capped at maxRunes (with an ellipsis). maxRunes <= 3 disables the cap.
func sanitizeField(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = injectionRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes > 3 && utf8.RuneCountInString(s) > maxRunes {
		r := []rune(s)
		s = string(r[:maxRunes-3]) + "..."
	}
	return s
}

// estTokens is a cheap token estimate (~4 chars/token) used to budget briefing
// and recall output without a tokenizer dependency.
func estTokens(s string) int { return (len(s) + 3) / 4 }

// humanAge renders how long ago t was, compactly (e.g. "3d", "5h", "just now").
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}
