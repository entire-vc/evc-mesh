package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func validInput() domain.CreateSecretInput {
	return domain.CreateSecretInput{
		WorkspaceID:   uuid.New(),
		Scope:         domain.SecretScopeWorkspace,
		Name:          "GH_TOKEN",
		Value:         "some-value",
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeAgent,
	}
}

func TestValidateCreateInput_ValidWorkspaceScopePasses(t *testing.T) {
	assert.NoError(t, validateCreateInput(validInput()))
}

func TestValidateCreateInput_NameMustBeUpperSnake(t *testing.T) {
	cases := []struct {
		name string
		want bool // true = valid
	}{
		{"GH_TOKEN", true},
		{"A", true},
		{"gh_token", false},
		{"GH-TOKEN", false},
		{"1GH_TOKEN", false},
		{"", false},
		{"GH TOKEN", false},
	}
	for _, c := range cases {
		input := validInput()
		input.Name = c.name
		err := validateCreateInput(input)
		if c.want {
			assert.NoError(t, err, "name %q should be valid", c.name)
		} else {
			assert.Error(t, err, "name %q should be rejected", c.name)
			var apiErr *apierror.Error
			assert.ErrorAs(t, err, &apiErr)
		}
	}
}

func TestValidateCreateInput_EmptyValueRejected(t *testing.T) {
	input := validInput()
	input.Value = ""
	assert.Error(t, validateCreateInput(input))
}

func TestValidateCreateInput_ScopeRefMismatchRejected(t *testing.T) {
	proj := uuid.New()
	agent := uuid.New()

	t.Run("workspace scope with project_id", func(t *testing.T) {
		input := validInput()
		input.ProjectID = &proj
		assert.Error(t, validateCreateInput(input))
	})
	t.Run("project scope without project_id", func(t *testing.T) {
		input := validInput()
		input.Scope = domain.SecretScopeProject
		assert.Error(t, validateCreateInput(input))
	})
	t.Run("project scope with agent_id also set", func(t *testing.T) {
		input := validInput()
		input.Scope = domain.SecretScopeProject
		input.ProjectID = &proj
		input.AgentID = &agent
		assert.Error(t, validateCreateInput(input))
	})
	t.Run("agent scope without agent_id", func(t *testing.T) {
		input := validInput()
		input.Scope = domain.SecretScopeAgent
		assert.Error(t, validateCreateInput(input))
	})
	t.Run("agent scope with valid agent_id passes", func(t *testing.T) {
		input := validInput()
		input.Scope = domain.SecretScopeAgent
		input.AgentID = &agent
		assert.NoError(t, validateCreateInput(input))
	})
	t.Run("unknown scope rejected", func(t *testing.T) {
		input := validInput()
		input.Scope = domain.SecretScope("bogus")
		assert.Error(t, validateCreateInput(input))
	})
}

func TestValidateCreateInput_ExpiresAtMustBeFuture(t *testing.T) {
	t.Run("past rejected", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		input := validInput()
		input.ExpiresAt = &past
		assert.Error(t, validateCreateInput(input))
	})
	t.Run("future accepted", func(t *testing.T) {
		future := time.Now().Add(time.Hour)
		input := validInput()
		input.ExpiresAt = &future
		assert.NoError(t, validateCreateInput(input))
	})
	t.Run("nil accepted (no expiry)", func(t *testing.T) {
		input := validInput()
		input.ExpiresAt = nil
		assert.NoError(t, validateCreateInput(input))
	})
}

// --- S3: the containment check the table's foreign keys cannot perform ---

type stubSecretRepo struct {
	assertErr    error
	assertCalled bool
	createCalled bool
	rotateCalled bool
	gotWS        uuid.UUID
	gotProject   *uuid.UUID
	gotAgent     *uuid.UUID
}

func (s *stubSecretRepo) Create(_ context.Context, _ domain.CreateSecretInput) (domain.Secret, error) {
	s.createCalled = true
	return domain.Secret{}, nil
}

func (s *stubSecretRepo) Rotate(_ context.Context, _ uuid.UUID, _ domain.SecretScope, _, _ *uuid.UUID, _ string, _ domain.CreateSecretInput) (domain.Secret, error) {
	s.rotateCalled = true
	return domain.Secret{}, nil
}

func (s *stubSecretRepo) Delete(_ context.Context, _ uuid.UUID, _ domain.SecretScope, _, _ *uuid.UUID, _ string, _ uuid.UUID, _ domain.ActorType) error {
	return nil
}

func (s *stubSecretRepo) ListCurrent(_ context.Context, _ uuid.UUID, _, _ *uuid.UUID) ([]domain.Secret, error) {
	return nil, nil
}

func (s *stubSecretRepo) GetByID(_ context.Context, _, _ uuid.UUID) (domain.Secret, error) {
	return domain.Secret{}, nil
}

func (s *stubSecretRepo) AssertScopeRefInWorkspace(_ context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) error {
	s.assertCalled = true
	s.gotWS, s.gotProject, s.gotAgent = workspaceID, projectID, agentID
	return s.assertErr
}

func projectScopedInput(wsID, projID uuid.UUID) domain.CreateSecretInput {
	in := validInput()
	in.WorkspaceID = wsID
	in.Scope = domain.SecretScopeProject
	in.ProjectID = &projID
	return in
}

func TestSecretService_CreateChecksScopeRefContainment(t *testing.T) {
	wsID, projID := uuid.New(), uuid.New()
	repo := &stubSecretRepo{}
	svc := NewSecretService(repo)

	_, err := svc.Create(context.Background(), projectScopedInput(wsID, projID))
	require.NoError(t, err)

	assert.True(t, repo.assertCalled, "Create wrote without checking the project belongs to the workspace")
	assert.Equal(t, wsID, repo.gotWS)
	require.NotNil(t, repo.gotProject)
	assert.Equal(t, projID, *repo.gotProject)
}

// The check has to run BEFORE the write, not alongside it: a row naming
// another tenant's project satisfies every constraint on the table, so once
// it lands nothing downstream rejects it — it just never materializes.
func TestSecretService_CreateDoesNotWriteWhenContainmentFails(t *testing.T) {
	wsID, projID := uuid.New(), uuid.New()
	repo := &stubSecretRepo{assertErr: apierror.ValidationError(map[string]string{"project_id": "must name a project in this workspace"})}
	svc := NewSecretService(repo)

	_, err := svc.Create(context.Background(), projectScopedInput(wsID, projID))
	require.Error(t, err)
	assert.False(t, repo.createCalled, "a foreign project_id was written to the secrets table")
}

func TestSecretService_RotateDoesNotWriteWhenContainmentFails(t *testing.T) {
	wsID, projID := uuid.New(), uuid.New()
	repo := &stubSecretRepo{assertErr: apierror.ValidationError(map[string]string{"project_id": "must name a project in this workspace"})}
	svc := NewSecretService(repo)

	_, err := svc.Rotate(context.Background(), wsID, domain.SecretScopeProject, &projID, nil, "GH_TOKEN",
		domain.CreateSecretInput{Value: "v", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser})
	require.Error(t, err)
	assert.False(t, repo.rotateCalled)
}

// Validation still runs first: an invalid name must not cost a DB round trip.
func TestSecretService_CreateRejectsBadInputBeforeTouchingTheDB(t *testing.T) {
	repo := &stubSecretRepo{}
	svc := NewSecretService(repo)

	in := validInput()
	in.Name = "lower_case"
	_, err := svc.Create(context.Background(), in)
	require.Error(t, err)
	assert.False(t, repo.assertCalled)
	assert.False(t, repo.createCalled)
}
