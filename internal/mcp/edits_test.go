package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyEdits_UniqueMatchReplacesOnce(t *testing.T) {
	out, err := applyEdits("alpha\nbeta\ngamma\n", []edit{{Old: "beta", New: "BETA"}})
	require.NoError(t, err)
	require.Equal(t, "alpha\nBETA\ngamma\n", out)
}

// The unique-or-fail guard is the whole safety story: an ambiguous old_string
// must refuse with the count, not silently pick the first match.
func TestApplyEdits_AmbiguousMatchRefusesAndCounts(t *testing.T) {
	_, err := applyEdits("x\nx\nx\n", []edit{{Old: "x", New: "y"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "matches 3 places")
	require.Contains(t, err.Error(), "replace_all")
}

func TestApplyEdits_ReplaceAllChangesEveryOccurrence(t *testing.T) {
	out, err := applyEdits("x\nx\nx\n", []edit{{Old: "x", New: "y", ReplaceAll: true}})
	require.NoError(t, err)
	require.Equal(t, "y\ny\ny\n", out)
}

func TestApplyEdits_NoMatchRefuses(t *testing.T) {
	_, err := applyEdits("alpha\n", []edit{{Old: "missing", New: "z"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// All-or-nothing: a later edit that cannot match must discard the earlier ones,
// so a partially-applied body never reaches the file.
func TestApplyEdits_LaterFailureDiscardsEarlierEdits(t *testing.T) {
	_, err := applyEdits("alpha\nbeta\n", []edit{
		{Old: "alpha", New: "ALPHA"},
		{Old: "nowhere", New: "x"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "edits[1]")
}

// Edits apply in order against the running result, which is what lets an agent
// express two changes to overlapping regions as a sequence.
func TestApplyEdits_SequentialEditsSeeEarlierResults(t *testing.T) {
	out, err := applyEdits("one\n", []edit{
		{Old: "one", New: "two"},
		{Old: "two", New: "three"},
	})
	require.NoError(t, err)
	require.Equal(t, "three\n", out)
}

// An empty new_string is a deletion, not a rejected edit.
func TestApplyEdits_EmptyNewStringDeletes(t *testing.T) {
	out, err := applyEdits("keep\ndrop\n", []edit{{Old: "drop\n", New: ""}})
	require.NoError(t, err)
	require.Equal(t, "keep\n", out)
}

// A set of edits that cancel out is refused rather than written: an unchanged
// write would bump the timestamp and record an event for nothing.
func TestApplyEdits_NetZeroChangeRefuses(t *testing.T) {
	_, err := applyEdits("a b\n", []edit{
		{Old: "a", New: "q"},
		{Old: "q", New: "a"},
	})
	require.ErrorContains(t, err, "no change")
}

// Whitespace is part of an exact match, so an edit that differs only in
// indentation must miss rather than be helpfully trimmed into a hit.
func TestApplyEdits_MatchIsExactIncludingIndentation(t *testing.T) {
	_, err := applyEdits("    indented\n", []edit{{Old: "indented\nmore", New: "x"}})
	require.Error(t, err)
}

func TestUnifiedDiff_ShowsAddedAndRemovedLines(t *testing.T) {
	d := unifiedDiff("alpha\nbeta\ngamma\n", "alpha\nBETA\ngamma\n")
	require.Contains(t, d, "-beta")
	require.Contains(t, d, "+BETA")
	require.Contains(t, d, " alpha")
	require.Contains(t, d, "@@")
}

func TestUnifiedDiff_UnchangedBodyRendersNothing(t *testing.T) {
	require.Empty(t, unifiedDiff("same\n", "same\n"))
}

// The diff is a convenience on a 1MB transport; a huge rewrite must be cut with
// a marker, never silently shortened into something that reads as complete.
func TestUnifiedDiff_TruncatesWithAMarker(t *testing.T) {
	before := strings.Repeat("old line of text\n", 2000)
	after := strings.Repeat("new line of text\n", 2000)
	d := unifiedDiff(before, after)
	require.Contains(t, d, "diff truncated")
	require.Less(t, len([]rune(d)), maxEditDiffRunes+200)
}

func TestUnifiedDiff_AdditionToAnEmptyBody(t *testing.T) {
	d := unifiedDiff("", "first\n")
	require.Contains(t, d, "+first")
}
