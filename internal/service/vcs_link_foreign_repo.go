package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// A card id quoted in an MR description is a mention, not a claim of ownership:
// the 25.10 incident was an MR of a fleet-tooling repository whose description
// cited a product card in an unrelated project; the merge moved that card from
// review to done although a human still had to accept it.
//
// The only reliable association Mesh holds between a project and its code is
// the PR/MR links its own cards already carry (projects have no repository
// settings). A mention that arrives from a repository none of the project's PR
// links point into is therefore recorded as a reference link and never drives a
// status transition. A project with no PR link yet has no association to
// compare against; there the mention keeps working as before and only the
// card-state guards in applyPRTransitionPolicy protect the card.

// vcsLinkMetaReferenceOnly marks a link that was recorded from a mention in a
// repository foreign to the card's project.
const vcsLinkMetaReferenceOnly = "reference_only"

// mrRepoName returns the lower-cased "group/project" of the repository an MR/PR
// belongs to, preferring the provider's own repository field over the URL.
func mrRepoName(repoField, rawURL string) string {
	if r := strings.ToLower(strings.Trim(strings.TrimSpace(repoField), "/")); r != "" {
		return r
	}
	return RepoFromVCSURL(rawURL)
}

// isForeignRepoMention reports whether an MR/PR that named taskID in its
// title/body/branch comes from a repository the task's project does not use.
// It is only meaningful for a mention-resolved task; a task found through an
// already stored link for this very MR is trusted by the caller.
//
// Order of evidence:
//  1. The card already holds a link to this exact MR: a "pr" link is an
//     established association (explicit add_vcs_link, or an earlier delivery
//     that passed this check); a "reference" link stays a reference.
//  2. The repository against the repositories of the project's other PR links.
//     Unknown repository, no project association, or any lookup failure all
//     read as "not foreign": the guard must not invent a refusal it cannot
//     justify, the card-state guards cover that case.
func (s *vcsLinkService) isForeignRepoMention(ctx context.Context, taskID uuid.UUID, repoField, rawURL string) bool {
	if s.taskRepo == nil {
		return false
	}
	if rawURL != "" {
		if links, err := s.repo.ListByTask(ctx, taskID); err == nil {
			want := domain.NormalizeVCSURL(rawURL)
			for _, l := range links {
				if domain.NormalizeVCSURL(l.URL) != want {
					continue
				}
				switch l.LinkType {
				case domain.VCSLinkTypePR:
					return false
				case domain.VCSLinkTypeReference:
					return true
				}
			}
		}
	}

	repo := mrRepoName(repoField, rawURL)
	if repo == "" {
		return false
	}
	lister, ok := s.repo.(repository.VCSLinkProjectRepos)
	if !ok {
		return false
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return false
	}
	uses, hasAny, err := lister.ProjectRepoUsage(ctx, task.ProjectID, repo)
	if err != nil {
		log.Printf("[vcs-webhook] project repos lookup project=%s: %v — treating mention as native", task.ProjectID, err)
		return false
	}
	if uses {
		// ProjectRepoUsage ignores the host; the same group/project on another
		// host is another repository. Lookup failure keeps the mention native.
		if key := vcsRepoKey(rawURL); key != "" {
			if same, herr := s.repo.ProjectHasPRLinkToRepo(ctx, task.ProjectID, key); herr == nil && !same {
				uses = false
			}
		}
	}
	if uses || !hasAny {
		return false
	}
	log.Printf("[vcs-webhook] mention of task=%s from foreign repo %q (project has PR links only in other repos): recording as reference only", taskID, repo)
	return true
}

// referenceOnlyMetadata adds reference_only=true to a link metadata object.
func referenceOnlyMetadata(base []byte) []byte {
	m := map[string]any{}
	if len(base) > 0 {
		_ = json.Unmarshal(base, &m)
	}
	m[vcsLinkMetaReferenceOnly] = true
	b, _ := json.Marshal(m) // a map of plain values cannot fail to marshal
	return b
}

// finishForeignMention is the whole reaction to a terminal MR/PR of a foreign
// repository: leave the card where it is, say so once when the MR merged.
func (s *vcsLinkService) finishForeignMention(ctx context.Context, taskID uuid.UUID, merged bool, refLabel, mergeRefLabel, repo, source string) PRHandleResult {
	if merged {
		where := "a repository outside this project"
		if repo != "" {
			where = fmt.Sprintf("repository %q, which this project does not use", repo)
		}
		body := fmt.Sprintf("🤖 Auto: %s merged (commit %#q) in %s. The card only mentions it, so it was NOT moved; the MR is recorded as a reference.", refLabel, mergeRefLabel, where)
		if cerr := s.postSystemComment(ctx, taskID, body, source); cerr != nil {
			log.Printf("[vcs-webhook] post comment failed task=%s: %v", taskID, cerr)
		}
	}
	return PRHandleResult{TaskID: taskID, Reason: "foreign_repo_reference"}
}
