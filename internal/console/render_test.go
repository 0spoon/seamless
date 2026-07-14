package console

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTS formats a title= tooltip timestamp as a clean minute-precision UTC
// stamp, and renders "" for a nil/zero time so the attribute stays empty.
func TestTS(t *testing.T) {
	tm := time.Date(2026, 7, 14, 7, 29, 45, 123456789, time.UTC)
	require.Equal(t, "2026-07-14 07:29 UTC", ts(tm))

	// A pointer is dereferenced; nil and zero render empty.
	require.Equal(t, "2026-07-14 07:29 UTC", ts(&tm))
	require.Equal(t, "", ts((*time.Time)(nil)))
	require.Equal(t, "", ts(time.Time{}))

	// A non-time value is ignored rather than panicking.
	require.Equal(t, "", ts("not a time"))

	// Non-UTC input is normalized to UTC.
	loc := time.FixedZone("PST", -8*3600)
	require.Equal(t, "2026-07-14 15:29 UTC", ts(time.Date(2026, 7, 14, 7, 29, 0, 0, loc)))
}

// TestShortID uses the last 8 chars so recent ULIDs (shared timestamp prefix)
// stay distinguishable, matching the Interactions client's id.slice(-8).
func TestShortID(t *testing.T) {
	require.Equal(t, "ABCDEFGH", shortID("01KXFM0000ABCDEFGH"))
	require.Equal(t, "short", shortID("short")) // <=8 returned as-is
}
