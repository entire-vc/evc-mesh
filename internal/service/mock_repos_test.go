package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	pgRepo "github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// MockWorkspaceRepository
// ---------------------------------------------------------------------------

type MockWorkspaceRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Workspace
	memberships map[uuid.UUID]map[uuid.UUID]bool // workspaceID -> set of userIDs
	errToReturn error
}

func NewMockWorkspaceRepository() *MockWorkspaceRepository {
	return &MockWorkspaceRepository{
		items:       make(map[uuid.UUID]*domain.Workspace),
		memberships: make(map[uuid.UUID]map[uuid.UUID]bool),
	}
}

// AddMember records a workspace_members row for ListForUser.
func (m *MockWorkspaceRepository) AddMember(workspaceID, userID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.memberships[workspaceID] == nil {
		m.memberships[workspaceID] = make(map[uuid.UUID]bool)
	}
	m.memberships[workspaceID][userID] = true
}

// RemoveMember drops a workspace_members row.
func (m *MockWorkspaceRepository) RemoveMember(workspaceID, userID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.memberships[workspaceID], userID)
}

func (m *MockWorkspaceRepository) Create(_ context.Context, ws *domain.Workspace) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[ws.ID] = ws
	return nil
}

func (m *MockWorkspaceRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ws, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return ws, nil
}

func (m *MockWorkspaceRepository) GetBySlug(_ context.Context, slug string) (*domain.Workspace, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ws := range m.items {
		if ws.Slug == slug {
			return ws, nil
		}
	}
	return nil, nil
}

func (m *MockWorkspaceRepository) Update(_ context.Context, ws *domain.Workspace) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[ws.ID] = ws
	return nil
}

func (m *MockWorkspaceRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockWorkspaceRepository) ListByOwner(_ context.Context, ownerID uuid.UUID) ([]domain.Workspace, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Workspace
	for _, ws := range m.items {
		if ws.OwnerID == ownerID {
			result = append(result, *ws)
		}
	}
	return result, nil
}

// ListForUser mirrors the SQL predicate: owner OR member, de-duplicated,
// ordered by created_at then id.
func (m *MockWorkspaceRepository) ListForUser(_ context.Context, userID uuid.UUID) ([]domain.Workspace, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Workspace
	for id, ws := range m.items {
		if ws.OwnerID == userID || m.memberships[id][userID] {
			result = append(result, *ws)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID.String() < result[j].ID.String()
	})
	return result, nil
}

// ---------------------------------------------------------------------------
// MockProjectRepository
// ---------------------------------------------------------------------------

type MockProjectRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Project
	errToReturn error
	// defaultWorkspace makes an unseeded project id resolve to one tenant instead
	// of to "no such project". Most service tests never create a project row and
	// only care that SOME workspace exists behind the id; the assignee tenancy
	// guard, which refuses when it cannot resolve one, would otherwise fail all of
	// them for a reason none of them are about. A test that needs a SECOND tenant
	// seeds a real project with its own workspace_id, and a test that needs "the
	// project is missing" leaves this unset.
	defaultWorkspace uuid.UUID
}

// WithDefaultWorkspace declares the tenant that unseeded project ids belong to.
func (m *MockProjectRepository) WithDefaultWorkspace(wsID uuid.UUID) *MockProjectRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultWorkspace = wsID
	return m
}

func NewMockProjectRepository() *MockProjectRepository {
	// NOT single-tenant by default: several tests depend on an unseeded id
	// resolving to "no such project" (TestMoveToProject_WithoutProjectRepoFailsClosed,
	// TestProjectService_GetByID). Opt in with WithDefaultWorkspace where a test
	// needs every id to resolve.
	return &MockProjectRepository{items: make(map[uuid.UUID]*domain.Project)}
}

func (m *MockProjectRepository) Create(_ context.Context, p *domain.Project) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[p.ID] = p
	return nil
}

func (m *MockProjectRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.items[id]
	if !ok {
		if m.defaultWorkspace != uuid.Nil {
			return &domain.Project{ID: id, WorkspaceID: m.defaultWorkspace}, nil
		}
		return nil, nil
	}
	// A project seeded without a workspace_id belongs to the default tenant, the
	// same rule MockAgentRepository applies to agents — otherwise the two mocks
	// disagree about who the default tenant is and every seeded project looks
	// foreign to every unseeded agent.
	if p.WorkspaceID == uuid.Nil && m.defaultWorkspace != uuid.Nil {
		clone := *p
		clone.WorkspaceID = m.defaultWorkspace
		return &clone, nil
	}
	return p, nil
}

func (m *MockProjectRepository) GetBySlug(_ context.Context, workspaceID uuid.UUID, slug string) (*domain.Project, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.items {
		if p.WorkspaceID == workspaceID && p.Slug == slug {
			return p, nil
		}
	}
	return nil, nil
}

func (m *MockProjectRepository) Update(_ context.Context, p *domain.Project) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[p.ID] = p
	return nil
}

func (m *MockProjectRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockProjectRepository) List(_ context.Context, workspaceID uuid.UUID, _ repository.ProjectFilter, pg pagination.Params) (*pagination.Page[domain.Project], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.Project
	for _, p := range m.items {
		if p.WorkspaceID == workspaceID {
			all = append(all, *p)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

// ---------------------------------------------------------------------------
// MockTaskRepository
// ---------------------------------------------------------------------------

type MockTaskRepository struct {
	// LastListByAssigneeWorkspace records the workspace argument of the most recent
	// ListByAssignee call, so a caller can be checked for scoping its own feed read.
	LastListByAssigneeWorkspace uuid.UUID
	// LastListByAssigneeFilter records the filter argument of the most recent
	// ListByAssignee call, so a caller can be checked for pushing its narrowing
	// down to the repository instead of doing it in Go afterwards.
	LastListByAssigneeFilter repository.AssigneeTaskFilter
	mu                       sync.RWMutex
	items                    map[uuid.UUID]*domain.Task
	errToReturn              error
	// statusCategoryOf, if set, resolves a status ID to its category — used by
	// FindDueMonitorBacklogTasks to emulate the real query's join against
	// task_statuses without this mock needing a direct dependency on
	// MockTaskStatusRepository. Tests wire it via WithStatusCategoryLookup.
	statusCategoryOf func(statusID uuid.UUID) domain.StatusCategory
}

func NewMockTaskRepository() *MockTaskRepository {
	return &MockTaskRepository{items: make(map[uuid.UUID]*domain.Task)}
}

// WithStatusCategoryLookup wires a status-category resolver (typically backed by a
// MockTaskStatusRepository seeded in the same test) so status-category-filtered
// mock queries (e.g. FindDueMonitorBacklogTasks) behave like the real SQL join.
func (m *MockTaskRepository) WithStatusCategoryLookup(statusRepo *MockTaskStatusRepository) *MockTaskRepository {
	m.statusCategoryOf = func(statusID uuid.UUID) domain.StatusCategory {
		statusRepo.mu.RLock()
		defer statusRepo.mu.RUnlock()
		if s, ok := statusRepo.items[statusID]; ok {
			return s.Category
		}
		return ""
	}
	return m
}

func (m *MockTaskRepository) Create(_ context.Context, t *domain.Task) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[t.ID] = t
	return nil
}

func (m *MockTaskRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *MockTaskRepository) Update(_ context.Context, t *domain.Task) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[t.ID] = t
	return nil
}

func (m *MockTaskRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockTaskRepository) List(_ context.Context, projectID uuid.UUID, _ repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.Task
	for _, t := range m.items {
		if t.ProjectID == projectID {
			all = append(all, *t)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

// ListByAssignee records the workspace it was scoped with so a caller can be
// checked for passing one, and otherwise filters on the assignee alone: this mock
// holds no project->workspace mapping, so it CANNOT prove the predicate reaches
// SQL. That proof lives in the integration test against a real database
// (TestCrossWorkspaceAssignee_*); asserting scoping here would only assert that
// the mock filters, which is not the claim.
func (m *MockTaskRepository) ListByAssignee(_ context.Context, workspaceID, assigneeID uuid.UUID, assigneeType domain.AssigneeType, filter repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
	if m.errToReturn != nil {
		return nil, 0, m.errToReturn
	}
	m.mu.Lock()
	m.LastListByAssigneeFilter = filter
	m.mu.Unlock()
	m.mu.Lock()
	m.LastListByAssigneeWorkspace = workspaceID
	m.mu.Unlock()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Task
	for _, t := range m.items {
		if t.AssigneeID != nil && *t.AssigneeID == assigneeID && t.AssigneeType == assigneeType {
			result = append(result, *t)
		}
	}
	total := len(result)
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, total, nil
}

func (m *MockTaskRepository) ListByUserActive(_ context.Context, _, _ uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	return pagination.NewPage([]domain.Task{}, 0, pg), nil
}

func (m *MockTaskRepository) ListSubtasks(_ context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Task
	for _, t := range m.items {
		if t.ParentTaskID != nil && *t.ParentTaskID == parentTaskID {
			result = append(result, *t)
		}
	}
	return result, nil
}

func (m *MockTaskRepository) CountByStatus(_ context.Context, projectID uuid.UUID) (map[uuid.UUID]int, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := make(map[uuid.UUID]int)
	for _, t := range m.items {
		if t.ProjectID == projectID {
			counts[t.StatusID]++
		}
	}
	return counts, nil
}

func (m *MockTaskRepository) CountByStatusCategory(_ context.Context, _ uuid.UUID) (map[domain.StatusCategory]int, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	return map[domain.StatusCategory]int{}, nil
}

func (m *MockTaskRepository) ListByStatusCategory(_ context.Context, _ uuid.UUID, _ domain.StatusCategory, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	return pagination.NewPage([]domain.Task{}, 0, pg), nil
}

func (m *MockTaskRepository) AtomicCheckout(_ context.Context, taskID, agentID, token uuid.UUID, expiresAt time.Time) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.items[taskID]
	if !ok {
		return nil
	}
	if t.CheckedOutBy != nil && *t.CheckedOutBy != agentID {
		if t.CheckoutExpires == nil || t.CheckoutExpires.After(timeNow()) {
			return pgRepo.ErrCheckoutConflict
		}
	}
	now := timeNow()
	if t.CheckedOutBy == nil || *t.CheckedOutBy != agentID {
		t.CheckoutAcquiredAt = &now
	}
	t.CheckedOutBy = &agentID
	t.CheckoutToken = &token
	t.CheckoutExpires = &expiresAt
	return nil
}

func (m *MockTaskRepository) ReleaseCheckout(_ context.Context, taskID, _ uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.items[taskID]
	if !ok {
		return nil
	}
	t.CheckedOutBy = nil
	t.CheckoutToken = nil
	t.CheckoutExpires = nil
	t.CheckoutAcquiredAt = nil
	return nil
}

func (m *MockTaskRepository) ExtendCheckout(_ context.Context, taskID, _ uuid.UUID, newExpires time.Time) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.items[taskID]
	if !ok {
		return nil
	}
	t.CheckoutExpires = &newExpires
	return nil
}

func (m *MockTaskRepository) ForceReleaseCheckout(_ context.Context, taskID uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.items[taskID]
	if !ok {
		return nil
	}
	t.CheckedOutBy = nil
	t.CheckoutToken = nil
	t.CheckoutExpires = nil
	t.CheckoutAcquiredAt = nil
	return nil
}

func (m *MockTaskRepository) ReleaseExpiredCheckouts(_ context.Context) (int64, error) {
	if m.errToReturn != nil {
		return 0, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var released int64
	now := time.Now()
	for _, t := range m.items {
		if t.CheckoutExpires == nil || !t.CheckoutExpires.Before(now) {
			continue
		}
		t.CheckedOutBy = nil
		t.CheckoutToken = nil
		t.CheckoutExpires = nil
		t.CheckoutAcquiredAt = nil
		released++
	}
	return released, nil
}

func (m *MockTaskRepository) FindExpiredInProgressCheckouts(_ context.Context) ([]domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []domain.Task
	for _, t := range m.items {
		if t.CheckoutExpires != nil && t.CheckoutExpires.Before(now) {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *MockTaskRepository) FindDueMonitorBacklogTasks(_ context.Context) ([]domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []domain.Task
	for _, t := range m.items {
		if t.DueDate == nil || t.DueDate.After(now) {
			continue
		}
		if !containsInStringArray(t.Labels, monitorLabelKindMonitor) {
			continue
		}
		if m.statusCategoryOf == nil || m.statusCategoryOf(t.StatusID) != domain.StatusCategoryBacklog {
			continue
		}
		out = append(out, *t)
	}
	return out, nil
}

func (m *MockTaskRepository) MoveToProject(_ context.Context, taskID, targetProjectID, targetStatusID uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.items[taskID]
	if !ok {
		return fmt.Errorf("task not found")
	}
	t.ProjectID = targetProjectID
	t.StatusID = targetStatusID
	return nil
}

func (m *MockTaskRepository) GetByShortID(_ context.Context, prefix string) (*domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var matches []*domain.Task
	for _, t := range m.items {
		if strings.HasPrefix(t.ID.String(), strings.ToLower(prefix)) {
			matches = append(matches, t)
		}
	}
	if len(matches) == 0 {
		return nil, apierror.NotFound("Task")
	}
	if len(matches) > 1 {
		return nil, apierror.BadRequest("ambiguous short ID")
	}
	return matches[0], nil
}

func (m *MockTaskRepository) Search(_ context.Context, _ uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var items []domain.Task
	for _, t := range m.items {
		if filter.Search == "" || strings.Contains(strings.ToLower(t.Title), strings.ToLower(filter.Search)) {
			items = append(items, *t)
		}
	}
	return pagination.NewPage(items, len(items), pg), nil
}

func (m *MockTaskRepository) ListOpenByRecurringScheduleID(_ context.Context, scheduleID, exceptTaskID uuid.UUID) ([]domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Task
	for _, t := range m.items {
		if t.RecurringScheduleID != nil && *t.RecurringScheduleID == scheduleID && t.ID != exceptTaskID {
			result = append(result, *t)
		}
	}
	return result, nil
}

func (m *MockTaskRepository) SetHumanGate(_ context.Context, taskID uuid.UUID, value bool) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.items[taskID]; ok {
		t.HumanGate = value
		m.items[taskID] = t
	}
	return nil
}

func (m *MockTaskRepository) SetShipped(_ context.Context, taskID uuid.UUID, value bool) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.items[taskID]; ok {
		t.IsShipped = value
		m.items[taskID] = t
	}
	return nil
}

func (m *MockTaskRepository) SetDodCheck(_ context.Context, taskID uuid.UUID, gateName, status, _ string) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.items[taskID]; ok {
		if t.DodChecks == nil {
			t.DodChecks = domain.DodChecks{}
		}
		t.DodChecks[gateName] = domain.DodCheck{Status: domain.DodCheckStatus(status)}
		m.items[taskID] = t
	}
	return nil
}

// ---------------------------------------------------------------------------
// MockTaskStatusRepository
// ---------------------------------------------------------------------------

type MockTaskStatusRepository struct {
	mu                 sync.RWMutex
	items              map[uuid.UUID]*domain.TaskStatus
	errToReturn        error
	listByProjectCalls int
}

func NewMockTaskStatusRepository() *MockTaskStatusRepository {
	return &MockTaskStatusRepository{items: make(map[uuid.UUID]*domain.TaskStatus)}
}

func (m *MockTaskStatusRepository) Create(_ context.Context, s *domain.TaskStatus) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[s.ID] = s
	return nil
}

func (m *MockTaskStatusRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *MockTaskStatusRepository) Update(_ context.Context, s *domain.TaskStatus) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[s.ID] = s
	return nil
}

func (m *MockTaskStatusRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockTaskStatusRepository) ListByProject(_ context.Context, projectID uuid.UUID) ([]domain.TaskStatus, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.Lock()
	m.listByProjectCalls++
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.TaskStatus
	for _, s := range m.items {
		if s.ProjectID == projectID {
			result = append(result, *s)
		}
	}
	return result, nil
}

func (m *MockTaskStatusRepository) GetDefaultForProject(_ context.Context, projectID uuid.UUID) (*domain.TaskStatus, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.items {
		if s.ProjectID == projectID && s.IsDefault {
			return s, nil
		}
	}
	return nil, nil
}

func (m *MockTaskStatusRepository) Reorder(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	return nil
}

// ---------------------------------------------------------------------------
// MockTaskDependencyRepository
// ---------------------------------------------------------------------------

type MockTaskDependencyRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.TaskDependency
	errToReturn error
}

func NewMockTaskDependencyRepository() *MockTaskDependencyRepository {
	return &MockTaskDependencyRepository{items: make(map[uuid.UUID]*domain.TaskDependency)}
}

func (m *MockTaskDependencyRepository) Create(_ context.Context, dep *domain.TaskDependency) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[dep.ID] = dep
	return nil
}

func (m *MockTaskDependencyRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.TaskDependency, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	dep, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return dep, nil
}

func (m *MockTaskDependencyRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockTaskDependencyRepository) ListByTask(_ context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.TaskDependency
	for _, dep := range m.items {
		if dep.TaskID == taskID {
			result = append(result, *dep)
		}
	}
	return result, nil
}

func (m *MockTaskDependencyRepository) ListDependents(_ context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.TaskDependency
	for _, dep := range m.items {
		if dep.DependsOnTaskID == taskID {
			result = append(result, *dep)
		}
	}
	return result, nil
}

func (m *MockTaskDependencyRepository) Exists(_ context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error) {
	if m.errToReturn != nil {
		return false, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, dep := range m.items {
		if dep.TaskID == taskID && dep.DependsOnTaskID == dependsOnTaskID {
			return true, nil
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// MockCustomFieldDefinitionRepository
// ---------------------------------------------------------------------------

type MockCustomFieldDefinitionRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.CustomFieldDefinition
	errToReturn error
}

func NewMockCustomFieldDefinitionRepository() *MockCustomFieldDefinitionRepository {
	return &MockCustomFieldDefinitionRepository{items: make(map[uuid.UUID]*domain.CustomFieldDefinition)}
}

func (m *MockCustomFieldDefinitionRepository) Create(_ context.Context, f *domain.CustomFieldDefinition) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[f.ID] = f
	return nil
}

func (m *MockCustomFieldDefinitionRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.CustomFieldDefinition, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return f, nil
}

func (m *MockCustomFieldDefinitionRepository) Update(_ context.Context, f *domain.CustomFieldDefinition) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[f.ID] = f
	return nil
}

func (m *MockCustomFieldDefinitionRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockCustomFieldDefinitionRepository) ListByProject(_ context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.CustomFieldDefinition
	for _, f := range m.items {
		if f.ProjectID == projectID {
			result = append(result, *f)
		}
	}
	return result, nil
}

func (m *MockCustomFieldDefinitionRepository) ListVisibleToAgents(_ context.Context, projectID uuid.UUID) ([]domain.CustomFieldDefinition, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.CustomFieldDefinition
	for _, f := range m.items {
		if f.ProjectID == projectID && f.IsVisibleToAgents {
			result = append(result, *f)
		}
	}
	return result, nil
}

func (m *MockCustomFieldDefinitionRepository) Reorder(_ context.Context, _ uuid.UUID, fieldIDs []uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, id := range fieldIDs {
		if f, ok := m.items[id]; ok {
			f.Position = i
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MockCommentRepository
// ---------------------------------------------------------------------------

type MockCommentRepository struct {
	mu                 sync.RWMutex
	items              map[uuid.UUID]*domain.Comment
	errToReturn        error
	enrichedAuthorName *string // simulates SQL CASE WHEN author_name subquery
}

func NewMockCommentRepository() *MockCommentRepository {
	return &MockCommentRepository{items: make(map[uuid.UUID]*domain.Comment)}
}

func (m *MockCommentRepository) Create(_ context.Context, c *domain.Comment) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[c.ID] = c
	return nil
}

func (m *MockCommentRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Comment, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	if m.enrichedAuthorName != nil {
		cp := *c
		cp.AuthorName = m.enrichedAuthorName
		return &cp, nil
	}
	return c, nil
}

func (m *MockCommentRepository) Update(_ context.Context, c *domain.Comment) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[c.ID] = c
	return nil
}

func (m *MockCommentRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockCommentRepository) ListByTask(_ context.Context, taskID uuid.UUID, _ repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.Comment
	for _, c := range m.items {
		if c.TaskID == taskID {
			all = append(all, *c)
		}
	}
	// Real CommentRepo.ListByTask hardcodes ORDER BY created_at ASC regardless of
	// pg.SortDir (see comment_repo.go) — ranging over m.items would return Go's
	// randomized map iteration order instead, which is a different bug for every
	// caller that (correctly, per the real repo) relies on chronological order,
	// not just an inconvenience for one test. Sort here so the mock matches prod.
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	return pagination.NewPage(all, len(all), pg), nil
}

func (m *MockCommentRepository) ListReplies(_ context.Context, parentCommentID uuid.UUID) ([]domain.Comment, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Comment
	for _, c := range m.items {
		if c.ParentCommentID != nil && *c.ParentCommentID == parentCommentID {
			result = append(result, *c)
		}
	}
	return result, nil
}

func (m *MockCommentRepository) ListByAuthor(_ context.Context, _ uuid.UUID, _ repository.CommentViewFilter) ([]domain.CommentView, *domain.CommentCursor, error) {
	return []domain.CommentView{}, nil, m.errToReturn
}

func (m *MockCommentRepository) ListRecentByWorkspace(_ context.Context, _ uuid.UUID, _ repository.CommentViewFilter) ([]domain.CommentView, *domain.CommentCursor, error) {
	return []domain.CommentView{}, nil, m.errToReturn
}

func (m *MockCommentRepository) HasAnyComment(_ context.Context, taskID uuid.UUID) (bool, error) {
	if m.errToReturn != nil {
		return false, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.items {
		if c.TaskID == taskID {
			return true, nil
		}
	}
	return false, nil
}

func (m *MockCommentRepository) HasRecentCommentBy(_ context.Context, taskID, authorID uuid.UUID, since time.Time, minLength int) (bool, error) {
	if m.errToReturn != nil {
		return false, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.items {
		if c.TaskID != taskID || c.AuthorID != authorID {
			continue
		}
		if c.CreatedAt.Before(since) {
			continue
		}
		if len(c.Body) >= minLength {
			return true, nil
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// MockArtifactRepository
// ---------------------------------------------------------------------------

type MockArtifactRepository struct {
	mu    sync.RWMutex
	items map[uuid.UUID]*domain.Artifact
	// workspaceOf maps artifact ID → workspace ID for GetByIDInWorkspace checks.
	// If an entry is absent the mock skips workspace filtering (permissive default).
	workspaceOf map[uuid.UUID]uuid.UUID
	errToReturn error
}

func NewMockArtifactRepository() *MockArtifactRepository {
	return &MockArtifactRepository{
		items:       make(map[uuid.UUID]*domain.Artifact),
		workspaceOf: make(map[uuid.UUID]uuid.UUID),
	}
}

// SetWorkspace records that artifactID belongs to workspaceID.
// Tests that exercise GetByIDInWorkspace isolation should call this after Create.
func (m *MockArtifactRepository) SetWorkspace(artifactID, workspaceID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceOf[artifactID] = workspaceID
}

func (m *MockArtifactRepository) Create(_ context.Context, a *domain.Artifact) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[a.ID] = a
	return nil
}

func (m *MockArtifactRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return a, nil
}

func (m *MockArtifactRepository) GetByIDInWorkspace(_ context.Context, id, workspaceID uuid.UUID) (*domain.Artifact, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	// If workspace mapping is recorded, enforce it.
	if wsID, hasMapping := m.workspaceOf[id]; hasMapping && wsID != workspaceID {
		return nil, nil
	}
	return a, nil
}

func (m *MockArtifactRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockArtifactRepository) UpdateMetadata(_ context.Context, id uuid.UUID, metadata json.RawMessage) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.items[id]
	if !ok {
		return apierror.NotFound("Artifact")
	}
	a.Metadata = metadata
	return nil
}

// MetadataOf returns the stored artifact's metadata under the mock's mutex, so
// tests can read it safely while the service's background relay goroutine
// writes via UpdateMetadata (avoids a data race under -race).
func (m *MockArtifactRepository) MetadataOf(id uuid.UUID) json.RawMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if a, ok := m.items[id]; ok && a != nil {
		return a.Metadata
	}
	return nil
}

func (m *MockArtifactRepository) ListByTask(_ context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Artifact], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.Artifact
	for _, a := range m.items {
		if a.TaskID == taskID {
			all = append(all, *a)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

// ---------------------------------------------------------------------------
// MockAgentRepository
// ---------------------------------------------------------------------------

type MockAgentRepository struct {
	// defaultWorkspace — see WithDefaultWorkspace.
	defaultWorkspace uuid.UUID
	mu               sync.RWMutex
	items            map[uuid.UUID]*domain.Agent
	errToReturn      error
}

func NewMockAgentRepository() *MockAgentRepository {
	// Agents seeded without a workspace_id belong to the single default tenant; an
	// agent seeded WITH one keeps it, which is how a foreign principal is built.
	return &MockAgentRepository{
		items:            make(map[uuid.UUID]*domain.Agent),
		defaultWorkspace: testDefaultWorkspaceID,
	}
}

func (m *MockAgentRepository) Create(_ context.Context, a *domain.Agent) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[a.ID] = a
	return nil
}

// WithDefaultWorkspace declares the tenant that agents seeded WITHOUT an explicit
// workspace_id belong to. An agent seeded WITH one keeps it — that is how a test
// builds the foreign principal the tenancy guard must refuse.
func (m *MockAgentRepository) WithDefaultWorkspace(wsID uuid.UUID) *MockAgentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultWorkspace = wsID
	return m
}

func (m *MockAgentRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Agent, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	if a.WorkspaceID == uuid.Nil && m.defaultWorkspace != uuid.Nil {
		clone := *a
		clone.WorkspaceID = m.defaultWorkspace
		return &clone, nil
	}
	return a, nil
}

// SetAPIKeySHA256 mirrors the real repository's guard: the write only lands
// while the bcrypt hash the caller verified against is still the current one.
func (m *MockAgentRepository) SetAPIKeySHA256(_ context.Context, agentID uuid.UUID, digest, expectedBcryptHash string) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[agentID]; ok && a.APIKeyHash == expectedBcryptHash {
		a.APIKeySHA256 = digest
	}
	return nil
}

func (m *MockAgentRepository) GetByAPIKeyPrefix(_ context.Context, workspaceID uuid.UUID, prefix string) (*domain.Agent, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.items {
		if a.WorkspaceID == workspaceID && a.APIKeyPrefix == prefix {
			return a, nil
		}
	}
	return nil, nil
}

func (m *MockAgentRepository) Update(_ context.Context, a *domain.Agent) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[a.ID] = a
	return nil
}

func (m *MockAgentRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *MockAgentRepository) List(_ context.Context, workspaceID uuid.UUID, _ repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.Agent
	for _, a := range m.items {
		if a.WorkspaceID == workspaceID {
			all = append(all, *a)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

func (m *MockAgentRepository) UpdateHeartbeat(_ context.Context, id uuid.UUID, params *repository.UpdateHeartbeatParams) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[id]; ok {
		now := timeNow()
		a.LastHeartbeat = &now
		a.Status = domain.AgentStatusOnline
		if params != nil {
			a.HeartbeatStatus = params.Status
			a.HeartbeatMessage = params.Message
		}
	}
	return nil
}

func (m *MockAgentRepository) UpdateStatus(_ context.Context, id uuid.UUID, status domain.AgentStatus) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.items[id]; ok {
		a.Status = status
	}
	return nil
}

func (m *MockAgentRepository) GetSubAgentTree(_ context.Context, parentID uuid.UUID) ([]domain.Agent, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Agent
	for _, a := range m.items {
		if a.ParentAgentID != nil && *a.ParentAgentID == parentID {
			result = append(result, *a)
		}
	}
	return result, nil
}

func (m *MockAgentRepository) ListWithProjects(_ context.Context, workspaceID uuid.UUID) ([]repository.AgentWithProjects, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []repository.AgentWithProjects
	for _, a := range m.items {
		if a.WorkspaceID == workspaceID {
			result = append(result, repository.AgentWithProjects{Agent: *a, Projects: []string{}})
		}
	}
	return result, nil
}

func (m *MockAgentRepository) TouchLastSeenBatch(_ context.Context, ids []uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if a, ok := m.items[id]; ok {
			a.LastHeartbeat = &now
		}
	}
	return nil
}

func (m *MockAgentRepository) GetBySlug(_ context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.items {
		if a.WorkspaceID == workspaceID && a.Slug == slug {
			return a, nil
		}
	}
	return nil, nil
}

func (m *MockAgentRepository) SearchByPrefix(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.Agent, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// MockAgentNotifyService — records NotifyAgent calls for assertion in tests.
// ---------------------------------------------------------------------------

type MockAgentNotifyService struct {
	mu    sync.Mutex
	calls []AgentNotification
}

func NewMockAgentNotifyService() *MockAgentNotifyService {
	return &MockAgentNotifyService{}
}

func (m *MockAgentNotifyService) NotifyAgent(_ context.Context, _ uuid.UUID, event AgentNotification) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, event)
}

func (m *MockAgentNotifyService) Calls() []AgentNotification {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AgentNotification, len(m.calls))
	copy(out, m.calls)
	return out
}

// ---------------------------------------------------------------------------
// MockNotificationService — records Notify calls for assertion in tests.
// Preference/read-state methods are unused by task_service tests and return
// zero values.
// ---------------------------------------------------------------------------

type MockNotificationService struct {
	mu    sync.Mutex
	calls []domain.NotificationEvent
	// prefs is a real store, not a nil return: the watch path decides whether to
	// provision a channel by reading what is already there, so a mock that always
	// answers "no preferences" would make every such test pass for the wrong
	// reason.
	prefs   []domain.NotificationPreference
	prefErr error
}

func NewMockNotificationService() *MockNotificationService {
	return &MockNotificationService{}
}

func (m *MockNotificationService) Notify(_ context.Context, event domain.NotificationEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, event)
}

func (m *MockNotificationService) Calls() []domain.NotificationEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.NotificationEvent, len(m.calls))
	copy(out, m.calls)
	return out
}

func (m *MockNotificationService) GetPreferences(_ context.Context, userID uuid.UUID) ([]domain.NotificationPreference, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prefErr != nil {
		return nil, m.prefErr
	}
	var out []domain.NotificationPreference
	for _, p := range m.prefs {
		if p.UserID != nil && *p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

// UpsertPreferences matches the production repository's key — one row per
// (workspace, actor, channel) — because a mock that appended instead would hide
// exactly the duplicate-row bug that key exists to prevent.
func (m *MockNotificationService) UpsertPreferences(_ context.Context, pref *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.prefs {
		p := &m.prefs[i]
		if p.WorkspaceID == pref.WorkspaceID && p.Channel == pref.Channel &&
			p.UserID != nil && pref.UserID != nil && *p.UserID == *pref.UserID {
			p.Events = pref.Events
			p.IsEnabled = pref.IsEnabled
			return p, nil
		}
	}
	m.prefs = append(m.prefs, *pref)
	return pref, nil
}

// SeedPreference installs a row the code under test will find.
func (m *MockNotificationService) SeedPreference(p domain.NotificationPreference) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prefs = append(m.prefs, p)
}

// Preferences returns a copy of the store.
func (m *MockNotificationService) Preferences() []domain.NotificationPreference {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.NotificationPreference, len(m.prefs))
	copy(out, m.prefs)
	return out
}

// FailPreferenceReads makes GetPreferences report err.
func (m *MockNotificationService) FailPreferenceReads(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prefErr = err
}

func (m *MockNotificationService) DeletePreference(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (m *MockNotificationService) ListUnread(_ context.Context, _ uuid.UUID) ([]domain.Notification, error) {
	return nil, nil
}

func (m *MockNotificationService) CountUnread(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (m *MockNotificationService) MarkRead(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return nil
}

func (m *MockNotificationService) MarkAllRead(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (m *MockNotificationService) EmailAvailable() bool {
	return false
}

func (m *MockNotificationService) TelegramBotInfo(context.Context, uuid.UUID) (string, bool) {
	return "", false
}

func (m *MockNotificationService) TelegramReachable(context.Context, uuid.UUID) (reachable bool, reason string) {
	return false, ""
}

// ---------------------------------------------------------------------------
// MockAgentService — minimal stub for comment mention tests.
// Only GetBySlug is implemented; all other methods panic.
// ---------------------------------------------------------------------------

type MockAgentService struct {
	mu     sync.RWMutex
	bySlug map[string]*domain.Agent // key: workspaceID.String()+":"+slug
	// errToReturn makes GetBySlug fail. "Nobody by that name" and "the lookup
	// broke" are different answers — the first is a typo the author must be told
	// about, the second is not — and telling them apart needs a mock that can
	// produce both.
	errToReturn error
}

func NewMockAgentService() *MockAgentService {
	return &MockAgentService{bySlug: make(map[string]*domain.Agent)}
}

func (m *MockAgentService) AddAgent(workspaceID uuid.UUID, a *domain.Agent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bySlug[workspaceID.String()+":"+a.Slug] = a
}

func (m *MockAgentService) GetBySlug(_ context.Context, workspaceID uuid.UUID, slug string) (*domain.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	a := m.bySlug[workspaceID.String()+":"+slug]
	return a, nil
}

func (m *MockAgentService) Register(_ context.Context, _ RegisterAgentInput) (*RegisterAgentOutput, error) {
	panic("MockAgentService.Register not implemented")
}
func (m *MockAgentService) GetByID(_ context.Context, _ uuid.UUID) (*domain.Agent, error) {
	panic("MockAgentService.GetByID not implemented")
}
func (m *MockAgentService) Update(_ context.Context, _ *domain.Agent) error {
	panic("MockAgentService.Update not implemented")
}
func (m *MockAgentService) Delete(_ context.Context, _ uuid.UUID) error {
	panic("MockAgentService.Delete not implemented")
}
func (m *MockAgentService) List(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, _ pagination.Params) (*pagination.Page[domain.Agent], error) {
	panic("MockAgentService.List not implemented")
}
func (m *MockAgentService) Heartbeat(_ context.Context, _ uuid.UUID, _ *HeartbeatInput) error {
	panic("MockAgentService.Heartbeat not implemented")
}
func (m *MockAgentService) Authenticate(_ context.Context, _, _ string) (*domain.Agent, error) {
	panic("MockAgentService.Authenticate not implemented")
}
func (m *MockAgentService) RotateAPIKey(_ context.Context, _ uuid.UUID) (string, error) {
	panic("MockAgentService.RotateAPIKey not implemented")
}
func (m *MockAgentService) ListSubAgents(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Agent, error) {
	panic("MockAgentService.ListSubAgents not implemented")
}
func (m *MockAgentService) CreateActivityLog(_ context.Context, _ *domain.AgentActivityLog) error {
	panic("MockAgentService.CreateActivityLog not implemented")
}
func (m *MockAgentService) ListActivityLog(_ context.Context, _ uuid.UUID, _ repository.AgentActivityLogFilter, _ pagination.Params) (*pagination.Page[domain.AgentActivityLog], error) {
	panic("MockAgentService.ListActivityLog not implemented")
}
func (m *MockAgentService) TouchLastSeen(_ context.Context, _ uuid.UUID) error {
	panic("MockAgentService.TouchLastSeen not implemented")
}

// ---------------------------------------------------------------------------
// MockEventBusMessageRepository
// ---------------------------------------------------------------------------

type MockEventBusMessageRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.EventBusMessage
	errToReturn error
}

func NewMockEventBusMessageRepository() *MockEventBusMessageRepository {
	return &MockEventBusMessageRepository{items: make(map[uuid.UUID]*domain.EventBusMessage)}
}

func (m *MockEventBusMessageRepository) Create(_ context.Context, msg *domain.EventBusMessage) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[msg.ID] = msg
	return nil
}

func (m *MockEventBusMessageRepository) Upsert(_ context.Context, msg *domain.EventBusMessage) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Upsert: only insert if not already present.
	if _, exists := m.items[msg.ID]; !exists {
		m.items[msg.ID] = msg
	}
	return nil
}

func (m *MockEventBusMessageRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.EventBusMessage, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	msg, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return msg, nil
}

func (m *MockEventBusMessageRepository) List(_ context.Context, projectID uuid.UUID, _ repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.EventBusMessage
	for _, msg := range m.items {
		if msg.ProjectID == projectID {
			all = append(all, *msg)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

func (m *MockEventBusMessageRepository) ListEnriched(_ context.Context, projectID uuid.UUID, _ repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EnrichedEventBusMessage], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.EnrichedEventBusMessage
	for _, msg := range m.items {
		if msg.ProjectID == projectID {
			all = append(all, domain.EnrichedEventBusMessage{EventBusMessage: *msg})
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

func (m *MockEventBusMessageRepository) DeleteExpired(_ context.Context) (int64, error) {
	if m.errToReturn != nil {
		return 0, m.errToReturn
	}
	return 0, nil
}

// ---------------------------------------------------------------------------
// MockActivityLogRepository
// ---------------------------------------------------------------------------

type MockActivityLogRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.ActivityLog
	errToReturn error
}

func NewMockActivityLogRepository() *MockActivityLogRepository {
	return &MockActivityLogRepository{items: make(map[uuid.UUID]*domain.ActivityLog)}
}

func (m *MockActivityLogRepository) Create(_ context.Context, entry *domain.ActivityLog) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[entry.ID] = entry
	return nil
}

func (m *MockActivityLogRepository) List(_ context.Context, workspaceID uuid.UUID, _ repository.ActivityLogFilter, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.ActivityLog
	for _, entry := range m.items {
		if entry.WorkspaceID == workspaceID {
			all = append(all, *entry)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

func (m *MockActivityLogRepository) ListByTask(_ context.Context, taskID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.ActivityLog
	for _, entry := range m.items {
		if entry.EntityType == "task" && entry.EntityID == taskID {
			all = append(all, *entry)
		}
	}
	return pagination.NewPage(all, len(all), pg), nil
}

func (m *MockActivityLogRepository) Export(_ context.Context, workspaceID uuid.UUID, filter repository.ActivityLogFilter, limit int) ([]domain.ActivityLog, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []domain.ActivityLog
	for _, entry := range m.items {
		if entry.WorkspaceID != workspaceID {
			continue
		}
		if filter.EntityType != nil && entry.EntityType != *filter.EntityType {
			continue
		}
		if filter.Action != nil && entry.Action != *filter.Action {
			continue
		}
		if filter.From != nil && entry.CreatedAt.Before(*filter.From) {
			continue
		}
		if filter.To != nil && entry.CreatedAt.After(*filter.To) {
			continue
		}
		all = append(all, *entry)
		if len(all) >= limit {
			break
		}
	}
	return all, nil
}

// ---------------------------------------------------------------------------
// MockStorageClient
// ---------------------------------------------------------------------------

type MockStorageClient struct {
	mu           sync.RWMutex
	objects      map[string][]byte
	errToReturn  error
	lastFilename string
}

func NewMockStorageClient() *MockStorageClient {
	return &MockStorageClient{objects: make(map[string][]byte)}
}

func (m *MockStorageClient) Upload(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	m.objects[key] = data
	return nil
}

func (m *MockStorageClient) GetPresignedURL(_ context.Context, key string, expiry time.Duration, _, filename string) (string, error) {
	if m.errToReturn != nil {
		return "", m.errToReturn
	}
	m.mu.Lock()
	m.lastFilename = filename
	m.mu.Unlock()
	return fmt.Sprintf("https://s3.example.com/%s?expiry=%s", key, expiry), nil
}

func (m *MockStorageClient) Delete(_ context.Context, key string) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *MockStorageClient) Download(_ context.Context, key string) (io.ReadCloser, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("object not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// ---------------------------------------------------------------------------
// MockRulesService — minimal stub that implements RulesService for task tests.
// GetEffectiveAssignmentRules is exercised by applyAutoAssign.
// GetProjectWorkflowRules is exercised by applyReviewAssignee (returns nil by default = no rules).
// All other methods panic to make test gaps immediately obvious.
// ---------------------------------------------------------------------------

type MockRulesService struct {
	effectiveRules *domain.EffectiveAssignmentRules
	workflowRules  *domain.WorkflowRulesResponse
	errToReturn    error
}

func NewMockRulesService(rules *domain.EffectiveAssignmentRules) *MockRulesService {
	return &MockRulesService{effectiveRules: rules}
}

func (m *MockRulesService) WithWorkflowRules(r *domain.WorkflowRulesResponse) *MockRulesService {
	m.workflowRules = r
	return m
}

func (m *MockRulesService) GetEffectiveAssignmentRules(_ context.Context, _ uuid.UUID) (*domain.EffectiveAssignmentRules, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	if m.effectiveRules == nil {
		return &domain.EffectiveAssignmentRules{}, nil
	}
	return m.effectiveRules, nil
}

// Remaining RulesService methods — not exercised by task tests.
func (m *MockRulesService) GetTeamDirectory(_ context.Context, _ uuid.UUID) (*domain.TeamDirectory, error) {
	panic("MockRulesService.GetTeamDirectory not implemented")
}
func (m *MockRulesService) GetTeamDirectoryTree(_ context.Context, _ uuid.UUID) (*domain.TeamDirectoryTree, error) {
	panic("MockRulesService.GetTeamDirectoryTree not implemented")
}
func (m *MockRulesService) UpdateAgentProfile(_ context.Context, _ uuid.UUID, _ domain.AgentProfileUpdate) error {
	panic("MockRulesService.UpdateAgentProfile not implemented")
}
func (m *MockRulesService) GetWorkspaceAssignmentRules(_ context.Context, _ uuid.UUID) (*domain.AssignmentRulesConfig, error) {
	panic("MockRulesService.GetWorkspaceAssignmentRules not implemented")
}
func (m *MockRulesService) SetWorkspaceAssignmentRules(_ context.Context, _ uuid.UUID, _ domain.AssignmentRulesConfig) error {
	panic("MockRulesService.SetWorkspaceAssignmentRules not implemented")
}
func (m *MockRulesService) SetProjectAssignmentRules(_ context.Context, _ uuid.UUID, _ domain.AssignmentRulesConfig) error {
	panic("MockRulesService.SetProjectAssignmentRules not implemented")
}
func (m *MockRulesService) GetProjectWorkflowRules(_ context.Context, _ uuid.UUID, _ *uuid.UUID) (*domain.WorkflowRulesResponse, error) {
	return m.workflowRules, nil
}
func (m *MockRulesService) SetProjectWorkflowRules(_ context.Context, _ uuid.UUID, _ domain.WorkflowRulesConfig) error {
	panic("MockRulesService.SetProjectWorkflowRules not implemented")
}
func (m *MockRulesService) ListViolations(_ context.Context, _ uuid.UUID, _ int) ([]domain.RuleViolationLog, error) {
	panic("MockRulesService.ListViolations not implemented")
}
func (m *MockRulesService) LogViolation(_ context.Context, _ *domain.RuleViolationLog) error {
	panic("MockRulesService.LogViolation not implemented")
}
func (m *MockRulesService) ImportConfig(_ context.Context, _ uuid.UUID, _ []byte) (*domain.ImportResult, error) {
	panic("MockRulesService.ImportConfig not implemented")
}
func (m *MockRulesService) ExportConfig(_ context.Context, _ uuid.UUID) ([]byte, error) {
	panic("MockRulesService.ExportConfig not implemented")
}
func (m *MockRulesService) ImportTeam(_ context.Context, _ uuid.UUID, _ []byte) (*domain.TeamImportResult, error) {
	panic("MockRulesService.ImportTeam not implemented")
}
func (m *MockRulesService) GetWorkflowTemplates(_ context.Context, _ uuid.UUID) (map[string]domain.WorkflowRulesConfig, error) {
	panic("MockRulesService.GetWorkflowTemplates not implemented")
}
func (m *MockRulesService) SetWorkflowTemplates(_ context.Context, _ uuid.UUID, _ map[string]domain.WorkflowRulesConfig) error {
	panic("MockRulesService.SetWorkflowTemplates not implemented")
}

// ---------------------------------------------------------------------------
// MockUserRepository
// ---------------------------------------------------------------------------

// MockUserRepository implements repository.UserRepository. Users are keyed by
// "<workspaceID>/<username>" for GetByUsername lookups and by ID for GetByID.
type MockUserRepository struct {
	mu            sync.RWMutex
	byUsername    map[string]*domain.User
	byID          map[uuid.UUID]*domain.User
	searchResults []domain.User
	errToReturn   error
}

func NewMockUserRepository() *MockUserRepository {
	return &MockUserRepository{
		byUsername: make(map[string]*domain.User),
		byID:       make(map[uuid.UUID]*domain.User),
	}
}

// AddUser registers a user resolvable by GetByUsername in the given workspace and by ID.
func (m *MockUserRepository) AddUser(workspaceID uuid.UUID, u *domain.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byUsername[workspaceID.String()+"/"+u.Username] = u
	m.byID[u.ID] = u
}

func (m *MockUserRepository) GetByUsername(_ context.Context, workspaceID uuid.UUID, username string) (*domain.User, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.byUsername[workspaceID.String()+"/"+username]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (m *MockUserRepository) Create(_ context.Context, u *domain.User) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[u.ID] = u
	return nil
}

func (m *MockUserRepository) UsernameExists(_ context.Context, _ string) (bool, error) {
	return false, m.errToReturn
}

func (m *MockUserRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.byID[id]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (m *MockUserRepository) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.byID {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

// Update persists a copy rather than the caller's pointer, so a test that
// asserts a write really happened cannot be satisfied by the service having
// mutated the struct GetByID handed it.
func (m *MockUserRepository) Update(_ context.Context, u *domain.User) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := *u
	m.byID[u.ID] = &stored
	return nil
}

func (m *MockUserRepository) SearchAddableUsers(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.User, error) {
	return m.searchResults, m.errToReturn
}

func (m *MockUserRepository) SearchInWorkspace(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.User, error) {
	return nil, nil
}

func (m *MockUserRepository) GetByUsernameGlobal(_ context.Context, _ string) (*domain.User, error) {
	return nil, nil
}

func (m *MockUserRepository) Count(_ context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byID), nil
}

// ---------------------------------------------------------------------------
// MockProjectMemberRepository
// ---------------------------------------------------------------------------

type MockProjectMemberRepository struct {
	mu      sync.RWMutex
	members []*domain.ProjectMember
}

func NewMockProjectMemberRepository() *MockProjectMemberRepository {
	return &MockProjectMemberRepository{}
}

func (m *MockProjectMemberRepository) Create(_ context.Context, pm *domain.ProjectMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members = append(m.members, pm)
	return nil
}

func (m *MockProjectMemberRepository) ExistsMember(_ context.Context, projectID uuid.UUID, userID, agentID *uuid.UUID) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, pm := range m.members {
		if pm.ProjectID != projectID {
			continue
		}
		if userID != nil && pm.UserID != nil && *pm.UserID == *userID {
			return true, nil
		}
		if agentID != nil && pm.AgentID != nil && *pm.AgentID == *agentID {
			return true, nil
		}
	}
	return false, nil
}

func (m *MockProjectMemberRepository) GetByProjectAndUser(_ context.Context, projectID, userID uuid.UUID) (*domain.ProjectMember, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, pm := range m.members {
		if pm.ProjectID == projectID && pm.UserID != nil && *pm.UserID == userID {
			return pm, nil
		}
	}
	return nil, nil
}

func (m *MockProjectMemberRepository) GetByProjectAndAgent(_ context.Context, projectID, agentID uuid.UUID) (*domain.ProjectMember, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, pm := range m.members {
		if pm.ProjectID == projectID && pm.AgentID != nil && *pm.AgentID == agentID {
			return pm, nil
		}
	}
	return nil, nil
}

func (m *MockProjectMemberRepository) List(_ context.Context, _ uuid.UUID) ([]domain.ProjectMemberWithUser, error) {
	return nil, nil
}

func (m *MockProjectMemberRepository) UpdateRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}

func (m *MockProjectMemberRepository) Delete(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (m *MockProjectMemberRepository) DeleteAgent(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (m *MockProjectMemberRepository) DeleteByWorkspaceAndUser(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

// ---------------------------------------------------------------------------
// fakeTaskMover — minimal TaskService double for comment-triage tests
// ---------------------------------------------------------------------------

// moveCall records a single MoveTask invocation.
type moveCall struct {
	taskID uuid.UUID
	input  MoveTaskInput
}

// fakeTaskMover satisfies the TaskService interface by embedding it (all unused
// methods are nil and would panic if called). Only MoveTask is implemented — it
// records calls so tests can assert the auto-triage move happened.
type humanGateCall struct {
	taskID uuid.UUID
	value  bool
}

type fakeTaskMover struct {
	TaskService
	mu           sync.Mutex
	moves        []moveCall
	gateSetCalls []humanGateCall
	err          error
}

func (f *fakeTaskMover) MoveTask(_ context.Context, taskID uuid.UUID, input MoveTaskInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, moveCall{taskID: taskID, input: input})
	return f.err
}

func (f *fakeTaskMover) SetHumanGate(_ context.Context, taskID uuid.UUID, value bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gateSetCalls = append(f.gateSetCalls, humanGateCall{taskID: taskID, value: value})
	return nil
}

func (f *fakeTaskMover) ShipTask(_ context.Context, _ uuid.UUID, _ bool) error { return nil }
func (f *fakeTaskMover) SetDodCheck(_ context.Context, _ uuid.UUID, _, _, _ string) error {
	return nil
}

func (f *fakeTaskMover) calls() []moveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]moveCall(nil), f.moves...)
}

func (f *fakeTaskMover) humanGateCalls() []humanGateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]humanGateCall(nil), f.gateSetCalls...)
}

// ---------------------------------------------------------------------------
// MockProjectIntegrationRepository
// ---------------------------------------------------------------------------

type MockProjectIntegrationRepository struct {
	bySlug map[string]*domain.ProjectIntegration
	byKey  map[uuid.UUID]map[string]*domain.ProjectIntegration
	err    error
}

func NewMockProjectIntegrationRepository() *MockProjectIntegrationRepository {
	return &MockProjectIntegrationRepository{
		bySlug: make(map[string]*domain.ProjectIntegration),
		byKey:  make(map[uuid.UUID]map[string]*domain.ProjectIntegration),
	}
}

func (m *MockProjectIntegrationRepository) Get(_ context.Context, projectID uuid.UUID, intType string) (*domain.ProjectIntegration, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.byKey[projectID] == nil {
		return nil, nil
	}
	pi, ok := m.byKey[projectID][intType]
	if !ok {
		return nil, nil
	}
	return pi, nil
}

func (m *MockProjectIntegrationRepository) GetByShareSlug(_ context.Context, shareSlug string) (*domain.ProjectIntegration, error) {
	if m.err != nil {
		return nil, m.err
	}
	pi, ok := m.bySlug[shareSlug]
	if !ok {
		return nil, nil
	}
	return pi, nil
}

func (m *MockProjectIntegrationRepository) Upsert(_ context.Context, pi *domain.ProjectIntegration) error {
	if m.err != nil {
		return m.err
	}
	if m.byKey[pi.ProjectID] == nil {
		m.byKey[pi.ProjectID] = make(map[string]*domain.ProjectIntegration)
	}
	m.byKey[pi.ProjectID][pi.Type] = pi
	return nil
}

func (m *MockProjectIntegrationRepository) Delete(_ context.Context, projectID uuid.UUID, intType string) error {
	if m.err != nil {
		return m.err
	}
	if m.byKey[projectID] != nil {
		delete(m.byKey[projectID], intType)
	}
	return nil
}

func (m *MockProjectIntegrationRepository) ListByProject(_ context.Context, projectID uuid.UUID) ([]domain.ProjectIntegration, error) {
	if m.err != nil {
		return nil, m.err
	}
	var result []domain.ProjectIntegration
	for _, pi := range m.byKey[projectID] {
		result = append(result, *pi)
	}
	return result, nil
}

var _ repository.ProjectIntegrationRepository = (*MockProjectIntegrationRepository)(nil)

// ---------------------------------------------------------------------------
// MockVCSLinkRepository
// ---------------------------------------------------------------------------

type MockVCSLinkRepository struct {
	mu          sync.RWMutex
	items       []domain.VCSLink
	errToReturn error
}

func NewMockVCSLinkRepository() *MockVCSLinkRepository {
	return &MockVCSLinkRepository{}
}

func (m *MockVCSLinkRepository) Create(_ context.Context, link *domain.VCSLink) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append(m.items, *link)
	return nil
}

func (m *MockVCSLinkRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, l := range m.items {
		if l.ID == id {
			cp := l
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *MockVCSLinkRepository) Delete(_ context.Context, _ uuid.UUID) error {
	return m.errToReturn
}

func (m *MockVCSLinkRepository) ListByTask(_ context.Context, taskID uuid.UUID) ([]domain.VCSLink, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []domain.VCSLink
	for _, l := range m.items {
		if l.TaskID == taskID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (m *MockVCSLinkRepository) Upsert(_ context.Context, link *domain.VCSLink) (bool, error) {
	if m.errToReturn != nil {
		return false, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, l := range m.items {
		if l.TaskID == link.TaskID && l.Provider == link.Provider && l.LinkType == link.LinkType && l.ExternalID == link.ExternalID {
			// Mirrors real Postgres: ON CONFLICT DO UPDATE never touches
			// id/created_at — preserve them and reflect that back into the
			// caller's link, same contract as VCSLinkRepo.Upsert.
			link.ID = l.ID
			link.CreatedAt = l.CreatedAt
			m.items[i] = *link
			return false, nil
		}
	}
	m.items = append(m.items, *link)
	return true, nil
}

func (m *MockVCSLinkRepository) ListByExternalID(_ context.Context, provider domain.VCSProvider, linkType domain.VCSLinkType, externalID string) ([]domain.VCSLink, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []domain.VCSLink
	for _, l := range m.items {
		if l.Provider == provider && l.LinkType == linkType && l.ExternalID == externalID {
			out = append(out, l)
		}
	}
	return out, nil
}

var _ repository.VCSLinkRepository = (*MockVCSLinkRepository)(nil)

// ---------------------------------------------------------------------------
// MockRuleRepository — minimal stub for evaluator unit tests.
// Only CountTasksByAssigneeAndCategory is implemented; all other methods panic.
// ---------------------------------------------------------------------------

type MockRuleRepository struct {
	mu    sync.RWMutex
	count int
	err   error
}

func NewMockRuleRepository(count int) *MockRuleRepository {
	return &MockRuleRepository{count: count}
}

func (m *MockRuleRepository) SetCount(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count = n
}

func (m *MockRuleRepository) CountTasksByAssigneeAndCategory(_ context.Context, _, _ uuid.UUID, _ string, _ []string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.count, m.err
}

func (m *MockRuleRepository) Create(_ context.Context, _ *domain.Rule) error {
	panic("MockRuleRepository.Create not implemented")
}
func (m *MockRuleRepository) GetByID(_ context.Context, _ uuid.UUID) (*domain.Rule, error) {
	panic("MockRuleRepository.GetByID not implemented")
}
func (m *MockRuleRepository) Update(_ context.Context, _ *domain.Rule) error {
	panic("MockRuleRepository.Update not implemented")
}
func (m *MockRuleRepository) Delete(_ context.Context, _ uuid.UUID) error {
	panic("MockRuleRepository.Delete not implemented")
}
func (m *MockRuleRepository) ListByWorkspace(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Rule, error) {
	panic("MockRuleRepository.ListByWorkspace not implemented")
}
func (m *MockRuleRepository) ListByProject(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Rule, error) {
	panic("MockRuleRepository.ListByProject not implemented")
}
func (m *MockRuleRepository) ListByAgent(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Rule, error) {
	panic("MockRuleRepository.ListByAgent not implemented")
}
func (m *MockRuleRepository) GetEffective(_ context.Context, _ uuid.UUID, _, _ *uuid.UUID) ([]domain.Rule, error) {
	panic("MockRuleRepository.GetEffective not implemented")
}

var _ repository.RuleRepository = (*MockRuleRepository)(nil)

// ---------------------------------------------------------------------------
// MockWorkspaceMembershipReader — the human half of the assignee tenancy guard.
// ---------------------------------------------------------------------------

// MockWorkspaceMembershipReader answers service.WorkspaceMembershipReader.
//
// Default-allow inside defaultWorkspace mirrors what MockProjectRepository does
// for projects: the ordinary service test has no workspace_members fixture and is
// not about membership. A test that needs a user REFUSED names a different
// workspace, or calls Deny.
type MockWorkspaceMembershipReader struct {
	mu          sync.RWMutex
	permissive  bool
	allowed     map[string]bool
	denied      map[string]bool
	errToReturn error
}

// NewMembershipTableReader models an actual workspace_members table: nobody is a
// member until Allow says so.
//
// The first version of this mock returned "true for any user, as long as the
// WORKSPACE is the default one" — which never looked at the user at all, so the
// foreign-user test passed a foreign user and got a cheerful yes. A membership
// mock that ignores the principal cannot fail the test it exists for.
func NewMembershipTableReader() *MockWorkspaceMembershipReader {
	return &MockWorkspaceMembershipReader{
		allowed: make(map[string]bool),
		denied:  make(map[string]bool),
	}
}

// NewPermissiveWorkspaceMembershipReader treats every user as a member of every
// workspace except pairs passed to Deny.
//
// For tests that are about ENROLMENT rather than tenancy and that mint their own
// workspace ids: forcing each of them to also build a membership fixture would be
// noise, and the tenancy rule itself is proven by the tests that use the strict
// reader. The agent half of the guard stays live either way — it reads the agent
// directory, not this.
func NewPermissiveWorkspaceMembershipReader() *MockWorkspaceMembershipReader {
	return &MockWorkspaceMembershipReader{
		permissive: true,
		allowed:    make(map[string]bool),
		denied:     make(map[string]bool),
	}
}

// Allow records a workspace_members row.
func (m *MockWorkspaceMembershipReader) Allow(workspaceID, userID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowed[workspaceID.String()+"/"+userID.String()] = true
}

// Deny marks one (workspace, user) pair as a non-member.
func (m *MockWorkspaceMembershipReader) Deny(workspaceID, userID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.denied[workspaceID.String()+"/"+userID.String()] = true
}

// FailWith makes every lookup return an error, so a test can prove the guard
// refuses when it cannot read rather than assuming a "no".
func (m *MockWorkspaceMembershipReader) FailWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errToReturn = err
}

func (m *MockWorkspaceMembershipReader) IsWorkspaceMember(_ context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.errToReturn != nil {
		return false, m.errToReturn
	}
	if m.denied[workspaceID.String()+"/"+userID.String()] {
		return false, nil
	}
	if m.permissive {
		return true, nil
	}
	return m.allowed[workspaceID.String()+"/"+userID.String()], nil
}

// ---------------------------------------------------------------------------
// MockDocumentRepository
// ---------------------------------------------------------------------------

// MockDocumentRepository is an in-memory repository.DocumentRepository.
//
// workspaceOf maps a project to its tenant so that GetByIDInWorkspace can refuse
// a document belonging to another one — the cross-tenant behaviour is the point
// of several of the tests, so the mock has to be able to get it wrong.
type MockDocumentRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Document
	workspaceOf map[uuid.UUID]uuid.UUID
	searchText  map[uuid.UUID]string
	// failSearchTextOnly makes SetSearchText fail while every other call
	// succeeds — the shape needed to prove that a failed INDEX write does not
	// fail the document write.
	failSearchTextOnly bool
	errToReturn        error
	createErr          error
	// beforeUpdate runs once, inside Update, before the version is compared. It is
	// how a test lands a competing write in the gap between "the service read the
	// document" and "the service wrote it" without needing real concurrency.
	beforeUpdate func()
}

func NewMockDocumentRepository() *MockDocumentRepository {
	return &MockDocumentRepository{
		items:       make(map[uuid.UUID]*domain.Document),
		workspaceOf: make(map[uuid.UUID]uuid.UUID),
	}
}

// WithProjectWorkspace declares which tenant a project belongs to.
func (m *MockDocumentRepository) WithProjectWorkspace(projectID, workspaceID uuid.UUID) *MockDocumentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceOf[projectID] = workspaceID
	return m
}

// Seed inserts a document directly, bypassing Create.
func (m *MockDocumentRepository) Seed(doc *domain.Document) *MockDocumentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *doc
	m.items[doc.ID] = &copied
	return m
}

func (m *MockDocumentRepository) Create(_ context.Context, doc *domain.Document) error {
	if m.createErr != nil {
		return m.createErr
	}
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *doc
	m.items[doc.ID] = &copied
	return nil
}

func (m *MockDocumentRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Document, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	doc, ok := m.items[id]
	if !ok || doc.DeletedAt != nil {
		return nil, nil
	}
	copied := *doc
	return &copied, nil
}

func (m *MockDocumentRepository) GetByIDInWorkspace(_ context.Context, id, workspaceID uuid.UUID) (*domain.Document, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	doc, ok := m.items[id]
	if !ok || doc.DeletedAt != nil {
		return nil, nil
	}
	if m.workspaceOf[doc.ProjectID] != workspaceID {
		return nil, nil
	}
	copied := *doc
	return &copied, nil
}

// Update mirrors the real conditional write: the version compare, the bump and
// the field writes happen together under the lock, so a test can drive two
// interleaved writers and see the same refusal production would give.
//
// beforeUpdate, when set, runs after the row has been read and before it is
// compared — the seam a test uses to land a competing write in the middle of
// this one.
func (m *MockDocumentRepository) Update(_ context.Context, doc *domain.Document, expectedVersion *int, bumpVersion bool) (int, error) {
	// The hook runs before errToReturn is read, so a test can arm a failure that
	// applies to this write only and not to the read that preceded it.
	if m.beforeUpdate != nil {
		hook := m.beforeUpdate
		m.beforeUpdate = nil
		hook()
	}
	if m.errToReturn != nil {
		return 0, m.errToReturn
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.items[doc.ID]
	if !ok || existing.DeletedAt != nil {
		return 0, apierror.NotFound("Document")
	}
	if expectedVersion != nil && existing.Version != *expectedVersion {
		return existing.Version, repository.ErrDocumentVersionMismatch
	}

	newVersion := existing.Version
	if bumpVersion {
		newVersion++
	}
	copied := *doc
	copied.Version = newVersion
	m.items[doc.ID] = &copied
	return newVersion, nil
}

// GetByPathInProject walks the slug path the same way the recursive CTE does,
// including the part that matters: a soft-deleted document is not a step, at any
// level.
func (m *MockDocumentRepository) GetByPathInProject(_ context.Context, projectID uuid.UUID, segments []string) (*domain.Document, int, error) {
	if m.errToReturn != nil {
		return nil, 0, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var parent *uuid.UUID
	var current *domain.Document
	for depth, slug := range segments {
		current = nil
		for _, d := range m.items {
			if d.ProjectID != projectID || d.DeletedAt != nil || d.Slug != slug {
				continue
			}
			if (parent == nil) != (d.ParentID == nil) {
				continue
			}
			if parent != nil && *parent != *d.ParentID {
				continue
			}
			current = d
			break
		}
		if current == nil {
			return nil, depth, nil
		}
		id := current.ID
		parent = &id
	}
	copied := *current
	return &copied, len(segments), nil
}

func (m *MockDocumentRepository) SoftDelete(_ context.Context, id uuid.UUID, at time.Time, by uuid.UUID, byType domain.ActorType) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.items[id]
	if !ok || doc.DeletedAt != nil {
		return apierror.NotFound("Document")
	}
	// Mirrors the recursive statement in the real repository: the subtree goes
	// with the document, or a child outlives its parent — and every row it touches
	// records who made the change, the delete included.
	stamp := at
	editor, editorType := by, byType
	var markSubtree func(parent uuid.UUID)
	markSubtree = func(parent uuid.UUID) {
		for _, d := range m.items {
			if d.ParentID != nil && *d.ParentID == parent && d.DeletedAt == nil {
				d.DeletedAt = &stamp
				d.UpdatedBy = &editor
				d.UpdatedByType = &editorType
				markSubtree(d.ID)
			}
		}
	}
	doc.DeletedAt = &stamp
	doc.UpdatedBy = &editor
	doc.UpdatedByType = &editorType
	markSubtree(id)
	return nil
}

func (m *MockDocumentRepository) ListByProject(_ context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Document], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var items []domain.Document
	for _, d := range m.items {
		if d.ProjectID == projectID && d.DeletedAt == nil {
			items = append(items, *d)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Position != items[j].Position {
			return items[i].Position < items[j].Position
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return pagination.NewPage(items, len(items), pg), nil
}

func (m *MockDocumentRepository) HasAncestor(_ context.Context, docID, ancestorID uuid.UUID) (bool, error) {
	if m.errToReturn != nil {
		return false, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[uuid.UUID]bool{}
	cur, ok := m.items[docID]
	for ok && cur != nil && cur.ParentID != nil && !seen[*cur.ParentID] {
		if *cur.ParentID == ancestorID {
			return true, nil
		}
		seen[*cur.ParentID] = true
		cur, ok = m.items[*cur.ParentID]
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// MockDocumentAttachmentRepository
// ---------------------------------------------------------------------------

// MockDocumentAttachmentRepository is an in-memory
// repository.DocumentAttachmentRepository.
//
// documentOf maps an attachment's document to its tenant, the same way
// MockDocumentRepository's workspaceOf maps a project to one: refusing another
// tenant's attachment is the behaviour several tests are about, so the mock has
// to be able to get it wrong.
type MockDocumentAttachmentRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.DocumentAttachment
	workspaceOf map[uuid.UUID]uuid.UUID
	errToReturn error
	createErr   error
}

func NewMockDocumentAttachmentRepository() *MockDocumentAttachmentRepository {
	return &MockDocumentAttachmentRepository{
		items:       make(map[uuid.UUID]*domain.DocumentAttachment),
		workspaceOf: make(map[uuid.UUID]uuid.UUID),
	}
}

// WithDocumentWorkspace declares which tenant a document belongs to.
func (m *MockDocumentAttachmentRepository) WithDocumentWorkspace(documentID, workspaceID uuid.UUID) *MockDocumentAttachmentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceOf[documentID] = workspaceID
	return m
}

func (m *MockDocumentAttachmentRepository) Create(_ context.Context, att *domain.DocumentAttachment) error {
	if m.createErr != nil {
		return m.createErr
	}
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *att
	m.items[att.ID] = &copied
	return nil
}

func (m *MockDocumentAttachmentRepository) GetByIDInWorkspace(_ context.Context, id, workspaceID uuid.UUID) (*domain.DocumentAttachment, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	att, ok := m.items[id]
	if !ok || att.DeletedAt != nil {
		return nil, nil
	}
	if m.workspaceOf[att.DocumentID] != workspaceID {
		return nil, nil
	}
	copied := *att
	return &copied, nil
}

func (m *MockDocumentAttachmentRepository) ListByDocument(_ context.Context, documentID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.DocumentAttachment], error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var items []domain.DocumentAttachment
	for _, a := range m.items {
		if a.DocumentID == documentID && a.DeletedAt == nil {
			items = append(items, *a)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID.String() < items[j].ID.String()
	})
	return pagination.NewPage(items, len(items), pg), nil
}

func (m *MockDocumentAttachmentRepository) SoftDelete(_ context.Context, id uuid.UUID, at time.Time) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	att, ok := m.items[id]
	if !ok || att.DeletedAt != nil {
		return apierror.NotFound("Attachment")
	}
	stamp := at
	att.DeletedAt = &stamp
	return nil
}

// ---------------------------------------------------------------------------
// MockDocumentCommentRepository
// ---------------------------------------------------------------------------

// MockDocumentCommentRepository is an in-memory
// repository.DocumentCommentRepository.
//
// documentWorkspace maps a document to its tenant so that GetByIDInWorkspace can
// refuse a comment on another one — the cross-tenant behaviour is the point of
// several of the tests, so the mock has to be able to get it wrong.
type MockDocumentCommentRepository struct {
	mu                sync.RWMutex
	items             map[uuid.UUID]*domain.DocumentComment
	documentWorkspace map[uuid.UUID]uuid.UUID
	errToReturn       error
	createErr         error
	anchorListErr     error
	anchorWriteErr    error
	anchorWrites      int
}

func NewMockDocumentCommentRepository() *MockDocumentCommentRepository {
	return &MockDocumentCommentRepository{
		items:             make(map[uuid.UUID]*domain.DocumentComment),
		documentWorkspace: make(map[uuid.UUID]uuid.UUID),
	}
}

// WithDocumentWorkspace declares which tenant a document belongs to.
func (m *MockDocumentCommentRepository) WithDocumentWorkspace(documentID, workspaceID uuid.UUID) *MockDocumentCommentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.documentWorkspace[documentID] = workspaceID
	return m
}

// FailWith makes every call return err.
func (m *MockDocumentCommentRepository) FailWith(err error) *MockDocumentCommentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errToReturn = err
	return m
}

// FailCreateWith makes only Create return err, so a test can prove the service
// surfaces a write failure without also breaking the reads that precede it.
func (m *MockDocumentCommentRepository) FailCreateWith(err error) *MockDocumentCommentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
	return m
}

// Count is how many comments were actually written, so a test asserting that a
// refused request stored nothing can check the table rather than infer it from
// the error it got back.
func (m *MockDocumentCommentRepository) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// copyDocumentComment is a DEEP copy: the anchor is a pointer, and a shallow
// copy hands the caller the very struct the mock will later mutate. That aliasing
// makes "did this write change the anchor?" unanswerable — a value read before
// the write silently becomes the value after it — which is exactly the question
// the re-anchoring tests ask. The real repository scans fresh rows and cannot
// alias; a mock that can is a mock that hides regressions.
func copyDocumentComment(c *domain.DocumentComment) *domain.DocumentComment {
	copied := *c
	if c.Anchor != nil {
		anchor := *c.Anchor
		if c.Anchor.Start != nil {
			start := *c.Anchor.Start
			anchor.Start = &start
		}
		if c.Anchor.End != nil {
			end := *c.Anchor.End
			anchor.End = &end
		}
		copied.Anchor = &anchor
	}
	return &copied
}

func (m *MockDocumentCommentRepository) Create(_ context.Context, comment *domain.DocumentComment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.items[comment.ID] = copyDocumentComment(comment)
	return nil
}

func (m *MockDocumentCommentRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.DocumentComment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	c, ok := m.items[id]
	if !ok || c.DeletedAt != nil {
		return nil, nil
	}
	return copyDocumentComment(c), nil
}

func (m *MockDocumentCommentRepository) GetByIDInWorkspace(_ context.Context, id, workspaceID uuid.UUID) (*domain.DocumentComment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	c, ok := m.items[id]
	if !ok || c.DeletedAt != nil {
		return nil, nil
	}
	if m.documentWorkspace[c.DocumentID] != workspaceID {
		return nil, nil
	}
	return copyDocumentComment(c), nil
}

func (m *MockDocumentCommentRepository) Update(_ context.Context, comment *domain.DocumentComment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errToReturn != nil {
		return m.errToReturn
	}
	existing, ok := m.items[comment.ID]
	if !ok || existing.DeletedAt != nil {
		return apierror.NotFound("Comment")
	}
	// Mirrors the real UPDATE's SET list: body, the resolution triple and
	// updated_at. The anchor is deliberately not writable here either, so a test
	// that expected an edit to move it would fail rather than pass on the mock.
	existing.Body = comment.Body
	existing.ResolvedAt = comment.ResolvedAt
	existing.ResolvedBy = comment.ResolvedBy
	existing.ResolvedByType = comment.ResolvedByType
	existing.UpdatedAt = comment.UpdatedAt
	return nil
}

func (m *MockDocumentCommentRepository) SoftDelete(_ context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errToReturn != nil {
		return m.errToReturn
	}
	root, ok := m.items[id]
	if !ok || root.DeletedAt != nil {
		return apierror.NotFound("Comment")
	}
	stamp := at
	root.DeletedAt = &stamp
	// The replies go with the root, one level deep — the same reach as the real
	// statement's `id = $1 OR parent_comment_id = $1`.
	for _, c := range m.items {
		if c.ParentCommentID != nil && *c.ParentCommentID == id && c.DeletedAt == nil {
			c.DeletedAt = &stamp
		}
	}
	return nil
}

func (m *MockDocumentCommentRepository) ListByDocument(
	_ context.Context,
	documentID uuid.UUID,
	filter repository.DocumentCommentFilter,
	pg pagination.Params,
) (*pagination.Page[domain.DocumentComment], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}

	resolvedRoots := map[uuid.UUID]bool{}
	for _, c := range m.items {
		if c.DeletedAt == nil && c.ParentCommentID == nil && c.ResolvedAt != nil {
			resolvedRoots[c.ID] = true
		}
	}

	var items []domain.DocumentComment
	for _, c := range m.items {
		if c.DocumentID != documentID || c.DeletedAt != nil {
			continue
		}
		if !filter.IncludeResolved {
			// Hidden by THREAD, as the real predicate is: COALESCE(parent, id).
			root := c.ID
			if c.ParentCommentID != nil {
				root = *c.ParentCommentID
			}
			if resolvedRoots[root] {
				continue
			}
		}
		items = append(items, *copyDocumentComment(c))
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID.String() < items[j].ID.String()
	})
	return pagination.NewPage(items, len(items), pg), nil
}

// ListAnchorsByDocument mirrors the real query's filter exactly: live comments
// that carry a quote, resolved threads included. A mock that quietly skipped
// resolved threads would make the service look correct on a case the database
// treats differently, which is the whole failure mode a mock is supposed to not
// introduce.
func (m *MockDocumentCommentRepository) ListAnchorsByDocument(_ context.Context, documentID uuid.UUID) ([]repository.DocumentCommentAnchorRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.anchorListErr != nil {
		return nil, m.anchorListErr
	}
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}

	var rows []repository.DocumentCommentAnchorRow
	var ordered []*domain.DocumentComment
	for _, c := range m.items {
		if c.DocumentID != documentID || c.DeletedAt != nil || c.Anchor == nil || c.Anchor.Exact == "" {
			continue
		}
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	for _, c := range ordered {
		prefix, suffix := c.Anchor.Prefix, c.Anchor.Suffix
		rows = append(rows, repository.DocumentCommentAnchorRow{
			ID:     c.ID,
			Exact:  c.Anchor.Exact,
			Prefix: &prefix,
			Suffix: &suffix,
			Start:  c.Anchor.Start,
			End:    c.Anchor.End,
		})
	}
	return rows, nil
}

// UpdateAnchorPositions writes the offsets back, and counts the calls so a test
// can tell "the pass ran and found nothing to move" from "the pass never ran" —
// two states that produce identical rows and opposite conclusions.
func (m *MockDocumentCommentRepository) UpdateAnchorPositions(_ context.Context, positions []repository.DocumentCommentAnchorPosition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchorWrites++
	if m.anchorWriteErr != nil {
		return m.anchorWriteErr
	}
	if m.errToReturn != nil {
		return m.errToReturn
	}
	for _, p := range positions {
		c, ok := m.items[p.ID]
		if !ok || c.DeletedAt != nil || c.Anchor == nil {
			continue
		}
		c.Anchor.Prefix, c.Anchor.Suffix = p.Prefix, p.Suffix
		c.Anchor.Start, c.Anchor.End = p.Start, p.End
	}
	return nil
}

// AnchorWrites is how many times the re-anchoring pass reached the write.
func (m *MockDocumentCommentRepository) AnchorWrites() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.anchorWrites
}

// FailAnchorListWith / FailAnchorWriteWith break one half of the pass each, so a
// test can prove the document write still succeeds when re-anchoring cannot run
// — the trade this best-effort pass deliberately makes.
func (m *MockDocumentCommentRepository) FailAnchorListWith(err error) *MockDocumentCommentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchorListErr = err
	return m
}

func (m *MockDocumentCommentRepository) FailAnchorWriteWith(err error) *MockDocumentCommentRepository {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anchorWriteErr = err
	return m
}

// searchText mirrors what the trigger indexes, so a service test can assert that
// the body actually reached the index rather than that a method was called.
func (m *MockDocumentRepository) SetSearchText(_ context.Context, documentID uuid.UUID, text string) error {
	if m.failSearchTextOnly {
		return assertAnError{}
	}
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.searchText == nil {
		m.searchText = make(map[uuid.UUID]string)
	}
	m.searchText[documentID] = text
	return nil
}

// SearchText reports what was indexed for a document, for assertions.
func (m *MockDocumentRepository) SearchText(documentID uuid.UUID) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	text, ok := m.searchText[documentID]
	return text, ok
}

func (m *MockDocumentRepository) SearchInProject(
	_ context.Context,
	projectID, workspaceID uuid.UUID,
	query string,
	limit int,
) ([]domain.DocumentSearchHit, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var hits []domain.DocumentSearchHit
	for _, doc := range m.items {
		if doc.ProjectID != projectID || doc.DeletedAt != nil {
			continue
		}
		if m.workspaceOf[doc.ProjectID] != workspaceID {
			continue
		}
		// Substring over title AND indexed text — a stand-in for the tsvector,
		// enough to tell "searched the content" from "searched the title".
		haystack := strings.ToLower(doc.Title + " " + m.searchText[doc.ID])
		if !strings.Contains(haystack, strings.ToLower(query)) {
			continue
		}
		hits = append(hits, domain.DocumentSearchHit{
			ID: doc.ID, ProjectID: doc.ProjectID, Title: doc.Title, Slug: doc.Slug,
		})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}
