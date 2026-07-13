package gardener

import (
	"context"
	"strings"
	"time"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

const digestSystemPrompt = "You are a concise technical writer. Summarize an AI agent's recent work sessions into a short monthly digest: the themes worked on, what was decided or learned, and anything left open. Use compact markdown bullet points. Do not invent details beyond the findings provided."

// proposeDigests rolls the trailing DigestDays of completed sessions into one
// monthly digest proposal per project, summarized by the chat client. It is a
// no-op without a chat client. Each digest is keyed by project + calendar month,
// so a month's digest is proposed at most once (and re-proposed only next month).
func (s *Service) proposeDigests(ctx context.Context, seen map[string]struct{}) (int, error) {
	if s.chat == nil {
		return 0, nil
	}
	since := s.now().UTC().Add(-time.Duration(s.cfg.DigestDays) * 24 * time.Hour)
	sessions, err := store.CompletedSessionsSince(ctx, s.db, since)
	if err != nil {
		return 0, err
	}
	byProject := make(map[string][]core.Session)
	for _, sess := range sessions {
		byProject[sess.ProjectSlug] = append(byProject[sess.ProjectSlug], sess)
	}

	month := s.now().UTC().Format("2006-01")
	created := 0
	for project, group := range byProject {
		if len(group) < minDigestSessions {
			continue
		}
		key := "digest:" + project + ":" + month
		if _, dup := seen[key]; dup {
			continue
		}
		body, err := s.chat.Complete(ctx, digestSystemPrompt, digestUserPrompt(group))
		if err != nil {
			s.logger.Warn("gardener: digest completion", "project", project, "error", err)
			continue // one project's digest failing must not block the others
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		payload := map[string]any{
			"project": project, "month": month, "session_count": len(group),
			"title": digestTitle(project, month), "body": body,
		}
		if _, err := s.createProposal(ctx, store.ProposalDigest, key, payload, seen); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// digestUserPrompt renders the session findings as the digest's source material,
// newest first, one dated block per session.
func digestUserPrompt(sessions []core.Session) string {
	var b strings.Builder
	b.WriteString("Sessions to summarize:\n\n")
	for _, s := range sessions {
		b.WriteString("- ")
		b.WriteString(s.UpdatedAt.Format("2006-01-02"))
		if s.Name != "" {
			b.WriteString(" (" + s.Name + ")")
		}
		b.WriteString(": ")
		b.WriteString(s.Findings)
		b.WriteString("\n")
	}
	return b.String()
}

// digestTitle names a digest note for a project + month.
func digestTitle(project, month string) string {
	if project == "" {
		return "Session digest (global) -- " + month
	}
	return "Session digest: " + project + " -- " + month
}
