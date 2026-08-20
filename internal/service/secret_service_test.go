package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

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
