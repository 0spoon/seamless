// Package core holds Seamless domain types shared across packages: Project,
// Memory, Session, Task, Trial, Event, and their enums. It has no dependencies
// on store, config, or any I/O -- pure data plus small helpers.
package core

import (
	"crypto/rand"
	"fmt"

	"github.com/oklog/ulid/v2"
)

// NewID returns a new lexicographically-sortable ULID string. It uses
// crypto/rand for entropy and returns an error rather than panicking, so it is
// safe on request paths (never use ulid.MustNew -- see AGENTS.md).
func NewID() (string, error) {
	id, err := ulid.New(ulid.Now(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("core.NewID: %w", err)
	}
	return id.String(), nil
}
