package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a clean published release number, major.minor.patch. It is the
// only form a version takes anywhere this package renders text: a value that
// did not parse into one never reaches a notice.
type Version struct {
	Major, Minor, Patch int
}

// Parse extracts a release version from s, tolerating surrounding space and a
// leading "v". A pre-release ("-...") or build ("+...") suffix means s is not
// a clean published release -- the 0.0.0-dev sentinel, a 0.3.4-SNAPSHOT-<sha>
// goreleaser build, a pseudo-version, an -rc -- so it reports ok=false rather
// than compare a partial number and call a dev build "up to date".
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if strings.ContainsAny(s, "+-") {
		return Version{}, false
	}
	fields := strings.Split(s, ".")
	if len(fields) != 3 {
		return Version{}, false
	}
	var out [3]int
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return Version{}, false
		}
		out[i] = n
	}
	return Version{Major: out[0], Minor: out[1], Patch: out[2]}, true
}

// ParseTag is Parse for a git tag, held to the exact spelling a release tag
// has: a "v" followed by the canonical number, so "v0.7.3" parses and
// "0.7.3", "v0.07.3" and " v0.7.3" do not. The release list filter uses it,
// which keeps an oddly spelled tag from ever being compared or shown.
func ParseTag(tag string) (Version, bool) {
	rest, ok := strings.CutPrefix(tag, "v")
	if !ok {
		return Version{}, false
	}
	v, ok := Parse(rest)
	if !ok || v.String() != rest {
		return Version{}, false
	}
	return v, true
}

// String renders the version as "major.minor.patch", without a "v".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// IsZero reports whether v is the zero Version (no version at all).
func (v Version) IsZero() bool { return v == Version{} }

// Compare returns -1, 0 or 1 as v is older than, equal to, or newer than o.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		switch {
		case d < 0:
			return -1
		case d > 0:
			return 1
		}
	}
	return 0
}

// Compare compares two version strings by their numeric major.minor.patch,
// returning -1/0/1 (a<b / a==b / a>b). ok is false when either side is not a
// clean published release (see Parse), in which case the result is
// meaningless and callers report a development build instead.
func Compare(a, b string) (int, bool) {
	av, aok := Parse(a)
	bv, bok := Parse(b)
	if !aok || !bok {
		return 0, false
	}
	return av.Compare(bv), true
}

// MarshalText renders the version for JSON (state.json) as "0.7.3".
func (v Version) MarshalText() ([]byte, error) {
	return []byte(v.String()), nil
}

// UnmarshalText reads a version written by MarshalText. Anything that does not
// parse is an error, so a hand-edited or corrupt state file cannot smuggle a
// free-text "version" into a notice.
func (v *Version) UnmarshalText(b []byte) error {
	parsed, ok := Parse(string(b))
	if !ok {
		return fmt.Errorf("update: invalid version %q", string(b))
	}
	*v = parsed
	return nil
}
