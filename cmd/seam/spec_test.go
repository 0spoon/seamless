package main

import (
	"context"
	"flag"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/core"
)

// probeOpts mirrors what the real commands bind: a plain string flag, a bool, a
// constrained enum, and a positive int.
type probeOpts struct {
	project *string
	status  *string
	lease   *int
	force   *bool
}

func bindProbe(fs *flag.FlagSet) *probeOpts {
	return &probeOpts{
		project: fs.String("project", "", "project `SLUG`"),
		status:  enumFlag(fs, "status", "", "filter: `STATUS`", enumOf(core.TaskStatuses)),
		lease:   posIntFlag(fs, "lease", 0, "lease `SECS` before the claim lapses"),
		force:   fs.Bool("force", false, "owner override"),
	}
}

func probeRun(context.Context, *env, *probeOpts, []string) error { return nil }

// probeTable is the fixture command table. parse and lookup take the table as a
// parameter precisely so tests own theirs while commands.go owns the real one.
func probeTable() []cmd {
	return []cmd{
		spec("capture", "agent loop", "capture a web page as a note",
			exactly(1, "url"), bindProbe, probeRun),
		spec("recall", "agent loop", "search memories and notes",
			atLeast(1, "word"), bindProbe, probeRun),
		spec("task list", "tasks", "list tasks",
			noArgs().withHint("to load one task by id, use --id"), bindProbe, probeRun),
		spec("task done", "tasks", "close a task",
			exactly(1, "id"), bindProbe, probeRun),
		spec("sessions", "observability", "list sessions",
			between(0, 1, "id"), bindProbe, probeRun),
	}
}

// parseProbe runs args through a flag set shaped like the real commands',
// silenced the way parse silences its own.
func parseProbe(t *testing.T, args []string) (*probeOpts, []string, error) {
	t.Helper()
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	o := bindProbe(fs)
	pos, err := parseArgs(fs, args)
	return o, pos, err
}

// TestParseArgs_Permutes is the headline behavior: flags bind whichever side of
// the positional they arrive on. Today Go's flag stops at the first positional,
// so "seam capture URL --project p" silently files the note to the wrong scope.
func TestParseArgs_Permutes(t *testing.T) {
	const url = "https://example.com"
	tests := []struct {
		name string
		args []string
	}{
		{"flags first", []string{"--project", "p", url}},
		{"flags last", []string{url, "--project", "p"}},
		{"equals form last", []string{url, "--project=p"}},
		{"single dash last", []string{url, "-project", "p"}},
		{"straddling the positional", []string{"--force", url, "--project", "p"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, pos, err := parseProbe(t, tt.args)
			require.NoError(t, err)
			require.Equal(t, "p", *o.project)
			require.Equal(t, []string{url}, pos)
		})
	}
}

// The loop re-Parses the remainder on every iteration. That is only sound because
// a repeated Parse never clears f.actual, so values bound by an earlier pass
// survive later ones.
func TestParseArgs_FlagsPersistAcrossIterations(t *testing.T) {
	o, pos, err := parseProbe(t, []string{"a", "--project", "p", "b", "--force", "c"})
	require.NoError(t, err)
	require.Equal(t, "p", *o.project)
	require.True(t, *o.force)
	require.Equal(t, []string{"a", "b", "c"}, pos)
}

// TestParseArgs_NegativeFlagValue pins the fact the permute loop rests on: flag's
// value consumption is unconditional (parseOne does not check for a leading
// dash), so "--n -5" binds -5 rather than reading -5 as a flag and resurfacing it
// as a positional. Uses a plain int flag: the subject here is flag's behavior,
// not posIntValue's policy.
func TestParseArgs_NegativeFlagValue(t *testing.T) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	n := fs.Int("n", 0, "a plain int")
	pos, err := parseArgs(fs, []string{"--n", "-5", "id"})
	require.NoError(t, err)
	require.Equal(t, -5, *n)
	require.Equal(t, []string{"id"}, pos)
}

// TestParseArgs_DoubleDashTerminator is the one case the terminator exists for,
// and it fails without the pre-split: flag consumes the "--" and stops, the next
// iteration starts a fresh Parse with no memory of it, and "-b" is read as an
// undefined flag.
func TestParseArgs_DoubleDashTerminator(t *testing.T) {
	_, pos, err := parseProbe(t, []string{"foo", "--", "-a", "-b"})
	require.NoError(t, err)
	require.Equal(t, []string{"foo", "-a", "-b"}, pos)
}

func TestParseArgs_DoubleDashAfterFlags(t *testing.T) {
	o, pos, err := parseProbe(t, []string{"--project", "p", "--", "-x", "--force"})
	require.NoError(t, err)
	require.Equal(t, "p", *o.project, "flags before the terminator still bind")
	require.False(t, *o.force, "--force after the terminator is a literal, not a flag")
	require.Equal(t, []string{"-x", "--force"}, pos)
}

// A bool flag consumes no value, so the token after it stays a positional. This
// is also why "[--force]" needs no placeholder in the generated synopsis.
func TestParseArgs_BoolConsumesNoValue(t *testing.T) {
	o, pos, err := parseProbe(t, []string{"--force", "id"})
	require.NoError(t, err)
	require.True(t, *o.force)
	require.Equal(t, []string{"id"}, pos)
}

// The reason permutation is safe here where a hand-rolled parser was not: flag
// errors on an unknown flag instead of swallowing it into the positionals. A
// typo'd "--projct p" is a parse error, not two extra search words.
func TestParseArgs_UnknownFlagErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"leading", []string{"--bogus"}},
		{"trailing", []string{"id", "--bogus"}},
		{"typo with a value", []string{"--projct", "typo"}},
		{"typo after the positional", []string{"query", "--projct", "typo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseProbe(t, tt.args)
			require.Error(t, err)
			require.Contains(t, err.Error(), "not defined")
		})
	}
}

// "--limit" with no value is an error rather than an empty string quietly
// becoming the default.
func TestParseArgs_FlagNeedsArgument(t *testing.T) {
	_, _, err := parseProbe(t, []string{"--project"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs an argument")
}

// flag.ErrHelp passes through the loop untouched, so the caller can answer
// "--help" with the help text and exit 0.
func TestParseArgs_HelpPassesThrough(t *testing.T) {
	_, _, err := parseProbe(t, []string{"--help"})
	require.ErrorIs(t, err, flag.ErrHelp)
}

func TestEnumValue_Set(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid", "in_progress", false},
		{"invalid", "bogus", true},
		{"empty", "", true},
		{"case sensitive", "OPEN", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, err := parseProbe(t, []string{"--status", tt.in})
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "valid values are")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.in, *o.status)
		})
	}
}

// The whole reason the enum is a flag.Value rather than a post-parse check: Set's
// message is the tail of flag's own wrapper, and the two compose into the house
// phrasing with no work at the call site.
func TestEnumValue_ComposesWithFlagsWrapper(t *testing.T) {
	_, _, err := parseProbe(t, []string{"--status", "bogus"})
	require.EqualError(t, err,
		`invalid value "bogus" for flag -status: valid values are open, in_progress, done, dropped`)
}

func TestPosIntValue_Set(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"positive", "60", 60, false},
		{"zero", "0", 0, true},
		{"negative", "-5", 0, true},
		{"non numeric", "abc", 0, true},
		{"empty", "", 0, true},
		{"out of range", "99999999999999999999", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, err := parseProbe(t, []string{"--lease", tt.in})
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "must be a positive integer")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, *o.lease)
		})
	}
}

// Why no "was it set?" plumbing is needed: 0 cannot be set explicitly, so a 0
// default can only mean absent and `if *lease > 0` (task.go:140) is correct
// rather than lossy.
func TestPosIntValue_ZeroDefaultMeansAbsent(t *testing.T) {
	o, _, err := parseProbe(t, nil)
	require.NoError(t, err)
	require.Zero(t, *o.lease)

	_, _, err = parseProbe(t, []string{"--lease", "0"})
	require.Error(t, err, "0 is unrepresentable as an explicit value, which is what makes it unambiguous")
}

func TestEnumOf_WidensCanonicalSets(t *testing.T) {
	require.Equal(t, []string{"open", "in_progress", "done", "dropped"}, enumOf(core.TaskStatuses))
	require.Equal(t, []string{"active", "completed", "expired"}, enumOf(core.SessionStatuses))
	require.Len(t, enumOf(core.MemoryKinds), len(core.MemoryKinds))
	require.Empty(t, enumOf([]core.TaskStatus{}))
}

func TestArity_Render(t *testing.T) {
	tests := []struct {
		name string
		a    arity
		want string
	}{
		{"none", noArgs(), ""},
		{"exactly one", exactly(1, "id"), "<id>"},
		{"exactly two", exactly(2, "src", "dst"), "<src> <dst>"},
		{"optional", between(0, 1, "id"), "[<id>]"},
		{"one or two", between(1, 2, "a", "b"), "<a> [<b>]"},
		{"unbounded", atLeast(1, "word"), "<word>..."},
		{"unbounded optional", atLeast(0, "word"), "[<word>...]"},
		{"unnamed", exactly(1), "<arg>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.a.render())
		})
	}
}

func TestArity_Check(t *testing.T) {
	tests := []struct {
		name string
		a    arity
		pos  []string
		want string // "" means no error
	}{
		{"none ok", noArgs(), nil, ""},
		{"none rejects", noArgs(), []string{"01K7ABCD"},
			`unexpected argument "01K7ABCD": seam probe takes no positional arguments`},
		{"exactly ok", exactly(1, "id"), []string{"x"}, ""},
		{"exactly too few", exactly(1, "id"), nil, "missing argument: seam probe requires <id>"},
		{"exactly too many", exactly(1, "id"), []string{"a", "b"},
			`unexpected argument "b": seam probe takes at most 1 positional argument`},
		{"exactly two too many", exactly(2, "a", "b"), []string{"a", "b", "c"},
			`unexpected argument "c": seam probe takes at most 2 positional arguments`},
		{"between ok empty", between(0, 1, "id"), nil, ""},
		{"between ok one", between(0, 1, "id"), []string{"x"}, ""},
		{"between too many", between(0, 1, "id"), []string{"a", "b"},
			`unexpected argument "b": seam probe takes at most 1 positional argument`},
		{"atLeast too few", atLeast(1, "word"), nil, "missing argument: seam probe requires <word>..."},
		{"atLeast unbounded", atLeast(1, "word"), []string{"a", "b", "c"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.a.check("probe", tt.pos)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.want)
		})
	}
}

// The plan's shape verbatim. The hint is per-spec because --id is not guessable
// from a generic message, and it is indented to line up under the message once
// main has printed that behind "error: ".
func TestArity_CheckIncludesTheHint(t *testing.T) {
	err := noArgs().withHint("to load one task by id, use --id").check("task list", []string{"01K7ABCD"})
	require.EqualError(t, err,
		`unexpected argument "01K7ABCD": seam task list takes no positional arguments`+
			"\n       (to load one task by id, use --id)")
}

func TestLookup_LongestNameWins(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantName string
		wantRest []string
	}{
		{"single word", []string{"capture", "url"}, "capture", []string{"url"}},
		{"two words", []string{"task", "list", "--project", "p"}, "task list", []string{"--project", "p"}},
		{"two words then a positional", []string{"task", "done", "id"}, "task done", []string{"id"}},
		{"nothing left", []string{"sessions"}, "sessions", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rest, ok := lookup(probeTable(), tt.argv)
			require.True(t, ok)
			require.Equal(t, tt.wantName, c.name)
			require.Equal(t, tt.wantRest, rest)
		})
	}
}

func TestLookup_Misses(t *testing.T) {
	// "task" alone misses: the fixture registers only "task list" and "task done".
	// A family with a default subcommand must register the bare name itself.
	for _, argv := range [][]string{nil, {}, {"bogus"}, {"task"}, {"task", "bogus"}} {
		_, _, ok := lookup(probeTable(), argv)
		require.False(t, ok, "%v", argv)
	}
}

func TestParse_BindsAndSplits(t *testing.T) {
	p, err := parse(probeTable(), []string{"capture", "--project", "p", "https://example.com"})
	require.NoError(t, err)
	require.Equal(t, "capture", p.cmd.name)
	o, ok := p.opts.(*probeOpts)
	require.True(t, ok)
	require.Equal(t, "p", *o.project)
	require.Equal(t, []string{"https://example.com"}, p.pos)
}

func TestParse_UnknownCommand(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"empty", nil, "unknown command"},
		{"unknown", []string{"bogus"}, `unknown command "bogus"`},
		{"unknown with flags", []string{"bogus", "--project", "p"}, `unknown command "bogus"`},
		{"bad subcommand of a real family", []string{"task", "bogus"},
			`unknown command "task bogus": valid values are task list, task done`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(probeTable(), tt.argv)
			require.ErrorIs(t, err, errUnknownCommand,
				"the caller answers this with the full help, not one command's synopsis")
			require.EqualError(t, err, tt.want)
		})
	}
}

// The regression this whole track exists for: `seam task list <id>` lists every
// task today, silently ignoring the argument.
func TestParse_ArityErrorNamesTheCommandAndTheHint(t *testing.T) {
	_, err := parse(probeTable(), []string{"task", "list", "01K7ABCD"})
	require.EqualError(t, err,
		`unexpected argument "01K7ABCD": seam task list takes no positional arguments`+
			"\n       (to load one task by id, use --id)")
}

// flag panics on duplicate registration, so bind must get a fresh FlagSet every
// call -- a shared one would panic on the second parse and leak the first call's
// values into it.
func TestParse_FreshFlagSetPerCall(t *testing.T) {
	table := probeTable()
	first, err := parse(table, []string{"capture", "--project", "a", "u1"})
	require.NoError(t, err)

	require.NotPanics(t, func() {
		second, serr := parse(table, []string{"capture", "u2"})
		require.NoError(t, serr)
		require.Empty(t, *second.opts.(*probeOpts).project,
			"the second parse must not inherit the first's binding")
	})
	require.Equal(t, "a", *first.opts.(*probeOpts).project)
}

// flag.failf prints the error AND dumps usage to the FlagSet's output before
// returning that same error, which main then prints again. parse points that
// output at io.Discard and stubs Usage, so a parse failure writes nothing and the
// returned error is the only report.
func TestParse_FlagWritesNothingToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w // flag.FlagSet.Output() reads os.Stderr at call time when unset
	t.Cleanup(func() { os.Stderr = orig })

	_, perr := parse(probeTable(), []string{"capture", "--bogus"})
	require.Error(t, perr)
	require.NoError(t, w.Close())

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Empty(t, string(out), "flag's own printing is silenced; the error is the only report")
}

// Placeholders come free from flag's backquote convention: UnquoteUsage returns
// "" for a bool, so "[--force]" vs "[--project SLUG]" needs no type switch in the
// generated help. A custom flag.Value renders as "value" unless backquoted, which
// is why the enum and posInt usage strings carry one.
func TestUnquoteUsage_BoolHasNoPlaceholder(t *testing.T) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	bindProbe(fs)
	got := map[string]string{}
	fs.VisitAll(func(f *flag.Flag) {
		name, _ := flag.UnquoteUsage(f)
		got[f.Name] = name
	})
	require.Equal(t, "", got["force"])
	require.Equal(t, "SLUG", got["project"])
	require.Equal(t, "STATUS", got["status"])
	require.Equal(t, "SECS", got["lease"])
}

// spec's type erasure round trip: the table is []cmd, but run receives exactly
// the *probeOpts this bind returned.
func TestSpec_RunReceivesTheBoundOptions(t *testing.T) {
	var gotOpts *probeOpts
	var gotPos []string
	c := spec("probe", "test", "a probe", exactly(1, "id"), bindProbe,
		func(_ context.Context, _ *env, o *probeOpts, pos []string) error {
			gotOpts, gotPos = o, pos
			return nil
		})

	p, err := parse([]cmd{c}, []string{"probe", "--status", "done", "01K7ABCD"})
	require.NoError(t, err)
	require.NoError(t, p.cmd.run(context.Background(), &env{}, p.opts, p.pos))
	require.NotNil(t, gotOpts)
	require.Equal(t, "done", *gotOpts.status)
	require.Equal(t, []string{"01K7ABCD"}, gotPos)
}

func TestSpec_CarriesItsHelpMetadata(t *testing.T) {
	c := spec("task list", "tasks", "list tasks",
		noArgs().withHint("to load one task by id, use --id"), bindProbe, probeRun).
		withLong("detail\nlines")
	require.Equal(t, "task list", c.name)
	require.Equal(t, "tasks", c.group)
	require.Equal(t, "list tasks", c.summary)
	require.Equal(t, "detail\nlines", c.long)
	require.Equal(t, "to load one task by id, use --id", c.args.hint)
}

func TestNewEnv_WiresTheRealWorld(t *testing.T) {
	e := newEnv()
	require.Equal(t, os.Stdin, e.stdin)
	require.Equal(t, os.Stdout, e.stdout)
	require.Equal(t, os.Stderr, e.stderr)
	require.NotNil(t, e.dial)
	require.NotNil(t, e.loadConfig)
}
