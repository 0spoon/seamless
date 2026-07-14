package main

// Terminal formatting shared by every seam subcommand.

import (
	"fmt"
	"time"
)

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pctOf(n, d float64) int {
	if d == 0 {
		return 0
	}
	return int(n/d*100 + 0.5)
}

// agoShort renders a compact age for the CLI (mirrors the console's ago helper).
func agoShort(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
