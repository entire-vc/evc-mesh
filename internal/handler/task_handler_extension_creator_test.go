package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// A card opened with an extension token is signed by the extension's actor, not
// by uuid.Nil/user (Keep 06 §9 looks the gate card up by created_by).
func TestTaskHandler_Create_ExtensionActorIsCreator(t *testing.T) {
	actorID := uuid.New()
	var got *domain.Task
	mockSvc := &MockTaskService{
		CreateFunc: func(_ context.Context, task *domain.Task) error {
			got = task
			return nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"Gate card"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeExtension)
	c.Set(mw.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{ActorID: actorID, ExtensionID: "keep"})

	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotNil(t, got)
	assert.Equal(t, actorID, got.CreatedBy)
	assert.Equal(t, domain.ActorTypeExtension, got.CreatedByType)
}

// Control: an agent request still signs as the agent.
func TestTaskHandler_Create_AgentIsCreator(t *testing.T) {
	agentID := uuid.New()
	var got *domain.Task
	mockSvc := &MockTaskService{
		CreateFunc: func(_ context.Context, task *domain.Task) error {
			got = task
			return nil
		},
	}
	h, e := setupTaskTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"Agent card"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tasks")
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
	c.Set(mw.ContextKeyAgentID, agentID)

	require.NoError(t, h.Create(c))
	require.NotNil(t, got)
	assert.Equal(t, agentID, got.CreatedBy)
	assert.Equal(t, domain.ActorTypeAgent, got.CreatedByType)
}
