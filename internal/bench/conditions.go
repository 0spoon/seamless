// The conditions/arms model: named conditions, not a hard with/without
// binary, so the same harness serves version comparison and the
// mechanism-vs-prose study. Each Condition maps 1:1 onto a
// scripts/fixture/harness.sh --mode bench arm, and this file mirrors that
// script's condition grammar (name[:profile[:client]]) and validation --
// a list rejected here would also have been rejected there.

package bench

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Profile is what a condition arm has installed.
type Profile string

const (
	// ProfileVanilla is the model-only control: a bare agent config dir, no
	// Seamless anywhere.
	ProfileVanilla Profile = "vanilla"
	// ProfileMechanism is everything install-hooks wires by default: hooks
	// (SessionStart briefing + SubagentStart subagent briefings), MCP
	// registration including the initialize server instructions, and the
	// default-installed seam-onboard/seam-research skill files.
	ProfileMechanism Profile = "mechanism"
	// ProfileFull is mechanism plus the /seam-onboard CLAUDE.md awareness
	// block pre-written into that arm's demo repo.
	ProfileFull Profile = "full"
)

// Profiles is the canonical profile set; validation derives from it.
var Profiles = []Profile{ProfileVanilla, ProfileMechanism, ProfileFull}

// Client is the agent CLI a condition runs.
type Client string

const (
	ClientClaude Client = "claude"
	// ClientCodex is design-only for now: codex exec cannot run unattended
	// (hook trust is interactive-only and MCP calls need approval -- memory
	// codex-headless-two-gates-hooktrust-and-mcp-approval). The dimension
	// stays first-class because the Codex hook-output cap makes
	// client-specific uplift regressions real.
	ClientCodex Client = "codex"
)

// Clients is the canonical client set. Known is not the same as runnable:
// Validate rejects codex arms until they can run unattended.
var Clients = []Client{ClientClaude, ClientCodex}

// conditionNameRE matches the harness's arm-name grammar.
var conditionNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Condition is one named benchmark arm: a condition name (unique within a
// run), the profile the harness builds for it, and the client that runs it.
type Condition struct {
	Name    string
	Profile Profile
	Client  Client
}

// DefaultConditions returns the default arm list -- one Claude arm per
// profile, named after it -- matching the harness default
// (vanilla,mechanism,full).
func DefaultConditions() []Condition {
	out := make([]Condition, len(Profiles))
	for i, p := range Profiles {
		out[i] = Condition{Name: string(p), Profile: p, Client: ClientClaude}
	}
	return out
}

// Spec renders the condition in the harness's name:profile:client form,
// suitable for --conditions.
func (c Condition) Spec() string {
	return c.Name + ":" + string(c.Profile) + ":" + string(c.Client)
}

// Validate checks the condition against what the harness can build today.
func (c Condition) Validate() error {
	if !conditionNameRE.MatchString(c.Name) {
		return fmt.Errorf("bench: bad condition name %q (lowercase letters, digits, -, _)", c.Name)
	}
	if !slices.Contains(Profiles, c.Profile) {
		return fmt.Errorf("bench: condition %s: unknown profile %q: valid profiles are %s", c.Name, c.Profile, joinProfiles())
	}
	switch c.Client {
	case ClientClaude:
		return nil
	case ClientCodex:
		return fmt.Errorf("bench: condition %s: codex arms are design-only -- codex exec cannot run unattended (memory codex-headless-two-gates-hooktrust-and-mcp-approval)", c.Name)
	default:
		return fmt.Errorf("bench: condition %s: unknown client %q: valid clients are %s", c.Name, c.Client, ClientClaude)
	}
}

// ParseCondition parses one name[:profile[:client]] entry: the profile
// defaults to the name, the client to claude, and the result is validated.
func ParseCondition(spec string) (Condition, error) {
	spec = strings.Join(strings.Fields(spec), "")
	parts := strings.Split(spec, ":")
	if len(parts) > 3 {
		return Condition{}, fmt.Errorf("bench: bad condition %q (want name[:profile[:client]])", spec)
	}
	c := Condition{Name: parts[0], Client: ClientClaude}
	c.Profile = Profile(c.Name)
	if len(parts) > 1 && parts[1] != "" {
		c.Profile = Profile(parts[1])
	}
	if len(parts) > 2 && parts[2] != "" {
		c.Client = Client(parts[2])
	}
	if err := c.Validate(); err != nil {
		return Condition{}, err
	}
	return c, nil
}

// ParseConditions parses a comma-separated --conditions list. Empty entries
// are skipped; duplicate condition names and an empty result are errors.
func ParseConditions(list string) ([]Condition, error) {
	var out []Condition
	seen := map[string]bool{}
	for entry := range strings.SplitSeq(list, ",") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		c, err := ParseCondition(entry)
		if err != nil {
			return nil, err
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("bench: duplicate condition name %q", c.Name)
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bench: conditions list %q selected no arms", list)
	}
	return out, nil
}

func joinProfiles() string {
	ss := make([]string, len(Profiles))
	for i, p := range Profiles {
		ss[i] = string(p)
	}
	return strings.Join(ss, ", ")
}
