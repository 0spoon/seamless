package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPctOf(t *testing.T) {
	require.Equal(t, 0, pctOf(0, 0))
	require.Equal(t, 0, pctOf(1, 0))
	require.Equal(t, 50, pctOf(1, 2))
	require.Equal(t, 100, pctOf(3, 3))
	require.Equal(t, 33, pctOf(1, 3))
}

func TestShortIDAndDash(t *testing.T) {
	require.Equal(t, "01KX7R4H", shortID("01KX7R4H6HZEAK1MX7ARTQVFNJ"))
	require.Equal(t, "short", shortID("short"))
	require.Equal(t, "-", orDash(""))
	require.Equal(t, "seamless", orDash("seamless"))
}

func TestAgoShort(t *testing.T) {
	require.Equal(t, "-", agoShort(time.Time{}))
	require.Equal(t, "now", agoShort(time.Now()))
	require.Equal(t, "2h", agoShort(time.Now().Add(-2*time.Hour)))
	require.Equal(t, "3d", agoShort(time.Now().Add(-72*time.Hour)))
}
