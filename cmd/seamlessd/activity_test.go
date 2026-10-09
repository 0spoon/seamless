package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/console"
)

// activityT0 is when every tracker in these tests is built.
var activityT0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// activityClock is a settable clock. The tracker reads it from handler
// goroutines, hence the atomic.
type activityClock struct{ ns atomic.Int64 }

func newActivityClock(t time.Time) *activityClock {
	c := &activityClock{}
	c.set(t)
	return c
}

func (c *activityClock) now() time.Time  { return time.Unix(0, c.ns.Load()).UTC() }
func (c *activityClock) set(t time.Time) { c.ns.Store(t.UnixNano()) }

// activityHeldHandler announces on entered that a request is inside the
// handler, then holds it there until release is closed.
func activityHeldHandler(entered chan<- struct{}, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
}

// activityRouteMux is the daemon's route table as far as the exemptions go: the
// console's real registration, plus the two routes runServe mounts itself
// (main.go), restated because runServe's mux is out of a test's reach.
func activityRouteMux(t *testing.T) *http.ServeMux {
	t.Helper()
	svc, err := console.New(console.Config{})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/healthz", http.NotFoundHandler())
	mux.Handle("/api/mcp", http.NotFoundHandler())
	svc.Register(mux)
	return mux
}

// The exemptions follow the mux rather than a guess at it. Each row names the
// pattern the request is routed to, checked against the real registrations, so
// moving the console's feed fails here instead of silently counting a stream;
// and the tracker counts the request unless it is one of the exempt three.
func TestActivityTracker_ExemptionsFollowTheRoutes(t *testing.T) {
	mux := activityRouteMux(t)
	clock := newActivityClock(activityT0)
	tr := newActivityTracker(clock.now)
	var during int64
	h := tr.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		during, _ = tr.snapshot()
	}))

	for i, tc := range []struct {
		name    string
		method  string
		target  string
		pattern string // what mux.Handler reports; "" when no route matches
		exempt  bool
	}{
		// The console's live feed. Its pattern is GET, which the mux also
		// serves HEAD through, so both hold the stream open.
		{"feed", http.MethodGet, "/console/events", "GET /console/events", true},
		{"feed with a query", http.MethodGet, "/console/events?feed=interactions", "GET /console/events", true},
		{"feed by HEAD", http.MethodHead, "/console/events", "GET /console/events", true},
		{"feed with an escaped letter", http.MethodGet, "/console/%65vents", "GET /console/events", true},
		{"feed path by POST", http.MethodPost, "/console/events", "", false},
		{"event detail page", http.MethodGet, "/console/events/01K9Z3M7W1Q2C4E6G8J0N2R4T6", "GET /console/events/{id}", false},
		{"feed path with a trailing slash", http.MethodGet, "/console/events/", "GET /console/", false},
		// Not canonical: the mux answers with its own redirect. Handler names
		// the pattern the redirect leads to, but this request is short.
		{"feed path not yet cleaned", http.MethodGet, "/console//events", "GET /console/events", false},
		// The one over-match: an encoded slash decodes to the feed's path but
		// routes nowhere, and a 404 left uncounted costs nothing.
		{"encoded slash", http.MethodGet, "/console%2Fevents", "", true},
		{"console home", http.MethodGet, "/console/", "GET /console/{$}", false},

		// MCP: the GET is the standing stream; everything else is a call.
		{"mcp stream", http.MethodGet, "/api/mcp", "/api/mcp", true},
		{"mcp stream with an escaped letter", http.MethodGet, "/api/%6dcp", "/api/mcp", true},
		{"mcp tool call", http.MethodPost, "/api/mcp", "/api/mcp", false},
		{"mcp session delete", http.MethodDelete, "/api/mcp", "/api/mcp", false},
		{"mcp HEAD", http.MethodHead, "/api/mcp", "/api/mcp", false},
		{"mcp path with a trailing slash", http.MethodGet, "/api/mcp/", "", false},

		// Health probes, whatever the method.
		{"health", http.MethodGet, "/healthz", "/healthz", true},
		{"health by HEAD", http.MethodHead, "/healthz", "/healthz", true},
		{"health by POST", http.MethodPost, "/healthz", "/healthz", true},
		{"health with a query", http.MethodGet, "/healthz?probe=1", "/healthz", true},
		{"health with a trailing slash", http.MethodGet, "/healthz/", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			_, pattern := mux.Handler(req)
			require.Equal(t, tc.pattern, pattern, "the route this request reaches")

			_, before := tr.snapshot()
			end := activityT0.Add(time.Duration(i+1) * time.Minute)
			clock.set(end)
			during = -1
			h.ServeHTTP(httptest.NewRecorder(), req)
			inFlight, last := tr.snapshot()
			require.Zero(t, inFlight)
			if tc.exempt {
				require.Zero(t, during, "an exempt request is never in flight")
				require.Equal(t, before, last, "an exempt request leaves no stamp")
				return
			}
			require.EqualValues(t, 1, during, "a counted request is in flight while it is served")
			require.Equal(t, end, last, "a counted request is stamped when it ends")
		})
	}
}

// A request is in flight from the moment it reaches the tracker until its
// handler returns, and the stamp is taken at that end: the clock moves on while
// the handler is held, and the start time is never what gets recorded.
func TestActivityTracker_InFlightWhileHeldStampedAtEnd(t *testing.T) {
	clock := newActivityClock(activityT0)
	tr := newActivityTracker(clock.now)

	inFlight, last := tr.snapshot()
	require.Zero(t, inFlight)
	require.Equal(t, activityT0, last, "before any request, the last activity is the construction time")

	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := tr.wrap(activityHeldHandler(entered, release))
	clock.set(activityT0.Add(time.Minute))
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/mcp", nil))
	}()
	<-entered

	inFlight, last = tr.snapshot()
	require.EqualValues(t, 1, inFlight)
	require.Equal(t, activityT0, last, "a request that has only started is not stamped")

	end := activityT0.Add(5 * time.Minute)
	clock.set(end)
	close(release)
	<-done

	inFlight, last = tr.snapshot()
	require.Zero(t, inFlight)
	require.Equal(t, end, last)
}

// Concurrent requests each hold one count, and the count steps back down as
// they finish. An exempt stream held open alongside them adds nothing, and its
// end stamps nothing.
func TestActivityTracker_ConcurrentRequests(t *testing.T) {
	const perBatch = 8
	clock := newActivityClock(activityT0)
	tr := newActivityTracker(clock.now)
	entered := make(chan struct{})
	serve := func(wg *sync.WaitGroup, release <-chan struct{}, method, target string) {
		h := tr.wrap(activityHeldHandler(entered, release))
		wg.Go(func() {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
		})
	}

	var first, second, stream sync.WaitGroup
	releaseFirst, releaseSecond, releaseStream := make(chan struct{}), make(chan struct{}), make(chan struct{})
	for range perBatch {
		serve(&first, releaseFirst, http.MethodPost, "/api/mcp")
		serve(&second, releaseSecond, http.MethodGet, "/console/sessions")
	}
	serve(&stream, releaseStream, http.MethodGet, "/console/events")
	for range 2*perBatch + 1 {
		<-entered
	}
	inFlight, last := tr.snapshot()
	require.EqualValues(t, 2*perBatch, inFlight, "each counted request holds one count; the stream holds none")
	require.Equal(t, activityT0, last)

	firstEnd := activityT0.Add(time.Minute)
	clock.set(firstEnd)
	close(releaseFirst)
	first.Wait()
	inFlight, last = tr.snapshot()
	require.EqualValues(t, perBatch, inFlight)
	require.Equal(t, firstEnd, last)

	secondEnd := activityT0.Add(2 * time.Minute)
	clock.set(secondEnd)
	close(releaseSecond)
	second.Wait()
	inFlight, last = tr.snapshot()
	require.Zero(t, inFlight)
	require.Equal(t, secondEnd, last)

	clock.set(activityT0.Add(3 * time.Minute))
	close(releaseStream)
	stream.Wait()
	inFlight, last = tr.snapshot()
	require.Zero(t, inFlight)
	require.Equal(t, secondEnd, last, "an exempt stream ending is not activity")
}

// A panicking handler still gives its count back and is stamped, and the panic
// reaches net/http unchanged, which recovers it per connection and keeps
// serving. A leaked count would outlive the request for the daemon's life.
func TestActivityTracker_PanicGivesItsCountBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"plain panic", "handler exploded"},
		// net/http's sentinel for abandoning a response, recovered unlogged.
		{"abort sentinel", http.ErrAbortHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newActivityClock(activityT0)
			tr := newActivityTracker(clock.now)
			tracked := tr.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/boom" {
					panic(tc.value)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			// The outermost frame unwinds after the tracker's defer has run: it
			// records the panic and hands it on to net/http, as the daemon's
			// stack would. The send never blocks, so a client retry cannot
			// wedge the server.
			unwound := make(chan any, 1)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() {
					if v := recover(); v != nil {
						select {
						case unwound <- v:
						default:
						}
						panic(v)
					}
				}()
				tracked.ServeHTTP(w, r)
			}))
			srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the plain panic's stack trace
			srv.Start()
			defer srv.Close()

			boom := activityT0.Add(time.Minute)
			clock.set(boom)
			resp, err := srv.Client().Get(srv.URL + "/boom")
			if err == nil {
				resp.Body.Close()
			}
			require.Error(t, err, "net/http drops the connection of a panicking handler")
			select {
			case v := <-unwound:
				require.Equal(t, tc.value, v, "the tracker hands the panic on unchanged")
			case <-time.After(5 * time.Second):
				t.Fatal("the panic never reached the outermost frame")
			}
			inFlight, last := tr.snapshot()
			require.Zero(t, inFlight, "the deferred release ran while the panic unwound")
			require.Equal(t, boom, last, "and so did the end stamp")

			// The daemon is still serving, and the count still balances.
			after := activityT0.Add(2 * time.Minute)
			clock.set(after)
			resp, err = srv.Client().Get(srv.URL + "/ok")
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusNoContent, resp.StatusCode)
			inFlight, last = tr.snapshot()
			require.Zero(t, inFlight)
			require.Equal(t, after, last)
		})
	}
}

// Wired inside the host guard, a request the guard refuses never reaches the
// tracker, while the same request under an allowed Host counts.
func TestActivityTracker_InsideTheHostGuard(t *testing.T) {
	clock := newActivityClock(activityT0)
	tr := newActivityTracker(clock.now)
	h := hostGuard("127.0.0.1:8081", nil, tr.wrap(okHandler()))
	end := activityT0.Add(time.Minute)
	clock.set(end)

	for _, tc := range []struct {
		host     string
		wantCode int
		wantLast time.Time
	}{
		{"evil.example.com:8081", http.StatusMisdirectedRequest, activityT0},
		{"127.0.0.1:8081", http.StatusTeapot, end},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
		req.Host = tc.host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, tc.wantCode, rr.Code, "Host: %s", tc.host)
		inFlight, last := tr.snapshot()
		require.Zero(t, inFlight)
		require.Equal(t, tc.wantLast, last, "Host: %s", tc.host)
	}
}

// The stamp is a high-water mark: a request ending on an earlier clock reading
// than the last stamp -- a wall clock stepped back, or a racer that read the
// clock first and stored second -- cannot move it backwards.
func TestActivityTracker_LastActivityNeverMovesBack(t *testing.T) {
	clock := newActivityClock(activityT0)
	tr := newActivityTracker(clock.now)
	h := tr.wrap(okHandler())

	later := activityT0.Add(10 * time.Minute)
	for _, at := range []time.Time{later, activityT0.Add(time.Minute), activityT0.Add(-time.Hour)} {
		clock.set(at)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/console/", nil))
	}
	_, last := tr.snapshot()
	require.Equal(t, later, last)
}
