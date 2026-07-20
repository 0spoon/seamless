package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// memoryName is what makes memory_write and memory_read agree on identity. The
// audit item (I12) was consistency with notes_create, which slugifies; before
// this, an agent could write "My Gotcha" and then fail to read it back under
// the name it had just used.
func TestMemoryName_Canonicalizes(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"chroma-boot-race", "chroma-boot-race"},
		{"My Gotcha", "my-gotcha"},
		{"  padded  ", "padded"},
		{"Mixed_Case Name", "mixed-case-name"},
		{"double--dash", "double-dash"},
		{"", ""}, // empty stays empty; the handlers report it as a missing arg
	} {
		got, err := memoryName(tc.in)
		require.NoError(t, err, "input %q", tc.in)
		require.Equal(t, tc.want, got, "input %q", tc.in)
	}
}

// Write and read must land on the same canonical name, whatever casing or
// spacing each call used.
func TestMemoryName_WriteAndReadFormsAgree(t *testing.T) {
	written, err := memoryName("Chroma Boot Race")
	require.NoError(t, err)
	read, err := memoryName("chroma-boot-race")
	require.NoError(t, err)
	require.Equal(t, written, read)
}

// Slugging cannot rescue a Windows reserved device name -- "con" is already
// lowercase and dash-free -- so the validate pass has to catch it, and the
// error has to arrive at the tool boundary rather than deep in a file write.
func TestMemoryName_RejectsReservedName(t *testing.T) {
	_, err := memoryName("CON")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid memory name")
	require.Contains(t, err.Error(), "reserved device name")
}
