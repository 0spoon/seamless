package main

// The command-spec table and the parser that drives it.
//
// A command is declared once, as data: its name, help metadata, positional
// contract, flag binding, and handler. The alternative -- spec declares for help
// while the handler still binds -- recreates the exact decoupling that let the
// usage heredoc advertise a broken flag order for months.
//
// Two things carry most of the weight here:
//
//   - Enums and positive ints are flag.Value types, not post-parse checks, so
//     "present but uninterpretable -> error" falls out of flag itself, at parse
//     time, with no "was it set?" plumbing.
//   - parse opens no connection and loads no config. That line is both the
//     exit-code boundary (a parse failure is 2, a handler failure is 1) and what
//     makes the whole argument matrix testable without a network.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// cmd is one declarative command spec. Specs live next to their handler
// (var captureCmd = spec(...) above runCapture); the table only orders them.
//
// bind and run are type-erased so the table can be a homogeneous []cmd. spec[O]
// is the only constructor and it pairs them statically.
type cmd struct {
	name    string // full name, including any subcommand: "task list"
	group   string // help section heading: "tasks"
	summary string // one line, shown in the command list
	long    string // optional detail for per-command help
	args    arity
	bind    func(*flag.FlagSet) any
	run     func(context.Context, *env, any, []string) error
}

// withLong attaches multi-line detail for per-command help.
func (c cmd) withLong(s string) cmd {
	c.long = s
	return c
}

// usageExit reports the exit code for a command line seam could not parse. It is
// 2 everywhere except hook: Claude Code reads exit 2 from a hook as a BLOCKING
// error and feeds stderr back to the model, so a misconfigured hook would wedge
// the very session it exists to serve. hook fails open at 1 instead, which is
// what it did before the table, and why its event name is validated in runHook
// rather than declared as a spec enum.
//
// 2 for the rest is a small, deliberate extension of what the CLI did before,
// where a parse failure and a handler failure both exited 1: flag.ExitOnError
// itself exits 2 on a parse failure, and the split makes seam scriptable -- 2
// means the caller typed it wrong, 1 means it went wrong. It costs nothing
// because the parse/execute boundary already computes it.
func (c *cmd) usageExit() int {
	if c != nil && c.name == "hook" {
		return 1
	}
	return 2
}

// spec builds a cmd from a statically-typed bind/run pair. The table stays
// homogeneous while O remains matched across the two halves, so the o.(*O)
// assertion cannot fail: the only value ever handed to run is the one this bind
// returned.
func spec[O any](name, group, summary string, a arity,
	bind func(*flag.FlagSet) *O,
	run func(context.Context, *env, *O, []string) error,
) cmd {
	return cmd{
		name:    name,
		group:   group,
		summary: summary,
		args:    a,
		bind:    func(fs *flag.FlagSet) any { return bind(fs) },
		run: func(ctx context.Context, e *env, o any, pos []string) error {
			return run(ctx, e, o.(*O), pos)
		},
	}
}

// noOpts is the options type for a command that takes no flags. spec pairs bind
// and run through a type parameter, so bind must still return a pointer; this is
// what it points at. (`task done <id>` and friends are pure positionals.)
type noOpts struct{}

// bindNoOpts registers no flags.
func bindNoOpts(*flag.FlagSet) *noOpts { return &noOpts{} }

// arity is a command's positional-argument contract. A max below zero means
// unbounded.
type arity struct {
	min, max int
	names    []string // placeholders, in order; the last one repeats
	hint     string   // per-spec remedy appended to an arity error
}

// noArgs declares a command that takes no positionals.
func noArgs() arity { return arity{} }

// exactly declares a command that takes exactly n positionals.
func exactly(n int, names ...string) arity { return arity{min: n, max: n, names: names} }

// between declares a lo..hi positional range. It is not hypothetical:
// `seam sessions <id>` is a real 0-or-1 positional (sessions.go:28-30) -- the
// list and the detail view are one command.
func between(lo, hi int, names ...string) arity { return arity{min: lo, max: hi, names: names} }

// atLeast declares n or more positionals -- an unbounded tail, as recall's query
// words are.
func atLeast(n int, names ...string) arity { return arity{min: n, max: -1, names: names} }

// withHint attaches a remedy shown under an arity error. It is per-spec because
// the fix is rarely guessable from a generic message: `seam task list <id>` wants
// --id, and no amount of arity arithmetic knows that.
func (a arity) withHint(h string) arity {
	a.hint = h
	return a
}

// name returns the placeholder for the i-th positional. The last declared name
// repeats, covering an unbounded tail.
func (a arity) name(i int) string {
	switch {
	case i < len(a.names):
		return a.names[i]
	case len(a.names) > 0:
		return a.names[len(a.names)-1]
	default:
		return "arg"
	}
}

// render returns the positional half of a usage synopsis: "<id>" for a required
// argument, "[<id>]" for an optional one, "<word>..." for an unbounded tail.
func (a arity) render() string {
	if a.max == 0 {
		return "" // noArgs; a negative max is unbounded, not empty
	}
	n := a.max
	if a.max < 0 {
		n = max(a.min, 1) // unbounded: the last slot carries the "..."
	}
	parts := make([]string, 0, n)
	for i := range n {
		s := "<" + a.name(i) + ">"
		if a.max < 0 && i == n-1 {
			s += "..."
		}
		if i >= a.min {
			s = "[" + s + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// check validates the positional count. name is the command name ("task list"):
// arity does not know it, and the error text names it.
//
// The caller prints the result behind an "error: " prefix and follows it with the
// command's synopsis, which needs the FlagSet and so belongs to the help layer:
//
//	error: unexpected argument "01K7ABCD": seam task list takes no positional arguments
//	       (to load one task by id, use --id)
//	usage: seam task list [--id ID] [--plan SLUG] [--project SLUG] [--status STATUS]
func (a arity) check(name string, pos []string) error {
	if len(pos) < a.min {
		return a.hinted(fmt.Sprintf("missing argument: seam %s requires %s", name, a.render()))
	}
	if a.max >= 0 && len(pos) > a.max {
		return a.hinted(fmt.Sprintf("unexpected argument %q: seam %s %s", pos[a.max], name, a.takes()))
	}
	return nil
}

// takes phrases the upper bound for an arity error.
func (a arity) takes() string {
	switch a.max {
	case 0:
		return "takes no positional arguments"
	case 1:
		return "takes at most 1 positional argument"
	default:
		return fmt.Sprintf("takes at most %d positional arguments", a.max)
	}
}

// hintIndent lines a hint up under the message above it, once the caller has
// printed that message behind an "error: " prefix.
const hintIndent = "       " // len("error: ")

func (a arity) hinted(msg string) error {
	if a.hint == "" {
		return errors.New(msg)
	}
	return fmt.Errorf("%s\n%s(%s)", msg, hintIndent, a.hint)
}

// enumValue is a string flag constrained to a fixed set. It is a flag.Value
// rather than a post-parse check so that rejection happens inside flag, at parse
// time. Set's message is deliberately only the tail of flag's own wrapper, which
// renders the house phrasing in full:
//
//	invalid value "bogus" for flag -status: valid values are open, in_progress, done, dropped
type enumValue struct {
	val   *string
	valid []string
}

func (e *enumValue) String() string {
	if e.val == nil {
		return "" // flag reflects a zero value to detect a default; do not panic
	}
	return *e.val
}

func (e *enumValue) Set(s string) error {
	if !slices.Contains(e.valid, s) {
		return fmt.Errorf("valid values are %s", strings.Join(e.valid, ", "))
	}
	*e.val = s
	return nil
}

// enumFlag registers a string flag accepting only one of valid. Backquote the
// placeholder in usage ("filter: `STATUS`"): flag.UnquoteUsage renders any custom
// flag.Value as "value" otherwise.
func enumFlag(fs *flag.FlagSet, name, def, usage string, valid []string) *string {
	p := new(string)
	*p = def
	fs.Var(&enumValue{val: p, valid: valid}, name, usage)
	return p
}

// posIntValue is an int flag rejecting non-numeric, zero, and negative values.
// Rejecting 0 is what makes a 0 default unambiguous: 0 can only mean absent, so
// `if *lease > 0` (task.go:140) is correct rather than lossy, and no "was it
// set?" plumbing is needed.
type posIntValue struct{ val *int }

func (p *posIntValue) String() string {
	if p.val == nil {
		return "0" // matches flag's own intValue zero, so PrintDefaults omits it
	}
	return strconv.Itoa(*p.val)
}

func (p *posIntValue) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		// flag prepends `invalid value "-5" for flag -lease: `, which names the
		// offending value; this half only has to name the contract.
		return errors.New("must be a positive integer")
	}
	*p.val = n
	return nil
}

// posIntFlag registers an int flag that only accepts positive values.
func posIntFlag(fs *flag.FlagSet, name string, def int, usage string) *int {
	p := new(int)
	*p = def
	fs.Var(&posIntValue{val: p}, name, usage)
	return p
}

// enumOf widens a canonical enum slice (core.TaskStatuses and friends, declared
// over a named string type) to the []string enumValue compares against. Deriving
// from the canonical set is the whole point: a transcribed list drifts, as the
// MCP surface's hand-written proposal-kind enum already has.
//
// Not to be confused with internal/mcp's enumOf, which serves the same purpose on
// that surface but returns an mcp.PropertyOption.
func enumOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// parseArgs parses flags and positionals in any order, so "seam capture URL
// --project p" and "seam capture --project p URL" agree. Go's flag stops at the
// first positional; the loop lifts each one out and re-parses the rest, which
// works because a repeated Parse never clears the values already bound.
//
// Everything after "--" is literal, and it is split off BEFORE the loop: flag
// consumes the terminator and stops, but the next iteration starts a fresh Parse
// with no memory of it and would read a second "-x" as a flag again. Without the
// pre-split "seam recall -- -a -b" fails, which is the one case the terminator
// exists for.
//
// The pre-split costs one edge: a literal "--" as a flag's value ("--project --")
// reads as the terminator. Write "--project=--".
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var literal []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, literal = args[:i], args[i+1:]
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return append(pos, literal...), nil
}

// lookup resolves the longest command name in table that prefixes argv, so a
// two-word "task list" wins over a bare "task", and returns the arguments left
// after it.
//
// The table is a parameter rather than a package-level var so that this file
// stays independent of the command set: tests bring their own fixture table, and
// the real one lives in commands.go.
func lookup(table []cmd, argv []string) (*cmd, []string, bool) {
	for n := maxNameWords(table); n >= 1; n-- {
		if n > len(argv) {
			continue
		}
		name := strings.Join(argv[:n], " ")
		if i := slices.IndexFunc(table, func(c cmd) bool { return c.name == name }); i >= 0 {
			return &table[i], argv[n:], true
		}
	}
	return nil, nil, false
}

func maxNameWords(table []cmd) int {
	n := 0
	for _, c := range table {
		n = max(n, len(strings.Fields(c.name)))
	}
	return n
}

// errUnknownCommand marks a dispatch failure, so the caller can answer with the
// full help rather than one command's synopsis. Both exit 2.
var errUnknownCommand = errors.New("unknown command")

// parsed is a resolved command with its bound options and positionals.
type parsed struct {
	cmd  *cmd
	opts any
	pos  []string
}

// parse resolves argv against the table and binds its flags. It opens no
// connection and loads no config, which is what lets the caller split exit codes
// on it (parse failure -> 2, handler failure -> 1) and what makes the argument
// matrix testable without a network.
//
// flag.ErrHelp passes through untouched, so the caller can answer "--help" with
// exit 0.
func parse(table []cmd, argv []string) (*parsed, error) {
	c, rest, ok := lookup(table, argv)
	if !ok {
		return nil, unknownCommand(table, argv)
	}
	// A fresh FlagSet every call: flag panics on duplicate registration, so a
	// shared one would panic the second time a command is parsed and would leak
	// the first call's bound values into the second.
	fs := flag.NewFlagSet("seam "+c.name, flag.ContinueOnError)
	// flag.failf prints the error AND dumps usage before returning that same
	// error, which the caller then prints again -- "either log or return, never
	// both". Silence both halves; reporting belongs to the caller.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	opts := c.bind(fs)
	pos, err := parseArgs(fs, rest)
	if err != nil {
		return nil, err
	}
	if err := c.args.check(c.name, pos); err != nil {
		return nil, err
	}
	return &parsed{cmd: c, opts: opts, pos: pos}, nil
}

// unknownCommand names the failure as precisely as the table allows: anything to
// do with a real family lists that family's members rather than reporting the
// family unknown. Members are listed in table order, which is curated and
// deterministic.
//
// That covers a bad subcommand ("task bogus") and a bare family name ("task"),
// which is not a command but is not a typo either -- the caller knows what they
// want and needs to be told the word for it, not that "task" does not exist.
func unknownCommand(table []cmd, argv []string) error {
	if len(argv) == 0 {
		return errUnknownCommand
	}
	if family := familyOf(table, argv[0]); len(family) > 0 {
		return fmt.Errorf("%w %q: valid values are %s",
			errUnknownCommand, subcommandOf(argv), strings.Join(family, ", "))
	}
	return fmt.Errorf("%w %q", errUnknownCommand, argv[0])
}

// familyOf returns the table's commands whose names begin with "<name> ".
func familyOf(table []cmd, name string) []string {
	var family []string
	for _, c := range table {
		if strings.HasPrefix(c.name, name+" ") {
			family = append(family, c.name)
		}
	}
	return family
}

// subcommandOf renders what the caller asked a family for. A flag is not a
// subcommand attempt ("seam task --project p" is a bare family name with a flag
// after it), so quoting it back as one would misname the mistake.
func subcommandOf(argv []string) string {
	if len(argv) > 1 && !strings.HasPrefix(argv[1], "-") {
		return argv[0] + " " + argv[1]
	}
	return argv[0]
}
