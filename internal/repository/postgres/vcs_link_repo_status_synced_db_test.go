package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// #b593c566: an open PR link carried no signal of how recently anything
// confirmed it was still open, so a lost "closed" webhook left it open
// forever, invisibly. Upsert (the path every status-carrying write takes)
// must stamp status_synced_at on both branches; Create (no status stated)
// must leave it NULL. Same untagged skip-if-no-DB convention as
// vcs_link_repo_upsert_db_test.go.

func TestVCSLinkRepo_Upsert_StampsStatusSyncedAtOnInsertAndUpdate(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	const url = "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/77"
	before := time.Now().Add(-time.Second)

	first := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "77", URL: url,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	created, err := repo.Upsert(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, first.StatusSyncedAt, "insert branch must reflect the stamp back to the caller")

	stored, err := repo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NotNil(t, stored.StatusSyncedAt, "insert branch must persist status_synced_at")
	assert.True(t, stored.StatusSyncedAt.After(before), "stamp must be fresh, got %v", stored.StatusSyncedAt)
	insertStamp := *stored.StatusSyncedAt

	// Make the update branch's stamp distinguishable from the insert's.
	_, err = db.ExecContext(ctx, `UPDATE vcs_links SET status_synced_at = now() - interval '1 day' WHERE id = $1`, first.ID)
	require.NoError(t, err)

	second := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "77", URL: url,
		Status: domain.VCSLinkStatusClosed, CreatedAt: time.Now(),
	}
	created, err = repo.Upsert(ctx, second)
	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, second.StatusSyncedAt, "update branch must reflect the stamp back to the caller")

	links, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, links, 1, "a redelivery must update the same row, never add one")
	require.NotNil(t, links[0].StatusSyncedAt, "update branch must persist status_synced_at")
	assert.Equal(t, domain.VCSLinkStatusClosed, links[0].Status)
	assert.True(t, links[0].StatusSyncedAt.After(before), "update must refresh the stamp, got %v (insert stamp %v)", links[0].StatusSyncedAt, insertStamp)
}

func TestVCSLinkRepo_Create_LeavesStatusSyncedAtNull(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "78",
		URL:    "https://github.com/entire-vc/evc-mesh/pull/78",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(ctx, link))

	stored, err := repo.GetByID(ctx, link.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Nil(t, stored.StatusSyncedAt, "a link created without a confirmed status reads NULL = never verified")
}

// An Upsert that would rewrite a link's identity onto one a sibling link on
// the same task already holds must surface a 409, not a raw unique-index
// error, and must leave both rows untouched.
func TestVCSLinkRepo_Upsert_UpdateOntoSiblingIdentityIsConflict(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	const urlA = "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/91"
	const urlB = "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/92"
	a := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "91", URL: urlA,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	b := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "92", URL: urlB,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	_, err := repo.Upsert(ctx, a)
	require.NoError(t, err)
	_, err = repo.Upsert(ctx, b)
	require.NoError(t, err)

	// Same url as A, but the identity of B.
	clash := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "92", URL: urlA,
		Status: domain.VCSLinkStatusClosed, CreatedAt: time.Now(),
	}
	_, err = repo.Upsert(ctx, clash)
	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode())
}
