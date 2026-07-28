// The optional LLM-judge layer: the fuzzy remainder that assertions cannot
// reach -- did the agent explain the constraint it was honoring, did it avoid
// re-running the dead end a failed trial already ruled out, is the change
// coherent rather than merely token-present.
//
// It is ADDITIVE. The judge never gates a verdict (see grade.go), and it is
// reached through a small interface so tests fake it and no unit test needs a
// provider.
//
// Degradation follows constraint llm-degradation-remote-vs-local, adapted to a
// benchmark run: a judge outage or a missing provider must never fail or crash
// a run, because losing the whole run costs far more than losing one advisory
// signal. The two halves of the split are kept honest by REPORTING them
// differently rather than by failing differently -- a local ErrConfig (the
// request was never built; no retry helps) logs at Error and says so in the
// Details line, while a remote outage logs at Warn. The information is not
// swallowed; only the fatality is dropped.

package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/0spoon/seamless/internal/config"
	"github.com/0spoon/seamless/internal/llm"
)

// judgeTranscriptMaxRunes caps how much transcript is sent. The tail is what
// carries the agent's reasoning and its closing summary, so truncation drops
// from the front.
const judgeTranscriptMaxRunes = 48000

// JudgeRequest is one grading question: a scenario's rubric applied to a run's
// transcript.
type JudgeRequest struct {
	Scenario   string
	Condition  string
	Prompt     string
	Rubric     string
	Transcript string
}

// JudgeVerdict is the judge's answer. Reason is one or two sentences, recorded
// verbatim in Result.Details.
type JudgeVerdict struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

// Judge scores a run's transcript against a scenario rubric.
type Judge interface {
	Judge(ctx context.Context, req JudgeRequest) (JudgeVerdict, error)
}

// judgeSystemPrompt keeps the model to a machine-readable verdict.
const judgeSystemPrompt = `You grade one run of an AI coding agent against a rubric.
Judge only what the transcript shows. Do not reward intent that never became work.
Answer with a single JSON object and nothing else:
{"pass": true|false, "reason": "<one or two sentences citing the transcript>"}`

// llmJudge is the real judge, over the repo's existing chat abstraction.
type llmJudge struct {
	chat llm.Chat
}

// NewJudge wraps a chat client as a Judge. Passing nil returns nil, so a caller
// that could not build a client simply grades without the layer.
func NewJudge(chat llm.Chat) Judge {
	if chat == nil {
		return nil
	}
	return &llmJudge{chat: chat}
}

// NewLLMJudge builds a judge from the LLM config. It returns an error when the
// provider is unusable (missing key, bad base_url) -- construction is where
// that must surface, which is exactly why a judge failure at grade time is
// allowed to degrade.
func NewLLMJudge(cfg config.LLM) (Judge, error) {
	chat, err := llm.NewChatClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("bench: build llm judge: %w", err)
	}
	return NewJudge(chat), nil
}

func (j *llmJudge) Judge(ctx context.Context, req JudgeRequest) (JudgeVerdict, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Scenario: %s\nCondition: %s\n\nThe agent was given this prompt:\n%s\n\nRubric:\n%s\n\nTranscript:\n%s\n",
		req.Scenario, req.Condition, req.Prompt, req.Rubric, req.Transcript)
	out, err := j.chat.Complete(ctx, judgeSystemPrompt, b.String())
	if err != nil {
		return JudgeVerdict{}, fmt.Errorf("bench.Judge: %w", err)
	}
	v, err := parseJudgeVerdict(out)
	if err != nil {
		return JudgeVerdict{}, err
	}
	return v, nil
}

// parseJudgeVerdict pulls the verdict object out of a completion that may carry
// a code fence or a stray sentence around it. A completion with no object at
// all is an error, not a default verdict: a fabricated pass is worse than no
// judge (AGENTS.md > "No fake results on error").
func parseJudgeVerdict(out string) (JudgeVerdict, error) {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end <= start {
		return JudgeVerdict{}, fmt.Errorf("bench.Judge: completion carried no verdict object")
	}
	var v JudgeVerdict
	if err := json.Unmarshal([]byte(out[start:end+1]), &v); err != nil {
		return JudgeVerdict{}, fmt.Errorf("bench.Judge: parse verdict: %w", err)
	}
	return v, nil
}

// judgeLine runs the judge layer and renders its one Details line. It never
// returns an error: every failure mode degrades to a line saying what was lost.
func (g *rubricGrader) judgeLine(ctx context.Context, a RunArtifacts) string {
	switch {
	case g.rubric == "":
		return "judge: n/a -- scenario has no rubric"
	case g.judge == nil:
		return "judge: n/a -- no LLM judge configured (graded on assertions + event log)"
	case a.Transcript == "":
		return "judge: n/a -- run preserved no transcript"
	}
	transcript, err := readTranscript(a.Transcript)
	if err != nil {
		slog.Warn("bench: judge transcript unreadable", "path", a.Transcript, "error", err)
		return "judge: degraded -- transcript unreadable: " + firstLine(err.Error())
	}
	v, err := g.judge.Judge(ctx, JudgeRequest{
		Scenario: g.scenario, Condition: a.Condition.Name, Prompt: promptFor(g.scenario),
		Rubric: g.rubric, Transcript: transcript,
	})
	if err != nil {
		// Local vs remote: both degrade (a run must survive a judge outage),
		// but a local misconfiguration is loud and named, because no retry and
		// no amount of waiting will clear it.
		if errors.Is(err, llm.ErrConfig) {
			slog.Error("bench: llm judge misconfigured", "scenario", g.scenario, "error", err)
			return "judge: degraded (local misconfiguration, will not clear) -- " + firstLine(err.Error())
		}
		slog.Warn("bench: llm judge unavailable", "scenario", g.scenario, "error", err)
		return "judge: degraded (provider unavailable) -- " + firstLine(err.Error())
	}
	verdict := "FAIL"
	if v.Pass {
		verdict = "PASS"
	}
	line := "judge/advisory: " + verdict
	if r := strings.TrimSpace(v.Reason); r != "" {
		line += ": " + firstLine(r)
	}
	return line
}

// readTranscript reads a run's transcript, keeping the tail when it is large.
func readTranscript(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("bench: read transcript: %w", err)
	}
	runes := []rune(string(b))
	if len(runes) <= judgeTranscriptMaxRunes {
		return string(runes), nil
	}
	return "... [transcript truncated to its last " + fmt.Sprint(judgeTranscriptMaxRunes) + " characters]\n" +
		string(runes[len(runes)-judgeTranscriptMaxRunes:]), nil
}

// promptFor returns the scenario's prompt so the judge sees what the agent was
// actually asked; an unknown scenario simply omits it.
func promptFor(scenario string) string {
	if sc, ok := ScenarioByName(scenario); ok {
		return sc.Prompt
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
