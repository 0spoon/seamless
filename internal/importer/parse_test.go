package importer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

func TestSlugify(t *testing.T) {
	require.Equal(t, "backup-strategy", slugify("backup-strategy"))
	require.Equal(t, "sync-sh-windows-ssh-pitfalls", slugify("sync.sh Windows SSH pitfalls"))
	require.Equal(t, "projd-hands-off-parallel-agent-worktree-pitfalls",
		slugify("projd-hands-off: parallel-agent worktree pitfalls"))
	require.Equal(t, "nrf54l15-2-4ghz-antenna", slugify("nRF54L15 2.4GHz antenna"))
	require.Equal(t, "untitled", slugify("!!!"))
	require.LessOrEqual(t, len(slugify(veryLong)), 80)
}

const veryLong = "this is an extremely long memory name that keeps going well past the eighty character filesystem-safe limit we impose"

func TestParseV1WithUnquotedTimestamps(t *testing.T) {
	content := `---
id: 01KS8T9F00QCE517J0TTP70B5P
title: 'Knowledge: runbook - backup-strategy'
description: 'backup desc'
project: agent-memory
tags: ['created-by:agent', 'domain:runbook', 'project:arctop-backend', 'type:knowledge']
created: 2026-05-22T21:43:31Z
modified: 2026-07-09T18:46:48Z
---
body line one
body line two
`
	fm, body, err := parseV1(content)
	require.NoError(t, err)
	require.Equal(t, "01KS8T9F00QCE517J0TTP70B5P", fm.ID)
	require.Equal(t, "Knowledge: runbook - backup-strategy", fm.Title)
	require.Equal(t, "2026-05-22T21:43:31Z", fm.Created)
	require.Equal(t, "body line one\nbody line two\n", body)
}

func TestClassify(t *testing.T) {
	require.Equal(t, classMemory, classify(v1Frontmatter{Title: "Knowledge: gotcha - x"}))
	require.Equal(t, classTrial, classify(v1Frontmatter{Title: "Trial: y"}))
	require.Equal(t, classTrial, classify(v1Frontmatter{Title: "z", Tags: []string{"type:trial"}}))
	require.Equal(t, classNote, classify(v1Frontmatter{Title: "Landscape scan"}))
}

func TestMemoryFromV1(t *testing.T) {
	fm := v1Frontmatter{
		ID:          "01KS8T9F00QCE517J0TTP70B5P",
		Title:       "Knowledge: gotcha - sync.sh Windows SSH pitfalls",
		Description: "desc",
		Project:     "agent-memory",
		Tags:        []string{"created-by:agent", "domain:gotcha", "project:mw75-neuro-firmware", "session:cc/abc123", "type:knowledge", "related:01ABC"},
		Created:     "2026-05-22T21:43:31Z",
		Modified:    "2026-07-09T18:46:48Z",
	}
	m, warn := memoryFromV1(fm, "the body")
	require.Empty(t, warn)
	require.Equal(t, core.KindGotcha, m.Kind)
	require.Equal(t, "sync-sh-windows-ssh-pitfalls", m.Name)
	require.Equal(t, "mw75-neuro-firmware", m.Project) // from project: tag, not storage bucket
	require.Equal(t, "cc/abc123", m.SourceSession)
	require.Equal(t, "the body", m.Body)
	require.Equal(t, []string{"created-by:agent", "related:01ABC"}, m.Tags) // structural tags stripped
	require.False(t, m.Created.IsZero())
	require.Equal(t, m.Created, m.ValidFrom)
}

func TestMemoryFromV1UnknownKindFallsBack(t *testing.T) {
	fm := v1Frontmatter{
		ID:    "01AAA",
		Title: "Knowledge: mystery - some-thing",
		Tags:  []string{"type:knowledge"},
	}
	m, warn := memoryFromV1(fm, "b")
	require.Equal(t, core.KindReference, m.Kind)
	require.Contains(t, warn, "unknown kind")
}

func TestMemoryFromV1KindFromDomainTagWhenTitleLacksSeparator(t *testing.T) {
	fm := v1Frontmatter{
		ID:    "01BBB",
		Title: "Knowledge: constraint-without-separator",
		Tags:  []string{"domain:constraint"},
	}
	m, _ := memoryFromV1(fm, "b")
	require.Equal(t, core.KindConstraint, m.Kind)
}

func TestTrialFromV1(t *testing.T) {
	body := "# Trial: x\n\n**Lab:** mw75-firmware-ble\n**Outcome:** pending\n\n## Changesfeat/foo did things\n\n## Expected\nit works\n\n## Actual\nit did not\n\n## Notes\nmore\n"
	fm := v1Frontmatter{
		ID:      "01TRIAL",
		Title:   "Trial: GATT over BR/EDR go/no-go",
		Tags:    []string{"lab:mw75-firmware-ble", "type:trial"},
		Created: "2026-07-07T23:25:00Z",
	}
	tr := trialFromV1(fm, body, "research")
	require.Equal(t, "mw75-firmware-ble", tr.Lab)
	require.Equal(t, "GATT over BR/EDR go/no-go", tr.Title)
	require.Equal(t, core.TrialOutcome("pending"), tr.Outcome)
	require.Equal(t, "feat/foo did things", tr.Changes)
	require.Equal(t, "it works", tr.Expected)
	require.Equal(t, "it did not", tr.Actual)
	require.Equal(t, "research", tr.ProjectSlug)
}

func TestNoteFromV1(t *testing.T) {
	fm := v1Frontmatter{
		ID:               "01NOTE",
		Title:            "Landscape scan",
		Description:      "survey",
		Tags:             []string{"created-by:agent"},
		SourceURL:        "https://x",
		TranscriptSource: true,
		Created:          "2026-06-18T20:30:31Z",
	}
	n := noteFromV1(fm, "note body", "research", "landscape-scan")
	require.Equal(t, "research", n.Project)
	require.Equal(t, "landscape-scan", n.Slug)
	require.Equal(t, "https://x", n.SourceURL)
	require.Equal(t, true, n.Extra["transcript_source"])
}
