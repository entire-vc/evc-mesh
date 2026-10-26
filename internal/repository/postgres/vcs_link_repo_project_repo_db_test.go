package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// an internal ticket: the webhook asks whether a repository is one the project works
// in. Only the project's own PR links count, mentions and review subjects do not, the match is the
// exact repository (no subgroup, LIKE metacharacters literal), and an old link
// is found however many newer ones the project has.
func TestProjectHasPRLinkToRepo(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)

	seed := func(fx coverageFixture, url, meta string, at time.Time) {
		t.Helper()
		_, err := repo.Upsert(ctx, &domain.VCSLink{
			ID: uuid.New(), TaskID: fx.taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
			ExternalID: "1", URL: url, Status: domain.VCSLinkStatusMerged, Metadata: []byte(meta), CreatedAt: at,
		})
		require.NoError(t, err)
	}
	has := func(fx coverageFixture, r string) bool {
		t.Helper()
		ok, err := repo.ProjectHasPRLinkToRepo(ctx, fx.projectID, r)
		require.NoError(t, err)
		return ok
	}

	own := newCoverageFixture(t, db)
	r := "git.example.com/g/p_" + own.suffix
	old := time.Now().Add(-time.Hour)
	seed(own, "http://GIT.example.com/G/P_"+own.suffix+"/-/merge_requests/1", `{}`, old)
	for i := 0; i < 60; i++ { // newer links of other repositories must not hide the old match
		seed(own, "https://git.example.com/g/z"+strconv.Itoa(i)+"-"+own.suffix+"/-/merge_requests/1", `{}`, time.Now())
	}
	assert.True(t, has(own, r), "old link, other scheme and case, behind 60 newer links")
	assert.False(t, has(own, "git.example.com/g/pX"+own.suffix), "_ is literal, not a wildcard")
	assert.False(t, has(own, "git.example.com/g"), "a parent group is not the repository")

	sub := newCoverageFixture(t, db)
	seed(sub, "https://git.example.com/g/s-"+sub.suffix+"/sub/-/merge_requests/1", `{}`, time.Now())
	assert.False(t, has(sub, "git.example.com/g/s-"+sub.suffix), "a subgroup project is a different repository")

	gh := newCoverageFixture(t, db)
	seed(gh, "https://github.com/o/r-"+gh.suffix+"/pull/7", `{}`, time.Now())
	assert.True(t, has(gh, "github.com/o/r-"+gh.suffix))

	other := newCoverageFixture(t, db)
	assert.False(t, has(other, r), "another project's link does not count")

	mention := newCoverageFixture(t, db)
	seed(mention, "https://git.example.com/g/m-"+mention.suffix+"/-/merge_requests/3", `{"relation":"relates_to"}`, time.Now())
	assert.False(t, has(mention, "git.example.com/g/m-"+mention.suffix), "a relates_to mention does not make the repository the project's")

	review := newCoverageFixture(t, db)
	seed(review, "https://git.example.com/g/v-"+review.suffix+"/-/merge_requests/4", `{"role":"review"}`, time.Now())
	assert.False(t, has(review, "git.example.com/g/v-"+review.suffix), "a review card's subject MR does not make the repository the project's")
}
