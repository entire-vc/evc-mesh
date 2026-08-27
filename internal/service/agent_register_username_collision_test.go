package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Task fee35355: agents.slug and users.username live in separate namespaces
// nothing else kept disjoint. Before this fix, Register would happily create
// an agent whose slug matches an existing workspace member's username — the
// exact ambiguous-@-mention state task f4f47938 had to cope with after the
// fact, without anything stopping it from being created in the first place.

// TestAgentService_Register_RejectsSlugMatchingExistingMemberUsername is the
// RED case: a user "mario" already exists as a member of the workspace, and
// registering a new agent named "Mario" (slugify → "mario") must be refused,
// naming the reason — not a generic validation failure the caller has to
// guess at.
func TestAgentService_Register_RejectsSlugMatchingExistingMemberUsername(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()
	userRepo := svc.userRepo.(*MockUserRepository)

	existingUser := &domain.User{ID: uuid.New(), Username: "mario", Email: "mario@example.com"}
	userRepo.AddUser(ws.ID, existingUser)

	_, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Mario",
	})
	require.Error(t, err)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode(), "collision must be reported as a conflict, not a generic failure")
	assert.Contains(t, err.Error(), "mario", "the refusal must name who already holds the handle, not just say 'validation failed'")

	// Nothing should have been created.
	agent, getErr := agentRepo.GetBySlug(context.Background(), ws.ID, "mario")
	require.NoError(t, getErr)
	assert.Nil(t, agent, "no agent should exist after a rejected registration")
}

// TestAgentService_Register_FreeNameStillWorks is the negative control: a name
// with no collision at all must keep creating an agent exactly as before this
// fix — the ordinary path must not be disturbed by an added guard.
func TestAgentService_Register_FreeNameStillWorks(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Completely Free Name",
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	agent, err := agentRepo.GetBySlug(context.Background(), ws.ID, "completely-free-name")
	require.NoError(t, err)
	require.NotNil(t, agent, "the ordinary registration path must still create the agent")
}

// TestAgentService_Register_SameSlugDifferentWorkspaceIsNotACollision proves
// the guard is workspace-scoped, per the task's explicit "область — воркспейс,
// а не глобально" requirement: a username taken in a DIFFERENT workspace must
// never block registering an agent with the same slug in THIS workspace — the
// two workspaces have entirely separate rosters.
func TestAgentService_Register_SameSlugDifferentWorkspaceIsNotACollision(t *testing.T) {
	svc, agentRepo, ws := setupAgentService()
	userRepo := svc.userRepo.(*MockUserRepository)

	otherWorkspaceID := uuid.New()
	userRepo.AddUser(otherWorkspaceID, &domain.User{ID: uuid.New(), Username: "hugh", Email: "hugh@example.com"})

	out, err := svc.Register(context.Background(), RegisterAgentInput{
		WorkspaceID: ws.ID,
		Name:        "Hugh",
	})
	require.NoError(t, err, "a username collision in an unrelated workspace must not block this one")
	require.NotNil(t, out)

	agent, err := agentRepo.GetBySlug(context.Background(), ws.ID, "hugh")
	require.NoError(t, err)
	require.NotNil(t, agent)
}
