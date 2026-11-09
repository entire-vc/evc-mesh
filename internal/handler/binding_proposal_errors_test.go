package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type failingProposals struct {
	*memProposals
	createErr, getErr, applyErr, fingerprintErr error
}

func (s *failingProposals) Create(ctx context.Context, p *domain.StoredBindingProposal) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.memProposals.Create(ctx, p)
}

func (s *failingProposals) Get(ctx context.Context, id uuid.UUID) (*domain.StoredBindingProposal, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memProposals.Get(ctx, id)
}

func (s *failingProposals) EdgeStateFingerprint(ctx context.Context, taskID uuid.UUID) (string, error) {
	if s.fingerprintErr != nil {
		return "", s.fingerprintErr
	}
	return s.memProposals.EdgeStateFingerprint(ctx, taskID)
}

func (s *failingProposals) ApplyConfirm(ctx context.Context, id, edge, task, by uuid.UUID, at time.Time, expect string) (*domain.TaskDependency, error) {
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	return s.memProposals.ApplyConfirm(ctx, id, edge, task, by, at, expect)
}

const validProposeBody = `{"new_condition":"contract_ready","sources":["comment:abc"],"scope":"edge","base_generation":"g"}`

func (b *bindingEnv) deps() *MockTaskDependencyService {
	return b.h.depService.(*MockTaskDependencyService)
}

func (b *bindingEnv) failing() *failingProposals {
	f := &failingProposals{memProposals: b.store()}
	b.h.WithBindingProposals(f)
	return f
}

func TestProposeBinding_ErrorPaths(t *testing.T) {
	boom := errors.New("boom")
	t.Run("invalid ids", func(t *testing.T) {
		b := newBindingEnv(t)
		rec := b.call(t, b.other, b.h.ProposeBinding, validProposeBody, "nope", b.edge.String())
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("store not wired", func(t *testing.T) {
		b := newBindingEnv(t)
		b.h.proposals = nil
		rec := b.call(t, b.other, b.h.ProposeBinding, validProposeBody, b.task.String(), b.edge.String())
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
	t.Run("malformed body", func(t *testing.T) {
		b := newBindingEnv(t)
		rec := b.call(t, b.other, b.h.ProposeBinding, `{not json`, b.task.String(), b.edge.String())
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("edge lookup fails", func(t *testing.T) {
		b := newBindingEnv(t)
		b.deps().ListByTaskFunc = func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) { return nil, boom }
		rec := b.call(t, b.other, b.h.ProposeBinding, validProposeBody, b.task.String(), b.edge.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
	t.Run("edge not on task", func(t *testing.T) {
		b := newBindingEnv(t)
		rec := b.call(t, b.other, b.h.ProposeBinding, validProposeBody, b.task.String(), uuid.NewString())
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("non-blocks edge has no binding", func(t *testing.T) {
		b := newBindingEnv(t)
		b.deps().ListByTaskFunc = func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
			return []domain.TaskDependency{{ID: b.edge, TaskID: b.task, DependencyType: domain.DependencyTypeRelatesTo}}, nil
		}
		rec := b.call(t, b.other, b.h.ProposeBinding, validProposeBody, b.task.String(), b.edge.String())
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})
	t.Run("store create fails", func(t *testing.T) {
		b := newBindingEnv(t)
		b.failing().createErr = boom
		rec := b.call(t, b.other, b.h.ProposeBinding, validProposeBody, b.task.String(), b.edge.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestConfirmBinding_ErrorPaths(t *testing.T) {
	boom := errors.New("boom")
	confirm := func(b *bindingEnv, actor uuid.UUID, id string) int {
		return b.call(t, actor, b.h.ConfirmBinding, "", b.task.String(), b.edge.String(), id).Code
	}
	t.Run("invalid ids", func(t *testing.T) {
		b := newBindingEnv(t)
		rec := b.call(t, b.owner, b.h.ConfirmBinding, "", b.task.String(), b.edge.String(), "nope")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("services not wired", func(t *testing.T) {
		b := newBindingEnv(t)
		b.h.taskService = nil
		assert.Equal(t, http.StatusServiceUnavailable, confirm(b, b.owner, uuid.NewString()))
	})
	t.Run("proposal lookup fails", func(t *testing.T) {
		b := newBindingEnv(t)
		b.failing().getErr = boom
		assert.Equal(t, http.StatusInternalServerError, confirm(b, b.owner, uuid.NewString()))
	})
	t.Run("unknown proposal", func(t *testing.T) {
		b := newBindingEnv(t)
		assert.Equal(t, http.StatusNotFound, confirm(b, b.owner, uuid.NewString()))
	})
	t.Run("proposal of another edge", func(t *testing.T) {
		b := newBindingEnv(t)
		id := b.propose(t, b.generation(t))
		rec := b.call(t, b.owner, b.h.ConfirmBinding, "", b.task.String(), uuid.NewString(), id)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("task lookup fails", func(t *testing.T) {
		b := newBindingEnv(t)
		id := b.propose(t, b.generation(t))
		b.h.taskService = &MockTaskService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Task, error) { return nil, boom }}
		assert.Equal(t, http.StatusInternalServerError, confirm(b, b.owner, id))
	})
	t.Run("edge lookup fails", func(t *testing.T) {
		b := newBindingEnv(t)
		id := b.propose(t, b.generation(t))
		b.deps().ListByTaskFunc = func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) { return nil, boom }
		assert.Equal(t, http.StatusInternalServerError, confirm(b, b.owner, id))
	})
	t.Run("task missing", func(t *testing.T) {
		b := newBindingEnv(t)
		id := b.propose(t, b.generation(t))
		b.h.taskService = &MockTaskService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Task, error) { return nil, nil }}
		assert.Equal(t, http.StatusNotFound, confirm(b, b.owner, id))
	})
	t.Run("projection fails", func(t *testing.T) {
		b := newBindingEnv(t)
		id := b.propose(t, b.generation(t))
		b.deps().ListByTaskBothDirectionsFunc = func(context.Context, uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
			return nil, nil, boom
		}
		assert.Equal(t, http.StatusInternalServerError, confirm(b, b.owner, id))
	})
	t.Run("edge condition moved since proposal", func(t *testing.T) {
		b := newBindingEnv(t)
		id := b.propose(t, b.generation(t))
		b.cond = domain.DependencyConditionShipped
		assert.Equal(t, http.StatusConflict, confirm(b, b.owner, id))
		assert.Nil(t, b.updated)
	})
	t.Run("fingerprint read fails", func(t *testing.T) {
		b := newBindingEnv(t)
		f := b.failing()
		id := b.propose(t, b.generation(t))
		f.fingerprintErr = boom
		assert.Equal(t, http.StatusInternalServerError, confirm(b, b.owner, id))
	})
	t.Run("failed apply leaves edge and pending proposal intact, retry applies once", func(t *testing.T) {
		b := newBindingEnv(t)
		f := b.failing()
		id := b.propose(t, b.generation(t))
		f.applyErr = boom
		assert.Equal(t, http.StatusInternalServerError, confirm(b, b.owner, id))
		assert.Nil(t, b.updated)
		assert.Equal(t, domain.DependencyConditionTaskDone, b.cond)
		assert.False(t, f.m[uuid.MustParse(id)].Applied, "proposal still pending")
		f.applyErr = nil
		assert.Equal(t, http.StatusOK, confirm(b, b.owner, id), "retry succeeds")
		assert.Equal(t, domain.DependencyConditionContractReady, b.cond)
		assert.Equal(t, http.StatusConflict, confirm(b, b.owner, id), "and only once")
	})
	t.Run("owner or gate changed inside the transaction is refused with 403", func(t *testing.T) {
		for _, refusal := range []error{domain.ErrNotOwner, domain.ErrHeldGate} {
			b := newBindingEnv(t)
			f := b.failing()
			id := b.propose(t, b.generation(t))
			f.applyErr = refusal
			assert.Equal(t, http.StatusForbidden, confirm(b, b.owner, id), refusal.Error())
			assert.Nil(t, b.updated)
			assert.False(t, f.m[uuid.MustParse(id)].Applied)
		}
	})
	t.Run("concurrent change after the generation check is not overwritten", func(t *testing.T) {
		b := newBindingEnv(t)
		f := b.failing()
		id := b.propose(t, b.generation(t))
		f.afterRead = func() { b.cond = domain.DependencyConditionShipped; f.afterRead = nil }
		assert.Equal(t, http.StatusConflict, confirm(b, b.owner, id))
		assert.Nil(t, b.updated)
		assert.Equal(t, domain.DependencyConditionShipped, b.cond, "the concurrent write survives")
		assert.False(t, f.m[uuid.MustParse(id)].Applied, "proposal not consumed")
	})
}

func TestConfirmStatus_Mapping(t *testing.T) {
	assert.Equal(t, http.StatusConflict, confirmStatus(domain.ErrStaleGeneration))
	assert.Equal(t, http.StatusConflict, confirmStatus(domain.ErrReplay))
	assert.Equal(t, http.StatusForbidden, confirmStatus(domain.ErrNotOwner))
	assert.Equal(t, http.StatusForbidden, confirmStatus(domain.ErrAutomatedActor))
	assert.Equal(t, http.StatusForbidden, confirmStatus(domain.ErrHeldGate))
	assert.Equal(t, http.StatusUnprocessableEntity, confirmStatus(errors.New("other")))
}

func TestNextAction_ErrorPaths(t *testing.T) {
	b := newBindingEnv(t)
	rec := b.call(t, b.owner, b.h.NextAction, "", "nope")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	b.deps().ListByTaskBothDirectionsFunc = func(context.Context, uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
		return nil, nil, errors.New("boom")
	}
	rec = b.call(t, b.owner, b.h.NextAction, "", b.task.String())
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
