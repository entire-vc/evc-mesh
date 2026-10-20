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

	const url = "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/77"
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

// Identity includes the url (#16c1dd76), so an Upsert carrying A's url but the
// external_id of sibling B can no longer clash with B: B's url differs. The
// url names A, so A is corrected in place and B is left untouched.
func TestVCSLinkRepo_Upsert_UpdateOntoSiblingExternalIDKeepsBothRows(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	const urlA = "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/91"
	const urlB = "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/92"
	mk := func(eid, url string, st domain.VCSLinkStatus) *domain.VCSLink {
		return &domain.VCSLink{
			ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
			LinkType: domain.VCSLinkTypePR, ExternalID: eid, URL: url,
			Status: st, CreatedAt: time.Now(),
		}
	}
	_, err := repo.Upsert(ctx, mk("91", urlA, domain.VCSLinkStatusOpen))
	require.NoError(t, err)
	_, err = repo.Upsert(ctx, mk("92", urlB, domain.VCSLinkStatusOpen))
	require.NoError(t, err)

	_, err = repo.Upsert(ctx, mk("92", urlA, domain.VCSLinkStatusClosed))
	require.NoError(t, err)

	links, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, links, 2)
	for _, l := range links {
		if l.URL == urlA {
			assert.Equal(t, domain.VCSLinkStatusClosed, l.Status)
		} else {
			assert.Equal(t, domain.VCSLinkStatusOpen, l.Status, "B untouched")
		}
	}
}
