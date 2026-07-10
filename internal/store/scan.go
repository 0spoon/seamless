package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/0spoon/seamless/internal/core"
)

// decodeTags decodes a JSON tags array as stored in the *_index.tags column. A
// blank, "[]", or invalid value yields nil.
func decodeTags(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return nil
	}
	return tags
}

// nullTime parses a nullable canonical timestamp, returning the zero time when
// the column is NULL or empty.
func nullTime(ns sql.NullString) (time.Time, error) {
	if !ns.Valid {
		return time.Time{}, nil
	}
	return core.ParseTime(ns.String)
}

// nullTimePtr parses a nullable timestamp into a *time.Time: nil when the column
// is NULL (or empty), otherwise a pointer to the parsed value.
func nullTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := core.ParseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
