package retrieve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0spoon/seamless/internal/store"
)

// TestSubagentBriefing_PromptFieldUnread pins the staging contract of
// BriefingInput.Prompt (plan:subagent-briefing, D2): the field is carried into
// the briefing call but intentionally unread, so identical inputs with and
// without a prompt produce byte-identical output and injected ids. The
// RELEVANT-section step will consume the field and retire this invariant.
// Deliberately NOT a golden snapshot of today's rendering: the subagent
// briefing's content may change independently, and this invariant must hold
// regardless of what it renders.
func TestSubagentBriefing_PromptFieldUnread(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	require.NoError(t, store.SetSetting(ctx, db, store.SettingRepoProjectMap, `{"/work/seam":"seam"}`))
	insMem(t, db, "01A", "constraint", "no-force-push", "never force push to main", "seam")
	insMem(t, db, "01B", "gotcha", "chroma-boot-race", "chroma container health check race", "seam")
	svc := New(db, nil, budgets(), nil)

	subagent := BriefingInput{CWD: "/work/seam", AgentType: "Explore"}
	without, idsWithout, err := svc.Briefing(ctx, subagent)
	require.NoError(t, err)
	require.NotEmpty(t, without, "the invariant would be vacuous on empty output")

	prompted := subagent
	prompted.Prompt = "Fix the chroma boot race in the compose file"
	with, idsWith, err := svc.Briefing(ctx, prompted)
	require.NoError(t, err)
	require.Equal(t, without, with, "an unread Prompt must not change the subagent briefing")
	require.Equal(t, idsWithout, idsWith, "an unread Prompt must not change the injected ids")

	// Guard the comparison itself: a repeat of the prompt-less call is
	// byte-identical too, so a failure above means the field was read, not
	// that the output is nondeterministic.
	again, _, err := svc.Briefing(ctx, subagent)
	require.NoError(t, err)
	require.Equal(t, without, again)

	// The main-session path ignores the field just the same.
	main := BriefingInput{CWD: "/work/seam", Source: "startup"}
	mainWithout, _, err := svc.Briefing(ctx, main)
	require.NoError(t, err)
	promptedMain := main
	promptedMain.Prompt = "anything at all"
	mainWith, _, err := svc.Briefing(ctx, promptedMain)
	require.NoError(t, err)
	require.Equal(t, mainWithout, mainWith)
}
