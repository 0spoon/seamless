package core

import (
	"fmt"
	"time"
)

// TimeFormat is the canonical timestamp layout for all TEXT timestamp columns.
// It is fixed-width in UTC (always 9 fractional digits, always "Z"), so string
// comparison of stored timestamps matches chronological order.
const TimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// FormatTime renders t as a canonical UTC timestamp string.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// ParseTime parses a canonical timestamp. An empty string yields the zero time.
// It also accepts plain RFC3339 for values written by other tools (e.g. v1 data
// during import).
func ParseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(TimeFormat, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("core.ParseTime: %w", err)
	}
	return t.UTC(), nil
}
