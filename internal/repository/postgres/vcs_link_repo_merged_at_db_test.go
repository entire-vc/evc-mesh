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

// vcs_links.merged_at on real Postgres: set once from the
// provider, kept by every later status upsert, cleared only when the status is
// corrected away from merged, never stamped on a link that is not merged.

func TestVCSLinkRepo_MergedAt_SetOnceAndKept_DB(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)
	url := "https://git.example/g/p/-/merge_requests/91"
	provider := time.Date(2026, 11, 20, 18, 3, 4, 120000e3, time.FixedZone("MSK", 3*3600))

	link := &domain.VCSLink{ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "91", URL: url, Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(), MergedAt: &provider}
	_, err := repo.Upsert(ctx, link)
	require.NoError(t, err)
	require.NotNil(t, link.MergedAt)
	assert.Equal(t, time.UTC, link.MergedAt.Location(), "returned in UTC")
	assert.True(t, link.MergedAt.Equal(provider))

	// A later upsert with another time (e.g. the time of a re-read) keeps it.
	later := time.Now()
	again := &domain.VCSLink{ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "91", URL: url, Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(), MergedAt: &later}
	_, err = repo.Upsert(ctx, again)
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, link.ID)
	require.NoError(t, err)
	require.NotNil(t, got.MergedAt)
	assert.True(t, got.MergedAt.Equal(provider), "known merged_at kept: %v", got.MergedAt)
	assert.Equal(t, time.UTC, got.MergedAt.Location())

	// SetMergedAt never replaces a known time.
	w, err := repo.SetMergedAt(ctx, link.ID, later)
	require.NoError(t, err)
	assert.False(t, w)

	// A status corrected away from merged clears it; SetMergedAt then refuses
	// to stamp the non-merged link, and the backfill list does not offer it.
	open := &domain.VCSLink{ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "91", URL: url, Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(), MergedAt: &later}
	_, err = repo.Upsert(ctx, open)
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, link.ID)
	require.NoError(t, err)
	assert.Nil(t, got.MergedAt, "non-merged link has no merged_at")
	w, err = repo.SetMergedAt(ctx, link.ID, provider)
	require.NoError(t, err)
	assert.False(t, w, "an open link is never stamped")

	// Merged again without a time: unknown, NULL; offered to the backfill;
	// SetMergedAt fills it once; a zero time is never written.
	merged := &domain.VCSLink{ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: "91", URL: url, Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now()}
	_, err = repo.Upsert(ctx, merged)
	require.NoError(t, err)
	assert.Nil(t, merged.MergedAt)
	w, err = repo.SetMergedAt(ctx, link.ID, time.Time{})
	require.NoError(t, err)
	assert.False(t, w, "zero time is unknown")
	pending, err := repo.ListMergedPRLinksWithoutMergedAt(ctx, uuid.Nil, 100000)
	require.NoError(t, err)
	found := false
	for _, p := range pending {
		found = found || p.ID == link.ID
	}
	assert.True(t, found, "merged link with NULL merged_at is offered to the backfill")
	w, err = repo.SetMergedAt(ctx, link.ID, provider)
	require.NoError(t, err)
	assert.True(t, w)
	w, err = repo.SetMergedAt(ctx, link.ID, provider)
	require.NoError(t, err)
	assert.False(t, w, "repeat is a no-op")
	got, err = repo.GetByID(ctx, link.ID)
	require.NoError(t, err)
	require.NotNil(t, got.MergedAt)
	assert.True(t, got.MergedAt.Equal(provider))
}
