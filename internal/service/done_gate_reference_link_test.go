package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A review-only card links someone else's open MR as a reference: the verdict
// is its deliverable, so move→done must pass. A plain pr link still blocks.
func TestMoveTask_Done_ReferenceLinkOnOpenMR(t *testing.T) {
	for _, tc := range []struct {
		name     string
		linkType domain.VCSLinkType
		blocked  bool
	}{
		{"reference link passes", domain.VCSLinkTypeReference, false},
		{"pr link still blocks", domain.VCSLinkTypePR, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectID, taskID, statusID := uuid.New(), uuid.New(), uuid.New()
			svc, taskRepo, statusRepo, vcsRepo, _ := setupTaskServiceWithDoneGate()
			taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projectID, StatusID: uuid.New(), Title: "review-only", VCSLinkCount: 1}
			statusRepo.items[statusID] = &domain.TaskStatus{ID: statusID, ProjectID: projectID, Category: domain.StatusCategoryDone}
			vcsRepo.items = append(vcsRepo.items, domain.VCSLink{
				ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab, LinkType: tc.linkType,
				Status: domain.VCSLinkStatusOpen, URL: "https://gitlab.example.invalid/entire-vc/other/-/merge_requests/90", ExternalID: "90",
			})

			err := svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &statusID})

			if tc.blocked {
				var doneErr *DoneEvidenceError
				require.ErrorAs(t, err, &doneErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestParseVCSLinkType_Reference(t *testing.T) {
	lt, ok := domain.ParseVCSLinkType(" Reference ")
	assert.True(t, ok)
	assert.Equal(t, domain.VCSLinkTypeReference, lt)
}
