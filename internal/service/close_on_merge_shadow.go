package service

import (
	"context"
	"encoding/json"
	"log"
	"net/url"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// closeOnMergeShadowBatch bounds one sweep tick.
const closeOnMergeShadowBatch = 200

// closeOnMergeShadowMaxPages caps one tick at batch*pages candidates. Projects
// without the flag are skipped rather than journaled, so they stay at the head of
// the query; paging past them keeps them from starving enabled projects.
const closeOnMergeShadowMaxPages = 25

// CloseOnMergeShadowService is the advisory twin of board-services' "close what
// is merged": for projects with mid_pipeline.close_on_merge it journals
// workflow.shadow_close_on_merge for each open card whose PR/MR is merged. It
// never moves a card or writes a comment. lift_backlog_blockers needs no twin
// here: BacklogPromotionAdvisoryService journals it as workflow.shadow_lift_blocker
// (WithLiftBlockersShadow).
type CloseOnMergeShadowService interface {
	Sweep(ctx context.Context) (int, error)
}

type closeOnMergeShadowService struct {
	links       repository.VCSLinkRepository
	activity    repository.ActivityLogRepository
	projectRepo repository.ProjectRepository
	rules       midPipelineSource
}

// NewCloseOnMergeShadowService wires the shadow sweep.
func NewCloseOnMergeShadowService(links repository.VCSLinkRepository, activity repository.ActivityLogRepository, projectRepo repository.ProjectRepository, rules midPipelineSource) CloseOnMergeShadowService {
	return &closeOnMergeShadowService{links: links, activity: activity, projectRepo: projectRepo, rules: rules}
}

// Sweep returns how many journal entries it wrote. A project whose rules cannot
// be read is skipped (and retried next tick), never journaled on a guess.
func (s *closeOnMergeShadowService) Sweep(ctx context.Context) (int, error) {
	enabled := make(map[uuid.UUID]*uuid.UUID) // project -> workspace, nil = off or unknown
	written := 0
	// The query drops a card once it is journaled, so the result set shrinks under
	// the sweep: paging by a growing offset would skip cards. Only the cards left
	// unjournaled (rules unreadable, journal write failed) stay in the set and have
	// to be stepped over.
	skipped := 0
	for page := 0; page < closeOnMergeShadowMaxPages; page++ {
		cands, err := s.links.ListOpenTasksWithMergedPR(ctx, closeOnMergeShadowBatch, skipped)
		if err != nil {
			return written, err
		}
		pageWritten := 0
		for _, c := range cands {
			ws, seen := enabled[c.ProjectID]
			if !seen {
				ws = s.workspaceIfEnabled(ctx, c.ProjectID)
				enabled[c.ProjectID] = ws
			}
			if ws == nil {
				continue
			}
			changes, _ := json.Marshal(map[string]interface{}{
				"mode": "shadow", "pr_url": redactPRURL(c.LinkURL), "merged_at": c.MergedAt, "would": "close",
			})
			entry := &domain.ActivityLog{
				ID: uuid.New(), WorkspaceID: *ws, EntityType: "task", EntityID: c.TaskID,
				Action: "workflow.shadow_close_on_merge", ActorID: uuid.Nil, ActorType: domain.ActorTypeSystem,
				Changes: changes, CreatedAt: timeNow(),
			}
			if err := s.activity.Create(ctx, entry); err != nil {
				log.Printf("[close-on-merge-shadow] task %s: %v", c.TaskID, err)
				continue
			}
			pageWritten++
		}
		written += pageWritten
		skipped += len(cands) - pageWritten
		if len(cands) < closeOnMergeShadowBatch {
			break
		}
	}
	return written, nil
}

func (s *closeOnMergeShadowService) workspaceIfEnabled(ctx context.Context, projectID uuid.UUID) *uuid.UUID {
	wf, err := s.rules.GetProjectWorkflowRules(ctx, projectID, nil)
	if err != nil || wf == nil || wf.MidPipeline == nil || !wf.MidPipeline.CloseOnMerge {
		return nil
	}
	p, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || p == nil {
		return nil
	}
	return &p.WorkspaceID
}

// redactPRURL keeps scheme, host and path of a stored PR/MR URL and drops
// userinfo, query and fragment, so a credential embedded in a link never reaches
// the activity journal. An unparseable value is replaced, not copied.
func redactPRURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[unparseable url]"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Hostname(), Path: u.Path}).String()
}
