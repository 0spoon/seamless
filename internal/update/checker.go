package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arctop/seamless/internal/config"
	"github.com/arctop/seamless/internal/core"
)

// Scheduling constants.
const (
	// firstCheckDelay + rand(firstCheckJitter) is when a daemon with no sane
	// saved schedule first checks: soon after it starts, never in its first
	// moments, and spread so a fleet restarted together does not ask at once.
	firstCheckDelay  = 2 * time.Minute
	firstCheckJitter = 8 * time.Minute
	firstCheckSpread = firstCheckDelay + firstCheckJitter

	// errorBackoffBase is the wait after the first failed check; each further
	// failure doubles it, up to the check interval.
	errorBackoffBase = 15 * time.Minute

	// checkNowThrottle is how soon after any check "Check now" may ask again.
	checkNowThrottle = time.Minute

	// tickInterval is how often the loop compares the clock with its saved
	// deadlines. Deadlines are wall-clock times in the state, and the tick is
	// short, so a laptop that slept through a check catches up within a
	// minute of waking -- a long monotonic timer would stall instead.
	tickInterval = time.Minute
)

// Errors CheckNow returns.
var (
	// ErrChecksOff: update checks are off, so nothing may be asked.
	ErrChecksOff = errors.New("update: update checks are off")
	// ErrTooSoon: a check ran within the last minute.
	ErrTooSoon = errors.New("update: checked less than a minute ago")
	// ErrNotRunning: the checker is not started, or stopped after a fault.
	ErrNotRunning = errors.New("update: the update checker is not running")
)

// ReleaseFetcher reads the release list (*Fetcher in production).
type ReleaseFetcher interface {
	Fetch(ctx context.Context, etag string) (Page, error)
}

// Recorder is the slice of the event log the checker writes to
// (*events.Recorder in production).
type Recorder interface {
	Record(ctx context.Context, e core.Event) (string, error)
	RecordOnce(ctx context.Context, e core.Event) (string, bool, error)
}

// Deps is everything the checker reads or writes, injected so a test drives
// the clock, the ticks, the network and the event log.
type Deps struct {
	DataDir      string // state lives in <DataDir>/update
	Version      string // the running version, as built
	Distribution string // the build stamp (DistributionRelease or not)
	GOOS         string // which installer's assets a release must carry
	Install      Install
	Instance     string // this process's id, also served by /healthz
	PID          int
	LocalHost    string // the daemon's own host name, for the briefing notice

	// Base is the file/env config. Override reads the console's stored
	// override on every use; an error falls back to Base alone.
	Base     config.Update
	Override func(ctx context.Context) (config.UpdateOverride, error)

	Fetcher ReleaseFetcher
	Events  Recorder // nil: no events

	Now   func() time.Time                  // nil: time.Now
	Ticks func() (<-chan time.Time, func()) // nil: a tickInterval ticker
	Rand  func(n int64) int64               // uniform in [0, n); nil: math/rand/v2
	Log   *slog.Logger                      // nil: slog.Default()
}

// Checker is the background update check. One goroutine (the loop) owns the
// state and is its only writer: CheckNow and Refresh hand it a command and
// wait. Every reader gets a copy of the published Status.
type Checker struct {
	d       Deps
	release bool
	log     *slog.Logger

	mu     sync.RWMutex
	status Status

	cmds    chan command
	done    chan struct{}
	started atomic.Bool

	// Owned by the loop goroutine (and by Start before it launches).
	state       State
	lastFetch   time.Time
	persistErr  string
	overrideErr string
}

type command struct {
	check bool // true: Check now; false: just re-read settings and republish
	reply chan result
}

type result struct {
	status Status
	err    error
}

// New builds a Checker. Nothing runs until Start.
func New(d Deps) *Checker {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Ticks == nil {
		d.Ticks = func() (<-chan time.Time, func()) {
			t := time.NewTicker(tickInterval)
			return t.C, t.Stop
		}
	}
	if d.Rand == nil {
		d.Rand = rand.Int64N
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	c := &Checker{
		d:       d,
		release: IsReleaseBuild(d.Distribution, d.Version),
		log:     d.Log,
		cmds:    make(chan command),
		done:    make(chan struct{}),
	}
	c.status = c.snapshot(Effective(d.Base, config.UpdateOverride{}, c.release))
	return c
}

// Start runs the startup step -- load and clean the saved state, notice a
// version change, schedule the first check -- and then launches the loop,
// which stops when ctx is cancelled. Start it only once the daemon has bound
// its listener: a daemon that fails to bind must not record itself as
// running. Calling Start twice is a no-op.
func (c *Checker) Start(ctx context.Context) {
	if !c.started.CompareAndSwap(false, true) {
		return
	}
	c.boot(ctx)
	go c.loop(ctx)
}

// Wait blocks until the loop has exited. It returns at once when Start was
// never called.
func (c *Checker) Wait() {
	if !c.started.Load() {
		return
	}
	<-c.done
}

// Status returns a copy of the current snapshot.
func (c *Checker) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status.clone()
}

// Notice is the briefing line for a session on host, in the shape the
// retrieve service's update-notice hook takes.
func (c *Checker) Notice(_ context.Context, host string) string {
	return c.Status().Notice(host, c.d.LocalHost, c.d.Now())
}

// CheckNow asks GitHub right away -- the console's "Check now". It refuses
// with ErrChecksOff when checks are off and ErrTooSoon within a minute of the
// previous check. A failed request comes back as its error alongside the
// updated status (which records it).
func (c *Checker) CheckNow(ctx context.Context) (Status, error) {
	return c.send(ctx, true)
}

// Refresh re-reads the settings and republishes the status: the console calls
// it after saving or resetting its override, so the page it redirects to
// already shows the new state.
func (c *Checker) Refresh(ctx context.Context) (Status, error) {
	return c.send(ctx, false)
}

func (c *Checker) send(ctx context.Context, check bool) (Status, error) {
	if !c.started.Load() {
		return c.Status(), ErrNotRunning
	}
	reply := make(chan result, 1)
	select {
	case c.cmds <- command{check: check, reply: reply}:
	case <-c.done:
		return c.Status(), ErrNotRunning
	case <-ctx.Done():
		return c.Status(), ctx.Err()
	}
	select {
	case r := <-reply:
		return r.status, r.err
	case <-ctx.Done():
		return c.Status(), ctx.Err()
	}
}

// boot is the startup step. For a release build it loads the saved state
// (quarantining a corrupt file), clamps future timestamps, records the version
// change since the previous daemon, notes this process as the running one and
// saves. A source build keeps everything in memory and never touches the data
// dir (constraint dev-and-fixture-daemons-never-self-update).
func (c *Checker) boot(ctx context.Context) {
	now := c.d.Now()
	s := c.settings(ctx)
	if !c.release {
		c.scheduleFirst(now)
		c.publish(s)
		return
	}

	st, quarantined, err := LoadState(c.d.DataDir, now)
	if err != nil {
		c.log.Warn("update: could not read the update state; starting fresh", "err", err)
		st = State{}
	}
	if quarantined != "" {
		c.log.Warn("update: the update state file was corrupt and has been moved aside", "moved_to", quarantined)
	}
	st.Clamp(now, s.CheckInterval)

	cur, _ := Parse(c.d.Version) // a release build always parses
	var changed *Updated
	if st.LastRunVersion != nil && *st.LastRunVersion != cur {
		dir := DirectionUpgrade
		if cur.Compare(*st.LastRunVersion) < 0 {
			dir = DirectionDowngrade
		}
		changed = &Updated{From: *st.LastRunVersion, To: cur, At: now, Direction: dir}
		st.Updated = changed
	}
	st.LastRunVersion = &cur
	st.Running = Running{
		Version: cur, Distribution: c.d.Distribution, Kind: c.d.Install.Kind, Reason: c.d.Install.Reason,
		Instance: c.d.Instance, PID: c.d.PID, StartedAt: now,
	}
	c.state = st
	c.scheduleFirst(now)

	// The event follows the write: if the write fails, the next start sees
	// the same change again, so the event is better lost than doubled.
	if c.persist() && changed != nil {
		c.log.Info("update: running a different version than last time",
			"from", changed.From.String(), "to", changed.To.String(), "direction", changed.Direction)
		c.record(ctx, core.Event{
			Kind:   core.EventUpdateApplied,
			ItemID: changed.To.String(),
			Payload: map[string]any{
				"from": changed.From.String(), "to": changed.To.String(), "direction": changed.Direction,
			},
		})
	}
	c.publish(s)
}

// scheduleFirst keeps a saved next-check time that is still ahead (a daemon
// that restarts mid-backoff keeps waiting), and otherwise schedules the first
// check firstCheckDelay plus jitter from now.
func (c *Checker) scheduleFirst(now time.Time) {
	if c.state.NextCheckAt.After(now) {
		return
	}
	c.state.NextCheckAt = now.Add(firstCheckDelay + time.Duration(c.d.Rand(int64(firstCheckJitter))))
}

// loop is the checker's goroutine. A panic in it is recovered and logged:
// the daemon keeps serving, the status reports the checker stopped, and Wait
// returns.
func (c *Checker) loop(ctx context.Context) {
	defer close(c.done)
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("update: the update checker stopped after a panic; the daemon keeps serving",
				"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			c.mu.Lock()
			c.status.Stopped = true
			c.mu.Unlock()
		}
	}()

	ticks, stop := c.d.Ticks()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			c.tick(ctx)
		case cmd := <-c.cmds:
			cmd.reply <- c.handle(ctx, cmd)
		}
	}
}

// tick checks when the saved deadline has passed and checks are on.
func (c *Checker) tick(ctx context.Context) {
	s := c.settings(ctx)
	if now := c.d.Now(); s.Check && !now.Before(c.state.NextCheckAt) {
		// A scheduled check has no caller to tell: fail() logged the error and
		// the status carries it as CheckError.
		_ = c.check(ctx, now, s) //nolint:errcheck // reported through the status, see above
	}
	c.publish(s)
}

// handle serves a CheckNow or Refresh command.
func (c *Checker) handle(ctx context.Context, cmd command) result {
	s := c.settings(ctx)
	defer c.publish(s)
	if !cmd.check {
		c.publish(s)
		return result{status: c.Status()}
	}
	now := c.d.Now()
	switch {
	case !s.Check:
		return result{status: c.snapshot(s), err: ErrChecksOff}
	case !c.lastFetch.IsZero() && now.Sub(c.lastFetch) < checkNowThrottle:
		return result{status: c.snapshot(s), err: ErrTooSoon}
	}
	err := c.check(ctx, now, s)
	c.publish(s)
	return result{status: c.Status(), err: err}
}

// check asks GitHub once and folds the answer into the state.
func (c *Checker) check(ctx context.Context, now time.Time, s Settings) error {
	// Save a pessimistic next time BEFORE asking: if this process dies
	// mid-request (or crash-loops), the next start reads it and waits
	// instead of asking again at once.
	c.state.NextCheckAt = now.Add(c.errorBackoff(s.CheckInterval))
	c.persist()

	page, err := c.d.Fetcher.Fetch(ctx, c.state.ETag)
	now = c.d.Now()
	if err != nil {
		if ctx.Err() != nil {
			return err // shutting down: not the network's fault, not a failure
		}
		c.lastFetch = now
		c.fail(now, s, err)
		c.persist()
		return err
	}
	c.lastFetch = now

	if c.state.CheckError != nil {
		c.log.Info("update: the release check works again", "after_failures", c.state.CheckError.Count)
	}
	c.state.CheckError = nil
	c.state.CheckedAt = now
	c.state.ServerDate = page.ServerDate
	if page.ETag != "" {
		c.state.ETag = page.ETag
	}
	if !page.NotModified {
		c.state.Releases = Filter(page.Releases, c.d.GOOS)
	}
	c.state.NextCheckAt = now.Add(c.jitter(s.CheckInterval))

	newest, ok := Newest(c.state.Releases)
	if ok && (c.state.Seen == nil || c.state.Seen.Version != newest.Version) {
		c.state.Seen = &Seen{Version: newest.Version, At: now}
	}
	c.persist()

	if cur, curOK := Parse(c.d.Version); ok && curOK && newest.Version.Compare(cur) > 0 {
		c.record(ctx, core.Event{
			Kind:   core.EventUpdateAvailable,
			ItemID: newest.Version.String(),
			Payload: map[string]any{
				"version": newest.Version.String(), "current": cur.String(), "kind": string(c.d.Install.Kind),
			},
		})
	}
	return nil
}

// fail records a failed check and schedules the retry: the error backoff
// (15m doubling per consecutive failure, capped at the interval), or the
// rate limit's own retry time when that is later.
func (c *Checker) fail(now time.Time, s Settings, err error) {
	kind := CheckErrorUnavailable
	var rl *RateLimitError
	if errors.As(err, &rl) {
		kind = CheckErrorRateLimited
	}
	ce := c.state.CheckError
	if ce == nil {
		ce = &CheckError{Since: now}
		c.log.Warn("update: the release check failed; retrying later", "err", err)
	} else {
		c.log.Debug("update: the release check failed again", "err", err, "failures", ce.Count+1)
	}
	ce.Kind, ce.Message, ce.Count = kind, err.Error(), ce.Count+1
	c.state.CheckError = ce

	next := now.Add(c.errorBackoff(s.CheckInterval))
	if rl != nil && rl.RetryAt.After(next) {
		next = rl.RetryAt
	}
	c.state.NextCheckAt = next
}

// errorBackoff is the wait after the current streak of failures plus one:
// errorBackoffBase doubled per failure already counted, capped at interval.
func (c *Checker) errorBackoff(interval time.Duration) time.Duration {
	n := 0
	if c.state.CheckError != nil {
		n = c.state.CheckError.Count
	}
	d := errorBackoffBase
	for i := 1; i < n && d < interval; i++ {
		d *= 2
	}
	return min(d, max(interval, errorBackoffBase))
}

// jitter spreads an interval by +-10%, so installs that started together do
// not stay in step.
func (c *Checker) jitter(d time.Duration) time.Duration {
	return d - d/10 + time.Duration(c.d.Rand(int64(d/5)+1))
}

// settings resolves the effective settings, reading the console override.
// An unreadable override falls back to the file/env values, logged once per
// distinct error.
func (c *Checker) settings(ctx context.Context) Settings {
	var o config.UpdateOverride
	if c.d.Override != nil {
		got, err := c.d.Override(ctx)
		switch {
		case err != nil:
			if msg := err.Error(); msg != c.overrideErr {
				c.log.Warn("update: the console's update settings are unreadable; using the config file's", "err", err)
				c.overrideErr = msg
			}
		default:
			c.overrideErr = ""
			o = got
		}
	}
	return Effective(c.d.Base, o, c.release)
}

// persist saves the state for a release build and reports whether it did; a
// source build never writes. A failure is logged once per distinct error:
// memory stays the source of truth and the next save tries again.
func (c *Checker) persist() bool {
	if !c.release {
		return false
	}
	if err := SaveState(c.d.DataDir, c.state); err != nil {
		if msg := err.Error(); msg != c.persistErr {
			c.log.Warn("update: could not save the update state; keeping it in memory", "err", err)
			c.persistErr = msg
		}
		return false
	}
	c.persistErr = ""
	return true
}

// record writes one event, best-effort: the event log is telemetry, and the
// check must not fail over it.
func (c *Checker) record(ctx context.Context, e core.Event) {
	if c.d.Events == nil {
		return
	}
	var err error
	if e.Kind == core.EventUpdateAvailable {
		_, _, err = c.d.Events.RecordOnce(ctx, e)
	} else {
		_, err = c.d.Events.Record(ctx, e)
	}
	if err != nil {
		c.log.Warn("update: could not record an update event", "kind", string(e.Kind), "err", err)
	}
}

// publish replaces the shared snapshot.
func (c *Checker) publish(s Settings) {
	snap := c.snapshot(s)
	c.mu.Lock()
	snap.Stopped = c.status.Stopped
	c.status = snap
	c.mu.Unlock()
}

// snapshot builds a Status from the loop's state under settings s.
func (c *Checker) snapshot(s Settings) Status {
	st := Status{
		Version:      c.d.Version,
		Distribution: c.d.Distribution,
		Release:      c.release,
		Instance:     c.d.Instance,
		StartedAt:    c.state.Running.StartedAt,
		Install:      c.d.Install,
		Settings:     s,
		CheckedAt:    c.state.CheckedAt,
		NextCheckAt:  c.state.NextCheckAt,
	}
	if ce := c.state.CheckError; ce != nil {
		cp := *ce
		st.CheckError = &cp
	}
	if u := c.state.Updated; u != nil {
		cp := *u
		st.Updated = &cp
	}
	if newest, ok := Newest(c.state.Releases); ok {
		st.Newest = &newest
		if cur, curOK := Parse(c.d.Version); curOK && newest.Version.Compare(cur) > 0 {
			st.Available = true
		}
		if c.state.Seen != nil && c.state.Seen.Version == newest.Version {
			st.SeenAt = c.state.Seen.At
		}
	}
	return st
}

// clone deep-copies the pointer fields, so a reader can never alias the
// loop's state.
func (s Status) clone() Status {
	out := s
	if s.Newest != nil {
		n := *s.Newest
		out.Newest = &n
	}
	if s.CheckError != nil {
		ce := *s.CheckError
		out.CheckError = &ce
	}
	if s.Updated != nil {
		u := *s.Updated
		out.Updated = &u
	}
	return out
}
