package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// gitlabEvidenceTimeout bounds the one GitLab read a link sync makes.
const gitlabEvidenceTimeout = 8 * time.Second

// Reasons a GitLab link sync wrote nothing (or wrote the head but not the
// pipeline). Returned to the caller of the sync endpoint as is.
const (
	GitLabSyncReasonNotGitLabMR      = "not_gitlab_delivery_mr"
	GitLabSyncReasonUnparseableURL   = "unparseable_url"
	GitLabSyncReasonExternalID       = "external_id_mismatch"
	GitLabSyncReasonURLMismatch      = "url_mismatch"
	GitLabSyncReasonNotMerged        = "not_merged"
	GitLabSyncReasonNoHeadSHA        = "no_head_sha"
	GitLabSyncReasonNoPipeline       = "no_pipeline"
	GitLabSyncReasonPipelineMismatch = "pipeline_sha_mismatch"
	GitLabSyncReasonUnknownStatus    = "unknown_pipeline_status"
	GitLabSyncReasonOlderPipeline    = "older_pipeline_stored"
	GitLabSyncReasonUnchanged        = "unchanged"
)

// VCSLinkSyncResult is the outcome of SyncLink: the link as stored after the
// sync, whether the row was written, and why not (or why only partly).
type VCSLinkSyncResult struct {
	Link    *domain.VCSLink `json:"link"`
	Changed bool            `json:"changed"`
	Reason  string          `json:"reason,omitempty"`
}

// WithVCSMREvidenceSource lets SyncLink and Create read a merged MR's state,
// head commit and head pipeline from GitLab, so a link added after the merge
// (no webhook will ever reach it) still gets the HEAD and CI result the
// delivery-proof gate reads.
func WithVCSMREvidenceSource(f func(ctx context.Context, workspaceID uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool)) VCSLinkServiceOption {
	return func(s *vcsLinkService) { s.mrEvidence = f }
}

// gitlabLinkIsDelivery reports whether l is a GitLab MR link that stands for
// the card's own deliverable.
func gitlabLinkIsDelivery(l domain.VCSLink) bool {
	return l.Provider == domain.VCSProviderGitLab && l.LinkType == domain.VCSLinkTypePR && !l.IsReviewSubject() && !l.IsRelatesTo()
}

// sameMRWebURL reports whether two MR URLs name the same host and path; the
// scheme, case of the host, a trailing slash, query and fragment are ignored.
func sameMRWebURL(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) &&
		strings.TrimRight(ua.Path, "/") == strings.TrimRight(ub.Path, "/")
}

// syncGitLabLinkEvidence asks GitLab about the exact MR the link names and,
// when GitLab says it is merged, stores its head commit as head_sha and its
// head pipeline as pipeline_status/pipeline_sha — the pipeline only when it
// ran on that same head. Only the sync fields are written: status, ship_sha,
// ci_verdict, acceptance, deployed, implementation_sha and the manual status
// declaration stay as they are. A repeat with nothing new writes nothing.
// Returns whether the row was written and, when something was not stored, why.
func syncGitLabLinkEvidence(ctx context.Context, s *vcsLinkService, src gitlabapi.MergeRequestEvidenceSource, l domain.VCSLink) (written bool, reason string, err error) {
	if !gitlabLinkIsDelivery(l) {
		return false, GitLabSyncReasonNotGitLabMR, nil
	}
	projectPath, iid, ok := gitlabapi.ParseMergeRequestURL(l.URL)
	iidStr := strconv.Itoa(iid)
	if !ok || !mrURLNamesProject(l.URL, projectPath, iidStr) {
		return false, GitLabSyncReasonUnparseableURL, nil
	}
	if l.ExternalID != iidStr {
		return false, GitLabSyncReasonExternalID, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, gitlabEvidenceTimeout)
	defer cancel()
	ev, err := src.GetMergeRequestEvidence(readCtx, projectPath, iid)
	if err != nil {
		return false, "", fmt.Errorf("read gitlab mr %s!%d: %w", projectPath, iid, err)
	}
	// The client only talks to the configured host; GitLab's own web_url
	// proves the link names this MR on this host, not a namesake elsewhere.
	if !sameMRWebURL(ev.WebURL, l.URL) {
		return false, GitLabSyncReasonURLMismatch, nil
	}
	if ev.State != "merged" {
		return false, GitLabSyncReasonNotMerged, nil
	}
	if ev.SHA == "" {
		return false, GitLabSyncReasonNoHeadSHA, nil
	}

	if l.HeadSHA() != ev.SHA {
		patch, _ := json.Marshal(map[string]string{domain.VCSLinkMetaHeadSHA: ev.SHA}) // a string map cannot fail to marshal
		updated, mErr := s.repo.MergeMetadata(ctx, l.TaskID, l.ID, patch)
		if mErr != nil {
			return false, "", fmt.Errorf("store gitlab head sha: %w", mErr)
		}
		if updated != nil {
			l = *updated
		}
		written = true
	}

	hp := ev.HeadPipeline
	if hp == nil {
		return written, GitLabSyncReasonNoPipeline, nil
	}
	if hp.SHA != ev.SHA {
		return written, GitLabSyncReasonPipelineMismatch, nil
	}
	status, ok := domain.NormalizePipelineStatus(hp.Status)
	if !ok {
		return written, GitLabSyncReasonUnknownStatus, nil
	}
	if l.PipelineID != nil && *l.PipelineID == hp.ID &&
		l.PipelineStatus != nil && *l.PipelineStatus == status && l.PipelineSHA() == hp.SHA {
		if written {
			return true, "", nil
		}
		return false, GitLabSyncReasonUnchanged, nil
	}
	w, sErr := s.repo.SetPipelineStatus(ctx, l.ID, hp.ID, status, hp.SHA)
	if sErr != nil {
		return written, "", fmt.Errorf("store gitlab head pipeline: %w", sErr)
	}
	if !w {
		return written, GitLabSyncReasonOlderPipeline, nil
	}
	return true, "", nil
}

// gitlabEvidenceSourceFor resolves the GitLab client of the workspace that
// owns taskID. ok=false when no source is wired, the workspace cannot be
// proven or it has no GitLab integration.
func (s *vcsLinkService) gitlabEvidenceSourceFor(ctx context.Context, taskID uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
	if s.mrEvidence == nil {
		return nil, false
	}
	ws := s.workspaceOfTask(ctx, taskID)
	if ws == uuid.Nil {
		return nil, false
	}
	src, ok := s.mrEvidence(ctx, ws)
	if !ok || src == nil {
		return nil, false
	}
	return src, true
}

// SyncLink re-reads one GitLab MR link of taskID from GitLab and stores its
// HEAD and head pipeline (see syncGitLabLinkEvidence). The link must belong to
// taskID; any other link is NotFound. It never creates links, never changes a
// link's status and never moves the card.
func (s *vcsLinkService) SyncLink(ctx context.Context, taskID, linkID uuid.UUID) (*VCSLinkSyncResult, error) {
	l, err := s.repo.GetByID(ctx, linkID)
	if err != nil {
		return nil, fmt.Errorf("get vcs link: %w", err)
	}
	if l == nil || l.TaskID != taskID {
		return nil, apierror.NotFound("VCSLink")
	}
	if !gitlabLinkIsDelivery(*l) {
		return &VCSLinkSyncResult{Link: l, Reason: GitLabSyncReasonNotGitLabMR}, nil
	}
	src, ok := s.gitlabEvidenceSourceFor(ctx, l.TaskID)
	if !ok {
		return nil, apierror.ServiceUnavailable("gitlab integration is not configured for this workspace")
	}
	changed, reason, err := syncGitLabLinkEvidence(ctx, s, src, *l)
	if err != nil {
		log.Printf("[vcs-link] sync link=%s task=%s url=%q: %v", l.ID, l.TaskID, l.URL, err)
		if !changed {
			return nil, apierror.ServiceUnavailable("gitlab did not confirm the merge request; nothing was stored")
		}
	}
	stored, err := s.repo.GetByID(ctx, l.ID)
	if err != nil || stored == nil {
		return nil, fmt.Errorf("re-read vcs link: %w", err)
	}
	return &VCSLinkSyncResult{Link: stored, Changed: changed, Reason: reason}, nil
}

// syncCreatedGitLabLink runs the same sync for a GitLab MR link Create just
// stored as merged. Best effort: a failure is logged and the link is returned
// as stored, so link creation never fails because of GitLab.
func (s *vcsLinkService) syncCreatedGitLabLink(ctx context.Context, link *domain.VCSLink) *domain.VCSLink {
	if link == nil || link.Status != domain.VCSLinkStatusMerged || link.Provider != domain.VCSProviderGitLab || link.LinkType != domain.VCSLinkTypePR {
		return link
	}
	src, ok := s.gitlabEvidenceSourceFor(ctx, link.TaskID)
	if !ok {
		return link
	}
	stored, err := s.repo.GetByID(ctx, link.ID)
	if err != nil || stored == nil {
		return link
	}
	changed, _, err := syncGitLabLinkEvidence(ctx, s, src, *stored)
	if err != nil {
		log.Printf("[vcs-link] sync new link=%s task=%s url=%q: %v", link.ID, link.TaskID, link.URL, err)
	}
	if !changed {
		return link
	}
	if fresh, gErr := s.repo.GetByID(ctx, link.ID); gErr == nil && fresh != nil {
		return fresh
	}
	return link
}
