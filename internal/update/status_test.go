package update

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// injectionScrub is a copy of retrieve's injectionRe: the briefing passes every
// notice through sanitizeField, which deletes from any of these words to the
// end of the line. A notice that matched would arrive amputated, so none may.
var injectionScrub = regexp.MustCompile(`(?i)(^|[^\w-])(?:ignore|disregard|from now on|you must|override)\b[^\n]*`)

func noticeStatus(now time.Time) Status {
	return Status{
		Version:      "0.7.2",
		Distribution: DistributionRelease,
		Release:      true,
		Install:      Install{Kind: KindHomebrew, Reason: "installed by Homebrew", Hint: Hint(KindHomebrew)},
		Settings:     Settings{Check: true, CheckSource: SourceDefault, CheckInterval: 6 * time.Hour},
		Newest:       &Release{Version: Version{0, 7, 3}, PublishedAt: now.Add(-30 * time.Hour)},
		Available:    true,
		SeenAt:       now.Add(-2 * time.Hour),
	}
}

func TestNotice(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	const local = "studio"
	tests := []struct {
		name   string
		mutate func(s *Status)
		host   string
		want   string // exact; "" for no notice
	}{
		{"available, local session", nil, local,
			"Seamless v0.7.3 is available (running v0.7.2). Owner action, not a task for this session: brew upgrade --cask arctop/tap/seamless"},
		{"available, host unnamed is local", nil, "",
			"Seamless v0.7.3 is available (running v0.7.2). Owner action, not a task for this session: brew upgrade --cask arctop/tap/seamless"},
		{"available, host compares case-insensitively", nil, "STUDIO",
			"Seamless v0.7.3 is available (running v0.7.2). Owner action, not a task for this session: brew upgrade --cask arctop/tap/seamless"},
		{"available, remote session", nil, "laptop",
			"The Seamless server can update to v0.7.3 (it runs v0.7.2). Owner action on the server, not a task for this session: brew upgrade --cask arctop/tap/seamless"},
		{"installer kind still only hears about it", func(s *Status) {
			s.Install = Install{Kind: KindInstaller, Hint: Hint(KindInstaller)}
		}, local, "Seamless v0.7.3 is available (running v0.7.2). Owner action, not a task for this session: seamlessd update"},
		{"a week after first seen, sessions stop hearing", func(s *Status) { s.SeenAt = now.Add(-7 * 24 * time.Hour) }, local, ""},
		{"checks off", func(s *Status) { s.Settings.Check = false }, local, ""},
		{"up to date", func(s *Status) { s.Available = false }, local, ""},
		{"never checked", func(s *Status) { s.Newest = nil }, local, ""},
		{"dev build", func(s *Status) { s.Version = "0.0.0-dev" }, local, ""},
		{"updated within a day, local", func(s *Status) {
			s.Updated = &Updated{From: Version{0, 7, 1}, To: Version{0, 7, 2}, At: now.Add(-5 * time.Hour), Direction: DirectionUpgrade}
		}, local, "Seamless updated to v0.7.2 (from v0.7.1) 5h ago. Release notes: https://github.com/arctop/seamless/releases/tag/v0.7.2"},
		{"updated within a day, remote", func(s *Status) {
			s.Updated = &Updated{From: Version{0, 7, 1}, To: Version{0, 7, 2}, At: now.Add(-30 * time.Minute), Direction: DirectionUpgrade}
		}, "laptop", "The Seamless server updated to v0.7.2 (from v0.7.1) 30m ago. The Seamless client on this machine updates separately; owner action, not a task for this session."},
		{"updated more than a day ago falls through to available", func(s *Status) {
			s.Updated = &Updated{From: Version{0, 7, 1}, To: Version{0, 7, 2}, At: now.Add(-25 * time.Hour), Direction: DirectionUpgrade}
		}, local, "Seamless v0.7.3 is available (running v0.7.2). Owner action, not a task for this session: brew upgrade --cask arctop/tap/seamless"},
		{"a downgrade is not announced", func(s *Status) {
			s.Updated = &Updated{From: Version{0, 7, 3}, To: Version{0, 7, 2}, At: now.Add(-time.Hour), Direction: DirectionDowngrade}
			s.Available = false
		}, local, ""},
		{"updated notice shows even with checks off", func(s *Status) {
			s.Settings.Check = false
			s.Updated = &Updated{From: Version{0, 7, 1}, To: Version{0, 7, 2}, At: now.Add(-time.Minute), Direction: DirectionUpgrade}
		}, local, "Seamless updated to v0.7.2 (from v0.7.1) 1m ago. Release notes: https://github.com/arctop/seamless/releases/tag/v0.7.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := noticeStatus(now)
			if tt.mutate != nil {
				tt.mutate(&s)
			}
			got := s.Notice(tt.host, local, now)
			require.Equal(t, tt.want, got)
			// Every notice survives the briefing's sanitizer untouched.
			require.NotContains(t, got, "\n")
			require.False(t, injectionScrub.MatchString(got), "sanitizeField would cut %q", got)
		})
	}
}

// TestNotice_EveryHintSurvivesTheSanitizer renders the available line for every
// install kind, local and remote.
func TestNotice_EveryHintSurvivesTheSanitizer(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, k := range []Kind{KindInstaller, KindHomebrew, KindSource, KindClient, KindUnknown} {
		for _, host := range []string{"", "remote"} {
			s := noticeStatus(now)
			s.Install = Install{Kind: k, Hint: Hint(k)}
			got := s.Notice(host, "", now)
			require.NotEmpty(t, got)
			require.False(t, injectionScrub.MatchString(got), "sanitizeField would cut %q", got)
			require.True(t, strings.HasSuffix(got, Hint(k)))
		}
	}
}

func TestMode(t *testing.T) {
	tests := []struct {
		name     string
		settings Settings
		release  bool
		install  Install
		want     Mode
		reason   string
	}{
		{"locked off", Settings{CheckLocked: true, CheckSource: SourceConfig}, true, Install{}, ModeOff, "config file"},
		{"console off", Settings{CheckSource: SourceConsole}, true, Install{}, ModeOff, "console"},
		{"source default off", Settings{CheckSource: SourceDefault}, false, Install{}, ModeOff, "from source"},
		{"snapshot default off", Settings{CheckSource: SourceDefault}, false, Install{}, ModeOff, "snapshot"},
		{"installer notify", Settings{Check: true}, true, Install{Kind: KindInstaller, Hint: "seamlessd update"}, ModeNotify, "your call (seamlessd update)"},
		{"homebrew notify", Settings{Check: true}, true, Install{Kind: KindHomebrew, Reason: "installed by Homebrew"}, ModeNotify,
			"told about new releases, never updated unattended: installed by Homebrew"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := Status{Settings: tt.settings, Release: tt.release, Install: tt.install}
			if tt.name == "snapshot default off" {
				st.Distribution = DistributionRelease
			}
			m, reason := st.Mode()
			require.Equal(t, tt.want, m)
			require.Contains(t, reason, tt.reason)
		})
	}
}

func TestCompactAge(t *testing.T) {
	require.Equal(t, "1m", compactAge(10*time.Second))
	require.Equal(t, "45m", compactAge(45*time.Minute))
	require.Equal(t, "5h", compactAge(5*time.Hour+59*time.Minute))
	require.Equal(t, "3d", compactAge(3*24*time.Hour+time.Hour))
}
