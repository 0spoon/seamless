package skills

import (
	"fmt"
	"path"
)

// Published lists the maintained skills in distribution order -- the order
// Install delivers them and the site's Agent Skills discovery index
// (cmd/docsgen) publishes them.
func Published() []string { return []string{OnboardName, ResearchName} }

// SkillMD returns the embedded SKILL.md for one maintained skill: the exact
// bytes Install writes into a client's skill home. The site's discovery index
// publishes and digests these same bytes, so the installed artifact and the
// published one cannot disagree.
func SkillMD(name string) ([]byte, error) {
	raw, err := assets.ReadFile(path.Join("assets", name, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("skills.SkillMD %s: %w", name, err)
	}
	return raw, nil
}
