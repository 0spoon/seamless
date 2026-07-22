package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/skills"
)

// TestAgentSkillsIndexMatchesPublishedArtifacts: the index is only trustworthy
// if every entry's url names a file this generator actually publishes and
// every digest is the SHA-256 of that file's exact bytes -- a fetching agent
// verifies against the digest, so a stale hash is a broken skill, not a nit.
func TestAgentSkillsIndexMatchesPublishedArtifacts(t *testing.T) {
	files, err := agentSkills()
	require.NoError(t, err)

	raw, ok := files[skillsIndexPath]
	require.True(t, ok, "the index itself is published")
	require.Len(t, files, len(skills.Published())+1, "one artifact per maintained skill plus the index")

	var doc struct {
		Schema string `json:"$schema"`
		Skills []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
			URL         string `json:"url"`
			Digest      string `json:"digest"`
		} `json:"skills"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, skillsIndexSchema, doc.Schema)
	require.Len(t, doc.Skills, len(skills.Published()))

	for i, entry := range doc.Skills {
		require.Equal(t, skills.Published()[i], entry.Name, "index order is distribution order")
		require.Equal(t, "skill-md", entry.Type)
		require.NotEmpty(t, entry.Description)
		require.LessOrEqual(t, len(entry.Description), maxSkillDescriptionLen)

		rel, ok := strings.CutPrefix(entry.URL, siteBaseURL+"/")
		require.True(t, ok, "%s: url stays on the canonical host", entry.Name)
		artifact, ok := files[rel]
		require.True(t, ok, "%s: url names a published file", entry.Name)

		sum := sha256.Sum256(artifact)
		require.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), entry.Digest,
			"%s: digest is the SHA-256 of the published bytes", entry.Name)
	}
}

// TestAgentSkillsArtifactsAreTheInstalledBytes: the published SKILL.md and the
// one seamlessd installs come from the same embedded asset -- byte-equal by
// construction, and this pins that the generator never rewrites them.
func TestAgentSkillsArtifactsAreTheInstalledBytes(t *testing.T) {
	files, err := agentSkills()
	require.NoError(t, err)

	for _, name := range skills.Published() {
		installed, err := skills.SkillMD(name)
		require.NoError(t, err)
		require.Equal(t, string(installed), string(files[agentSkillsDir+"/"+name+"/SKILL.md"]),
			"%s: published artifact is the installed artifact, verbatim", name)
	}
}

// TestSkillEntryForRejectsBadArtifacts: a bad asset must fail the render, not
// publish an index that validators reject or that lies about its artifact.
func TestSkillEntryForRejectsBadArtifacts(t *testing.T) {
	valid := "---\nname: good-skill\ndescription: fine\n---\nbody\n"

	cases := []struct {
		name    string
		skill   string
		raw     string
		wantErr string
	}{
		{"no frontmatter", "good-skill", "just a body\n", "missing frontmatter"},
		{"frontmatter name mismatch", "other-skill", valid, `frontmatter names "good-skill"`},
		{"empty description", "good-skill", "---\nname: good-skill\ndescription: \"\"\n---\nbody\n", "description must be"},
		{"oversized description", "good-skill", "---\nname: good-skill\ndescription: " + strings.Repeat("x", maxSkillDescriptionLen+1) + "\n---\nbody\n", "description must be"},
		{"uppercase name", "Bad-Skill", "---\nname: Bad-Skill\ndescription: fine\n---\nbody\n", "not a valid RFC skill name"},
		{"consecutive hyphens", "bad--skill", "---\nname: bad--skill\ndescription: fine\n---\nbody\n", "not a valid RFC skill name"},
		{"overlong name", strings.Repeat("a", maxSkillNameLen+1), "---\nname: " + strings.Repeat("a", maxSkillNameLen+1) + "\ndescription: fine\n---\nbody\n", "not a valid RFC skill name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := skillEntryFor(tc.skill, []byte(tc.raw))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	entry, err := skillEntryFor("good-skill", []byte(valid))
	require.NoError(t, err)
	require.Equal(t, siteBaseURL+"/"+agentSkillsDir+"/good-skill/SKILL.md", entry.URL)
}
