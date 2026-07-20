package validate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"simple", "notes/foo.md", false},
		{"nested", "memory/seam/chroma-boot-race.md", false},
		{"dot-prefix", "./foo.md", false},
		{"empty", "", true},
		{"absolute", "/etc/passwd", true},
		{"traversal", "../secret", true},
		{"traversal-middle", "notes/../../etc/passwd", true},
		{"null-byte", "foo\x00bar", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Path(tt.path)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPathWithinDir(t *testing.T) {
	base := "/data/notes"
	tests := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"within", "inbox/foo.md", false},
		{"root", ".", false},
		{"escape", "../etc/passwd", true},
		{"absolute", "/etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PathWithinDir(tt.rel, base)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantErr bool
	}{
		{"simple", "My Note", false},
		{"slash", "TCP/IP notes", false},
		{"ab-testing", "A/B testing", false},
		// Regression: date-range titles with ".." must be accepted (v1 rejected
		// these, causing a 37% notes_create error rate).
		{"date-range-dotdot", "summary 2026-07-05..08", false},
		{"leading-dotdot", "..hidden thoughts", false},
		{"empty", "", true},
		{"null-byte", "bad\x00title", true},
		{"backslash", "path\\to\\thing", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Title(tt.title)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestTitleLength(t *testing.T) {
	long := make([]byte, 256)
	for i := range long {
		long[i] = 'a'
	}
	require.Error(t, Title(string(long)))
	require.NoError(t, Title(string(long[:255])))
}

func TestName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "chroma-boot-race", false},
		{"underscored", "f5_ssh_signing", false},
		// The ".." fix applies to Title only: Name still rejects it, since Name
		// values feed filenames directly.
		{"date-range-dotdot", "summary 2026-07-05..08", true},
		{"slash", "a/b", true},
		{"backslash", "a\\b", true},
		{"empty", "", true},
		{"null-byte", "bad\x00name", true},

		// Audit I11: a leading dot hides the file from the owner reviewing
		// their own corpus, and "." alone yields the alarming-looking "..md".
		{"leading-dot", ".hidden", true},
		{"bare-dot", ".", true},
		{"interior-dot-is-fine", "v1.2-notes", false},

		// Audit I11: Windows resolves these as devices in every directory, so
		// the file cannot exist there. Rejected everywhere, because the corpus
		// is markdown that gets synced and cloned across machines.
		{"reserved-con", "con", true},
		{"reserved-uppercase", "CON", true},
		{"reserved-with-extension", "nul.md", true},
		{"reserved-com1", "COM1", true},
		{"reserved-lpt9", "lpt9", true},
		{"reserved-prefix-only-is-fine", "console-layout", false},
		{"reserved-substring-is-fine", "auxiliary", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Name(tt.input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
