package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupCustomFieldService returns a customFieldService wired to fresh mocks.
func setupCustomFieldService() (*customFieldService, *MockCustomFieldDefinitionRepository, *MockActivityLogRepository) {
	fieldRepo := NewMockCustomFieldDefinitionRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewCustomFieldService(fieldRepo, activityRepo).(*customFieldService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, fieldRepo, activityRepo
}

// ---------------------------------------------------------------------------
// TestCustomFieldService_Create
// ---------------------------------------------------------------------------

func TestCustomFieldService_Create(t *testing.T) {
	projectID := uuid.New()

	tests := []struct {
		name      string
		setup     func(repo *MockCustomFieldDefinitionRepository)
		field     *domain.CustomFieldDefinition
		wantErr   bool
		errCode   int
		checkFunc func(t *testing.T, field *domain.CustomFieldDefinition, repo *MockCustomFieldDefinitionRepository)
	}{
		{
			name:  "success - generates slug and position",
			setup: func(_ *MockCustomFieldDefinitionRepository) {},
			field: &domain.CustomFieldDefinition{
				ProjectID: projectID,
				Name:      "Story Points",
				FieldType: domain.FieldTypeNumber,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, field *domain.CustomFieldDefinition, repo *MockCustomFieldDefinitionRepository) {
				assert.NotEqual(t, uuid.Nil, field.ID)
				assert.Equal(t, "story-points", field.Slug)
				assert.Equal(t, 0, field.Position)
				assert.Equal(t, frozenTime, field.CreatedAt)

				stored := repo.items[field.ID]
				require.NotNil(t, stored)
			},
		},
		{
			name: "success - assigns next position",
			setup: func(repo *MockCustomFieldDefinitionRepository) {
				existingID := uuid.New()
				repo.items[existingID] = &domain.CustomFieldDefinition{
					ID:        existingID,
					ProjectID: projectID,
					Name:      "Existing Field",
					Position:  5,
				}
			},
			field: &domain.CustomFieldDefinition{
				ProjectID: projectID,
				Name:      "New Field",
				FieldType: domain.FieldTypeText,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, field *domain.CustomFieldDefinition, _ *MockCustomFieldDefinitionRepository) {
				assert.Equal(t, 6, field.Position)
			},
		},
		{
			name:  "error - empty name",
			setup: func(_ *MockCustomFieldDefinitionRepository) {},
			field: &domain.CustomFieldDefinition{
				ProjectID: projectID,
				Name:      "",
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fieldRepo, _ := setupCustomFieldService()
			ctx := context.Background()
			tt.setup(fieldRepo)

			err := svc.Create(ctx, tt.field)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, tt.field, fieldRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCustomFieldService_Delete
// ---------------------------------------------------------------------------

func TestCustomFieldService_Delete(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockCustomFieldDefinitionRepository) uuid.UUID
		wantErr bool
		errCode int
	}{
		{
			name: "success",
			setup: func(repo *MockCustomFieldDefinitionRepository) uuid.UUID {
				id := uuid.New()
				repo.items[id] = &domain.CustomFieldDefinition{ID: id, Name: "To Delete"}
				return id
			},
			wantErr: false,
		},
		{
			name: "not found",
			setup: func(_ *MockCustomFieldDefinitionRepository) uuid.UUID {
				return uuid.New()
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fieldRepo, _ := setupCustomFieldService()
			ctx := context.Background()
			id := tt.setup(fieldRepo)

			err := svc.Delete(ctx, id)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				_, exists := fieldRepo.items[id]
				assert.False(t, exists)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCustomFieldService_ListVisibleToAgents
// ---------------------------------------------------------------------------

func TestCustomFieldService_ListVisibleToAgents(t *testing.T) {
	svc, fieldRepo, _ := setupCustomFieldService()
	ctx := context.Background()

	projectID := uuid.New()

	// Visible to agents.
	visibleID := uuid.New()
	fieldRepo.items[visibleID] = &domain.CustomFieldDefinition{
		ID:                visibleID,
		ProjectID:         projectID,
		Name:              "Visible",
		IsVisibleToAgents: true,
	}

	// Not visible to agents.
	hiddenID := uuid.New()
	fieldRepo.items[hiddenID] = &domain.CustomFieldDefinition{
		ID:                hiddenID,
		ProjectID:         projectID,
		Name:              "Hidden",
		IsVisibleToAgents: false,
	}

	result, err := svc.ListVisibleToAgents(ctx, projectID)

	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "Visible", result[0].Name)
}
