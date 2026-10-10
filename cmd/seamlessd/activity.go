package main

import (
	"net/http"
	"sync/atomic"
	"time"
)

// This file holds the daemon's request-activity tracker: how many requests are
// being served right now, and when the last one finished. The automatic update
// reads both -- it applies only with nothing in flight, and its max_defer
// deadline also waits for a stretch of quiet -- so the two numbers have to mean
// "something is using this daemon". The requests that would make them say
// otherwise are not counted at all (activityExempt).

// activityTracker counts the requests in flight and stamps when the most recent
// one finished. It wraps the routes INSIDE the host guard (daemonHandler,
// main.go), so a request the guard refuses never counts: a rebound page
// knocking on the port is not someone using the daemon. runServe builds one,
// wraps its routes with it and hands its snapshot to the update checker
// (updateCheckerDeps); both must be the same tracker.
type activityTracker struct {
	now      func() time.Time
	inFlight atomic.Int64
	// last is the wall-clock UnixNano at which the most recent counted request
	// finished, or the construction time before any has. It only moves
	// forward, so two requests ending together cannot leave it at the earlier.
	last atomic.Int64
}

// newActivityTracker builds a tracker whose last activity starts at now(), not
// at the zero time. The deadline rule asks whether the daemon has been quiet
// for a while, and a process that started a moment ago has observed no quiet
// at all: after a reboot or a restart its clients are still reconnecting, and
// a zero stamp would read as "quiet forever" and let the deadline fire before
// any of them got back. now is injected so tests can drive the clock.
func newActivityTracker(now func() time.Time) *activityTracker {
	a := &activityTracker{now: now}
	a.last.Store(now().UnixNano())
	return a
}

// wrap counts every request next serves except the exempt ones. The release
// and the end stamp run in a defer, so a panicking handler -- which net/http
// recovers per connection, leaving the daemon up -- still gives its count
// back; a leaked increment would read as "busy" for the rest of the process's
// life and hold off every update. The panic itself passes through untouched.
//
// The stamp is taken when a request ENDS: a long tool call is covered by the
// in-flight count while it runs, and the quiet after it is measured from when
// it finished, not from when it began.
func (a *activityTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if activityExempt(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		a.inFlight.Add(1)
		defer a.end()
		next.ServeHTTP(w, r)
	})
}

// end stamps a request's finish, then releases its count, in that order: a
// reader that sees the count drop (snapshot reads it first) is then guaranteed
// to see this request's stamp, never the older one from before it ran.
func (a *activityTracker) end() {
	t := a.now().UnixNano()
	for {
		prev := a.last.Load()
		if t <= prev || a.last.CompareAndSwap(prev, t) {
			break
		}
	}
	a.inFlight.Add(-1)
}

// snapshot reports how many counted requests are in flight and when the most
// recent one finished (the construction time before any has). It is the one
// reader, because the order of the two loads matters: the count comes first,
// so a zero count always arrives with a stamp no earlier than the end of the
// request that brought it to zero; the other order can pair an empty count
// with a stale stamp.
//
// The time carries no monotonic reading, so a caller's now.Sub(last) is
// wall-clock elapsed time: the clock the update state's deadlines use, and one
// that keeps running while the machine sleeps.
func (a *activityTracker) snapshot() (inFlight int64, last time.Time) {
	inFlight = a.inFlight.Load()
	return inFlight, time.Unix(0, a.last.Load()).UTC()
}

// activityExempt reports whether a request is left out of the count. Of the
// routes runServe serves (daemonRoutes), three would otherwise make the
// numbers lie:
//
//   - GET /console/events is the console's live feed, and GET /api/mcp is an
//     MCP client's standing notification stream (mcp-go, legacy protocol).
//     Both stay open until the client goes away, so counting them would keep
//     the in-flight count above zero for as long as a console tab or an agent
//     is connected, and no update would ever find the daemon idle.
//   - /healthz is what the installer and the updater poll while an update is
//     under way, and what `seam status` and doctor probe; counting it would
//     keep the last activity fresh with the update's own health checks.
//
// Each is matched the way the mux routes it. "GET /console/events" is a
// method-qualified pattern, and net/http serves HEAD through a GET pattern, so
// a HEAD there holds the same stream and is exempt too. /api/mcp and /healthz
// are registered without a method, so every method reaches them, but only a
// GET streams from mcp-go (it answers HEAD with an immediate 200, and a POST
// is a tool call -- activity by definition), while /healthz is a probe
// whatever the method.
//
// The path match is exact and on the decoded path, which is what the mux
// matches segment by segment: /console/events/{id} is an ordinary page and
// counts, a query string never reaches r.URL.Path, and every request the mux
// routes to one of these handlers has exactly that path. The one request the
// match catches that the mux does not route there is an encoded slash
// (/console%2Fevents decodes to the feed's path and is a 404); leaving a 404
// uncounted costs nothing.
func activityExempt(method, path string) bool {
	switch path {
	case "/console/events":
		return method == http.MethodGet || method == http.MethodHead
	case "/api/mcp":
		return method == http.MethodGet
	case "/healthz":
		return true
	}
	return false
}
