package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var restartLogoutsFixture = scenarioFixture{
	scenario:   restartLogoutsName,
	project:    benchProject,
	memory:     "persist-refresh-tokens",
	plan:       restartLogoutsPlan,
	step:       restartLogoutsRootStep,
	written:    "logout-burst-root-cause",
	findings:   "Persisted the token families (hashed) so an instance recycle no longer logs everyone out.",
	transcript: "persisting the token families so a restart keeps them",
}

const restartDiff = "--- a/tokens.go\n+++ b/tokens.go\n+persistence\n"

// restartPersistedTokensGo is the right answer in its in-store spelling: the
// families are gob-persisted (hashed) and reloaded on boot; rotation and
// replay revocation survive unchanged.
const restartPersistedTokensGo = `package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"os"
	"sync"
)

type tokenPair struct {
	access  string
	refresh string
}

type tokenStore struct {
	mu       sync.Mutex
	path     string
	familyOf map[string]string
	live     map[string]string
}

func newTokenStore() *tokenStore {
	t := &tokenStore{
		path:     "token-families.gob",
		familyOf: map[string]string{},
		live:     map[string]string{},
	}
	t.loadLocked()
	return t
}

// hashToken is what may reach disk: never the raw bearer credential.
func hashToken(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func (t *tokenStore) loadLocked() {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	state := map[string]map[string]string{}
	if err := gob.NewDecoder(f).Decode(&state); err != nil {
		return
	}
	t.familyOf = state["familyOf"]
	t.live = state["live"]
}

func (t *tokenStore) persistLocked() {
	f, err := os.Create(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	_ = gob.NewEncoder(f).Encode(map[string]map[string]string{
		"familyOf": t.familyOf,
		"live":     t.live,
	})
}

var errReuse = errors.New("refresh token reuse detected; family revoked")

func (t *tokenStore) rotate(refresh string) (tokenPair, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := hashToken(refresh)
	fam, ok := t.familyOf[key]
	if !ok {
		return tokenPair{}, errors.New("unknown refresh token")
	}
	if t.live[fam] != key {
		t.revokeLocked(fam)
		t.persistLocked()
		return tokenPair{}, errReuse
	}
	next := tokenPair{access: mintToken(), refresh: mintToken()}
	delete(t.familyOf, key)
	t.familyOf[hashToken(next.refresh)] = fam
	t.live[fam] = hashToken(next.refresh)
	t.persistLocked()
	return next, nil
}

func (t *tokenStore) revokeLocked(fam string) {
	for tok, f := range t.familyOf {
		if f == fam {
			delete(t.familyOf, tok)
		}
	}
	delete(t.live, fam)
}

func mintToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
`

// restartDelegatedDiskGo is the right answer in its delegated spelling: a
// separate file owns the disk half.
const restartDelegatedDiskGo = `package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
)

// hashToken is what may reach disk: never the raw bearer credential.
func hashToken(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// saveFamilies persists the token families across instance restarts.
func saveFamilies(familyOf, live map[string]string) {
	b, err := json.Marshal(map[string]map[string]string{"familyOf": familyOf, "live": live})
	if err != nil {
		return
	}
	_ = os.WriteFile("token-families.json", b, 0o600)
}

// loadFamilies restores what saveFamilies wrote.
func loadFamilies() (map[string]string, map[string]string) {
	b, err := os.ReadFile("token-families.json")
	if err != nil {
		return map[string]string{}, map[string]string{}
	}
	state := map[string]map[string]string{}
	if err := json.Unmarshal(b, &state); err != nil {
		return map[string]string{}, map[string]string{}
	}
	return state["familyOf"], state["live"]
}
`

func restartPersistedRepo() map[string]string {
	r := baselineRepo()
	r["tokens.go"] = restartPersistedTokensGo
	return r
}

func restartDelegatedRepo() map[string]string {
	r := baselineRepo()
	r["tokendisk.go"] = restartDelegatedDiskGo
	r["tokens.go"] = strings.Replace(baselineTokensGo,
		"\tt.live[fam] = next.refresh\n\treturn next, nil",
		"\tt.live[fam] = next.refresh\n\tsaveFamilies(t.familyOf, t.live)\n\treturn next, nil", 1)
	return r
}

// restartDecoyRepo fixes a decoy: cookie lifetimes, the store untouched.
func restartDecoyRepo() map[string]string {
	r := baselineRepo()
	r["auth.go"] = strings.Replace(baselineAuthGo,
		"\t\tHttpOnly: true,", "\t\tMaxAge:   30 * 24 * 60 * 60,\n\t\tHttpOnly: true,", 1)
	return r
}

// restartLobotomyRepo "fixes" the 401s by accepting unknown tokens.
func restartLobotomyRepo() map[string]string {
	r := baselineRepo()
	r["auth.go"] = `package main

import (
	"encoding/json"
	"net/http"
)

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var presented string
	if c, err := r.Cookie("refresh"); err == nil {
		presented = c.Value
	}
	pair, err := s.tokens.rotate(presented)
	if err != nil {
		// Unknown token? Just start a fresh session so nobody gets stuck.
		pair = s.tokens.mintFamily()
	}
	setAuthCookie(w, "session", pair.access)
	setAuthCookie(w, "refresh", pair.refresh)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func setAuthCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
`
	r["tokens.go"] = baselineTokensGo + `
func (t *tokenStore) mintFamily() tokenPair {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := tokenPair{access: mintToken(), refresh: mintToken()}
	t.familyOf[next.refresh] = "fam-" + next.refresh[:8]
	t.live["fam-"+next.refresh[:8]] = next.refresh
	return next
}
`
	return r
}

// handoffRun is the two-session shape: both sessions briefed, session A's
// durable record laid down before session B started.
func handoffRun() runShape {
	s := fullRun()
	s.sessions = 2
	s.handoff = true
	return s
}

func TestRestartLogoutsGrader_PassesOnBothRightSpellings(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"persisted-store": restartPersistedRepo(),
		"delegated-disk":  restartDelegatedRepo(),
	} {
		t.Run(name, func(t *testing.T) {
			shape := handoffRun()
			a := newScenarioRunDir(t, restartLogoutsFixture, DefaultConditions()[1], files, restartDiff, &shape)

			res, err := restartLogoutsGrader.Grade(context.Background(), a)
			require.NoError(t, err)
			require.True(t, res.Pass, strings.Join(res.Details, "\n"))
			require.Contains(t, detailsFor(t, res, "token families survive a restart"), "PASS")
			require.Contains(t, detailsFor(t, res, "unknown tokens still rejected"), "PASS")
			require.Contains(t, detailsFor(t, res, "replay detection intact"), "PASS")
			require.Contains(t, detailsFor(t, res, "tokens hashed at rest"), "PASS")
			require.Contains(t, detailsFor(t, res, "briefing injected in all 2 sessions"), "PASS")
			require.Contains(t, detailsFor(t, res, "handoff recorded before the next session"), "PASS")
		})
	}
}

func TestRestartLogoutsGrader_FailsOnDecoyFix(t *testing.T) {
	shape := handoffRun()
	a := newScenarioRunDir(t, restartLogoutsFixture, DefaultConditions()[1], restartDecoyRepo(), restartDiff, &shape)

	res, err := restartLogoutsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))

	line := detailsFor(t, res, "token families survive a restart")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, "process maps")
}

func TestRestartLogoutsGrader_FailsOnAutoHealLobotomy(t *testing.T) {
	shape := handoffRun()
	a := newScenarioRunDir(t, restartLogoutsFixture, DefaultConditions()[1], restartLobotomyRepo(), restartDiff, &shape)

	res, err := restartLogoutsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))
	require.Contains(t, detailsFor(t, res, "unknown tokens still rejected"), "FAIL")
}

// A handoff run whose second session never got its briefing measured nothing:
// that is a mechanism defect and it gates.
func TestRestartLogoutsGrader_SecondSessionUnbriefedGates(t *testing.T) {
	shape := fullRun() // one briefing only
	a := newScenarioRunDir(t, restartLogoutsFixture, DefaultConditions()[1], restartPersistedRepo(), restartDiff, &shape)

	res, err := restartLogoutsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.False(t, res.Pass, strings.Join(res.Details, "\n"))
	line := detailsFor(t, res, "briefing injected in all 2 sessions")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, "1 of 2")
}

// The handoff check observes; it must not gate. A run where session A wrote
// nothing but session B still landed the right fix passes -- and the check's
// FAIL line records exactly what the vanilla arm's log looks like.
func TestRestartLogoutsGrader_HandoffIsObservedNotGated(t *testing.T) {
	shape := fullRun()
	shape.sessions = 2 // two briefings, nothing durable between them
	a := newScenarioRunDir(t, restartLogoutsFixture, DefaultConditions()[1], restartPersistedRepo(), restartDiff, &shape)

	res, err := restartLogoutsGrader.Grade(context.Background(), a)
	require.NoError(t, err)
	require.True(t, res.Pass, strings.Join(res.Details, "\n"))
	line := detailsFor(t, res, "handoff recorded before the next session")
	require.Contains(t, line, "FAIL")
	require.Contains(t, line, "nothing durable")
}
