package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// scenesPath is the landing page's verbatim transcript data. It is the single
// source of truth for the with/without sessions: the scene player animates it,
// the landing page's SSR fallbacks quote its outcomes (site-check assertion 9),
// and the /scenarios/ pages render their transcripts from it. Nothing below
// this file re-types a transcript line.
const scenesPath = "docs/static/scenes.js"

// Scene mirrors one entry of scenesPath -- see the header comment there for the
// step vocabulary and the curation rules (verbatim text, ffwd markers, beats).
type Scene struct {
	ID      string      `json:"id"`
	Kicker  string      `json:"kicker"`
	Tab     string      `json:"tab"`
	Title   string      `json:"title"`
	Prompt  string      `json:"prompt"`
	Layout  string      `json:"layout"`
	Caption string      `json:"caption"`
	Panes   []ScenePane `json:"panes"`
}

// ScenePane is one recorded session: its label, the session id it was recorded
// from, the outcome summary, and the curated transcript steps.
type ScenePane struct {
	Key     string      `json:"key"`
	Label   string      `json:"label"`
	Source  string      `json:"source"`
	Outcome string      `json:"outcome"`
	Steps   []SceneStep `json:"steps"`
}

// SceneStep is one transcript step. Role decides which of the other fields are
// meaningful; Beat is a pointer because only split-layout steps carry one.
type SceneStep struct {
	Role     string      `json:"role"`
	Text     string      `json:"text"`
	Tag      string      `json:"tag"`
	Focus    []string    `json:"focus"`
	Label    string      `json:"label"`
	Result   string      `json:"result"`
	Beat     *int        `json:"beat"`
	Emphasis string      `json:"emphasis"`
	K        string      `json:"k"`
	V        string      `json:"v"`
	Files    []SceneFile `json:"files"`
}

// SceneFile is one filename in a files-listing step.
type SceneFile struct {
	Name string `json:"name"`
	Tag  string `json:"tag"`
}

// loadScenes parses the scenes.js data: everything between the first `[` after
// the window.SEAMLESS_SCENES assignment and the last `]` is plain JSON (the
// file is authored that way on purpose). DisallowUnknownFields is deliberate --
// a new step field the scenario renderer would silently drop must fail the
// build instead, because a dropped field here is dropped published content.
func loadScenes(path string) ([]*Scene, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s (docsgen must run from the repo root): %w", path, err)
	}
	const marker = "window.SEAMLESS_SCENES"
	at := bytes.Index(raw, []byte(marker))
	if at < 0 {
		return nil, fmt.Errorf("%s: no %s assignment found", path, marker)
	}
	start := bytes.IndexByte(raw[at:], '[')
	end := bytes.LastIndexByte(raw, ']')
	if start < 0 || at+start > end {
		return nil, fmt.Errorf("%s: could not locate the scene array after %s", path, marker)
	}
	dec := json.NewDecoder(bytes.NewReader(raw[at+start : end+1]))
	dec.DisallowUnknownFields()
	var scenes []*Scene
	if err := dec.Decode(&scenes); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(scenes) == 0 {
		return nil, fmt.Errorf("%s: no scenes", path)
	}
	for _, s := range scenes {
		if s.ID == "" || s.Title == "" || s.Prompt == "" || len(s.Panes) == 0 {
			return nil, fmt.Errorf("%s: scene %q is missing id, title, prompt, or panes", path, s.ID)
		}
		for _, p := range s.Panes {
			if p.Outcome == "" || len(p.Steps) == 0 {
				return nil, fmt.Errorf("%s: scene %q pane %q is missing outcome or steps", path, s.ID, p.Key)
			}
		}
	}
	return scenes, nil
}
