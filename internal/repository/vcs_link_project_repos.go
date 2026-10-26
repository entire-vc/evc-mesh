package repository

import (
	"context"

	"github.com/google/uuid"
)

// VCSLinkProjectRepos is implemented by a VCSLinkRepository that can say which
// repositories a project already works in. It is a separate, optional interface
// (the service type-asserts it) so that read stays out of the core CRUD
// contract; an implementation that lacks it simply reports "no association
// known", and the merge webhook falls back to its card-state guards.
type VCSLinkProjectRepos interface {
	// ProjectRepoUsage reports, over ALL qualifying PR/MR links held by the
	// tasks of projectID, whether any of them points into repo (a lower-cased
	// "group/project", host ignored) and whether the project has any qualifying
	// link at all. It is an exact check, not a window or a capped list, so a
	// repository the project uses cannot be missed however old its links are or
	// however many repositories the project has. Reference links and
	// review-subject links are excluded: they point at someone else's repository
	// by design and say nothing about where the project's own code lives.
	ProjectRepoUsage(ctx context.Context, projectID uuid.UUID, repo string) (uses, hasAny bool, err error)
}
