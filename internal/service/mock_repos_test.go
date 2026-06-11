package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func (m *MockCommentRepository) ListByAuthor(_ context.Context, _ uuid.UUID, _ repository.CommentViewFilter) ([]domain.CommentView, *time.Time, error) {
	return []domain.CommentView{}, nil, m.errToReturn
}

func (m *MockCommentRepository) ListRecentByWorkspace(_ context.Context, _ uuid.UUID, _ repository.CommentViewFilter) ([]domain.CommentView, *time.Time, error) {
	return []domain.CommentView{}, nil, m.errToReturn
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
// MockAgentService — minimal stub for comment mention tests.
// Only GetBySlug is implemented; all other methods panic.
// ---------------------------------------------------------------------------

type MockAgentService struct {
	mu     sync.RWMutex
	bySlug map[string]*domain.Agent // key: workspaceID.String()+":"+slug
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

func (m *MockStorageClient) GetPresignedURL(_ context.Context, key string, expiry time.Duration, _, _ string) (string, error) {
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
	mu          sync.RWMutex
	byUsername  map[string]*domain.User
	byID        map[uuid.UUID]*domain.User
	errToReturn error
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

func (m *MockUserRepository) Create(_ context.Context, _ *domain.User) error {
	return m.errToReturn
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

func (m *MockUserRepository) GetByEmail(_ context.Context, _ string) (*domain.User, error) {
	return nil, m.errToReturn
}

func (m *MockUserRepository) Update(_ context.Context, _ *domain.User) error {
	return m.errToReturn
}

func (m *MockUserRepository) SearchUsers(_ context.Context, _ string, _ int) ([]domain.User, error) {
	return nil, m.errToReturn
}

func (m *MockUserRepository) SearchInWorkspace(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.User, error) {
	return nil, nil
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
type fakeTaskMover struct {
	TaskService
	mu    sync.Mutex
	moves []moveCall
	err   error
}

func (f *fakeTaskMover) MoveTask(_ context.Context, taskID uuid.UUID, input MoveTaskInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moves = append(f.moves, moveCall{taskID: taskID, input: input})
	return f.err
}

func (f *fakeTaskMover) calls() []moveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]moveCall(nil), f.moves...)
}
