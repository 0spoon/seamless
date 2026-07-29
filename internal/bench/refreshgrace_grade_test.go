package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var refreshGraceFixture = scenarioFixture{
	scenario:   refreshGraceName,
	project:    benchProject,
	memory:     "refresh-token-single-use",
	plan:       refreshGracePlan,
	step:       refreshGraceStep,
	written:    "grace-window-shipped",
	findings:   "rotate now tolerates a one-shot resubmit inside a 30s grace window; older replays still revoke.",
	transcript: "adding a bounded reuse interval to rotate instead of more client-side mitigation",
}

const graceDiff = "--- a/tokens.go\n+++ b/tokens.go\n+grace window\n"

// graceTokensGo is the right answer: rotate returns the already-minted pair
// for a resubmit of the immediately-previous token inside the window, and
// still revokes anything older.
const graceTokensGo = `package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// graceWindow is how long a resubmit of the just-rotated token is treated as
// benign (a lost response or a coalesced timer, not theft).
const graceWindow = 30 * time.Second

type tokenPair struct {
	access  string
	refresh string
}

type tokenStore struct {
	mu       sync.Mutex
	familyOf map[string]string
	live     map[string]string
	prevOf   map[string]string
	prevAt   map[string]time.Time
	lastPair map[string]tokenPair
}

func newTokenStore() *tokenStore {
	return &tokenStore{
		familyOf: map[string]string{},
		live:     map[string]string{},
		prevOf:   map[string]string{},
		prevAt:   map[string]time.Time{},
		lastPair: map[string]tokenPair{},
	}
}

var errReuse = errors.New("refresh token reuse detected; family revoked")

func (t *tokenStore) rotate(refresh string) (tokenPair, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	fam, ok := t.familyOf[refresh]
	if !ok {
		return tokenPair{}, errors.New("unknown refresh token")
	}
	if t.live[fam] != refresh {
		// A resubmit of the immediately-previous token inside the grace
		// window is a benign double-submit: hand back the same pair.
		if t.prevOf[fam] == refresh && time.Since(t.prevAt[fam]) < graceWindow {
			return t.lastPair[fam], nil
		}
		t.revokeLocked(fam)
		return tokenPair{}, errReuse
	}
	next := tokenPair{access: mintToken(), refresh: mintToken()}
	delete(t.familyOf, refresh)
	t.familyOf[next.refresh] = fam
	t.prevOf[fam] = refresh
	t.prevAt[fam] = time.Now()
	t.lastPair[fam] = next
	t.live[fam] = next.refresh
	return next, nil
}

func (t *tokenStore) revokeLocked(fam string) {
	for tok, f := range t.familyOf {
		if f == fam {
			delete(t.familyOf, tok)
		}
	}
	delete(t.live, fam)
	delete(t.prevOf, fam)
	delete(t.prevAt, fam)
	delete(t.lastPair, fam)
}

func mintToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
`

// graceNoRevokeTokensGo has the grace machinery but deleted the revocation:
// the lazy fix that turns scattered logouts into silent token theft.
var graceNoRevokeTokensGo = strings.NewReplacer(
	"\t\tt.revokeLocked(fam)\n\t\treturn tokenPair{}, errReuse", "\t\treturn t.lastPair[fam], nil",
	`func (t *tokenStore) revokeLocked(fam string) {
	for tok, f := range t.familyOf {
		if f == fam {
			delete(t.familyOf, tok)
		}
	}
	delete(t.live, fam)
	delete(t.prevOf, fam)
	delete(t.prevAt, fam)
	delete(t.lastPair, fam)
}

`, "",
	"var errReuse = errors.New(\"refresh token reuse detected; family revoked\")\n\n", "",
).Replace(graceTokensGo)

func graceRightRepo() map[string]string {
	r := baselineRepo()
	r["tokens.go"] = graceTokensGo
	return r
}

func graceNoRevokeRepo() map[string]string {
	r := baselineRepo()
	r["tokens.go"] = graceNoRevokeTokensGo
	return r
}

// graceJitterRepo re-treads the recorded dead end: client-side jitter, the
// store untouched.
func graceJitterRepo() map[string]string {
	r := baselineRepo()
	r["static/app.js"] = `async function refresh() {
  await fetch('/auth/refresh', { method: 'POST', credentials: 'same-origin' });
}
const jitter = Math.floor(Math.random() * 5000);
setInterval(refresh, 10 * 60 * 1000 + jitter);
`
	return r
}

const graceJitterDiff = "--- a/static/app.js\n+++ b/static/app.js\n+jitter\n"

func TestRefreshGraceGrader_PassesOnServerSideGraceWindow(t *testing.T) {
	shape := fullRun()
	shape.trials = true
	a := newScenarioRunDir(t, refreshGraceFixture, DefaultConditions()[1], graceRightRepo(), graceDiff, &shape)

	res, err := refreshGraceGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "rotate tolerates a benign resubmit"), "PASS")
	require.Contains(t, detailsFor(t, res, "theft detection preserved"), "PASS")
	require.Contains(t, detailsFor(t, res, "recorded dead end not re-trodden"), "PASS")
	require.Contains(t, detailsFor(t, res, "recorded trials consulted"), "PASS")
	require.Contains(t, detailsFor(t, res, "a new trial recorded"), "PASS")
}

func TestRefreshGraceGrader_FailsOnClientSideRetread(t *testing.T) {
	shape := fullRun()
	a := newScenarioRunDir(t, refreshGraceFixture, DefaultConditions()[1], graceJitterRepo(), graceJitterDiff, &shape)

	res, err := refreshGraceGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	require.Contains(t, detailsFor(t, res, "rotate tolerates a benign resubmit"), "rotate is unchanged")
	line := detailsFor(t, res, "recorded dead end not re-trodden")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, refreshGraceLab)
}

func TestRefreshGraceGrader_FailsWhenRevocationRemoved(t *testing.T) {
	// The replacer must have actually stripped the revocation, or the test
	// would silently exercise the right answer.
	require.NotContains(t, graceNoRevokeTokensGo, "revokeLocked")

	shape := fullRun()
	a := newScenarioRunDir(t, refreshGraceFixture, DefaultConditions()[1], graceNoRevokeRepo(), graceDiff, &shape)

	res, err := refreshGraceGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "rotate tolerates a benign resubmit"), "PASS")
	require.Contains(t, detailsFor(t, res, "theft detection preserved"), "FAIL")
}

func TestRefreshGraceGrader_FailsWhenNothingChanged(t *testing.T) {
	a := newScenarioRunDir(t, refreshGraceFixture, DefaultConditions()[0], baselineRepo(), "", nil)

	res, err := refreshGraceGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass)
	require.Contains(t, detailsFor(t, res, "rotate tolerates a benign resubmit"), "rotate is unchanged")
}

// The lab checks observe; they must not gate. An agent that landed the right
// fix without ever calling trial_query still passes.
func TestRefreshGraceGrader_TrialChecksDoNotGate(t *testing.T) {
	shape := fullRun() // no trials in the shape
	a := newScenarioRunDir(t, refreshGraceFixture, DefaultConditions()[1], graceRightRepo(), graceDiff, &shape)

	res, err := refreshGraceGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "recorded trials consulted"), "FAIL")
	require.Contains(t, detailsFor(t, res, "a new trial recorded"), "FAIL")
}
