// Error-returning seeding primitives for external callers -- the agent-scenario
// benchmark's scenario seeders in internal/bench. The demoseed CLI paths in
// seeder.go/data.go/scenes.go keep their log.Fatal style (a broken fixture
// aborts the CLI), but an importing library must own its error handling, so
// these wrap the same store/files calls and propagate instead.

package demokit

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// Now returns the seeder's wall-clock anchor, captured once at New. Every
// backdated timestamp should derive from it so a whole seed shares one "now".
func (s *Seeder) Now() time.Time { return s.now }

// EnsureProject creates the project if it does not exist yet.
func (s *Seeder) EnsureProject(slug, name string) error {
	if _, err := store.EnsureProject(s.ctx, s.db, slug, name); err != nil {
		return fmt.Errorf("demokit: ensure project %s: %w", slug, err)
	}
	return nil
}

// MapRepo records repoPath -> project in the repo map, so sessions starting in
// that repo bind to the project without a setup step.
func (s *Seeder) MapRepo(repoPath, project string) error {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("demokit: map repo %s: %w", repoPath, err)
	}
	if err := store.AddRepoMapping(s.ctx, s.db, abs, project); err != nil {
		return fmt.Errorf("demokit: map repo %s: %w", abs, err)
	}
	return nil
}

// WriteMemory writes one memory file plus its index row, keeping the caller's
// timestamps verbatim.
func (s *Seeder) WriteMemory(mem core.Memory) error {
	if _, err := s.mgr.WriteMemory(s.ctx, mem); err != nil {
		return fmt.Errorf("demokit: write memory %s: %w", mem.Name, err)
	}
	return nil
}

// CreateSession inserts a session row with the caller's timestamps.
func (s *Seeder) CreateSession(sess core.Session) error {
	if err := store.CreateSession(s.ctx, s.db, sess); err != nil {
		return fmt.Errorf("demokit: create session %s: %w", sess.Name, err)
	}
	return nil
}

// CreateTask inserts a task row with the caller's timestamps.
func (s *Seeder) CreateTask(t core.Task) error {
	if err := store.CreateTask(s.ctx, s.db, t); err != nil {
		return fmt.Errorf("demokit: create task %q: %w", t.Title, err)
	}
	return nil
}

// CreateTrial inserts a trial row with the caller's timestamps.
func (s *Seeder) CreateTrial(tr core.Trial) error {
	if err := store.CreateTrial(s.ctx, s.db, tr); err != nil {
		return fmt.Errorf("demokit: create trial %q: %w", tr.Title, err)
	}
	return nil
}
