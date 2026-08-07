package console

// Momentum's console-side helpers for project maturity stages: gated
// evaluation (the computation and its latch run only while the feature is
// on), crossing events, and the glyph the board rows and the project detail
// header share.

import (
	"context"
	"fmt"
	"html/template"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/features"
	"github.com/0spoon/seamless/internal/store"
)

// projectStages evaluates the momentum maturity stages for the given board
// rows: nil while the feature is off (no computation, no latch writes), and
// failure-soft -- an error costs the glyphs, never the page. Latch advances
// are recorded as project.stage_reached events, once per crossing by
// construction (the latch only moves forward).
func (s *Service) projectStages(ctx context.Context, rows []store.ProjectBoardRow, now time.Time) map[string]store.ProjectStageInfo {
	if !features.Enabled(s.effectiveFeatures(ctx), features.Momentum) {
		return nil
	}
	stages, crossings, err := store.EvaluateProjectStages(ctx, s.cfg.DB, rows, now)
	if err != nil {
		s.logger.Warn("console: project stages", "error", err)
		return nil
	}
	if s.cfg.Events != nil {
		for _, c := range crossings {
			if _, err := s.cfg.Events.Record(ctx, core.Event{
				Kind: core.EventProjectStage, ProjectSlug: c.Project,
				Payload: map[string]any{"stage": string(c.To), "from": string(c.From)},
			}); err != nil {
				s.logger.Warn("console: record stage crossing", "project", c.Project, "error", err)
			}
		}
	}
	return stages
}

// stageIcons maps each stage to its Lucide glyph.
var stageIcons = map[store.ProjectStage]string{
	store.StageSeedling:    "circle",
	store.StageSprouting:   "sprout",
	store.StageEstablished: "leaf",
	store.StageDeepRooted:  "tree-pine",
}

// stageGlyph renders a maturity stage as the small pill both surfaces share:
// the stage's glyph and name, with the "why not the next stage yet" progress
// in the title so the owner can see what the project is growing toward.
func stageGlyph(info store.ProjectStageInfo, now time.Time) template.HTML {
	icon := icon(stageIcons[info.Stage])
	return template.HTML(fmt.Sprintf(`<span class="mom-stage" data-stage="%s" title="%s">%s%s</span>`,
		template.HTMLEscapeString(string(info.Stage)),
		template.HTMLEscapeString(stageTitle(info, now)),
		icon, template.HTMLEscapeString(string(info.Stage))))
}

// stageTitle composes the tooltip: the stage held, and -- below the top stage
// -- the exact bars the next one asks for beside where the project stands,
// from the same exported constants the computation reads.
func stageTitle(info store.ProjectStageInfo, now time.Time) string {
	age := info.Inputs.AgeDays(now)
	state := fmt.Sprintf("now %dd, %d memories, %d events", age, info.Inputs.Memories, info.Inputs.Events)
	if info.Inputs.HasReach {
		state += fmt.Sprintf(", %d%% reach", info.Inputs.ReachRate)
	}
	switch info.Stage {
	case store.StageDeepRooted:
		return "deep-rooted -- " + state
	case store.StageEstablished:
		return fmt.Sprintf("established -- deep-rooted at %dd, %d memories, %d events, %d%%+ reach (%s)",
			store.StageDeepRootedMinAgeDays, store.StageDeepRootedMinMemories,
			store.StageDeepRootedMinEvents, store.StageDeepRootedMinReachPct, state)
	case store.StageSprouting:
		return fmt.Sprintf("sprouting -- established at %dd, %d memories, %d events (%s)",
			store.StageEstablishedMinAgeDays, store.StageEstablishedMinMemories,
			store.StageEstablishedMinEvents, state)
	default:
		return fmt.Sprintf("seedling -- sprouting at %dd, %d memories, %d events (%s)",
			store.StageSproutingMinAgeDays, store.StageSproutingMinMemories,
			store.StageSproutingMinEvents, state)
	}
}
