package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// waitRepoStub holds one wait and records resolutions.
type waitRepoStub struct {
	getErr   error
	w        *domain.TaskWait
	ws       uuid.UUID
	resolved []string
}

func (r *waitRepoStub) Declare(context.Context, *domain.TaskWait) error { return nil }
func (r *waitRepoStub) GetActive(context.Context, uuid.UUID, *uuid.UUID) (*domain.TaskWait, error) {
	return nil, nil
}
func (r *waitRepoStub) Resolve(context.Context, uuid.UUID, domain.WaitResolution) (bool, error) {
	return false, nil
}
func (r *waitRepoStub) GetForTask(_ context.Context, taskID, waitID uuid.UUID) (*domain.TaskWait, uuid.UUID, error) {
	if r.getErr != nil {
		return nil, uuid.Nil, r.getErr
	}
	if r.w == nil || r.w.ID != waitID || r.w.TaskID != taskID {
		return nil, uuid.Nil, nil
	}
	cp := *r.w
	return &cp, r.ws, nil
}
func (r *waitRepoStub) ResolveWithCause(_ context.Context, _, _ uuid.UUID, res domain.WaitResolution, cause string, by uuid.UUID) (*domain.TaskWait, bool, error) {
	if r.w.ResolvedAt != nil {
		cp := *r.w
		return &cp, false, nil
	}
	now := time.Now()
	r.w.ResolvedAt, r.w.Resolution, r.w.ResolveCause, r.w.ResolvedBy = &now, &res, &cause, &by
	r.resolved = append(r.resolved, string(res)+":"+cause)
	cp := *r.w
	return &cp, true, nil
}

func TestTypedOutcomeService_ResolveWait(t *testing.T) {
	setup := func(controller uuid.UUID) (TypedOutcomeService, *waitRepoStub) {
		repo := &waitRepoStub{ws: uuid.New(), w: &domain.TaskWait{ID: uuid.New(), TaskID: uuid.New(), AgentID: uuid.New(), Kind: domain.TaskWaitMR}}
		ctl := func(_ context.Context, ws uuid.UUID, by WaitResolver) (bool, error) {
			return ws == repo.ws && by.AgentID == controller && by.AuthWorkspaceID == ws, nil
		}
		return NewTypedOutcomeService(repo, nil, WithControllerCheck(ctl)), repo
	}
	ctx := context.Background()

	t.Run("own agent resolves, a repeat is idempotent, another resolution conflicts", func(t *testing.T) {
		svc, repo := setup(uuid.Nil)
		by := WaitResolver{AgentID: repo.w.AgentID}
		w, resolved, err := svc.ResolveWait(ctx, repo.w.TaskID, repo.w.ID, by, ResolveWaitInput{Cause: " MR merged at abc "})
		require.NoError(t, err)
		assert.True(t, resolved)
		assert.Equal(t, domain.WaitResolutionMet, *w.Resolution, "met is the default")
		assert.Equal(t, "MR merged at abc", *w.ResolveCause)
		assert.Equal(t, by.AgentID, *w.ResolvedBy)

		w, resolved, err = svc.ResolveWait(ctx, repo.w.TaskID, repo.w.ID, by, ResolveWaitInput{Resolution: "met", Cause: "again"})
		require.NoError(t, err)
		assert.False(t, resolved)
		assert.Equal(t, "MR merged at abc", *w.ResolveCause, "a repeat does not overwrite the cause")
		assert.Len(t, repo.resolved, 1)

		_, _, err = svc.ResolveWait(ctx, repo.w.TaskID, repo.w.ID, by, ResolveWaitInput{Resolution: "invalid", Cause: "gone"})
		assert.True(t, isAPI(409, err), "%v", err)
	})

	t.Run("controller of the workspace resolves another agent's wait", func(t *testing.T) {
		ctl := uuid.New()
		svc, repo := setup(ctl)
		_, resolved, err := svc.ResolveWait(ctx, repo.w.TaskID, repo.w.ID, WaitResolver{AgentID: ctl, AuthWorkspaceID: repo.ws},
			ResolveWaitInput{Resolution: "expired", Cause: "registration expired"})
		require.NoError(t, err)
		assert.True(t, resolved)
		assert.Equal(t, []string{"expired:registration expired"}, repo.resolved)
	})

	refused := []struct {
		name   string
		by     func(repo *waitRepoStub, ctl uuid.UUID) WaitResolver
		in     ResolveWaitInput
		waitID func(repo *waitRepoStub) uuid.UUID
		status int
	}{
		{"a person", func(*waitRepoStub, uuid.UUID) WaitResolver { return WaitResolver{} }, ResolveWaitInput{Cause: "x"}, nil, 403},
		{"another agent", func(*waitRepoStub, uuid.UUID) WaitResolver { return WaitResolver{AgentID: uuid.New()} }, ResolveWaitInput{Cause: "x"}, nil, 403},
		{"controller key of another workspace", func(_ *waitRepoStub, ctl uuid.UUID) WaitResolver {
			return WaitResolver{AgentID: ctl, AuthWorkspaceID: uuid.New()}
		}, ResolveWaitInput{Cause: "x"}, nil, 403},
		{"superseded is not the resolver's", func(r *waitRepoStub, _ uuid.UUID) WaitResolver { return WaitResolver{AgentID: r.w.AgentID} },
			ResolveWaitInput{Resolution: "superseded", Cause: "x"}, nil, 400},
		{"no cause", func(r *waitRepoStub, _ uuid.UUID) WaitResolver { return WaitResolver{AgentID: r.w.AgentID} }, ResolveWaitInput{}, nil, 400},
		{"cause too long", func(r *waitRepoStub, _ uuid.UUID) WaitResolver { return WaitResolver{AgentID: r.w.AgentID} },
			ResolveWaitInput{Cause: string(make([]byte, 201))}, nil, 400},
		{"unknown wait", func(r *waitRepoStub, _ uuid.UUID) WaitResolver { return WaitResolver{AgentID: r.w.AgentID} },
			ResolveWaitInput{Cause: "x"}, func(*waitRepoStub) uuid.UUID { return uuid.New() }, 404},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			ctl := uuid.New()
			svc, repo := setup(ctl)
			id := repo.w.ID
			if tc.waitID != nil {
				id = tc.waitID(repo)
			}
			_, _, err := svc.ResolveWait(ctx, repo.w.TaskID, id, tc.by(repo, ctl), tc.in)
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, tc.status, ae.Code, ae.Message)
			assert.Empty(t, repo.resolved)
		})
	}

	t.Run("without a controller check only the own agent resolves", func(t *testing.T) {
		repo := &waitRepoStub{ws: uuid.New(), w: &domain.TaskWait{ID: uuid.New(), TaskID: uuid.New(), AgentID: uuid.New()}}
		svc := NewTypedOutcomeService(repo, nil)
		_, _, err := svc.ResolveWait(ctx, repo.w.TaskID, repo.w.ID, WaitResolver{AgentID: uuid.New(), AuthWorkspaceID: repo.ws}, ResolveWaitInput{Cause: "x"})
		assert.True(t, isAPI(403, err))
	})
}

func TestTypedOutcomeService_ResolveWaitLookupFailures(t *testing.T) {
	boom := errors.New("db down")
	ctx := context.Background()
	repo := &waitRepoStub{ws: uuid.New(), w: &domain.TaskWait{ID: uuid.New(), TaskID: uuid.New(), AgentID: uuid.New()}}

	repo.getErr = boom
	_, _, err := NewTypedOutcomeService(repo, nil).ResolveWait(ctx, repo.w.TaskID, repo.w.ID, WaitResolver{AgentID: repo.w.AgentID}, ResolveWaitInput{Cause: "x"})
	assert.ErrorIs(t, err, boom)

	repo.getErr = nil
	failing := WithControllerCheck(func(context.Context, uuid.UUID, WaitResolver) (bool, error) { return false, boom })
	_, _, err = NewTypedOutcomeService(repo, nil, failing).ResolveWait(ctx, repo.w.TaskID, repo.w.ID, WaitResolver{AgentID: uuid.New()}, ResolveWaitInput{Cause: "x"})
	assert.ErrorIs(t, err, boom, "a failed controller check is an error, never a grant")
	assert.Empty(t, repo.resolved)
}
