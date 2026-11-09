package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// memProposals models the transactional store contract: ApplyConfirm either
// consumes the proposal AND writes the edge, or changes nothing.
type memProposals struct {
	m map[uuid.UUID]*domain.StoredBindingProposal
	// edge state seen by the "transaction" and the write it performs
	state     func() string
	write     func(domain.DependencyCondition) *domain.TaskDependency
	edgeCond  func() domain.DependencyCondition
	afterRead func() // simulates a concurrent change right after the fingerprint read
}

func (s *memProposals) Create(_ context.Context, p *domain.StoredBindingProposal) error {
	s.m[uuid.MustParse(p.ID)] = p
	return nil
}
func (s *memProposals) Get(_ context.Context, id uuid.UUID) (*domain.StoredBindingProposal, error) {
	p := s.m[id]
	if p == nil {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}
func (s *memProposals) EdgeStateFingerprint(context.Context, uuid.UUID) (string, error) {
	fp := s.state()
	if s.afterRead != nil {
		s.afterRead()
	}
	return fp, nil
}
func (s *memProposals) ApplyConfirm(_ context.Context, id, _, _, _ uuid.UUID, _ time.Time, expect string) (*domain.TaskDependency, error) {
	p := s.m[id]
	if p == nil {
		return nil, apierror.NotFound("Binding proposal")
	}
	if p.Applied {
		return nil, domain.ErrReplay
	}
	if expect == "" || expect != s.state() || s.edgeCond() != p.Old {
		return nil, domain.ErrStaleGeneration
	}
	p.Applied = true
	return s.write(p.New), nil
}

type bindingEnv struct {
	h                                 *DependencyHandler
	e                                 *echo.Echo
	task, edge, owner, other, blocker uuid.UUID
	cond                              domain.DependencyCondition
	gate                              bool
	updated                           *domain.DependencyCondition
}

func newBindingEnv(t *testing.T) *bindingEnv {
	t.Helper()
	b := &bindingEnv{task: uuid.New(), edge: uuid.New(), owner: uuid.New(), other: uuid.New(), blocker: uuid.New(), cond: domain.DependencyConditionTaskDone}
	svc := &MockTaskDependencyService{}
	svc.ListByTaskFunc = func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
		return []domain.TaskDependency{{ID: b.edge, TaskID: b.task, DependsOnTaskID: b.blocker, DependencyType: domain.DependencyTypeBlocks, Condition: b.cond}}, nil
	}
	svc.ListByTaskBothDirectionsFunc = func(ctx context.Context, id uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
		deps, _ := svc.ListByTaskFunc(ctx, id)
		return []domain.EnrichedTaskDependency{{TaskDependency: deps[0]}}, nil, nil
	}
	svc.UpdateConditionFunc = func(_ context.Context, _ uuid.UUID, in service.UpdateDependencyConditionInput) (*domain.TaskDependency, error) {
		b.updated = in.Condition
		b.cond = *in.Condition
		return &domain.TaskDependency{ID: b.edge, Condition: *in.Condition}, nil
	}
	ts := &MockTaskService{GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Task, error) {
		return &domain.Task{ID: b.task, AssigneeID: &b.owner, HumanGate: b.gate}, nil
	}}
	b.h = NewDependencyHandler(svc, ts).WithBindingProposals(b.store())
	b.e = echo.New()
	return b
}

func (b *bindingEnv) store() *memProposals {
	return &memProposals{
		m:        map[uuid.UUID]*domain.StoredBindingProposal{},
		state:    func() string { return string(b.cond) },
		edgeCond: func() domain.DependencyCondition { return b.cond },
		write: func(c domain.DependencyCondition) *domain.TaskDependency {
			b.updated = &c
			b.cond = c
			return &domain.TaskDependency{ID: b.edge, Condition: c}
		},
	}
}

func (b *bindingEnv) call(t *testing.T, actor uuid.UUID, fn func(echo.Context) error, body string, params ...string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := b.e.NewContext(req, rec)
	c.Set(middleware.ContextKeyAgentID, actor)
	names := []string{"task_id", "dep_id", "proposal_id"}[:len(params)]
	c.SetParamNames(names...)
	c.SetParamValues(params...)
	require.NoError(t, fn(c))
	return rec
}

func (b *bindingEnv) propose(t *testing.T, gen string) string {
	body := `{"new_condition":"contract_ready","sources":["comment:abc"],"scope":"edge","base_generation":"` + gen + `"}`
	rec := b.call(t, b.other, b.h.ProposeBinding, body, b.task.String(), b.edge.String())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var p domain.StoredBindingProposal
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	return p.ID
}

func (b *bindingEnv) generation(t *testing.T) string {
	p, err := b.h.projection(b.e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder()), b.task)
	require.NoError(t, err)
	return p.Generation
}

func TestBindingProposal_ConfirmFlow(t *testing.T) {
	b := newBindingEnv(t)
	id := b.propose(t, b.generation(t))
	confirm := func(actor uuid.UUID) *httptest.ResponseRecorder {
		return b.call(t, actor, b.h.ConfirmBinding, "", b.task.String(), b.edge.String(), id)
	}
	assert.Equal(t, http.StatusForbidden, confirm(b.other).Code, "non-owner proposer cannot confirm")
	assert.Nil(t, b.updated)
	b.gate = true
	assert.Equal(t, http.StatusForbidden, confirm(b.owner).Code, "held human gate refuses")
	b.gate = false
	rec := confirm(b.owner)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, b.updated)
	assert.Equal(t, domain.DependencyConditionContractReady, *b.updated)
	assert.Equal(t, http.StatusConflict, confirm(b.owner).Code, "replay refused")
}

func TestBindingProposal_OwnerCannotConfirmOwnProposal_ButStaleRefused(t *testing.T) {
	b := newBindingEnv(t)
	id := b.propose(t, "stale-generation")
	rec := b.call(t, b.owner, b.h.ConfirmBinding, "", b.task.String(), b.edge.String(), id)
	assert.Equal(t, http.StatusConflict, rec.Code, "stale generation (CAS) refused")
	assert.Nil(t, b.updated)
	// empty base generation is not a valid proposal
	rec = b.call(t, b.other, b.h.ProposeBinding, `{"new_condition":"contract_ready","sources":["x"],"scope":"edge","base_generation":""}`, b.task.String(), b.edge.String())
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestBindingProposal_ProposerCannotSelfConfirm(t *testing.T) {
	b := newBindingEnv(t)
	b.other = b.owner // owner proposes, then tries to confirm their own suggestion
	id := b.propose(t, b.generation(t))
	rec := b.call(t, b.owner, b.h.ConfirmBinding, "", b.task.String(), b.edge.String(), id)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Nil(t, b.updated)
}
