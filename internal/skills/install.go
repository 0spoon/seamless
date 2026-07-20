// Package skills installs the release-maintained Seamless agent skills into a
// selected client's per-user skill home. The assets are embedded so
// seamlessd install-hooks works from a release binary without a repository
// checkout; the same asset tree is also shipped in release archives.
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/0spoon/seamless/internal/files"
)

const (
	OnboardName  = "seam-onboard"
	ResearchName = "seam-research"

	OnboardMarker = ".seam-onboard-delivered"
)

//go:embed assets
var assets embed.FS

// Client identifies the agent client's skill discovery location.
type Client string

const (
	ClientClaude Client = "claude"
	ClientCodex  Client = "codex"
)

// Action describes what an install did for one skill.
type Action string

const (
	ActionInstalled        Action = "installed"
	ActionUpdated          Action = "updated"
	ActionAlreadyDelivered Action = "already-delivered"
	ActionSkipped          Action = "skipped"
)

// Options provides the client homes and opt-outs. HomeDir is the user's home;
// CodexHome is $CODEX_HOME when set, otherwise Codex defaults below HomeDir.
type Options struct {
	HomeDir      string
	CodexHome    string
	SkipOnboard  bool
	SkipResearch bool
}

// Result reports the exact root and per-skill actions for installer output.
type Result struct {
	Root     string
	Onboard  Action
	Research Action
}

// Removal reports what Remove found for one client. Skills lists package
// directories; Marker records the one-shot delivery marker independently,
// because the expected post-onboarding state is marker present and directory
// absent.
type Removal struct {
	Root   string
	Skills []string
	Marker bool
}

// OptionsFromEnvironment resolves the real per-user homes used by an installed
// seamlessd. Tests pass Options directly so they never touch live client state.
func OptionsFromEnvironment() (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, fmt.Errorf("skills.OptionsFromEnvironment: %w", err)
	}
	return Options{
		HomeDir:      home,
		CodexHome:    strings.TrimSpace(os.Getenv("CODEX_HOME")),
		SkipOnboard:  strings.TrimSpace(os.Getenv("SEAMLESS_NO_ONBOARD_SKILL")) != "",
		SkipResearch: strings.TrimSpace(os.Getenv("SEAMLESS_NO_RESEARCH_SKILL")) != "",
	}, nil
}

// Root returns the selected client's skill root. Codex honors CODEX_HOME;
// Claude Code's user skill home is always ~/.claude/skills.
func Root(client Client, opts Options) (string, error) {
	home := strings.TrimSpace(opts.HomeDir)
	if home == "" {
		return "", errors.New("skills.Root: empty user home")
	}
	switch client {
	case ClientClaude:
		return filepath.Join(home, ".claude", "skills"), nil
	case ClientCodex:
		codexHome := expandTilde(strings.TrimSpace(opts.CodexHome), home)
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return filepath.Join(codexHome, "skills"), nil
	default:
		return "", fmt.Errorf("skills.Root: invalid client %q: valid values are claude, codex", client)
	}
}

// Install delivers both Seamless skills for client. The one-shot onboarding
// marker lives in each client root independently: using the Claude copy never
// suppresses first delivery to Codex, and vice versa.
func Install(client Client, opts Options) (Result, error) {
	root, err := Root(client, opts)
	if err != nil {
		return Result{}, err
	}
	result := Result{Root: root, Onboard: ActionSkipped, Research: ActionSkipped}

	if !opts.SkipOnboard {
		result.Onboard, err = installOnboard(root)
		if err != nil {
			return Result{}, fmt.Errorf("skills.Install %s: onboard: %w", client, err)
		}
	}
	if !opts.SkipResearch {
		result.Research, err = installRecurring(root, ResearchName)
		if err != nil {
			return Result{}, fmt.Errorf("skills.Install %s: research: %w", client, err)
		}
	}
	return result, nil
}

// Remove deletes (or previews) the maintained skill packages and onboarding
// marker for one client. It never touches other packages in the skill root.
func Remove(client Client, opts Options, dryRun bool) (Removal, error) {
	root, err := Root(client, opts)
	if err != nil {
		return Removal{}, err
	}
	result := Removal{Root: root}
	for _, name := range []string{OnboardName, ResearchName} {
		target := filepath.Join(root, name)
		present, err := exists(target)
		if err != nil {
			return Removal{}, fmt.Errorf("skills.Remove %s: %w", client, err)
		}
		if !present {
			continue
		}
		result.Skills = append(result.Skills, name)
		if !dryRun {
			if err := os.RemoveAll(target); err != nil {
				return Removal{}, fmt.Errorf("skills.Remove %s: remove %s: %w", client, target, err)
			}
		}
	}
	marker := filepath.Join(root, OnboardMarker)
	result.Marker, err = exists(marker)
	if err != nil {
		return Removal{}, fmt.Errorf("skills.Remove %s: %w", client, err)
	}
	if result.Marker && !dryRun {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return Removal{}, fmt.Errorf("skills.Remove %s: remove marker: %w", client, err)
		}
	}
	return result, nil
}

func installOnboard(root string) (Action, error) {
	dst := filepath.Join(root, OnboardName)
	marker := filepath.Join(root, OnboardMarker)
	dstExists, err := exists(dst)
	if err != nil {
		return "", err
	}
	markerExists, err := exists(marker)
	if err != nil {
		return "", err
	}
	if markerExists && !dstExists {
		return ActionAlreadyDelivered, nil
	}

	action := ActionInstalled
	if dstExists {
		action = ActionUpdated
	}
	if err := installAssetDir(OnboardName, dst); err != nil {
		return "", err
	}
	if err := files.AtomicWrite(marker, nil, 0o644); err != nil {
		return "", err
	}
	return action, nil
}

func installRecurring(root, name string) (Action, error) {
	dst := filepath.Join(root, name)
	dstExists, err := exists(dst)
	if err != nil {
		return "", err
	}
	action := ActionInstalled
	if dstExists {
		action = ActionUpdated
	}
	if err := installAssetDir(name, dst); err != nil {
		return "", err
	}
	return action, nil
}

func installAssetDir(name, dst string) error {
	src := path.Join("assets", name)
	return fs.WalkDir(assets, src, func(assetPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(assetPath, src), "/")
		target := dst
		if rel != "" {
			target = filepath.Join(dst, filepath.FromSlash(rel))
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			return nil
		}
		data, err := assets.ReadFile(assetPath)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", assetPath, err)
		}
		if err := files.AtomicWrite(target, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}

func exists(filePath string) (bool, error) {
	_, err := os.Lstat(filePath)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, fmt.Errorf("stat %s: %w", filePath, err)
	}
}

func expandTilde(value, home string) string {
	switch {
	case value == "~":
		return home
	case strings.HasPrefix(value, "~/"), strings.HasPrefix(value, `~\`):
		return filepath.Join(home, value[2:])
	default:
		return value
	}
}
