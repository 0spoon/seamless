package hooks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const codexFixtureSourceRevision = "5d1fbf26c43abc65a203928b2e31561cb039e06d"

type codexContractEvent struct {
	file  string
	event string
}

var codexContractEvents = func() []codexContractEvent {
	out := make([]codexContractEvent, len(codexHooks))
	for i, spec := range codexHooks {
		out[i] = codexContractEvent{file: spec.CLIArg, event: spec.Event}
	}
	return out
}()

func codexContractRoot(parts ...string) string {
	return filepath.Join(append([]string{"testdata", "codex", currentCodexFixtureVersion}, parts...)...)
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got), path)
	return got
}

func stringValues(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	require.True(t, ok)
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		require.True(t, ok)
		out = append(out, text)
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestCodexContractFixtures_CurrentCaptureProvenanceAndSanitization(t *testing.T) {
	capture := readJSONMap(t, codexContractRoot("capture.json"))
	require.Equal(t, "codex-cli 0.144.6", capture["codex_version"])
	require.Equal(t, "rust-v0.144.6", capture["release_tag"])
	require.Equal(t, codexFixtureSourceRevision, capture["source_revision"])

	root := codexContractRoot()
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var value any
		if jsonErr := json.Unmarshal(raw, &value); jsonErr != nil {
			t.Errorf("%s is not valid JSON: %v", path, jsonErr)
		}
		for _, forbidden := range []string{
			"/Users/katata", "/private/tmp/seamless-codex-contract", "access_token", "refresh_token",
		} {
			require.NotContains(t, string(raw), forbidden, path)
		}
		return nil
	}))
}

func TestCodexContractFixtures_ReleasedSchemaDigests(t *testing.T) {
	capture := readJSONMap(t, codexContractRoot("capture.json"))
	want, ok := capture["schema_sha256"].(map[string]any)
	require.True(t, ok)
	require.Len(t, want, len(codexContractEvents)*2)
	for name, digest := range want {
		raw, err := os.ReadFile(codexContractRoot("schema", name))
		require.NoError(t, err)
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(raw)), name)
	}
}

func TestCodexContractFixtures_LiveInputsMatchReleasedSchemas(t *testing.T) {
	for _, frontend := range []string{"exec", "tui"} {
		permissionMode := "bypassPermissions"
		if frontend == "tui" {
			permissionMode = "default"
		}
		for _, contract := range codexContractEvents {
			t.Run(frontend+"/"+contract.file, func(t *testing.T) {
				input := readJSONMap(t, codexContractRoot(frontend, contract.file+".input.json"))
				require.Equal(t, contract.event, input["hook_event_name"])
				require.Equal(t, "/Users/dev/myrepo", input["cwd"])
				require.Equal(t, "gpt-5.6-sol", input["model"])
				require.Equal(t, permissionMode, input["permission_mode"])

				schema := readJSONMap(t, codexContractRoot("schema", contract.file+".command.input.schema.json"))
				require.Equal(t, contract.file+".command.input", schema["title"])
				require.Equal(t, false, schema["additionalProperties"])
				require.ElementsMatch(t, stringValues(t, schema["required"]), sortedKeys(input),
					"live payload fields must exactly match the released required contract")
				properties := schema["properties"].(map[string]any)
				hookEvent := properties["hook_event_name"].(map[string]any)
				require.Equal(t, contract.event, hookEvent["const"])
			})
		}
	}
}

func TestCodexContractFixtures_LiveOutputsFitReleasedSchemas(t *testing.T) {
	for _, frontend := range []string{"exec", "tui"} {
		for _, contract := range codexContractEvents {
			t.Run(frontend+"/"+contract.file, func(t *testing.T) {
				output := readJSONMap(t, codexContractRoot(frontend, contract.file+".output.json"))
				schema := readJSONMap(t, codexContractRoot("schema", contract.file+".command.output.schema.json"))
				require.Equal(t, contract.file+".command.output", schema["title"])
				properties := schema["properties"].(map[string]any)
				for key := range output {
					require.Contains(t, properties, key, "fixture output contains a field absent from the released schema")
				}

				if strings.HasSuffix(contract.file, "start") || contract.file == "user-prompt-submit" {
					specific := output["hookSpecificOutput"].(map[string]any)
					require.Equal(t, contract.event, specific["hookEventName"])
					require.NotEmpty(t, specific["additionalContext"])
				} else {
					require.Equal(t, true, output["continue"])
					require.NotEmpty(t, output["systemMessage"])
				}
			})
		}
	}
}

func TestCodexContractFixtures_ExecAndTUIWireShapesMatch(t *testing.T) {
	for _, contract := range codexContractEvents {
		execInput := readJSONMap(t, codexContractRoot("exec", contract.file+".input.json"))
		tuiInput := readJSONMap(t, codexContractRoot("tui", contract.file+".input.json"))
		require.Equal(t, sortedKeys(execInput), sortedKeys(tuiInput), contract.event)

		execOutput := readJSONMap(t, codexContractRoot("exec", contract.file+".output.json"))
		tuiOutput := readJSONMap(t, codexContractRoot("tui", contract.file+".output.json"))
		require.Equal(t, execOutput, tuiOutput, contract.event)
	}
}

func TestCodexContractFixtures_SubagentTranscriptRoles(t *testing.T) {
	for _, frontend := range []string{"exec", "tui"} {
		parent := readJSONMap(t, codexContractRoot(frontend, "session-start.input.json"))
		start := readJSONMap(t, codexContractRoot(frontend, "subagent-start.input.json"))
		stop := readJSONMap(t, codexContractRoot(frontend, "subagent-stop.input.json"))

		require.NotEqual(t, parent["transcript_path"], start["transcript_path"],
			"SubagentStart names the child rollout")
		require.Equal(t, parent["transcript_path"], stop["transcript_path"],
			"SubagentStop transcript_path names the parent rollout")
		require.Equal(t, start["transcript_path"], stop["agent_transcript_path"],
			"SubagentStop separately names the child rollout")
		require.Equal(t, start["agent_id"], stop["agent_id"])
		require.Equal(t, "SUBAGENT_CONTRACT_DONE", stop["last_assistant_message"])
	}
}

func TestCodexContractFixtures_MCPGetJSONShapes(t *testing.T) {
	for _, transport := range []string{"stdio", "streamable-http"} {
		for _, state := range []string{"enabled", "disabled"} {
			fixture := readJSONMap(t, codexContractRoot("mcp", transport+"-"+state+".json"))
			require.Equal(t, "seamless", fixture["name"])
			require.Equal(t, state == "enabled", fixture["enabled"])
			require.Contains(t, fixture, "disabled_reason")
			require.Contains(t, fixture, "enabled_tools")
			require.Contains(t, fixture, "disabled_tools")
			require.Equal(t, 12.5, fixture["startup_timeout_sec"])
			require.Equal(t, 45.0, fixture["tool_timeout_sec"])

			wire := fixture["transport"].(map[string]any)
			if transport == "stdio" {
				require.Equal(t, "stdio", wire["type"])
				require.Equal(t, "/opt/seam/bin/seam", wire["command"])
				require.Equal(t, []string{"mcp-proxy", "--config", "/Users/dev/.config/seamless/seamless.yaml"},
					stringValues(t, wire["args"]))
				require.Contains(t, wire, "env")
				require.Contains(t, wire, "env_vars")
				require.Equal(t, "/Users/dev/myrepo", wire["cwd"])
			} else {
				require.Equal(t, "streamable_http", wire["type"])
				require.Equal(t, "https://mcp.example.invalid/api/mcp", wire["url"])
				require.Equal(t, "SEAMLESS_FIXTURE_TOKEN", wire["bearer_token_env_var"])
				require.Contains(t, wire, "http_headers")
				require.Contains(t, wire, "env_http_headers")
			}
		}
	}
}

func TestCodexContractFixtures_SchemaSetAndHistoryStayVersioned(t *testing.T) {
	entries, err := os.ReadDir(codexContractRoot("schema"))
	require.NoError(t, err)
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		require.False(t, entry.IsDir())
		got = append(got, entry.Name())
	}

	want := make([]string, 0, len(codexContractEvents)*2)
	for _, contract := range codexContractEvents {
		want = append(want,
			contract.file+".command.input.schema.json",
			contract.file+".command.output.schema.json",
		)
	}
	slices.Sort(want)
	require.Equal(t, want, got)

	for _, historical := range []string{
		filepath.Join("v0.144.5", "README.md"),
		filepath.Join("v0.144.5", "exec", "session-start.input.json"),
		filepath.Join("v0.144.5", "exec", "user-prompt-submit.input.json"),
		filepath.Join("v0.144.5", "exec", "stop.input.json"),
		filepath.Join("v0.144.5", "rollout.jsonl"),
	} {
		_, statErr := os.Stat(filepath.Join("testdata", "codex", historical))
		require.NoError(t, statErr, "historical 0.144.5 evidence must remain traceable")
	}
}

func TestCodexContractCaptureHarness_IsolatedAndCoversAliases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex", "capture.sh"))
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "refusing to reuse existing ROOT")
	require.Contains(t, text, "ROOT must be outside the repository")
	require.Contains(t, text, "CODEX_HOME=\"$home\" codex mcp get seamless --json")
	require.Contains(t, text, "commandWindows")
	require.Contains(t, text, "command_windows")
	require.Contains(t, text, "--dangerously-bypass-hook-trust")
	require.Contains(t, text, "clean-auth")
}

func TestCodexDocumentation_HookTablesMatchCanonicalProfile(t *testing.T) {
	want, err := InstalledEvents(ClientCodex)
	require.NoError(t, err)

	for _, doc := range []struct {
		path  string
		start string
		end   string
	}{
		{
			path:  filepath.Join("..", "..", "docs-src", "codex-cli.md"),
			start: "## The five hooks, and what they inject",
			end:   "### The model-visible output ceiling",
		},
		{
			path:  filepath.Join("..", "..", "docs-src", "reference", "hooks.md"),
			start: "## Codex CLI: five hooks",
			end:   "## The fail-open contract",
		},
	} {
		require.Equal(t, want, documentedHookEvents(t, doc.path, doc.start, doc.end), doc.path)
	}

	compatibility, err := os.ReadFile(filepath.Join("..", "..", "docs-src", "reference", "codex-compatibility.md"))
	require.NoError(t, err)
	require.Contains(t, string(compatibility), strings.Join(want, ", "),
		"the current compatibility row must name the canonical Codex profile in order")
}

func documentedHookEvents(t *testing.T, path, startHeading, endHeading string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	start := strings.Index(text, startHeading)
	require.NotEqual(t, -1, start, path)
	text = text[start+len(startHeading):]
	end := strings.Index(text, endHeading)
	require.NotEqual(t, -1, end, path)
	text = text[:end]

	var events []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		name := strings.TrimPrefix(line, "| `")
		if i := strings.Index(name, "` |"); i >= 0 {
			events = append(events, name[:i])
		}
	}
	return events
}
