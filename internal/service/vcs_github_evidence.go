package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	githubapi "github.com/entire-vc/evc-mesh/internal/integration/github"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// githubEvidenceTimeout bounds the two GitHub reads one link sync makes.
const githubEvidenceTimeout = 8 * time.Second

// githubLinkIsDelivery reports whether l is a GitHub PR link that stands for
// the card's own deliverable.
func githubLinkIsDelivery(l domain.VCSLink) bool {
	return l.Provider == domain.VCSProviderGitHub && l.LinkType == domain.VCSLinkTypePR && !l.IsReviewSubject() && !l.IsRelatesTo()
}

// githubLinkNeedsEvidence reports whether a GitHub PR link lacks the head
// commit or a green check result bound to it, i.e. whether a provider sync
// could change what the delivery-proof gate sees.
func githubLinkNeedsEvidence(l domain.VCSLink) bool {
	if !githubLinkIsDelivery(l) {
		return false
	}
	head := l.HeadSHA()
	return head == "" || l.PipelineSHA() != head || l.PipelineStatus == nil || *l.PipelineStatus != "success"
}

// syncGitHubLinkEvidence reads the merged PR's head commit and the check runs
// of that exact commit from GitHub and stores them on the link: head_sha in
// metadata, the folded check result as pipeline_status with pipeline_sha.
// Nothing is guessed: a missing head or a commit with no check runs leaves the
// field as it was, and ship_sha is never touched. When GitHub no longer vouches
// for the PR (not merged, no runs) a cached green result is unbound.
// Returns true when the link row was written.
func syncGitHubLinkEvidence(ctx context.Context, repo repository.VCSLinkRepository, src githubapi.PullRequestEvidenceSource, l domain.VCSLink) (bool, error) {
	owner, name, number, ok := githubapi.ParsePullRequestURL(l.URL)
	if !ok {
		// A link GitHub cannot be asked about is unconfirmed: fail closed.
		return false, fmt.Errorf("unparseable github pr url %q", l.URL)
	}
	ctx, cancel := context.WithTimeout(ctx, githubEvidenceTimeout)
	defer cancel()
	pr, err := src.GetPullRequestHead(ctx, owner, name, number)
	if err != nil {
		return false, fmt.Errorf("read github pr head: %w", err)
	}
	if !pr.Merged || pr.HeadSHA == "" {
		// GitHub does not vouch for this PR now: cached green must not keep
		// proving delivery.
		return unbindCachedGreen(ctx, repo, l)
	}
	written := false
	if l.HeadSHA() != pr.HeadSHA {
		patch, _ := json.Marshal(map[string]string{domain.VCSLinkMetaHeadSHA: pr.HeadSHA}) // a string map cannot fail to marshal
		if _, mergeErr := repo.MergeMetadata(ctx, l.TaskID, l.ID, patch); mergeErr != nil {
			return false, fmt.Errorf("store github head sha: %w", mergeErr)
		}
		written = true
	}
	sum, err := src.GetCheckRunsSummary(ctx, owner, name, pr.HeadSHA)
	if err != nil {
		return written, fmt.Errorf("read github check runs: %w", err)
	}
	w := false
	if sum.Total > 0 {
		var serr error
		if w, serr = repo.SetPipelineStatus(ctx, l.ID, sum.RunID, sum.Status, pr.HeadSHA); serr != nil {
			return written, fmt.Errorf("store github checks: %w", serr)
		}
	}
	// GitHub reports no green result for this head (no runs, or a result the
	// store refused as older than what it holds): a cached success must not
	// keep proving delivery, so unbind it from the commit. The gate then sees
	// pipeline_sha_mismatch.
	if !w && sum.Status != "success" || sum.Total == 0 {
		u, uerr := unbindCachedGreen(ctx, repo, l)
		if uerr != nil {
			return written, uerr
		}
		w = w || u
	}
	return written || w, nil
}

// unbindCachedGreen clears pipeline_sha on a link whose stored pipeline_status
// is success, so the gate reports pipeline_sha_mismatch instead of trusting a
// result GitHub no longer confirms. Returns true when the link was written.
func unbindCachedGreen(ctx context.Context, repo repository.VCSLinkRepository, l domain.VCSLink) (bool, error) {
	if l.PipelineSHA() == "" || l.PipelineStatus == nil || *l.PipelineStatus != "success" {
		return false, nil
	}
	patch, _ := json.Marshal(map[string]string{domain.VCSLinkMetaPipelineSHA: ""}) // a string map cannot fail to marshal
	if _, err := repo.MergeMetadata(ctx, l.TaskID, l.ID, patch); err != nil {
		return false, fmt.Errorf("unbind stale github checks: %w", err)
	}
	return true, nil
}

// syncGitHubLinks applies syncGitHubLinkEvidence to every link that needs it
// (or to every PR link when force is set, as the merge webhook does so a
// cached green result is re-verified against GitHub) and reports whether any
// row changed, plus the links GitHub could not confirm this time (a failed
// read or write). Failures are logged, never returned: a GitHub outage must
// leave the gate closed, not break the caller. The caller must not trust
// cached evidence of an unconfirmed link even when its unbind write failed.
func syncGitHubLinks(ctx context.Context, repo repository.VCSLinkRepository, src githubapi.PullRequestEvidenceSource, links []domain.VCSLink, force bool) (changed bool, unconfirmed map[uuid.UUID]bool) {
	unconfirmed = map[uuid.UUID]bool{}
	for _, l := range links {
		if !githubLinkIsDelivery(l) || !force && !githubLinkNeedsEvidence(l) {
			continue
		}
		w, err := syncGitHubLinkEvidence(ctx, repo, src, l)
		// A row written before the failure (new head, then the check-runs
		// read failed) still changed: the caller must re-read it.
		changed = changed || w
		if err != nil {
			unconfirmed[l.ID] = true
			log.Printf("[vcs-github-sync] link=%s task=%s url=%q: %v", l.ID, l.TaskID, l.URL, err)
			// Fail closed: a cached green that GitHub could not confirm just
			// now stops vouching for delivery. The next successful sync binds
			// it again, so an outage costs a retry, not the evidence.
			if !w {
				u, uerr := unbindCachedGreen(ctx, repo, l)
				if uerr != nil {
					log.Printf("[vcs-github-sync] link=%s task=%s unbind after error: %v", l.ID, l.TaskID, uerr)
				}
				changed = changed || u
			}
		}
	}
	return changed, unconfirmed
}

// withoutCachedGreen returns links with the cached check binding cleared in
// memory for every unconfirmed id, so a failed unbind write cannot leave the
// stored green result vouching for delivery.
func withoutCachedGreen(links []domain.VCSLink, unconfirmed map[uuid.UUID]bool) []domain.VCSLink {
	if len(unconfirmed) == 0 {
		return links
	}
	out := make([]domain.VCSLink, len(links))
	copy(out, links)
	for i := range out {
		if !unconfirmed[out[i].ID] {
			continue
		}
		meta := map[string]json.RawMessage{}
		_ = json.Unmarshal(out[i].Metadata, &meta)
		meta[domain.VCSLinkMetaPipelineSHA] = json.RawMessage(`""`)
		out[i].Metadata, _ = json.Marshal(meta) // a map of raw JSON cannot fail to marshal
	}
	return out
}

// unbindGitHubCachedGreen unbinds the cached green of every GitHub delivery
// link and reports whether any row changed. Used when GitHub cannot be asked.
func unbindGitHubCachedGreen(ctx context.Context, repo repository.VCSLinkRepository, links []domain.VCSLink) bool {
	changed := false
	for _, l := range links {
		if !githubLinkIsDelivery(l) {
			continue
		}
		u, err := unbindCachedGreen(ctx, repo, l)
		if err != nil {
			log.Printf("[vcs-github-sync] link=%s task=%s unbind without checker: %v", l.ID, l.TaskID, err)
		}
		changed = changed || u
	}
	return changed
}
