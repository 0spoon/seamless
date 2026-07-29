package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var deployDrainFixture = scenarioFixture{
	scenario:   deployDrainName,
	project:    benchProject,
	memory:     deployDrainMemory,
	plan:       deployDrainPlan,
	step:       deployDrainStep,
	written:    "drain-window-note",
	findings:   "Shipped step 2: SIGTERM now fails healthz, waits out the LB window, then drains.",
	transcript: "failing healthz first so the LB stops routing before Shutdown",
}

const drainDiff = "--- a/main.go\n+++ b/main.go\n+graceful shutdown\n"

// drainRightMainGo is the informed fix: fail healthz, wait out the LB's poll
// window, then Shutdown.
const drainRightMainGo = `package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

const version = "1.4.2"

func main() {
	srv := newServer()
	httpSrv := &http.Server{Addr: ":8080", Handler: srv.routes()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("myapp %s listening on %s", version, httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	// Fail the health check first: the LB polls every 5s and needs two
	// consecutive failures before it stops routing here.
	srv.startDraining()
	time.Sleep(15 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
`

// drainServerGo teaches healthz to answer 503 while draining.
var drainServerGo = strings.NewReplacer(
	"\t\"html/template\"", "\t\"html/template\"\n\t\"sync/atomic\"",
	"type server struct {\n\ttokens *tokenStore\n\thome   *template.Template\n}",
	"type server struct {\n\ttokens   *tokenStore\n\thome     *template.Template\n\tdraining atomic.Bool\n}",
	`func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}`,
	`func (s *server) startDraining() { s.draining.Store(true) }

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}`,
).Replace(baselineServerGo)

// drainTextbookMainGo is the uninformed fix: signal handling plus Shutdown,
// the health endpoint untouched.
const drainTextbookMainGo = `package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

const version = "1.4.2"

func main() {
	srv := newServer()
	httpSrv := &http.Server{Addr: ":8080", Handler: srv.routes()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("myapp %s listening on %s", version, httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
`

func drainRightRepo() map[string]string {
	r := baselineRepo()
	r["main.go"] = drainRightMainGo
	r["server.go"] = drainServerGo
	return r
}

func drainTextbookRepo() map[string]string {
	r := baselineRepo()
	r["main.go"] = drainTextbookMainGo
	return r
}

func TestDeployDrainGrader_PassesOnDrainThenShutdown(t *testing.T) {
	// The replacer must have actually rewritten the fixture, or the test
	// would silently exercise the baseline.
	require.Contains(t, drainServerGo, "StatusServiceUnavailable")

	shape := fullRun()
	a := newScenarioRunDir(t, deployDrainFixture, DefaultConditions()[1], drainRightRepo(), drainDiff, &shape)

	res, err := deployDrainGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "graceful shutdown on SIGTERM"), "PASS")
	require.Contains(t, detailsFor(t, res, "healthz drains before shutdown"), "PASS")
	require.Contains(t, detailsFor(t, res, "a drain wait in the shutdown path"), "PASS")
}

func TestDeployDrainGrader_FailsOnTextbookShutdown(t *testing.T) {
	shape := fullRun()
	a := newScenarioRunDir(t, deployDrainFixture, DefaultConditions()[1], drainTextbookRepo(), drainDiff, &shape)

	res, err := deployDrainGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	// The work gate credits the shutdown; the discriminator names the memory.
	require.Contains(t, detailsFor(t, res, "graceful shutdown on SIGTERM"), "PASS")
	line := detailsFor(t, res, "healthz drains before shutdown")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, deployDrainMemory)
}

func TestDeployDrainGrader_FailsWhenNothingChanged(t *testing.T) {
	a := newScenarioRunDir(t, deployDrainFixture, DefaultConditions()[0], baselineRepo(), "", nil)

	res, err := deployDrainGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)
	require.Contains(t, detailsFor(t, res, "graceful shutdown on SIGTERM"), "no shutdown machinery")
	require.Contains(t, detailsFor(t, res, "healthz drains before shutdown"), "FAIL")
}

func TestDeployDrainGrader_FailsWhenHealthzRemoved(t *testing.T) {
	files := drainTextbookRepo()
	files["server.go"] = strings.NewReplacer(
		"\tmux.HandleFunc(\"/healthz\", s.handleHealthz)\n", "",
		`func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}`, "",
	).Replace(baselineServerGo)

	shape := fullRun()
	a := newScenarioRunDir(t, deployDrainFixture, DefaultConditions()[1], files, drainDiff, &shape)

	res, err := deployDrainGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "healthz drains before shutdown"), "gone")
}
