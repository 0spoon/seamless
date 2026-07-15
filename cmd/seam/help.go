package main

// Generated help.
//
// Every line here is rendered from a spec: the flags from the FlagSet its bind
// registers, the positionals from its arity. That is the whole point. The
// heredoc this replaces advertised a flag order the parser had never accepted,
// for months, because help and parsing were two hand-maintained descriptions of
// one contract. A synopsis that is derived cannot drift from what runs.
//
// legacySections is the other half of the bridge: the not-yet-migrated commands'
// lines, kept verbatim and rendered under the same group headings, so help stays
// whole while the table fills up one task at a time. B7 deletes it.

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// helpColumn is where a command's summary starts. A synopsis at or past it takes
// its summary on the next line instead of pushing the column out for every other
// command: remember binds five flags, and one long spec should not reflow the
// page.
const helpColumn = 46

// legacySections holds the help lines for commands still dispatched by
// legacyDispatch, keyed by the group they render under. Only hook is left; B6
// migrates it and B7 removes this map and the comment with it.
//
// Verbatim from the usage heredoc, minus its preamble: that preamble told the
// reader "seam capture URL --project p is an error", which the permuting parser
// made false. Every command that preamble described is now in the table, and
// hook's two lines describe events rather than flag order, so nothing is stranded
// by its absence.
var legacySections = map[string]string{
	groupHooks: `  seam hook session-start|user-prompt-submit|session-end   forward the stdin hook payload to seamlessd
  seam hook post-tool-use|subagent-stop|permission-request  plan-mode capture (post-tool-use pre-filters locally)`,
}

// bindTo returns the FlagSet a spec's bind registers on, silenced. Callers only
// read the registrations back out (VisitAll, PrintDefaults); nothing is parsed,
// so a fresh set each call costs nothing and avoids flag's duplicate-registration
// panic.
func bindTo(c cmd) *flag.FlagSet {
	fs := flag.NewFlagSet("seam "+c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	c.bind(fs)
	return fs
}

// synopsis renders a command's usage line from its own spec:
//
//	seam capture [--project SLUG] <url>
//
// Placeholders come free from flag's backquote convention -- UnquoteUsage returns
// "" for a bool, so [--force] needs no type switch here. VisitAll walks flags in
// lexical order, which is what makes the output stable.
func synopsis(c cmd) string {
	parts := []string{"seam", c.name}
	bindTo(c).VisitAll(func(f *flag.Flag) {
		name, _ := flag.UnquoteUsage(f)
		s := "--" + f.Name
		if name != "" {
			s += " " + name
		}
		parts = append(parts, "["+s+"]")
	})
	if a := c.args.render(); a != "" {
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// commandLine renders one entry in the command list: its synopsis, then its
// summary at helpColumn.
func commandLine(c cmd) string {
	s := "  " + synopsis(c)
	if c.summary == "" {
		return s
	}
	if len(s) >= helpColumn {
		return s + "\n" + strings.Repeat(" ", helpColumn) + c.summary
	}
	return s + strings.Repeat(" ", helpColumn-len(s)) + c.summary
}

// helpText renders the full command list: every group in groupOrder, its migrated
// commands rendered from the table, then whatever legacySections still holds for
// it. A group with neither renders nothing, which is what lets the table and the
// shrinking heredoc coexist without either knowing about the other.
//
// It is named helpText, not usage: usage.go carried a comment apologizing for the
// collision between runUsage (the command) and usage() (this). The apology goes
// with the name.
func helpText() string {
	var b strings.Builder
	b.WriteString("seam -- Seamless CLI (talks to a running seamlessd)\n")
	table := commands()
	for _, g := range groupOrder {
		var lines []string
		for _, c := range table {
			if c.group == g {
				lines = append(lines, commandLine(c))
			}
		}
		legacy := legacySections[g]
		if len(lines) == 0 && legacy == "" {
			continue
		}
		fmt.Fprintf(&b, "\n%s:\n", g)
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		if legacy != "" {
			b.WriteString(legacy + "\n")
		}
	}
	return b.String()
}

// commandHelp renders one command's help, the answer to "seam capture --help".
func commandHelp(c cmd) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s\n", synopsis(c))
	if c.summary != "" {
		fmt.Fprintf(&b, "\n%s\n", c.summary)
	}
	fs := bindTo(c)
	var flags strings.Builder
	fs.SetOutput(&flags)
	fs.PrintDefaults()
	if flags.Len() > 0 {
		b.WriteString("\nflags:\n")
		b.WriteString(flags.String())
	}
	if c.long != "" {
		fmt.Fprintf(&b, "\n%s\n", c.long)
	}
	if c.args.hint != "" {
		fmt.Fprintf(&b, "\n(%s)\n", c.args.hint)
	}
	return b.String()
}
