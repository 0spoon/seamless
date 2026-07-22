package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/0spoon/seamless/internal/skills"
)

// agentSkillsDir is the discovery root, relative to the site root:
// /.well-known/agent-skills/ per the Agent Skills Discovery RFC (v0.2.0). The
// index and every artifact publish beneath it, and both content types come
// from GitHub Pages natively (.json as application/json, .md as
// text/markdown), so no edge rule is needed.
const agentSkillsDir = ".well-known/agent-skills"

// skillsIndexPath is the discovery document the scanners probe.
const skillsIndexPath = agentSkillsDir + "/index.json"

// skillsIndexSchema pins the RFC revision the index claims conformance with.
const skillsIndexSchema = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"

// skillsIndexDoc is the published index: the $schema pin plus one entry per
// maintained skill, in distribution order.
type skillsIndexDoc struct {
	Schema string       `json:"$schema"`
	Skills []skillEntry `json:"skills"`
}

// skillEntry is one skill in the index. Type is always "skill-md": the
// published artifact is the single SKILL.md, deliberately without the
// agents/openai.yaml install metadata, which configures a Codex install rather
// than describing the skill. Digest is the SHA-256 of the artifact's exact
// bytes -- a fetching agent verifies against it, so it is computed from the
// same bytes this generator publishes, never transcribed.
type skillEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Digest      string `json:"digest"`
}

// skillMeta is the SKILL.md frontmatter slice the index republishes. The full
// frontmatter shape is pinned by the asset's own tests (internal/skills); only
// identity and description matter here.
type skillMeta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// skillNamePattern is the RFC's name grammar: lowercase alphanumerics and
// hyphens, with no leading, trailing, or consecutive hyphens. Length is
// bounded separately (maxSkillNameLen).
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const (
	maxSkillNameLen        = 64
	maxSkillDescriptionLen = 1024
)

// skillEntryFor builds one index entry from an artifact's bytes, enforcing the
// RFC's grammar so a bad asset fails the render rather than publishing an
// index validators reject.
func skillEntryFor(name string, raw []byte) (skillEntry, error) {
	yamlSrc, _, err := splitFrontmatterRaw(raw)
	if err != nil {
		return skillEntry{}, fmt.Errorf("%s SKILL.md: %w", name, err)
	}
	var meta skillMeta
	if err := yaml.Unmarshal(yamlSrc, &meta); err != nil {
		return skillEntry{}, fmt.Errorf("%s SKILL.md frontmatter: %w", name, err)
	}
	if meta.Name != name {
		return skillEntry{}, fmt.Errorf("%s SKILL.md: frontmatter names %q, the index would lie about what it links", name, meta.Name)
	}
	if !skillNamePattern.MatchString(name) || len(name) > maxSkillNameLen {
		return skillEntry{}, fmt.Errorf("%s: not a valid RFC skill name (1-%d lowercase alphanumerics and non-consecutive inner hyphens)", name, maxSkillNameLen)
	}
	if meta.Description == "" || len(meta.Description) > maxSkillDescriptionLen {
		return skillEntry{}, fmt.Errorf("%s SKILL.md: description must be 1-%d characters", name, maxSkillDescriptionLen)
	}
	sum := sha256.Sum256(raw)
	return skillEntry{
		Name:        name,
		Type:        "skill-md",
		Description: meta.Description,
		URL:         siteBaseURL + "/" + agentSkillsDir + "/" + name + "/SKILL.md",
		Digest:      "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// agentSkills renders the published discovery surface, keyed by
// site-root-relative path: the index at skillsIndexPath, plus each maintained
// skill's SKILL.md verbatim from the same embedded assets seamlessd installs,
// so the published artifact, its digest, and the installed skill are one set
// of bytes.
func agentSkills() (map[string][]byte, error) {
	files := make(map[string][]byte, len(skills.Published())+1)
	doc := skillsIndexDoc{Schema: skillsIndexSchema}
	for _, name := range skills.Published() {
		raw, err := skills.SkillMD(name)
		if err != nil {
			return nil, err
		}
		entry, err := skillEntryFor(name, raw)
		if err != nil {
			return nil, err
		}
		doc.Skills = append(doc.Skills, entry)
		files[agentSkillsDir+"/"+name+"/SKILL.md"] = raw
	}
	rendered, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal agent-skills index: %w", err)
	}
	files[skillsIndexPath] = append(rendered, '\n')
	return files, nil
}
