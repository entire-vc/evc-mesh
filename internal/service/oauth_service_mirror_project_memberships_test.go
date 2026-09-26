package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These are mock-backed (no Postgres) unit tests for registerConnectorAgent's
// defensive branches — the DB-backed happy path already lives in
// TestOAuthSvc_Decide_MirrorsProjectMembershipsOntoConnectorAgent
// (oauth_service_db_test.go). Both cases here are best-effort failure modes
// that are awkward to force through a real database without breaking the
// whole connection, so a mock projectMemberRepo/agentService is the more
// direct proof (task ec0bc566).

// TestOAuthSvc_RegisterConnectorAgent_MirrorFailureIsBestEffort proves a
// broken project-membership lookup is logged and swallowed, never surfaced as
// a registration failure — the new connector agent still comes back usable,
// just without inherited project access this one time.
func TestOAuthSvc_RegisterConnectorAgent_MirrorFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetRegisterResult(&RegisterAgentOutput{Agent: &domain.Agent{ID: agentID}}, nil)

	memberRepo := NewMockProjectMemberRepository()
	memberRepo.SetListByWorkspaceAndUserErr(errors.New("connection reset"))

	svc := &oauthService{agentService: agentSvc, projectMemberRepo: memberRepo}

	out, oerr := svc.registerConnectorAgent(context.Background(), uuid.New(), "Broken Lookup App", uuid.New())
	require.Nil(t, oerr, "a mirroring failure must not fail connector-agent registration")
	require.NotNil(t, out)
	assert.Equal(t, agentID, out.Agent.ID)
}

// TestOAuthSvc_RegisterConnectorAgent_MirrorCreateFailureIsBestEffort proves
// a per-membership Create failure (e.g. one project write hits a constraint
// violation) is logged and does not fail connector-agent registration either
// — mirroring is a strict best-effort add-on, not a transaction the whole
// registration rolls back on.
func TestOAuthSvc_RegisterConnectorAgent_MirrorCreateFailureIsBestEffort(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetRegisterResult(&RegisterAgentOutput{Agent: &domain.Agent{ID: agentID}}, nil)

	supervisor := uuid.New()
	memberRepo := NewMockProjectMemberRepository()
	memberRepo.members = []*domain.ProjectMember{{ID: uuid.New(), ProjectID: uuid.New(), UserID: &supervisor, Role: "member"}}
	memberRepo.SetCreateErr(errors.New("duplicate key"))

	svc := &oauthService{agentService: agentSvc, projectMemberRepo: memberRepo}

	out, oerr := svc.registerConnectorAgent(context.Background(), uuid.New(), "Broken Create App", supervisor)
	require.Nil(t, oerr, "a per-membership create failure must not fail connector-agent registration")
	require.NotNil(t, out)
	assert.Equal(t, agentID, out.Agent.ID)
}

// TestOAuthSvc_RegisterConnectorAgent_NilProjectMemberRepoIsANoop proves the
// nil guard: an oauthService wired without a projectMemberRepo must still
// register the connector agent, just without mirroring.
func TestOAuthSvc_RegisterConnectorAgent_NilProjectMemberRepoIsANoop(t *testing.T) {
	agentID := uuid.New()
	agentSvc := NewMockAgentService()
	agentSvc.SetRegisterResult(&RegisterAgentOutput{Agent: &domain.Agent{ID: agentID}}, nil)

	svc := &oauthService{agentService: agentSvc}

	out, oerr := svc.registerConnectorAgent(context.Background(), uuid.New(), "No Repo App", uuid.New())
	require.Nil(t, oerr)
	require.NotNil(t, out)
	assert.Equal(t, agentID, out.Agent.ID)
}
