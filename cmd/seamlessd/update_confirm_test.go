package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arctop/seamless/internal/update"
)

// confirmRig is a daemon whose /healthz answers are scripted per poll, and a
// clock that advances one poll interval per reading, so a confirmation's
// deadlines run deterministically and at once.
type confirmRig struct {
	t       *testing.T
	dataDir string
	srv     *httptest.Server

	mu      sync.Mutex
	answers []healthAnswer // one per poll; the last repeats, or all of them when cycle
	cycle   bool
	polls   int
	clock   time.Time
}

func newConfirmRig(t *testing.T, answers ...healthAnswer) *confirmRig {
	t.Helper()
	r := &confirmRig{t: t, dataDir: t.TempDir(), answers: answers, clock: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		i := min(r.polls, len(r.answers)-1)
		if r.cycle {
			i = r.polls % len(r.answers)
		}
		a := r.answers[i]
		r.polls++
		r.mu.Unlock()
		if a.Version == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(a)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// record writes the update state naming instance as the running release ver.
func (r *confirmRig) record(ver, instance string) {
	r.t.Helper()
	require.NoError(r.t, update.SaveState(r.dataDir, update.State{Running: update.Running{
		Version: mustVersion(r.t, ver), Instance: instance, PID: 7,
	}}))
}

func (r *confirmRig) params(want string, starting func() bool) confirmParams {
	closed := make(chan time.Time)
	close(closed)
	timing := confirmTiming{poll: 2 * time.Second, stable: 10 * time.Second, deadline: 120 * time.Second, starting: 5 * time.Minute}
	return confirmParams{
		want: mustVersion(r.t, want), client: r.srv.Client(), baseURL: r.srv.URL, dataDir: r.dataDir,
		prevInstance: "old", prevPID: 7, timing: timing,
		now: func() time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.clock = r.clock.Add(timing.poll)
			return r.clock
		},
		ticker:   func(time.Duration) (<-chan time.Time, func()) { return closed, func() {} },
		starting: starting,
	}
}

func never() bool { return false }

func TestConfirmServing_StableNewProcessConfirms(t *testing.T) {
	r := newConfirmRig(t, healthAnswer{}, healthAnswer{Version: "0.7.3+abc1234", Instance: "new"})
	r.record("0.7.3", "new")
	res := confirmServing(context.Background(), r.params("0.7.3", never))
	require.True(t, res.ok, res.reason)
	require.Equal(t, "new", res.instance)
	// Two polls at least 10s apart, at 2s a poll: never fewer than 6.
	require.GreaterOrEqual(t, r.polls, 6)
}

func TestConfirmServing_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		answers []healthAnswer
		state   [2]string // the recorded running release and instance
		reason  string
	}{
		{"the old release keeps answering", []healthAnswer{{Version: "0.7.2+abc", Instance: "new"}}, [2]string{"0.7.2", "new"}, "serves v0.7.2, not v0.7.3"},
		{"the same process as before the install", []healthAnswer{{Version: "0.7.3+abc", Instance: "old"}}, [2]string{"0.7.3", "old"}, "not from a process started since the install"},
		{"a daemon the update state does not name", []healthAnswer{{Version: "0.7.3+abc", Instance: "stranger"}}, [2]string{"0.7.3", "new"}, "does not name"},
		{"nothing answers", []healthAnswer{{}}, [2]string{"0.7.2", "old"}, "no answer"},
		{"a version that is not a release", []healthAnswer{{Version: "0.0.0-dev+abc", Instance: "new"}}, [2]string{"0.7.3", "new"}, "not a release"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newConfirmRig(t, tt.answers...)
			r.record(tt.state[0], tt.state[1])
			res := confirmServing(context.Background(), r.params("0.7.3", never))
			require.False(t, res.ok)
			require.Contains(t, res.reason, tt.reason)
			require.Contains(t, res.reason, "not confirmed after 2m")
		})
	}
}

// While a new daemon holds the data dir lock but is not listening yet (the
// store migration and the reconcile run before the listener), the deadline
// stretches to five minutes.
func TestConfirmServing_AStartingDaemonExtendsTheDeadline(t *testing.T) {
	r := newConfirmRig(t, healthAnswer{})
	r.record("0.7.2", "old")
	res := confirmServing(context.Background(), r.params("0.7.3", func() bool { return true }))
	require.False(t, res.ok)
	require.Contains(t, res.reason, "not confirmed after 5m")

	// A daemon that comes up within the extension is confirmed.
	answers := make([]healthAnswer, 80) // 160s of nothing
	answers = append(answers, healthAnswer{Version: "0.7.3+abc", Instance: "slow"})
	r = newConfirmRig(t, answers...)
	r.record("0.7.3", "slow")
	res = confirmServing(context.Background(), r.params("0.7.3", func() bool { return true }))
	require.True(t, res.ok, res.reason)
}

// A daemon that answers between restarts -- up, down, up -- is never held
// across two polls ten seconds apart, so a crash loop never confirms.
func TestConfirmServing_ACrashLoopNeverConfirms(t *testing.T) {
	r := newConfirmRig(t, healthAnswer{Version: "0.7.3+abc", Instance: "new"}, healthAnswer{})
	r.cycle = true
	r.record("0.7.3", "new")
	res := confirmServing(context.Background(), r.params("0.7.3", never))
	require.False(t, res.ok)
	require.Contains(t, res.reason, "not confirmed after")
}

func TestServedRelease(t *testing.T) {
	v, ok := servedRelease("0.8.0+1a2b3c4")
	require.True(t, ok)
	require.Equal(t, "0.8.0", v.String())
	_, ok = servedRelease("0.8.0-rc1+abc")
	require.False(t, ok)
	_, ok = servedRelease("")
	require.False(t, ok)
}

// The lock probe never creates the lock file, and takes nothing it keeps.
func TestDataDirLockHolder(t *testing.T) {
	dataDir := t.TempDir()
	held, _ := dataDirLockHolder(dataDir)
	require.False(t, held)
	_, err := os.Stat(filepath.Join(dataDir, dataDirLockName))
	require.ErrorIs(t, err, os.ErrNotExist, "the probe never creates the lock file")

	lock, err := tryLockFile(filepath.Join(dataDir, dataDirLockName))
	require.NoError(t, err)
	held, pid := dataDirLockHolder(dataDir)
	require.True(t, held)
	require.Equal(t, os.Getpid(), pid)
	require.NoError(t, lock.Close())

	held, _ = dataDirLockHolder(dataDir)
	require.False(t, held)
	again, err := tryLockFile(filepath.Join(dataDir, dataDirLockName))
	require.NoError(t, err, "a probe that found the lock free let it go")
	require.NoError(t, again.Close())
}
