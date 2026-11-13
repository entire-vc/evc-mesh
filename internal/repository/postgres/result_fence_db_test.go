package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Repository side of the result-write fence: a fenced
// comment or artifact commits only while the caller holds the named lock, and
// a refused write leaves no row behind.

func requireFenceCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "want *apierror.Error %s, got %v", code, err)
	require.Equal(t, http.StatusConflict, ae.Code)
	require.Equal(t, code, ae.ErrorCode)
}

type fenceRepoFixture struct {
	tasks   *TaskRepo
	taskID  uuid.UUID
	holder  uuid.UUID
	current *domain.ResultFence // names the live lock
	stale   *domain.ResultFence // names the lock of a previous generation
}

func newFenceRepoFixture(t *testing.T) fenceRepoFixture {
	t.Helper()
	repo, id, holder := checkoutGenerationFixture(t)
	ctx := context.Background()
	scope := generationScope()
	first, err := repo.AcquireCheckout(ctx, id, holder, uuid.New(), time.Now().Add(time.Hour), scope)
	require.NoError(t, err)
	stale := &domain.ResultFence{TaskID: id, Token: first.Token}
	// The same agent's next generation: release, then a new checkout.
	_, err = repo.CompareReleaseCheckout(ctx, id, domain.CheckoutExpectation{Token: first.Token})
	require.NoError(t, err)
	second, err := repo.AcquireCheckout(ctx, id, holder, uuid.New(), time.Now().Add(time.Hour), generationScope())
	require.NoError(t, err)
	require.Greater(t, second.Generation, first.Generation)
	return fenceRepoFixture{
		tasks: repo, taskID: id, holder: holder,
		current: &domain.ResultFence{TaskID: id, Holder: holder, SessionID: second.SessionID, Generation: second.Generation},
		stale:   stale,
	}
}

func (x fenceRepoFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, x.tasks.db.Get(&n, `SELECT count(*) FROM `+table+` WHERE task_id=$1`, x.taskID))
	return n
}

func (x fenceRepoFixture) comment(f *domain.ResultFence) *domain.Comment {
	now := time.Now().UTC()
	return &domain.Comment{
		ID: uuid.New(), TaskID: x.taskID, AuthorID: x.holder, AuthorType: domain.ActorTypeAgent,
		Body: "result", CreatedAt: now, UpdatedAt: now, Fence: f,
	}
}

func (x fenceRepoFixture) artifact(f *domain.ResultFence) *domain.Artifact {
	id := uuid.New()
	return &domain.Artifact{
		ID: id, TaskID: x.taskID, Name: "result.txt", ArtifactType: domain.ArtifactTypeFile,
		MimeType: "text/plain", StorageKey: "test/" + id.String(), SizeBytes: 1,
		Metadata: json.RawMessage(`{}`), UploadedBy: x.holder, UploadedByType: domain.UploaderTypeAgent,
		CreatedAt: time.Now().UTC(), Fence: f,
	}
}

func TestResultFenceRepo_Comment_DB(t *testing.T) {
	x := newFenceRepoFixture(t)
	ctx := context.Background()
	comments := NewCommentRepo(x.tasks.db)

	require.NoError(t, comments.Create(ctx, x.comment(x.current)), "the live lock's comment commits")
	require.Equal(t, 1, x.count(t, "comments"))

	requireFenceCode(t, comments.Create(ctx, x.comment(x.stale)), domain.CodeCheckoutPreconditionFailed)
	rev := int64(0)
	revFence := *x.current
	revFence.ExpectedInputRevision = &rev
	requireFenceCode(t, comments.Create(ctx, x.comment(&revFence)), domain.CodeInputRevisionChanged)
	require.Equal(t, 1, x.count(t, "comments"), "a refused comment is never written")

	gone := *x.current
	gone.TaskID = uuid.New()
	c := x.comment(&gone)
	err := comments.Create(ctx, c)
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae))
	require.Equal(t, http.StatusNotFound, ae.Code, "a fence on a missing task is 404")
}

func TestResultFenceRepo_Artifact_DB(t *testing.T) {
	x := newFenceRepoFixture(t)
	ctx := context.Background()
	artifacts := NewArtifactRepo(x.tasks.db)

	// The early, unlocked checks agree with the locked one.
	require.NoError(t, artifacts.CheckResultFence(ctx, x.current))
	require.NoError(t, x.tasks.CheckResultFence(ctx, x.current))
	requireFenceCode(t, artifacts.CheckResultFence(ctx, x.stale), domain.CodeCheckoutPreconditionFailed)
	requireFenceCode(t, x.tasks.CheckResultFence(ctx, x.stale), domain.CodeCheckoutPreconditionFailed)
	require.NoError(t, x.tasks.CheckResultFence(ctx, nil), "no fence, no check")

	require.NoError(t, artifacts.Create(ctx, x.artifact(x.current)))
	requireFenceCode(t, artifacts.Create(ctx, x.artifact(x.stale)), domain.CodeCheckoutPreconditionFailed)
	require.Equal(t, 1, x.count(t, "artifacts"), "a refused artifact is never written")

	// An upload through a reservation: a stale fence records nothing and
	// keeps the reservation for the caller to release.
	resID := uuid.New()
	ok, _, err := artifacts.ReserveTaskBytes(ctx, resID, x.taskID, 1, 100, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	a := x.artifact(x.stale)
	a.ID = resID
	requireFenceCode(t, artifacts.CreateFromReservation(ctx, resID, a), domain.CodeCheckoutPreconditionFailed)
	require.Equal(t, 1, x.count(t, "artifacts"))
	require.Equal(t, 1, x.count(t, "artifact_reservations"), "the refused upload's reservation stays for its release")
	a.Fence = x.current
	require.NoError(t, artifacts.CreateFromReservation(ctx, resID, a))
	require.Equal(t, 2, x.count(t, "artifacts"))
}
