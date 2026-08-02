package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/core"
)

// The promise sentences the console serves, quoted here so the assertions read
// against the real copy rather than a placeholder the CLI would never see.
const (
	openPromise   = "Open — shares normally. Global knowledge flows in; family surfaces may include it."
	sealedPromise = "Sealed — nothing in or out. Agents here see only this project: no global memories, no family, no cross-project reads."
)

// consoleEnv builds an env pointed at a console stub. Unlike errServer (one code
// for every path) the handler is the test's, so a single stub can serve both
// halves of this command -- the summary GET and the isolation POST -- and record
// what the CLI put on the wire.
func consoleEnv(t *testing.T, h http.HandlerFunc) (*env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	e, out, errb := stubEnv()
	e.loadConfig = func() (config.Config, error) {
		cfg := config.Defaults()
		cfg.Addr = strings.TrimPrefix(srv.URL, "http://")
		return cfg, nil
	}
	return e, out, errb
}

// recorded is one request the stub saw, reduced to what the transport contract
// constrains: the method, the path, and the parsed form.
type recorded struct {
	method string
	path   string
	form   url.Values
}

// isolationStub answers the summary GET with state/promise and the isolation POST
// with body, recording every request. body is written verbatim so a test can pin
// the CLI's rendering of the exact JSON the console documents.
func isolationStub(t *testing.T, state, promise string, code int, body string) (http.HandlerFunc, *[]recorded) {
	t.Helper()
	var seen []recorded
	return func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		seen = append(seen, recorded{method: r.Method, path: r.URL.Path, form: r.Form})
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"slug":"acme","isolation":"` + state + `","isolationPromise":"` + promise + `"}`))
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}, &seen
}

// The read path is a read: no state argument means a plain GET of the summary the
// console already serves, so asking what the fence is cannot move it.
func TestProjectIsolation_ShowNeverPosts(t *testing.T) {
	h, seen := isolationStub(t, "confidential", openPromise, http.StatusOK, "")
	e, out, _ := consoleEnv(t, h)

	require.Equal(t, 0, dispatch(context.Background(), e, []string{"project", "isolation", "acme"}))
	require.Contains(t, out.String(), "acme  isolation: confidential")
	require.Contains(t, out.String(), openPromise, "the promise is echoed from the console's copy, not transcribed")

	require.Len(t, *seen, 1)
	require.Equal(t, http.MethodGet, (*seen)[0].method)
	require.Equal(t, "/console/projects/acme", (*seen)[0].path)
}

// The tighten half, both directions of --yes, against the response contract
// verbatim. The flag is the console's confirm panel: without it the consequences
// print and NOTHING is written, which is why the outcome is read off `applied`
// rather than off the status code.
func TestProjectIsolation_Tighten(t *testing.T) {
	const confirmRequired = `{"project":"acme","requested":"sealed","state":"open",
		"promise":"` + openPromise + `","requestedPromise":"` + sealedPromise + `",
		"applied":false,"confirmRequired":true,"blocked":false,"globalMemories":-1,
		"effects":{"project":"acme","from":"open","to":"sealed","families":["product"],"parent":"acme-root"},
		"message":"Confirm required: acme open -> sealed. Left family product. Detached from parent acme-root."}`
	const applied = `{"project":"acme","requested":"sealed","state":"sealed",
		"promise":"` + sealedPromise + `","requestedPromise":"` + sealedPromise + `",
		"applied":true,"confirmRequired":false,"blocked":false,"globalMemories":-1,
		"effects":{"project":"acme","from":"open","to":"sealed","families":["product"],"parent":"acme-root"},
		"message":"Isolation set to sealed. Left family product. Detached from parent acme-root."}`

	for _, tc := range []struct {
		name    string
		argv    []string
		body    string
		exit    int
		confirm string // the confirm parameter the CLI must have sent, "" for none
		want    []string
		wantErr string
		wantNot []string
	}{
		{
			name:    "without --yes the consequences print and nothing applies",
			argv:    []string{"project", "isolation", "acme", "sealed"},
			body:    confirmRequired,
			exit:    1,
			confirm: "",
			want: []string{
				"acme  isolation: open -> sealed (not applied)",
				sealedPromise,
				"leaves family product",
				"detaches from parent acme-root",
			},
			// The verdict names the flag that would apply it, and says nothing changed.
			wantErr: "nothing applied -- re-run with --yes to apply this tighten",
			wantNot: []string{"set to", "left family", "detached from"},
		},
		{
			name:    "with --yes it applies, and the detachments are past tense",
			argv:    []string{"project", "isolation", "acme", "sealed", "--yes"},
			body:    applied,
			exit:    0,
			confirm: "1",
			want: []string{
				"acme  isolation set to sealed",
				sealedPromise,
				"left family product",
				"detached from parent acme-root",
			},
			wantNot: []string{"not applied", "--yes"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, seen := isolationStub(t, "open", openPromise, http.StatusOK, tc.body)
			e, out, errb := consoleEnv(t, h)

			require.Equal(t, tc.exit, dispatch(context.Background(), e, tc.argv))
			for _, want := range tc.want {
				require.Contains(t, out.String(), want)
			}
			for _, not := range tc.wantNot {
				require.NotContains(t, out.String(), not)
			}
			if tc.wantErr == "" {
				require.Empty(t, errb.String())
			} else {
				require.Contains(t, errb.String(), tc.wantErr)
			}

			require.Len(t, *seen, 1)
			got := (*seen)[0]
			require.Equal(t, http.MethodPost, got.method)
			require.Equal(t, "/console/projects/acme/isolation", got.path)
			// Exactly once each: the route reads r.Form, which concatenates query and
			// body, so a parameter sent in both places is two values and a 400.
			require.Equal(t, []string{"sealed"}, got.form["state"])
			require.Equal(t, []string{"json"}, got.form["format"])
			if tc.confirm == "" {
				require.NotContains(t, got.form, "confirm", "an unconfirmed tighten must not smuggle confirm=1")
			} else {
				require.Equal(t, []string{tc.confirm}, got.form["confirm"])
			}
		})
	}
}

// globalMemories is -1 while the provenance audit is unbuilt. It must never reach
// a count line: "-1 global memories" is nonsense and "0" would read as a promise
// that nothing needs relocating, which nothing has established.
func TestProjectIsolation_ProvenanceLine(t *testing.T) {
	body := func(n string) string {
		return `{"project":"acme","requested":"sealed","state":"open","promise":"` + openPromise +
			`","requestedPromise":"` + sealedPromise + `","applied":false,"confirmRequired":true,` +
			`"globalMemories":` + n + `,"effects":{"project":"acme","from":"open","to":"sealed"}}`
	}
	for _, tc := range []struct {
		name  string
		count string
		want  bool
	}{
		{"not computed yet", "-1", false},
		{"computed and empty", "0", false},
		{"computed", "12", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := isolationStub(t, "open", openPromise, http.StatusOK, body(tc.count))
			e, out, _ := consoleEnv(t, h)
			require.Equal(t, 1, dispatch(context.Background(), e, []string{"project", "isolation", "acme", "sealed"}))

			const line = "global memories originated in this project's sessions"
			if tc.want {
				require.Contains(t, out.String(), "12 "+line)
				require.Contains(t, out.String(), "relocation proposals will be filed in the Gardener inbox")
			} else {
				require.NotContains(t, out.String(), line)
			}
			// Whichever way the count went, a standalone project detaches nothing.
			require.NotContains(t, out.String(), "leaves family")
			require.NotContains(t, out.String(), "detaches from parent")
		})
	}
}

// Loosening is immediate: no --yes, no confirm step, and the response says
// applied. The state printed is the one the project holds now.
func TestProjectIsolation_LoosenIsImmediate(t *testing.T) {
	body := `{"project":"acme","requested":"open","state":"open","promise":"` + openPromise +
		`","requestedPromise":"` + openPromise + `","applied":true,"confirmRequired":false,` +
		`"blocked":false,"globalMemories":-1,"message":"Isolation set to open."}`
	h, seen := isolationStub(t, "sealed", sealedPromise, http.StatusOK, body)
	e, out, errb := consoleEnv(t, h)

	require.Equal(t, 0, dispatch(context.Background(), e, []string{"project", "isolation", "acme", "open"}))
	require.Contains(t, out.String(), "acme  isolation set to open")
	require.Contains(t, out.String(), openPromise)
	require.Empty(t, errb.String())

	require.Len(t, *seen, 1)
	require.NotContains(t, (*seen)[0].form, "confirm", "a loosen asks for no confirmation")
}

// A tighten blocked by children writes nothing and answers 409 with the reason and
// the remedy. consolePOST surfaces that message as the command's error, so the
// slugs to re-parent reach the caller instead of "console returned 409".
func TestProjectIsolation_BlockedByChildren(t *testing.T) {
	const blocked = `{"project":"acme","requested":"sealed","state":"open","applied":false,
		"confirmRequired":false,"blocked":true,"globalMemories":-1,
		"effects":{"project":"acme","from":"open","to":"sealed","children":["acme-ios"]},
		"error":"Isolation requires a standalone project. Re-parent acme-ios before making acme sealed.",
		"message":"Isolation requires a standalone project. Re-parent acme-ios before making acme sealed."}`
	h, _ := isolationStub(t, "open", openPromise, http.StatusConflict, blocked)
	e, out, errb := consoleEnv(t, h)

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"project", "isolation", "acme", "sealed", "--yes"}))
	require.Contains(t, errb.String(), "Re-parent acme-ios before making acme sealed.")
	require.Empty(t, out.String(), "a refusal wrote nothing, so it reports nothing as done")
}

// An unknown slug 404s on both halves. The GET path is the one that used to be
// easy to mistake for "the project is open".
func TestProjectIsolation_UnknownSlug(t *testing.T) {
	for _, argv := range [][]string{
		{"project", "isolation", "nope"},
		{"project", "isolation", "nope", "sealed", "--yes"},
	} {
		cfg := errServer(t, http.StatusNotFound, `{"error":"No project with slug nope."}`)
		e, out, errb := stubEnv()
		e.loadConfig = func() (config.Config, error) { return cfg, nil }

		require.Equal(t, 1, dispatch(context.Background(), e, argv), "%v", argv)
		require.Contains(t, errb.String(), "not found", "%v", argv)
		require.Empty(t, out.String(), "%v", argv)
	}
}

// A 200 that is neither applied nor awaiting confirmation is not seamlessd
// answering. Rendering it as a success would be exactly the plausible dummy the
// no-fake-results rule forbids.
func TestProjectIsolation_UnreadableOutcome(t *testing.T) {
	h, _ := isolationStub(t, "open", openPromise, http.StatusOK, `{"project":"acme","state":"open"}`)
	e, _, errb := consoleEnv(t, h)

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"project", "isolation", "acme", "sealed", "--yes"}))
	require.Contains(t, errb.String(), "neither applied nor awaiting confirmation")
}

// A blank summary means something other than seamlessd answered on the port: the
// console fills isolation in for every project.
func TestProjectIsolation_UnreadableSummary(t *testing.T) {
	h, _ := isolationStub(t, "", "", http.StatusOK, "")
	e, _, errb := consoleEnv(t, h)

	require.Equal(t, 1, dispatch(context.Background(), e, []string{"project", "isolation", "acme"}))
	require.Contains(t, errb.String(), "unreadable project summary for acme")
}

// A typo'd state is rejected before any I/O -- e.loadConfig is nil here, so a run
// that reached the network would panic rather than pass. The message names the
// accepted values instead of letting `private` become a silent default.
func TestProjectIsolation_InvalidStateNeverReachesTheWire(t *testing.T) {
	e, out, errb := stubEnv()
	require.Equal(t, 1, dispatch(context.Background(), e, []string{"project", "isolation", "acme", "private"}))
	require.Contains(t, errb.String(), `invalid isolation "private": valid values are open, confidential, sealed`)
	require.Empty(t, out.String())
}

// The vocabulary is derived from core.Isolations, not transcribed: the console
// validates the same argument off the same slice, so the client can never reject
// a state the route would have accepted.
func TestParseIsolation_AcceptsEveryCanonicalState(t *testing.T) {
	for _, state := range core.Isolations {
		got, err := parseIsolation(string(state))
		require.NoError(t, err, "%s is canonical", state)
		require.Equal(t, string(state), got)
	}
	require.Equal(t, enumOf(core.Isolations), isolationStates)

	_, err := parseIsolation("")
	require.Error(t, err, "an empty state is present-but-uninterpretable, not absent")
}

// The positional contract: a slug is required, the state is optional, and a third
// argument is named rather than dropped.
func TestProjectIsolationParse_Arity(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"the slug is required", []string{"project", "isolation"}, "missing argument: seam project isolation requires <slug> [<state>]"},
		{"the hint names the states", []string{"project", "isolation"}, "state is one of open, confidential, sealed"},
		{"a third argument is not dropped", []string{"project", "isolation", "acme", "sealed", "x"},
			`unexpected argument "x": seam project isolation takes at most 2 positional arguments`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(commands(), tc.argv)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	p, err := parse(commands(), []string{"project", "isolation", "acme"})
	require.NoError(t, err)
	require.Equal(t, []string{"acme"}, p.pos)
	require.False(t, *p.opts.(*projectIsolationOpts).yes)
}

// --yes binds from either side of the positionals, as every migrated command's
// flags do.
func TestProjectIsolationParse_FlagPermutes(t *testing.T) {
	for _, argv := range [][]string{
		{"project", "isolation", "--yes", "acme", "sealed"},
		{"project", "isolation", "acme", "--yes", "sealed"},
		{"project", "isolation", "acme", "sealed", "--yes"},
	} {
		p, err := parse(commands(), argv)
		require.NoError(t, err, "%v", argv)
		require.Equal(t, []string{"acme", "sealed"}, p.pos, "%v", argv)
		require.True(t, *p.opts.(*projectIsolationOpts).yes, "%v", argv)
	}
}

// A bare family name is not a command, but it is not a typo either: it lists its
// members, the same answer `seam plan` gives.
func TestProjectIsolationDispatch_BareFamilyName(t *testing.T) {
	e, _, errb := stubEnv()
	require.Equal(t, 2, dispatch(context.Background(), e, []string{"project"}))
	require.Contains(t, errb.String(), `unknown command "project": valid values are project isolation`)
}
