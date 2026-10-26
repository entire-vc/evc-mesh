package service

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Shadow mode for the board rules that board-services (the fiddler) still
// executes: the server computes what it would do and writes it to the activity
// log under workflow.shadow_*; nothing on the card changes. Comparing that
// journal with what board-services actually did is the gate for ever switching
// board-services off.

// shadowAgentMaxPages bounds the agent scan: MaxPageSize * pages agents per workspace.
const shadowAgentMaxPages = 25

// ShadowReviewCandidate is one agent that could review a card.
type ShadowReviewCandidate struct {
	AgentID uuid.UUID
	Slug    string
	Family  string
	Repos   []string
}

// ShadowReviewDecision is what the on_enter review route would do.
type ShadowReviewDecision struct {
	PickedID      *uuid.UUID
	PickedSlug    string
	AuthorFamily  string
	Repo          string
	AcceptorLabel string
	MergeBy       string
	Reason        string
}

// ModelFamily maps an agent's model name to a vendor family. Unknown or empty
// models return "" so "other family" is never satisfied by a guess.
func ModelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "claude"), strings.Contains(m, "sonnet"), strings.Contains(m, "opus"), strings.Contains(m, "haiku"), strings.Contains(m, "fable"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.Contains(m, "codex"):
		return "openai"
	case strings.Contains(m, "gemini"):
		return "google"
	}
	return ""
}

var vcsRepoPathRe = regexp.MustCompile(`^/(.+?)(?:/-)?/(?:merge_requests|pull)/\d+`)

// RepoFromVCSURL extracts "group/project" (GitLab) or "org/repo" (GitHub) from an
// MR/PR URL, or "" when the URL has no such shape.
func RepoFromVCSURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if m := vcsRepoPathRe.FindStringSubmatch(u.Path); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// DecideShadowReview picks the reviewer for a card: an agent that covers repo
// (when the rule says by_repo) and, for other_family, belongs to a model family
// other than the author's. Candidates are ordered by slug so the choice is
// stable and the journal comparable between runs. The author is never a
// candidate. No match is a decision too (PickedID nil + Reason), not an error.
func DecideShadowReview(rule domain.OnEnterAction, authorID *uuid.UUID, authorFamily, repo string, cands []ShadowReviewCandidate) ShadowReviewDecision {
	d := ShadowReviewDecision{AuthorFamily: authorFamily, Repo: repo, AcceptorLabel: rule.AcceptorLabel, MergeBy: rule.MergeBy}
	byRepo := rule.Reviewer == "by_repo"
	otherFamily := rule.ReviewerPick == "other_family"
	if byRepo && repo == "" {
		d.Reason = "no repository on the card (no PR/MR link)"
		return d
	}
	if otherFamily && authorFamily == "" {
		d.Reason = "author model family unknown"
		return d
	}
	sorted := append([]ShadowReviewCandidate(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })
	for _, c := range sorted {
		if authorID != nil && c.AgentID == *authorID {
			continue
		}
		if byRepo && !containsFold(c.Repos, repo) {
			continue
		}
		if otherFamily && (c.Family == "" || c.Family == authorFamily) {
			continue
		}
		id := c.AgentID
		d.PickedID, d.PickedSlug = &id, c.Slug
		d.Reason = "matched"
		return d
	}
	d.Reason = "no eligible reviewer"
	return d
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}

// shadowRecordReviewRoute evaluates the review route of the status the card just
// entered and journals the decision. It never alters the task and never returns
// an error: a failure here must not touch a transition the fleet depends on.
//
// The author is whoever held the card before the move (authorID/authorType, the
// caller's oldAssigneeID taken before any reassignment), per Runner v2.1 §11.
// created_by is deliberately not used: cards opened by a lead are executed by
// someone else, and the reviewer must differ from that executor.
func (s *taskService) shadowRecordReviewRoute(ctx context.Context, task *domain.Task, authorID *uuid.UUID, authorType domain.AssigneeType, next *domain.TaskStatus) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[shadow-review] panic on task %s: %v", task.ID, r)
		}
	}()
	if s.rulesConfigSvc == nil || s.agentRepo == nil || s.projectRepo == nil {
		return
	}
	wf, err := s.rulesConfigSvc.GetProjectWorkflowRules(ctx, task.ProjectID, nil)
	if err != nil || wf == nil {
		return
	}
	rule, ok := wf.OnEnter[next.Name]
	if !ok || !rule.HasReviewRoute() {
		return
	}
	proj, err := s.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || proj == nil {
		return
	}

	var repo string
	if s.vcsLinkRepo != nil {
		if links, lerr := s.vcsLinkRepo.ListByTask(ctx, task.ID); lerr == nil {
			for _, l := range links {
				if l.LinkType == domain.VCSLinkTypePR && !l.IsReviewSubject() && !l.IsRelatesTo() {
					if repo = RepoFromVCSURL(l.URL); repo != "" {
						break
					}
				}
			}
		}
	}

	var authorFamily string
	if authorID != nil && authorType == domain.AssigneeTypeAgent {
		if a, aerr := s.agentRepo.GetByID(ctx, *authorID); aerr == nil && a != nil && a.Model != nil {
			authorFamily = ModelFamily(*a.Model)
		}
	}

	cands := s.shadowCandidates(ctx, proj.WorkspaceID)

	d := DecideShadowReview(rule, authorID, authorFamily, repo, cands)
	changes := map[string]interface{}{
		"status": next.Name, "mode": "shadow", "repo": d.Repo, "author_family": d.AuthorFamily,
		"acceptor_label": d.AcceptorLabel, "merge_by": d.MergeBy, "reason": d.Reason,
		"candidates": len(cands),
	}
	if d.PickedID != nil {
		changes["would_assign_reviewer_id"] = d.PickedID.String()
		changes["would_assign_reviewer_slug"] = d.PickedSlug
	}
	s.logActivity(ctx, task.ProjectID, task.ID, "workflow.shadow_review_route", changes)
}

// shadowCandidates lists every agent of the workspace (paged, bounded by
// shadowAgentMaxPages) as a possible reviewer.
func (s *taskService) shadowCandidates(ctx context.Context, workspaceID uuid.UUID) []ShadowReviewCandidate {
	var cands []ShadowReviewCandidate
	for pg := 1; pg <= shadowAgentMaxPages; pg++ {
		page, lerr := s.agentRepo.List(ctx, workspaceID, repository.AgentFilter{}, pagination.Params{Page: pg, PageSize: pagination.MaxPageSize})
		if lerr != nil || page == nil {
			break
		}
		for _, a := range page.Items {
			c := ShadowReviewCandidate{AgentID: a.ID, Slug: a.Slug, Repos: agentRepos(a.Capabilities)}
			if a.Model != nil {
				c.Family = ModelFamily(*a.Model)
			}
			cands = append(cands, c)
		}
		if len(page.Items) < pagination.MaxPageSize {
			break
		}
	}
	return cands
}

// agentRepos reads the repositories an agent covers from capabilities.repos.
func agentRepos(caps json.RawMessage) []string {
	var c struct {
		Repos []string `json:"repos"`
	}
	if len(caps) == 0 || json.Unmarshal(caps, &c) != nil {
		return nil
	}
	return c.Repos
}
