package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The pipeline state of an MR link: stored by SetPipelineStatus, read back by
// ListByTask (the GET /tasks/{id}/vcs-links body) and summarised on the task.
// Same untagged skip-if-no-DB convention as vcs_link_repo_upsert_db_test.go.
func TestVCSLinkRepo_PipelineStatus_StoredGuardedAndReadBack(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "88",
		URL:    "https://gitlab.example.com/entire-vc/evc-mesh/-/merge_requests/88",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	_, err := repo.Upsert(ctx, link)
	require.NoError(t, err)

	links, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Nil(t, links[0].PipelineStatus, "no pipeline reported yet")

	written, err := repo.SetPipelineStatus(ctx, link.ID, 500, "running", "aaa")
	require.NoError(t, err)
	require.True(t, written)
	written, err = repo.SetPipelineStatus(ctx, link.ID, 500, "success", "bbb")
	require.NoError(t, err)
	require.True(t, written, "a later status of the same pipeline overwrites")
	written, err = repo.SetPipelineStatus(ctx, link.ID, 499, "failed", "old")
	require.NoError(t, err)
	assert.False(t, written, "an older pipeline must not overwrite a newer one")

	links, err = repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, links[0].PipelineStatus)
	assert.Equal(t, "success", *links[0].PipelineStatus)
	assert.Equal(t, "bbb", links[0].PipelineSHA(), "sha is written with its status and ignored with a stale pipeline")
	require.NotNil(t, links[0].PipelineUpdatedAt)

	var summary *string
	require.NoError(t, db.GetContext(ctx, &summary, `SELECT (SELECT v.pipeline_status FROM vcs_links v WHERE v.task_id = $1 AND v.pipeline_status IS NOT NULL ORDER BY v.pipeline_updated_at DESC LIMIT 1)`, taskID))
	require.NotNil(t, summary)
	assert.Equal(t, "success", *summary)

	// An Upsert (the MR webhook) must not wipe the pipeline state.
	link2 := *link
	link2.ID = uuid.New()
	link2.Status = domain.VCSLinkStatusMerged
	_, err = repo.Upsert(ctx, &link2)
	require.NoError(t, err)
	links, err = repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, links[0].PipelineStatus, "the MR hook rewrites status, not the pipeline")
	assert.Equal(t, "success", *links[0].PipelineStatus)
}

// MergeMetadata must set the patched key and keep every other key, and stay
// inside (task, link_type=pr).
func TestVCSLinkRepo_MergeMetadata_KeepsOtherKeys(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	head := strings.Repeat("a", 40)
	ship := strings.Repeat("b", 40)
	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "89",
		URL:    "https://gitlab.example.com/entire-vc/evc-mesh/-/merge_requests/89",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
		Metadata: json.RawMessage(`{"head_sha":"` + head + `","role":"review"}`),
	}
	_, err := repo.Upsert(ctx, link)
	require.NoError(t, err)

	got, err := repo.MergeMetadata(ctx, taskID, link.ID, json.RawMessage(`{"ship_sha":"`+ship+`"}`))
	require.NoError(t, err)
	require.NotNil(t, got)
	var meta map[string]string
	require.NoError(t, json.Unmarshal(got.Metadata, &meta))
	assert.Equal(t, map[string]string{"head_sha": head, "role": "review", "ship_sha": ship}, meta)

	other, err := repo.MergeMetadata(ctx, uuid.New(), link.ID, json.RawMessage(`{"ship_sha":"`+ship+`"}`))
	require.NoError(t, err)
	assert.Nil(t, other, "a link is addressed through its own task only")
}
