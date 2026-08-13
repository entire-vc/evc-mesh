package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// fakeTaskTemplateRepo is a minimal in-memory TaskTemplateRepository double.
// Update mirrors the postgres implementation's merge-then-persist shape (fetch,
// apply non-nil fields, save) so the service-level test exercises the same
// "what actually ends up on the row" question the real repo answers.
type fakeTaskTemplateRepo struct {
	byID map[uuid.UUID]*domain.TaskTemplate
	// getByIDErr, when set, makes GetByID fail regardless of whether the id
	// exists — exercises Update's own "could not read back the existing
	// template" branch, distinct from the tenancy funnel's refusal.
	getByIDErr error
}

func newFakeTaskTemplateRepo() *fakeTaskTemplateRepo {
	return &fakeTaskTemplateRepo{byID: map[uuid.UUID]*domain.TaskTemplate{}}
}

func (r *fakeTaskTemplateRepo) Create(_ context.Context, tmpl *domain.TaskTemplate) error {
	cp := *tmpl
	r.byID[tmpl.ID] = &cp
	return nil
}

func (r *fakeTaskTemplateRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.TaskTemplate, error) {
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	tmpl, ok := r.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *tmpl
	return &cp, nil
}

func (r *fakeTaskTemplateRepo) List(_ context.Context, projectID uuid.UUID) ([]domain.TaskTemplate, error) {
	var out []domain.TaskTemplate
	for _, t := range r.byID {
		if t.ProjectID == projectID {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (r *fakeTaskTemplateRepo) Update(_ context.Context, id uuid.UUID, input domain.UpdateTemplateInput) (*domain.TaskTemplate, error) {
	tmpl, ok := r.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	if input.Name != nil {
		tmpl.Name = *input.Name
	}
	if input.TitleTemplate != nil {
		tmpl.TitleTemplate = *input.TitleTemplate
	}
	if input.Labels != nil {
		tmpl.Labels = pq.StringArray(*input.Labels)
	}
	if input.AssigneeID != nil {
		tmpl.AssigneeID = input.AssigneeID
	}
	if input.AssigneeType != nil {
		tmpl.AssigneeType = input.AssigneeType
	}
	cp := *tmpl
	return &cp, nil
}

func (r *fakeTaskTemplateRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.byID, id)
	return nil
}

// spyValidatingTaskService wraps StubTaskService and records every
// ValidateAssigneeForProject call — these tests care about WHETHER the guard
// funnel was invoked and WHAT it decided, not about task creation.
type spyValidatingTaskService struct {
	*StubTaskService
	calls []struct {
		projectID    uuid.UUID
		assigneeID   *uuid.UUID
		assigneeType domain.AssigneeType
	}
}

func newSpyValidatingTaskService() *spyValidatingTaskService {
	return &spyValidatingTaskService{StubTaskService: NewStubTaskService()}
}

func (s *spyValidatingTaskService) ValidateAssigneeForProject(ctx context.Context, projectID uuid.UUID, assigneeID *uuid.UUID, assigneeType domain.AssigneeType) (domain.AssigneeType, error) {
	s.calls = append(s.calls, struct {
		projectID    uuid.UUID
		assigneeID   *uuid.UUID
		assigneeType domain.AssigneeType
	}{projectID, assigneeID, assigneeType})
	return s.StubTaskService.ValidateAssigneeForProject(ctx, projectID, assigneeID, assigneeType)
}

// TestTaskTemplateService_Create_CallsTheTenancyFunnel proves Create asks
// taskSvc.ValidateAssigneeForProject before persisting — the wiring the
// integration test's negative control (pre-fix: 201 with the foreign assignee
// stored) exists because this call was missing entirely.
func TestTaskTemplateService_Create_RefusesWhenFunnelRefuses(t *testing.T) {
	spy := newSpyValidatingTaskService()
	spy.validateAssigneeErr = &AssigneeNotInWorkspaceError{Reason: "agent belongs to a different workspace"}
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, spy)

	foreignAgent := uuid.New()
	projectID := uuid.New()
	agentType := domain.AssigneeTypeAgent

	_, err := svc.Create(context.Background(), domain.CreateTemplateInput{
		ProjectID: projectID, TitleTemplate: "x", AssigneeID: &foreignAgent, AssigneeType: &agentType,
	})

	var refused *AssigneeNotInWorkspaceError
	if !errors.As(err, &refused) {
		t.Fatalf("Create() error = %v, want *AssigneeNotInWorkspaceError", err)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("ValidateAssigneeForProject called %d times, want 1", len(spy.calls))
	}
	if repo.byID[uuid.Nil] != nil || len(repo.byID) != 0 {
		t.Fatalf("refused Create must not persist a row, found %d", len(repo.byID))
	}
}

func TestTaskTemplateService_Create_NativeAssigneeStillPersists(t *testing.T) {
	spy := newSpyValidatingTaskService() // no validateAssigneeErr set: funnel says OK
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, spy)

	nativeAgent := uuid.New()
	projectID := uuid.New()
	agentType := domain.AssigneeTypeAgent

	tmpl, err := svc.Create(context.Background(), domain.CreateTemplateInput{
		ProjectID: projectID, TitleTemplate: "x", AssigneeID: &nativeAgent, AssigneeType: &agentType,
	})
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}
	if tmpl.AssigneeID == nil || *tmpl.AssigneeID != nativeAgent {
		t.Fatalf("Create() assignee_id = %v, want %v", tmpl.AssigneeID, nativeAgent)
	}
	if len(spy.calls) != 1 || spy.calls[0].projectID != projectID {
		t.Fatalf("ValidateAssigneeForProject not called with the template's project: calls=%v", spy.calls)
	}
}

func TestTaskTemplateService_Create_NoAssignee_SkipsTheFunnel(t *testing.T) {
	spy := newSpyValidatingTaskService()
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, spy)

	_, err := svc.Create(context.Background(), domain.CreateTemplateInput{ProjectID: uuid.New(), TitleTemplate: "x"})
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("ValidateAssigneeForProject called %d times for an unassigned template, want 0", len(spy.calls))
	}
}

// TestTaskTemplateService_Update_RefusesWhenFunnelRefuses covers the PATCH path
// — the negative control's second write path, distinct from Create because
// Update has to read the existing row back to learn which project (and
// therefore which workspace) to validate against.
func TestTaskTemplateService_Update_RefusesWhenFunnelRefuses(t *testing.T) {
	spy := newSpyValidatingTaskService()
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, spy)

	projectID := uuid.New()
	created, err := svc.Create(context.Background(), domain.CreateTemplateInput{ProjectID: projectID, TitleTemplate: "x"})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	spy.validateAssigneeErr = &AssigneeNotInWorkspaceError{Reason: "agent belongs to a different workspace"}
	foreignAgent := uuid.New()
	agentType := domain.AssigneeTypeAgent

	_, err = svc.Update(context.Background(), created.ID, domain.UpdateTemplateInput{
		AssigneeID: &foreignAgent, AssigneeType: &agentType,
	})

	var refused *AssigneeNotInWorkspaceError
	if !errors.As(err, &refused) {
		t.Fatalf("Update() error = %v, want *AssigneeNotInWorkspaceError", err)
	}
	stored := repo.byID[created.ID]
	if stored.AssigneeID != nil {
		t.Fatalf("refused Update must not leave the foreign assignee on the row, got %v", stored.AssigneeID)
	}
	// The funnel must have been asked about THIS template's actual project,
	// not a zero value — Update has no ProjectID on its input, so this is the
	// one place a wrong read-back would silently validate against nothing.
	if len(spy.calls) != 1 || spy.calls[0].projectID != projectID {
		t.Fatalf("ValidateAssigneeForProject not called with the existing template's project: calls=%v, want project=%v", spy.calls, projectID)
	}
}

func TestTaskTemplateService_Update_UnrelatedFieldSkipsTheFunnel(t *testing.T) {
	spy := newSpyValidatingTaskService()
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, spy)

	created, err := svc.Create(context.Background(), domain.CreateTemplateInput{ProjectID: uuid.New(), TitleTemplate: "x"})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	newTitle := "renamed"
	_, err = svc.Update(context.Background(), created.ID, domain.UpdateTemplateInput{TitleTemplate: &newTitle})
	if err != nil {
		t.Fatalf("Update() unexpected error: %v", err)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("ValidateAssigneeForProject called %d times for a PATCH that never touches assignee_id, want 0", len(spy.calls))
	}
}

// TestTaskTemplateService_Update_NativeAssigneeSucceeds_RealTaskService is the
// success-path counterpart to TestTaskTemplateService_Update_RefusesWhenFunnelRefuses,
// run against the REAL taskService (setupTenancyEnv), not a stub/spy — proving
// the wiring works end to end through the actual resolveAssigneeType +
// assertAssigneeInProjectWorkspace funnel, not just against a double that
// always answers what the test wants.
func TestTaskTemplateService_Update_NativeAssigneeSucceeds_RealTaskService(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, env.svc)

	created, err := svc.Create(context.Background(), domain.CreateTemplateInput{
		ProjectID: env.projectID, TitleTemplate: "x",
	})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	agentType := domain.AssigneeTypeAgent
	updated, err := svc.Update(context.Background(), created.ID, domain.UpdateTemplateInput{
		AssigneeID: &env.nativeAgent, AssigneeType: &agentType,
	})
	if err != nil {
		t.Fatalf("Update() unexpected error for a native-workspace agent: %v", err)
	}
	if updated.AssigneeID == nil || *updated.AssigneeID != env.nativeAgent {
		t.Fatalf("Update() assignee_id = %v, want %v", updated.AssigneeID, env.nativeAgent)
	}
	if updated.AssigneeType == nil || *updated.AssigneeType != domain.AssigneeTypeAgent {
		t.Fatalf("Update() assignee_type = %v, want agent", updated.AssigneeType)
	}
}

// TestTaskTemplateService_Update_PropagatesReadBackFailure covers the read-back
// itself failing — distinct from the tenancy funnel refusing. Without a
// project_id to validate against, Update cannot proceed at all.
func TestTaskTemplateService_Update_PropagatesReadBackFailure(t *testing.T) {
	spy := newSpyValidatingTaskService()
	repo := newFakeTaskTemplateRepo()
	svc := NewTaskTemplateService(repo, spy)

	created, err := svc.Create(context.Background(), domain.CreateTemplateInput{ProjectID: uuid.New(), TitleTemplate: "x"})
	if err != nil {
		t.Fatalf("setup Create() failed: %v", err)
	}

	repo.getByIDErr = errors.New("db unavailable")
	agentType := domain.AssigneeTypeAgent
	someAgent := uuid.New()
	_, err = svc.Update(context.Background(), created.ID, domain.UpdateTemplateInput{
		AssigneeID: &someAgent, AssigneeType: &agentType,
	})
	if err == nil {
		t.Fatal("Update() expected an error when the existing template cannot be read back, got nil")
	}
	if len(spy.calls) != 0 {
		t.Fatalf("ValidateAssigneeForProject called %d times when the read-back failed before it could run, want 0", len(spy.calls))
	}
}
