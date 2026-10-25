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

// acceptance_missing: all children closed but a pr link has no acceptance.
// Same skip-if-no-DB convention as vcs_link_repo_upsert_db_test.go.
func TestTaskRepo_AcceptanceMissing(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	tasks := NewTaskRepo(db)
	links := NewVCSLinkRepo(db)
	parentID := vcsLinkTestTask(t, db)
	parent, err := tasks.GetByID(ctx, parentID)
	require.NoError(t, err)
	assert.False(t, parent.AcceptanceMissing, "no children, no links")

	done := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: parent.ProjectID, Name: "Done", Slug: "done",
		Color: "#0000FF", Position: 1, Category: domain.StatusCategoryDone,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, done))
	child := &domain.Task{
		ID: uuid.New(), ProjectID: parent.ProjectID, StatusID: done.ID, ParentTaskID: &parentID,
		Title: "acceptance_missing child", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, tasks.Create(ctx, child, nil))
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE id = $1", child.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE id = $1", done.ID)
	})

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: child.ID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "91",
		URL:    "https://gitlab.example.com/entire-vc/evc-mesh/-/merge_requests/91",
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	_, err = links.Upsert(ctx, link)
	require.NoError(t, err)

	parent, err = tasks.GetByID(ctx, parentID)
	require.NoError(t, err)
	assert.True(t, parent.AcceptanceMissing, "children done, link without acceptance")

	_, err = links.MergeMetadata(ctx, child.ID, link.ID, []byte(`{"acceptance":{"result":"passed"}}`))
	require.NoError(t, err)
	parent, err = tasks.GetByID(ctx, parentID)
	require.NoError(t, err)
	assert.False(t, parent.AcceptanceMissing, "acceptance recorded")
}

func TestVCSLinkRepo_MergeMetadata_KeepsFirstDeployedAt(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)
	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "92",
		URL:    "https://gitlab.example.com/entire-vc/evc-mesh/-/merge_requests/92",
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	_, err := repo.Upsert(ctx, link)
	require.NoError(t, err)
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	first, err := repo.MergeMetadata(ctx, taskID, link.ID, []byte(`{"deployed":{"sha":"`+sha+`","environment":"prod","at":"2026-10-25T00:00:00Z"}}`))
	require.NoError(t, err)
	second, err := repo.MergeMetadata(ctx, taskID, link.ID, []byte(`{"deployed":{"sha":"`+sha+`","environment":"prod","at":"2026-10-25T05:00:00Z"}}`))
	require.NoError(t, err)
	assert.True(t, first.Deployed().At.Equal(*second.Deployed().At), "same sha+env keeps the first at")

	third, err := repo.MergeMetadata(ctx, taskID, link.ID, []byte(`{"deployed":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","environment":"prod","at":"2026-10-25T06:00:00Z"}}`))
	require.NoError(t, err)
	assert.Equal(t, 6, third.Deployed().At.UTC().Hour(), "a new sha takes its own at")

	other, err := repo.MergeMetadata(ctx, taskID, link.ID, []byte(`{"merged_sha":"`+sha+`"}`))
	require.NoError(t, err)
	assert.Equal(t, 6, other.Deployed().At.UTC().Hour(), "a patch without deployed leaves it alone")
}
