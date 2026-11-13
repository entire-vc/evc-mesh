package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// fencingArtifactRepo answers the early fence check with err and counts calls.
type fencingArtifactRepo struct {
	*MockArtifactRepository
	err   error
	calls int
}

func (r *fencingArtifactRepo) CheckResultFence(context.Context, *domain.ResultFence) error {
	r.calls++
	return r.err
}

// A fenced upload whose lock is already gone is refused before any byte
// reaches storage; a holding fence uploads as before.
func TestArtifactService_Upload_FenceCheckedBeforeStorage(t *testing.T) {
	refusal := &apierror.Error{Code: http.StatusConflict, ErrorCode: domain.CodeCheckoutPreconditionFailed, Message: "lock gone"}
	for _, tc := range []struct {
		name    string
		fence   *domain.ResultFence
		err     error
		calls   int
		refused bool
	}{
		{"lock gone", &domain.ResultFence{Generation: 1}, refusal, 1, true},
		{"lock held", &domain.ResultFence{Generation: 1}, nil, 1, false},
		{"no fence", nil, refusal, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fencingArtifactRepo{MockArtifactRepository: NewMockArtifactRepository(), err: tc.err}
			storage := NewMockStorageClient()
			svc := NewArtifactService(repo, storage, NewMockActivityLogRepository())
			taskID := uuid.New()
			if tc.fence != nil {
				tc.fence.TaskID = taskID
			}
			_, err := svc.Upload(context.Background(), UploadArtifactInput{
				TaskID: taskID, Name: "r.txt", ArtifactType: domain.ArtifactTypeFile, MimeType: "text/plain",
				UploadedBy: uuid.New(), UploadedByType: domain.UploaderTypeAgent,
				Reader: strings.NewReader("x"), Size: 1, Fence: tc.fence,
			})
			assert.Equal(t, tc.calls, repo.calls)
			if tc.refused {
				var ae *apierror.Error
				require.True(t, errors.As(err, &ae))
				assert.Equal(t, domain.CodeCheckoutPreconditionFailed, ae.ErrorCode)
				assert.Empty(t, storage.objects, "nothing reaches storage")
				assert.Empty(t, repo.items, "nothing is recorded")
				return
			}
			require.NoError(t, err)
			assert.Len(t, storage.objects, 1)
		})
	}
}
