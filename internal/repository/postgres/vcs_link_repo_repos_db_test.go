package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// an internal ticket: the repositories a workspace's and a project's links point into,
// read from vcs_links.url in one aggregate per provider.
func TestVCSLinkRepo_ProviderRepos(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	fx := newCoverageFixture(t, db)

	seed := func(provider domain.VCSProvider, linkType domain.VCSLinkType, url string, at time.Time) {
		t.Helper()
		_, err := repo.Upsert(ctx, &domain.VCSLink{
			ID: uuid.New(), TaskID: fx.taskID, Provider: provider, LinkType: linkType,
			ExternalID: uuid.NewString(), URL: url, Status: domain.VCSLinkStatusOpen, CreatedAt: at,
		})
		require.NoError(t, err)
	}
	gl := "g/p-" + fx.suffix
	gh := "o/r-" + fx.suffix
	// recent sits seconds before the seeding on purpose: Upsert stamps
	// status_synced_at with the database now(), so last_event_at is the
	// moment of the last seed, not recent, and the check below bounds it by
	// the wall clock around seeding instead of a tolerance around recent.
	old, recent := time.Now().Add(-2*time.Hour).UTC().Truncate(time.Second), time.Now().Add(-3*time.Second).UTC().Truncate(time.Second)
	seededFrom := time.Now().UTC().Truncate(time.Microsecond)
	seed(domain.VCSProviderGitLab, domain.VCSLinkTypePR, "https://GIT.Example.com/G/P-"+fx.suffix+"/-/merge_requests/1", old)
	seed(domain.VCSProviderGitLab, domain.VCSLinkTypePR, "https://git.example.com/"+gl+"/merge_requests/2", old)
	seed(domain.VCSProviderGitLab, domain.VCSLinkTypeCommit, "https://git.example.com/"+gl+"/-/commit/abc", recent)
	seed(domain.VCSProviderGitLab, domain.VCSLinkTypeBranch, "https://git.example.com/"+gl+"/-/tree/feat", old)
	seed(domain.VCSProviderGitLab, domain.VCSLinkTypePR, "https://git.example.com/"+gl+"/sub/-/merge_requests/3", old)
	seed(domain.VCSProviderGitLab, domain.VCSLinkTypePR, "https://git.example.com/no-repo-segment", old)
	seed(domain.VCSProviderGitHub, domain.VCSLinkTypePR, "https://github.com/"+gh+"/pull/7", old)
	seededTo := time.Now().UTC()

	byRepo := func(rows []repository.VCSRepoActivity) map[string]repository.VCSRepoActivity {
		m := map[string]repository.VCSRepoActivity{}
		for _, r := range rows {
			m[r.Repo] = r
		}
		return m
	}

	glRows, err := repo.WorkspaceProviderRepos(ctx, fx.workspaceID, "gitlab", 20)
	require.NoError(t, err)
	require.Len(t, glRows, 2, "the project and its subgroup project; a url without a repository segment is left out")
	assert.Equal(t, gl, glRows[0].Repo, "most linked first")
	assert.Equal(t, "git.example.com", glRows[0].Host)
	assert.Equal(t, 4, glRows[0].Links, "MR (both url forms), commit and tree links, scheme and case normalised")
	assert.WithinRange(t, glRows[0].LastEventAt.UTC(), seededFrom, seededTo, "the newest sync stamp of the seeded links")
	assert.Equal(t, 1, byRepo(glRows)[gl+"/sub"].Links)

	ghRows, err := repo.WorkspaceProviderRepos(ctx, fx.workspaceID, "github", 20)
	require.NoError(t, err)
	require.Len(t, ghRows, 1)
	assert.Equal(t, repository.VCSRepoActivity{Repo: gh, Host: "github.com", Links: 1, LastEventAt: ghRows[0].LastEventAt}, ghRows[0])

	limited, err := repo.WorkspaceProviderRepos(ctx, fx.workspaceID, "gitlab", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)

	pRows, err := repo.ProjectProviderRepos(ctx, fx.projectID, "gitlab", gl+"/sub", 1)
	require.NoError(t, err)
	require.Len(t, pRows, 1)
	assert.Equal(t, gl+"/sub", pRows[0].Repo, "the preferred (bound) repository survives the limit")

	other := newCoverageFixture(t, db)
	none, err := repo.ProjectProviderRepos(ctx, other.projectID, "gitlab", "", 100)
	require.NoError(t, err)
	assert.Empty(t, none, "another project's links are not its repositories")
}
