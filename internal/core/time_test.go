package core

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFormatParseRoundTrip(t *testing.T) {
	original := time.Date(2026, 7, 10, 18, 30, 45, 123456789, time.UTC)
	s := FormatTime(original)
	parsed, err := ParseTime(s)
	require.NoError(t, err)
	require.True(t, original.Equal(parsed))
}

func TestFormatTime_NormalizesToUTC(t *testing.T) {
	loc := time.FixedZone("PST", -8*3600)
	local := time.Date(2026, 7, 10, 10, 0, 0, 0, loc)
	s := FormatTime(local)
	require.True(t, len(s) > 0 && s[len(s)-1] == 'Z', "canonical form is UTC/Z: %s", s)

	parsed, err := ParseTime(s)
	require.NoError(t, err)
	require.True(t, local.Equal(parsed))
}

func TestParseTime_EmptyIsZero(t *testing.T) {
	got, err := ParseTime("")
	require.NoError(t, err)
	require.True(t, got.IsZero())
}

func TestParseTime_AcceptsRFC3339(t *testing.T) {
	got, err := ParseTime("2026-07-10T18:00:00Z")
	require.NoError(t, err)
	require.Equal(t, 2026, got.Year())
	require.Equal(t, 18, got.Hour())
}

// Canonical timestamps must sort lexically in chronological order (fixed width).
func TestFormatTime_LexicallySortable(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 7, 10, 9, 0, 0, 5, time.UTC),
		time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(2026, 7, 10, 9, 0, 1, 0, time.UTC),
	}
	strs := make([]string, len(times))
	for i, tm := range times {
		strs[i] = FormatTime(tm)
		require.Len(t, strs[i], len(strs[0]), "all canonical timestamps are the same width")
	}
	sorted := append([]string(nil), strs...)
	sort.Strings(sorted)

	// The chronological order of the inputs.
	require.Equal(t, FormatTime(times[2]), sorted[0]) // 2025
	require.Equal(t, FormatTime(times[1]), sorted[1]) // 9:00:00.000000000
	require.Equal(t, FormatTime(times[0]), sorted[2]) // 9:00:00.000000005
	require.Equal(t, FormatTime(times[3]), sorted[3]) // 9:00:01
}
