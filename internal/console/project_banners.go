package console

// Cross-project tie banners: how a project shares or inherits memories at
// briefing time, plus the retired-by-split lineage note. Like the relations tree,
// these are shared by the project-detail Relations tab and /console/relations.

import (
	"context"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// relBanner is one "cross-project ties" banner: a shared-briefing note or a
// retired-by-split lineage note. HTML is server-built (bolded project names).
type relBanner struct {
	Icon    string
	Retired bool
	HTML    template.HTML
}

// projectBanners builds the cross-project ties for one project's relations view:
// the shared-briefing banner (root / parent / child) plus, when the project was
// retired by a split, its lineage banner.
func (s *Service) projectBanners(ctx context.Context, p core.Project) ([]relBanner, error) {
	var banners []relBanner
	children, err := store.ProjectsByParent(ctx, s.cfg.DB, p.Slug)
	if err != nil {
		return nil, err
	}
	switch {
	case p.Retired():
		// handled by the lineage banner below
	case p.ParentSlug != "":
		parentMem, err := store.GetProjectCounts(ctx, s.cfg.DB, p.ParentSlug)
		if err != nil {
			return nil, err
		}
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`<b>%s</b> inherits %s from its parent <b>%s</b> at briefing time.`,
			template.HTMLEscapeString(p.Slug), plural(parentMem.Memories, "active memory", "active memories"),
			template.HTMLEscapeString(p.ParentSlug)))})
	case len(children) > 0:
		mem, err := store.GetProjectCounts(ctx, s.cfg.DB, p.Slug)
		if err != nil {
			return nil, err
		}
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`<b>%s</b> shares %s into %s at briefing time &mdash; children inherit without duplicating.`,
			template.HTMLEscapeString(p.Slug), plural(mem.Memories, "active memory", "active memories"),
			childList(children)))})
	default:
		banners = append(banners, relBanner{Icon: "git-branch", HTML: template.HTML(fmt.Sprintf(
			`As a root project, <b>%s</b> injects only its own memories &mdash; no parent to inherit from and no children.`,
			template.HTMLEscapeString(p.Slug)))})
	}
	if p.Retired() {
		banner, err := s.splitLineageBanner(ctx, p)
		if err != nil {
			return nil, err
		}
		if banner != nil {
			banners = append(banners, *banner)
		}
	}
	return banners, nil
}

// splitLineageBanner builds the retired-by-split banner for a project: how long
// ago it was retired and where its memories moved (aggregated from the
// memory.moved event stream, from == the retired slug).
func (s *Service) splitLineageBanner(ctx context.Context, p core.Project) (*relBanner, error) {
	if s.cfg.Events == nil || p.RetiredAt == nil {
		return nil, nil
	}
	evs, err := s.cfg.Events.ByKinds(ctx, []core.EventKind{core.EventMemoryMoved}, "", "", 500)
	if err != nil {
		return nil, err
	}
	moved := 0
	targets := map[string]bool{}
	for _, e := range evs {
		if payloadStr(e.Payload, "from") != p.Slug {
			continue
		}
		moved++
		if to := payloadStr(e.Payload, "to"); to != "" {
			targets[to] = true
		}
	}
	dst := make([]string, 0, len(targets))
	for t := range targets {
		dst = append(dst, t)
	}
	sort.Strings(dst)
	days := int(p.RetiredAt.Sub(time.Time{}) / (24 * time.Hour))
	if !p.RetiredAt.IsZero() {
		days = int(time.Since(*p.RetiredAt).Hours() / 24)
	}
	var msg string
	if moved > 0 && len(dst) > 0 {
		msg = fmt.Sprintf(`<b>%s</b> was split %d days ago; its %s moved to %s. Kept readable for provenance.`,
			template.HTMLEscapeString(p.Slug), days, plural(moved, "memory", "memories"), boldList(dst))
	} else {
		msg = fmt.Sprintf(`<b>%s</b> was retired by a split %d days ago; kept readable for provenance.`,
			template.HTMLEscapeString(p.Slug), days)
	}
	return &relBanner{Icon: "split", Retired: true, HTML: template.HTML(msg)}, nil
}

// childList renders a project's children as a human "a, b and c" list, bolded.
func childList(children []core.Project) string {
	names := make([]string, 0, len(children))
	for _, c := range children {
		names = append(names, c.Slug)
	}
	return boldList(names)
}

// boldList renders slugs as a bolded, HTML-escaped "a, b and c" list.
func boldList(names []string) string {
	esc := make([]string, 0, len(names))
	for _, n := range names {
		esc = append(esc, "<b>"+template.HTMLEscapeString(n)+"</b>")
	}
	switch len(esc) {
	case 0:
		return ""
	case 1:
		return esc[0]
	case 2:
		return esc[0] + " and " + esc[1]
	default:
		return strings.Join(esc[:len(esc)-1], ", ") + ", and " + esc[len(esc)-1]
	}
}
