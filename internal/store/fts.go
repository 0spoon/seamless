package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// FTSSearch runs a full-text query over the unified fts table and returns hits
// ordered best-first. Scores are the negated bm25 rank (higher = better) so they
// share the "bigger is better" convention with CosineSearch. An empty kinds
// filter searches all kinds. A query with no usable terms yields no hits (not an
// error), so recall degrades quietly on punctuation-only input.
func FTSSearch(ctx context.Context, db *sql.DB, query string, kinds []string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}

	sqlStr := `SELECT item_id, kind, bm25(fts) AS rank FROM fts WHERE fts MATCH ?`
	args := []any{match}
	if len(kinds) > 0 {
		sqlStr += ` AND kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	sqlStr += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("store.FTSSearch: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var hits []SearchHit
	for rows.Next() {
		var (
			itemID, kind string
			rank         float64
		)
		if err := rows.Scan(&itemID, &kind, &rank); err != nil {
			return nil, fmt.Errorf("store.FTSSearch: scan: %w", err)
		}
		// bm25 returns lower (more negative) for better matches; negate so
		// higher is better, matching SearchHit's convention.
		hits = append(hits, SearchHit{ItemID: itemID, Kind: kind, Score: -rank})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.FTSSearch: %w", err)
	}
	return hits, nil
}

// ftsQuery builds a safe FTS5 MATCH expression from free user text: it splits on
// non-alphanumeric runes, drops single-character tokens, double-quotes each
// remaining token (so FTS5 treats it as a literal term, never an operator or a
// column filter), and ORs them. Returns "" when no usable token remains, which
// callers treat as "no results" rather than a syntax error. This is what keeps a
// query like "chroma-boot-race" from being parsed as a subtraction.
func ftsQuery(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 2 {
			continue
		}
		terms = append(terms, `"`+f+`"`)
	}
	return strings.Join(terms, " OR ")
}
