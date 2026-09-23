package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Activity-log coordinates of an issuance entry. EntityType is its own value
// rather than "secret" because one issuance covers many secrets: the entity
// is the ISSUANCE, and its entity_id is minted per request so each one is a
// separately addressable row.
const (
	materializationEntityType = "secret_materialization"
	materializationAction     = "materialized"
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
	activitySvc        service.ActivityLogService
}

// NewSecretMaterializeHandler returns the handler. activitySvc is required:
// every issuance is journaled before a single value leaves (task #1c9f527d),
// and there is no configuration in which handing out plaintext unrecorded is
// the intended behaviour.
func NewSecretMaterializeHandler(ms service.SecretMaterializationService, activitySvc service.ActivityLogService) *SecretMaterializeHandler {
	return &SecretMaterializeHandler{materializationSvc: ms, activitySvc: activitySvc}
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

	// Journal BEFORE responding, and refuse if the journal write fails:
	// an issuance nobody can later account for is exactly the gap this
	// entry closes, so "no record" must mean "no values". Same database as
	// the secrets themselves — if this insert fails, the resolve above was
	// already on borrowed time.
	if err := h.writeAudit(buildMaterializationAudit(req, secrets, c.Response().Header().Get(echo.HeaderXRequestID))); err != nil {
		log.Printf("[secrets] REFUSED materialize for workspace %s: audit write failed: %v", req.WorkspaceID, err)
		return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable(
			"issuance could not be journaled; no secrets were returned"))
	}

	out := make([]materializedSecretResponse, 0, len(secrets))
	for _, s := range secrets {
		out = append(out, materializedSecretResponse{Name: s.Name, Value: s.Value, Expired: s.Expired})
	}
	return c.JSON(http.StatusOK, out)
}

// materializationAuditSecret is one line of the issuance journal. It carries
// the secret's NAME and the identity of the VERSION handed out — row id plus
// the sha256[:8] fingerprint already shown by the masked list — and has no
// field a value could occupy. That is deliberate: the type, not a filter,
// is what keeps the plaintext out, so a later edit cannot "forget to strip"
// it. TestMaterializeAudit_NeverCarriesAValue pins this.
type materializationAuditSecret struct {
	Name              string    `json:"name"`
	SecretID          uuid.UUID `json:"secret_id"`
	ValueSHA256Prefix string    `json:"value_sha256_prefix"`
	Expired           bool      `json:"expired"`
}

type materializationAudit struct {
	RequestID   string                       `json:"request_id"`
	WorkspaceID uuid.UUID                    `json:"workspace_id"`
	ProjectID   *uuid.UUID                   `json:"project_id"`
	AgentID     *uuid.UUID                   `json:"agent_id"`
	Count       int                          `json:"count"`
	Secrets     []materializationAuditSecret `json:"secrets"`
}

// buildMaterializationAudit turns one issuance into its activity_log row.
// Actor is the agent the secrets were resolved FOR when the spawner named
// one (that is who ends up holding them); otherwise the issuance is
// attributed to the system — the spawn token identifies infra, not a person.
func buildMaterializationAudit(req materializeRequest, secrets []domain.MaterializedSecret, requestID string) (*domain.ActivityLog, error) {
	lines := make([]materializationAuditSecret, 0, len(secrets))
	for _, s := range secrets {
		lines = append(lines, materializationAuditSecret{
			Name:              s.Name,
			SecretID:          s.ID,
			ValueSHA256Prefix: s.ValueSHA256Prefix,
			Expired:           s.Expired,
		})
	}
	changes, err := json.Marshal(materializationAudit{
		RequestID:   requestID,
		WorkspaceID: req.WorkspaceID,
		ProjectID:   req.ProjectID,
		AgentID:     req.AgentID,
		Count:       len(lines),
		Secrets:     lines,
	})
	if err != nil {
		return nil, err
	}

	actorID, actorType := uuid.Nil, domain.ActorTypeSystem
	if req.AgentID != nil {
		actorID, actorType = *req.AgentID, domain.ActorTypeAgent
	}
	return &domain.ActivityLog{
		WorkspaceID: req.WorkspaceID,
		EntityType:  materializationEntityType,
		EntityID:    uuid.New(),
		Action:      materializationAction,
		ActorID:     actorID,
		ActorType:   actorType,
		Changes:     changes,
	}, nil
}

// writeAudit uses a fresh context for the same reason logSecretActivity
// does: the entry must not vanish because the caller hung up mid-request.
func (h *SecretMaterializeHandler) writeAudit(entry *domain.ActivityLog, buildErr error) error {
	if buildErr != nil {
		return buildErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.activitySvc.Log(ctx, entry)
}
