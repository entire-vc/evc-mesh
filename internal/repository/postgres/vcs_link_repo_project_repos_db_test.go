package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestProjectRepoUsage_OwnRepos(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	fx := newCoverageFixture(t, db)
	other := newCoverageFixture(t, db)

	put := func(taskID uuid.UUID, lt domain.VCSLinkType, url, meta string) {
		_, err := repo.Upsert(ctx, &domain.VCSLink{
			ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: lt,
			ExternalID: "1", URL: url, Status: domain.VCSLinkStatusMerged, Metadata: []byte(meta), CreatedAt: time.Now(),
		})
		require.NoError(t, err)
	}
	base := "https://git.example.com/g/" + fx.suffix
	put(fx.taskID, domain.VCSLinkTypePR, base+"-own/-/merge_requests/1", `{}`)
	put(fx.taskID, domain.VCSLinkTypePR, base+"-own/-/merge_requests/9", `{}`) // same repo, one name
	put(fx.taskID, domain.VCSLinkTypePR, "https://github.com/Org-"+fx.suffix+"/Mirror/pull/7", `{}`)
	put(fx.taskID, domain.VCSLinkTypeReference, base+"-ref/-/merge_requests/2", `{"reference_only":true}`)
	put(fx.taskID, domain.VCSLinkTypePR, base+"-review/-/merge_requests/3", `{"role":"review"}`)
	put(other.taskID, domain.VCSLinkTypePR, base+"-elsewhere/-/merge_requests/4", `{}`)

	use := func(r string) (bool, bool) {
		u, anyLink, err := repo.ProjectRepoUsage(ctx, fx.projectID, r)
		require.NoError(t, err)
		return u, anyLink
	}
	u, anyLink := use("g/" + fx.suffix + "-own")
	assert.True(t, u)
	assert.True(t, anyLink)
	u, _ = use("org-" + fx.suffix + "/mirror") // github URL, mixed case in the link
	assert.True(t, u)
	u, anyLink = use("g/" + fx.suffix + "-ref") // reference link only
	assert.False(t, u)
	assert.True(t, anyLink)
	u, _ = use("g/" + fx.suffix + "-review") // review-subject link only
	assert.False(t, u)
	u, _ = use("g/" + fx.suffix + "-elsewhere") // another project's link
	assert.False(t, u)

	u, anyLink, err := repo.ProjectRepoUsage(ctx, uuid.New(), "g/x") // project with no links
	require.NoError(t, err)
	assert.False(t, u)
	assert.False(t, anyLink)
}
