package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// SecretMaterializeHandler serves the ONE endpoint in this codebase that
// returns a decrypted secret value. It must sit behind
// middleware.SpawnAuth() only — never JWTAuth, never agent-key auth — since
// any agent identity able to authenticate normally would be able to decrypt
// every secret in its own scope, which is the leak this whole feature
// exists to close. See middleware.SpawnAuth's doc comment for the trust
// model this depends on.
type SecretMaterializeHandler struct {
	materializationSvc service.SecretMaterializationService
}

func NewSecretMaterializeHandler(ms service.SecretMaterializationService) *SecretMaterializeHandler {
	return &SecretMaterializeHandler{materializationSvc: ms}
}

// materializeRequest carries the resolution the caller wants secrets for.
// WorkspaceID is required; ProjectID and AgentID are optional, and their
// presence controls whether project- or agent-scoped secrets are included
// alongside workspace-scoped ones — see SecretMaterializationService.ResolveForSpawn.
type materializeRequest struct {
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	AgentID     *uuid.UUID `json:"agent_id,omitempty"`
}

// materializedSecretResponse mirrors domain.MaterializedSecret. A named
// response type, not the domain type reused directly, so a field added to
// MaterializedSecret for an unrelated caller does not silently start
// appearing on the wire here without a deliberate decision to expose it.
type materializedSecretResponse struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Expired bool   `json:"expired"`
}

// Materialize handles POST /internal/secrets/materialize.
//
// This is the only HTTP response body in this codebase that legitimately
// contains a secret's plaintext value. It is never logged (echo's request
// logger logs method/path/status, not bodies) and never reaches a browser —
// the caller is a spawner process on infra it controls, writing straight
// into an 0600 env file.
func (h *SecretMaterializeHandler) Materialize(c echo.Context) error {
	var req materializeRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.WorkspaceID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace_id is required"))
	}

	secrets, err := h.materializationSvc.ResolveForSpawn(c.Request().Context(), req.WorkspaceID, req.ProjectID, req.AgentID)
	if err != nil {
		return handleError(c, err)
	}

	out := make([]materializedSecretResponse, 0, len(secrets))
	for _, s := range secrets {
		out = append(out, materializedSecretResponse{Name: s.Name, Value: s.Value, Expired: s.Expired})
	}
	return c.JSON(http.StatusOK, out)
}
