package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// stateSchema is the state.json layout version. A reader ignores fields it
// does not know, so additions keep the number; a change an older binary would
// misread bumps it.
const stateSchema = 1

// StateDir is <data_dir>/update, the directory the update check owns.
func StateDir(dataDir string) string { return filepath.Join(dataDir, "update") }

// StatePath is the daemon's state file, <data_dir>/update/state.json.
func StatePath(dataDir string) string { return filepath.Join(StateDir(dataDir), "state.json") }

// State is what the checker keeps across restarts. Only a release daemon
// writes it (constraint dev-and-fixture-daemons-never-self-update); doctor and
// `seamlessd update --check` only read it. Versions are typed, so a value that
// is not a clean X.Y.Z fails to load rather than reach a notice.
type State struct {
	Schema int `json:"schema"`

	// The release list as of the last successful check.
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	ServerDate  time.Time `json:"server_date,omitzero"` // GitHub's Date header at CheckedAt
	NextCheckAt time.Time `json:"next_check_at,omitzero"`
	ETag        string    `json:"etag,omitempty"`
	Releases    []Release `json:"releases,omitempty"`
	// Seen is the newest release this install has been told about, and when
	// it was first seen: the briefing mentions an available update for a
	// limited time per version.
	Seen *Seen `json:"seen,omitempty"`

	CheckError *CheckError `json:"check_error,omitempty"`

	// Running is the daemon that wrote this file: doctor and the CLI read it
	// as the daemon's own view of itself.
	Running Running `json:"running,omitzero"`
	// LastRunVersion is the version the previous daemon ran; a different one
	// at startup is a version change (Updated).
	LastRunVersion *Version `json:"last_run_version,omitempty"`
	Updated        *Updated `json:"updated,omitempty"`
}

// Seen records when a release was first seen as the newest.
type Seen struct {
	Version Version   `json:"version"`
	At      time.Time `json:"at"`
}

// CheckError is the current streak of failed checks.
type CheckError struct {
	Kind    string    `json:"kind"` // CheckErrorRateLimited or CheckErrorUnavailable
	Message string    `json:"message"`
	Since   time.Time `json:"since"` // first failure of the streak
	Count   int       `json:"count"` // consecutive failures
}

// Check error kinds.
const (
	CheckErrorRateLimited = "rate_limited"
	CheckErrorUnavailable = "unavailable"
)

// Running describes one daemon process.
type Running struct {
	Version      Version   `json:"version"`
	Distribution string    `json:"distribution"`
	Kind         Kind      `json:"kind"`
	Reason       string    `json:"reason,omitempty"`
	Instance     string    `json:"instance"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
}

// Updated is a version change the daemon observed at startup.
type Updated struct {
	From      Version   `json:"from"`
	To        Version   `json:"to"`
	At        time.Time `json:"at"`
	Direction string    `json:"direction"` // DirectionUpgrade or DirectionDowngrade
}

// Version-change directions.
const (
	DirectionUpgrade   = "upgrade"
	DirectionDowngrade = "downgrade"
)

// ReadState reads the state file without changing anything: the read-only
// path doctor and the CLI use. A missing file is a zero State, not an error.
func ReadState(dataDir string) (State, error) {
	raw, err := os.ReadFile(StatePath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("update.ReadState: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("update.ReadState: decode %s: %w", StatePath(dataDir), err)
	}
	return s, nil
}

// LoadState is the daemon's read at startup. A missing file is a zero State.
// A file that does not decode is renamed aside to state.json.corrupt-<utc> --
// its contents are kept for a human, never trusted again -- and the daemon
// starts from a zero State; quarantined names where it went. The error is for
// a file that could be neither read nor moved aside.
func LoadState(dataDir string, now time.Time) (s State, quarantined string, err error) {
	path := StatePath(dataDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, "", nil
	}
	if err != nil {
		return State{}, "", fmt.Errorf("update.LoadState: %w", err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		aside := path + ".corrupt-" + now.UTC().Format("20060102T150405Z")
		if rerr := os.Rename(path, aside); rerr != nil {
			return State{}, "", fmt.Errorf("update.LoadState: quarantine corrupt %s: %w", path, rerr)
		}
		return State{}, aside, nil
	}
	return s, "", nil
}

// SaveState writes s atomically: a temp file in the same directory, fsync,
// then rename over state.json, so a crash leaves the old file or the new one
// and never half of either. The file is 0600 in a 0700 directory: it holds no
// secret, but nothing about this install is anyone else's business.
func SaveState(dataDir string, s State) error {
	dir := StateDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("update.SaveState: %w", err)
	}
	s.Schema = stateSchema
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("update.SaveState: encode: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(StatePath(dataDir), raw, 0o600); err != nil {
		return fmt.Errorf("update.SaveState: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to path through a temp file in path's directory:
// write, fsync, close, rename. The temp file is removed on any failure.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Clamp drops timestamps a wrong clock left in the future, so a clock that
// was briefly days ahead cannot silence the check or freeze a notice: a
// scheduled check further away than one interval (plus the first-check
// spread) is rescheduled by the caller, and the "happened at" times are
// pulled back to now.
func (s *State) Clamp(now time.Time, interval time.Duration) {
	if s.NextCheckAt.After(now.Add(interval + interval/10 + firstCheckSpread)) {
		s.NextCheckAt = time.Time{}
	}
	if s.CheckedAt.After(now) {
		s.CheckedAt = now
	}
	if s.Seen != nil && s.Seen.At.After(now) {
		s.Seen.At = now
	}
	if s.CheckError != nil && s.CheckError.Since.After(now) {
		s.CheckError.Since = now
	}
	if s.Updated != nil && s.Updated.At.After(now) {
		s.Updated.At = now
	}
}
