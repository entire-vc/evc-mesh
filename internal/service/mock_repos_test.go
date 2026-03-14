package service

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// MockWorkspaceRepository
// ---------------------------------------------------------------------------

type MockWorkspaceRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Workspace
	errToReturn error
}

func NewMockWorkspaceRepository() *MockWorkspaceRepository {
	return &MockWorkspaceRepository{items: make(map[uuid.UUID]*domain.Workspace)}
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

// ---------------------------------------------------------------------------
// MockProjectRepository
// ---------------------------------------------------------------------------

type MockProjectRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Project
	errToReturn error
}

func NewMockProjectRepository() *MockProjectRepository {
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
		return nil, nil
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
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Task
	errToReturn error
}

func NewMockTaskRepository() *MockTaskRepository {
	return &MockTaskRepository{items: make(map[uuid.UUID]*domain.Task)}
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

func (m *MockTaskRepository) ListByAssignee(_ context.Context, assigneeID uuid.UUID, assigneeType domain.AssigneeType) ([]domain.Task, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.Task
	for _, t := range m.items {
		if t.AssigneeID != nil && *t.AssigneeID == assigneeID && t.AssigneeType == assigneeType {
			result = append(result, *t)
		}
	}
	return result, nil
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

func (m *MockTaskRepository) AtomicCheckout(_ context.Context, taskID, agentID uuid.UUID, token uuid.UUID, expiresAt time.Time) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.items[taskID]
	if !ok {
		return nil
	}
	t.CheckedOutBy = &agentID
	t.CheckoutToken = &token
	t.CheckoutExpires = &expiresAt
	return nil
}

func (m *MockTaskRepository) ReleaseCheckout(_ context.Context, taskID uuid.UUID, _ uuid.UUID) error {
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
	return nil
}

func (m *MockTaskRepository) ExtendCheckout(_ context.Context, taskID uuid.UUID, _ uuid.UUID, newExpires time.Time) error {
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

// ---------------------------------------------------------------------------
// MockTaskStatusRepository
// ---------------------------------------------------------------------------

type MockTaskStatusRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.TaskStatus
	errToReturn error
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
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Comment
	errToReturn error
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

// ---------------------------------------------------------------------------
// MockArtifactRepository
// ---------------------------------------------------------------------------

type MockArtifactRepository struct {
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Artifact
	errToReturn error
}

func NewMockArtifactRepository() *MockArtifactRepository {
	return &MockArtifactRepository{items: make(map[uuid.UUID]*domain.Artifact)}
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

func (m *MockArtifactRepository) Delete(_ context.Context, id uuid.UUID) error {
	if m.errToReturn != nil {
		return m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
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
	mu          sync.RWMutex
	items       map[uuid.UUID]*domain.Agent
	errToReturn error
}

func NewMockAgentRepository() *MockAgentRepository {
	return &MockAgentRepository{items: make(map[uuid.UUID]*domain.Agent)}
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
	return a, nil
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
	mu          sync.RWMutex
	objects     map[string][]byte
	errToReturn error
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

func (m *MockStorageClient) GetPresignedURL(_ context.Context, key string, expiry time.Duration) (string, error) {
	if m.errToReturn != nil {
		return "", m.errToReturn
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
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

// ---------------------------------------------------------------------------
// MockRulesService — minimal stub that implements RulesService for task tests.
// Only GetEffectiveAssignmentRules is exercised by applyAutoAssign; all other
// methods panic to make test gaps immediately obvious.
// ---------------------------------------------------------------------------

type MockRulesService struct {
	effectiveRules *domain.EffectiveAssignmentRules
	errToReturn    error
}

func NewMockRulesService(rules *domain.EffectiveAssignmentRules) *MockRulesService {
	return &MockRulesService{effectiveRules: rules}
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
	panic("MockRulesService.GetProjectWorkflowRules not implemented")
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
