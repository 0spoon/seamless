package main

// seam plan -- owner surface over captured Claude Code plans, backed by the
// console JSON endpoints (list/show/approve) plus a local git staleness check.

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/config"
)

func runPlan(args []string) error {
	if len(args) == 0 {
		return runPlanList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runPlanList(rest)
	case "show":
		return runPlanShow(rest)
	case "check":
		return runPlanCheck(rest)
	case "approve":
		return runPlanApprove(rest)
	default:
		return fmt.Errorf("unknown plan subcommand %q (use: list, show, check, approve)", sub)
	}
}

// cliPlanRow mirrors the console's planRow JSON.
type cliPlanRow struct {
	NoteID     string    `json:"noteId"`
	Slug       string    `json:"slug"`
	Basename   string    `json:"basename"`
	Title      string    `json:"title"`
	Project    string    `json:"project"`
	Status     string    `json:"status"`
	Iteration  int       `json:"iteration"`
	Agents     int       `json:"agents"`
	TasksDone  int       `json:"tasksDone"`
	TasksTotal int       `json:"tasksTotal"`
	Updated    time.Time `json:"updated"`
}

// cliPlanDetail mirrors the console's planDetailData JSON.
type cliPlanDetail struct {
	Plan     cliPlanRow `json:"plan"`
	Body     string     `json:"body"`
	Attached []struct {
		ID      string    `json:"id"`
		Title   string    `json:"title"`
		Slug    string    `json:"slug"`
		IsAgent bool      `json:"isAgent"`
		Updated time.Time `json:"updated"`
	} `json:"attached"`
	Tasks []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"tasks"`
	CanApprove bool `json:"canApprove"`
}

func runPlanList(args []string) error {
	fs := flag.NewFlagSet("plan list", flag.ContinueOnError)
	project := fs.String("project", "", "filter by project slug")
	window := fs.String("window", "all", "time window: 24h|7d|30d|all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var data struct {
		Rows []cliPlanRow `json:"rows"`
	}
	if err := consoleJSON(cfg, "/console/plans?format=json&w="+url.QueryEscape(*window), &data); err != nil {
		return err
	}
	shown := 0
	for _, r := range data.Rows {
		if *project != "" && r.Project != *project {
			continue
		}
		fmt.Printf("  %-24s [%-9s] %s  (%s, iter %d, %d agents, tasks %d/%d, %s)\n",
			r.Slug, r.Status, r.Title, orDash(r.Project), r.Iteration, r.Agents,
			r.TasksDone, r.TasksTotal, agoShort(r.Updated))
		shown++
	}
	if shown == 0 {
		fmt.Println("(no captured plans)")
	}
	return nil
}

func runPlanShow(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: seam plan show <slug>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var d cliPlanDetail
	if err := consoleJSON(cfg, "/console/plans/"+url.PathEscape(args[0])+"?format=json", &d); err != nil {
		return err
	}
	fmt.Printf("%s  [%s]  %s\n", d.Plan.Slug, d.Plan.Status, orDash(d.Plan.Project))
	fmt.Printf("title:    %s\n", d.Plan.Title)
	fmt.Printf("file:     %s.md (iteration %d)\n", d.Plan.Basename, d.Plan.Iteration)
	if len(d.Attached) > 0 {
		fmt.Printf("attached: %d note(s)\n", len(d.Attached))
		for _, a := range d.Attached {
			kind := "note"
			if a.IsAgent {
				kind = "agent"
			}
			fmt.Printf("  [%-5s] %s  %s (%s)\n", kind, shortID(a.ID), a.Title, agoShort(a.Updated))
		}
	}
	if len(d.Tasks) > 0 {
		fmt.Printf("tasks:    %d\n", len(d.Tasks))
		for _, tk := range d.Tasks {
			fmt.Printf("  %s  [%-11s] %s\n", shortID(tk.ID), tk.Status, tk.Title)
		}
	}
	if strings.TrimSpace(d.Body) != "" {
		fmt.Printf("\n%s\n", d.Body)
	}
	return nil
}

func runPlanApprove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: seam plan approve <slug>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var out struct {
		Slug        string `json:"slug"`
		Status      string `json:"status"`
		TaskCreated bool   `json:"taskCreated"`
		TaskID      string `json:"taskId"`
	}
	if err := consolePOST(cfg, "/console/plans/"+url.PathEscape(args[0])+"/approve?format=json", &out); err != nil {
		return err
	}
	fmt.Printf("plan %s -> %s\n", out.Slug, out.Status)
	if out.TaskCreated {
		fmt.Printf("created task %s\n", shortID(out.TaskID))
	} else {
		fmt.Println("(tracking task already exists)")
	}
	return nil
}

// runPlanCheck compares each captured note's git stamp against the current
// HEAD of --cwd: a note is STALE when files it mentions changed since its
// stamped commit, FRESH otherwise, UNKNOWN when the stamp or commit cannot be
// resolved. It reads note bodies via the console and runs git locally.
func runPlanCheck(args []string) error {
	fs := flag.NewFlagSet("plan check", flag.ContinueOnError)
	cwd := fs.String("cwd", "", "repo to check against (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: seam plan check <slug> [--cwd DIR]")
	}
	slug := fs.Arg(0)
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		*cwd = wd
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var d cliPlanDetail
	if err := consoleJSON(cfg, "/console/plans/"+url.PathEscape(slug)+"?format=json", &d); err != nil {
		return err
	}

	head, err := gitOut(*cwd, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("%s is not a git repo (or git failed): %w", *cwd, err)
	}
	fmt.Printf("plan %s vs %s @ %.12s\n", slug, *cwd, head)

	type entry struct{ label, body string }
	entries := []entry{{"plan " + d.Plan.Basename + ".md", d.Body}}
	for _, a := range d.Attached {
		var n struct {
			Body string `json:"body"`
		}
		if err := consoleJSON(cfg, "/console/notes/"+url.PathEscape(a.ID)+"?format=json", &n); err != nil {
			fmt.Printf("  [UNKNOWN] %-40s note unreadable: %v\n", a.Slug, err)
			continue
		}
		entries = append(entries, entry{a.Slug, n.Body})
	}

	stale := 0
	for _, e := range entries {
		verdict, detail := checkEntry(*cwd, head, e.body)
		if verdict == "STALE" {
			stale++
		}
		fmt.Printf("  [%-7s] %-40s %s\n", verdict, e.label, detail)
	}
	if stale > 0 {
		return fmt.Errorf("%d note(s) stale -- re-verify before trusting them", stale)
	}
	return nil
}

// checkEntry resolves one note body to a verdict against the repo head.
func checkEntry(cwd, head, body string) (verdict, detail string) {
	stamp := stampHead(body)
	switch stamp {
	case "":
		return "UNKNOWN", "no git stamp"
	case "unknown":
		return "UNKNOWN", "captured outside a git repo"
	}
	if strings.HasPrefix(head, stamp) {
		return "FRESH", "stamped at current HEAD"
	}
	diff, err := gitOut(cwd, "diff", "--name-only", stamp+".."+head)
	if err != nil {
		return "UNKNOWN", fmt.Sprintf("commit %s not found (rebased away?)", stamp)
	}
	changed := strings.Fields(diff)
	if len(changed) == 0 {
		return "FRESH", "no changes since " + stamp
	}
	touched := overlap(changed, mentionedPaths(body))
	if len(touched) == 0 {
		return "FRESH", fmt.Sprintf("%d file(s) changed since %s, none mentioned here", len(changed), stamp)
	}
	if len(touched) > 5 {
		touched = append(touched[:5], "...")
	}
	return "STALE", "mentioned files changed: " + strings.Join(touched, ", ")
}

// stampHead extracts the short git head from a capture stamp line
// ("> captured from ... | git <head> | ..."), or "".
func stampHead(body string) string {
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "> captured from") {
			continue
		}
		for part := range strings.SplitSeq(line, "|") {
			part = strings.TrimSpace(part)
			if after, ok := strings.CutPrefix(part, "git "); ok {
				return strings.TrimSpace(after)
			}
		}
	}
	return ""
}

// pathToken matches file-path-looking tokens in prose: either containing a
// directory separator, or a bare filename with a code-ish extension.
var pathToken = regexp.MustCompile(`[A-Za-z0-9_~][A-Za-z0-9_./~-]*/[A-Za-z0-9_.-]+\.[A-Za-z0-9]{1,8}|\b[A-Za-z0-9_-]+\.(?:go|md|ts|tsx|js|jsx|py|rs|swift|kt|java|c|h|cpp|cc|yaml|yml|json|sql|html|css|sh|proto)\b`)

// mentionedPaths extracts the distinct path-like tokens from a note body,
// stripping trailing :line references and punctuation.
func mentionedPaths(body string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range pathToken.FindAllString(body, -1) {
		p := strings.TrimRight(m, ".,;:)`'\"")
		if _, dup := seen[p]; dup || p == "" {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// overlap returns the changed paths that a note mentions. Mentions may be
// absolute or repo-relative, so paths match when one ends with the other
// (component-aligned via the / boundary).
func overlap(changed, mentioned []string) []string {
	hit := map[string]struct{}{}
	for _, c := range changed {
		for _, m := range mentioned {
			if pathSuffixMatch(c, m) {
				hit[c] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(hit))
	for c := range hit {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// pathSuffixMatch reports whether a and b name the same file allowing one to
// carry a longer prefix (repo-relative vs absolute vs bare filename).
func pathSuffixMatch(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasSuffix(a, "/"+strings.TrimPrefix(b, "/")) ||
		strings.HasSuffix(b, "/"+strings.TrimPrefix(a, "/"))
}

// gitOut runs a git command in dir and returns its trimmed stdout.
func gitOut(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
