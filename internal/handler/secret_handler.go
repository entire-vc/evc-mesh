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
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// SecretHandler serves the write-only secrets CRUD (task #64e84eb1, S3).
//
// "Write-only" is not a convention this file follows carefully — it is a
// property of the types it is built from. Every method here calls
// service.SecretService, whose every method returns domain.Secret, which has
// no field a plaintext value could occupy. The one type that can carry a
// value, domain.MaterializedSecret, is reachable only through
// SecretMaterializationService, which this handler deliberately does not
// hold: there is no field on SecretHandler through which a future edit could
// reach a decrypt without also adding a dependency that review would see.
//
// The masked response fields are the same ones scripts/env-inventory.py
// prints (name, sha256[:8], length, char class) so an operator comparing the
// UI against the host inventory is comparing like with like.
type SecretHandler struct {
	secretSvc   service.SecretService
	activitySvc service.ActivityLogService
}

// NewSecretHandler returns a SecretHandler. activitySvc is required rather
// than optional: AC4 makes the audit entry part of what a mutation IS, and
// an optional dependency is one a wiring mistake can silently drop.
func NewSecretHandler(secretSvc service.SecretService, activitySvc service.ActivityLogService) *SecretHandler {
	return &SecretHandler{secretSvc: secretSvc, activitySvc: activitySvc}
}

// secretResponse is the masked view of a secret. It is a named response type
// rather than domain.Secret reused directly so that a field added to the
// domain type for the materializer's benefit cannot start appearing on the
// wire here without someone deciding to add it. There is deliberately no
// `value` field and no way to add one that the compiler would accept
// silently — service.SecretService has nothing to populate it from.
type secretResponse struct {
	ID                uuid.UUID          `json:"id"`
	Name              string             `json:"name"`
	Scope             domain.SecretScope `json:"scope"`
	ProjectID         *uuid.UUID         `json:"project_id,omitempty"`
	AgentID           *uuid.UUID         `json:"agent_id,omitempty"`
	ValueSHA256Prefix string             `json:"value_sha256_prefix"`
	ValueLength       int                `json:"value_length"`
	ValueCharClass    string             `json:"value_char_class"`
	ExpiresAt         *time.Time         `json:"expires_at,omitempty"`
	CreatedBy         uuid.UUID          `json:"created_by"`
	CreatedByType     domain.ActorType   `json:"created_by_type"`
	CreatedAt         time.Time          `json:"created_at"`
	RotatedAt         *time.Time         `json:"rotated_at,omitempty"`
}

func toSecretResponse(s domain.Secret) secretResponse {
	return secretResponse{
		ID:                s.ID,
		Name:              s.Name,
		Scope:             s.Scope,
		ProjectID:         s.ProjectID,
		AgentID:           s.AgentID,
		ValueSHA256Prefix: s.ValueSHA256Prefix,
		ValueLength:       s.ValueLength,
		ValueCharClass:    s.ValueCharClass,
		ExpiresAt:         s.ExpiresAt,
		CreatedBy:         s.CreatedBy,
		CreatedByType:     s.CreatedByType,
		CreatedAt:         s.CreatedAt,
		RotatedAt:         s.RotatedAt,
	}
}

// createSecretRequest is the create body. Value is the only field that ever
// carries plaintext, and it exists on this struct alone — it is copied into
// domain.CreateSecretInput and never into anything that outlives the call.
type createSecretRequest struct {
	Name      string             `json:"name"`
	Scope     domain.SecretScope `json:"scope"`
	ProjectID *uuid.UUID         `json:"project_id,omitempty"`
	AgentID   *uuid.UUID         `json:"agent_id,omitempty"`
	Value     string             `json:"value"`
	ExpiresAt *time.Time         `json:"expires_at,omitempty"`
}

// rotateSecretRequest carries only the replacement value and expiry. Name and
// scope are deliberately absent: they come from the row the :secret_id names,
// so a rotate cannot quietly re-point a secret at a different identity, and a
// caller cannot rename a secret by rotating it.
type rotateSecretRequest struct {
	Value     string     `json:"value"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Create handles POST /workspaces/:ws_id/secrets.
func (h *SecretHandler) Create(c echo.Context) error {
	workspaceID, err := middleware.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace_id could not be resolved"))
	}

	var req createSecretRequest
	if decErr := json.NewDecoder(c.Request().Body).Decode(&req); decErr != nil {
		// The body of a failed decode is never echoed back or logged: on this
		// route a malformed body is most likely a well-formed secret with a
		// stray character, and quoting it would put the value in the response
		// and the log — the exact leak this feature closes.
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())
	secret, err := h.secretSvc.Create(c.Request().Context(), domain.CreateSecretInput{
		WorkspaceID:   workspaceID,
		ProjectID:     req.ProjectID,
		AgentID:       req.AgentID,
		Scope:         req.Scope,
		Name:          req.Name,
		Value:         req.Value,
		ExpiresAt:     req.ExpiresAt,
		CreatedBy:     actorID,
		CreatedByType: actorType,
	})
	if err != nil {
		return handleError(c, err)
	}

	h.logSecretActivity(c, "secret_created", secret)
	return c.JSON(http.StatusCreated, toSecretResponse(secret))
}

// List handles GET /workspaces/:ws_id/secrets.
//
// Optional project_id / agent_id query parameters widen the resolution the
// same way the spawn materializer's do: workspace-scoped secrets always,
// plus the project- or agent-scoped ones for the ids given. Passing neither
// lists the workspace-scoped set alone.
func (h *SecretHandler) List(c echo.Context) error {
	workspaceID, err := middleware.GetWorkspaceID(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace_id could not be resolved"))
	}

	projectID, err := optionalUUIDQueryParam(c, "project_id")
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("project_id must be a uuid"))
	}
	agentID, err := optionalUUIDQueryParam(c, "agent_id")
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("agent_id must be a uuid"))
	}

	secrets, err := h.secretSvc.List(c.Request().Context(), workspaceID, projectID, agentID)
	if err != nil {
		return handleError(c, err)
	}

	out := make([]secretResponse, 0, len(secrets))
	for _, s := range secrets {
		out = append(out, toSecretResponse(s))
	}
	return c.JSON(http.StatusOK, out)
}

// Rotate handles POST /secrets/:secret_id/rotate.
//
// Rotation is an insert, not an edit: the row named by :secret_id is stamped
// rotated_at and a new current row takes its place. The old value is not read
// back to be shown, pre-filled, or diffed — there is no code path here that
// could, which is what makes "replace" safe to expose in a UI at all.
func (h *SecretHandler) Rotate(c echo.Context) error {
	workspaceID, current, ok, err := h.resolveCurrentSecret(c)
	if !ok {
		return err
	}

	var req rotateSecretRequest
	if decErr := json.NewDecoder(c.Request().Body).Decode(&req); decErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())
	rotated, err := h.secretSvc.Rotate(c.Request().Context(), workspaceID,
		current.Scope, current.ProjectID, current.AgentID, current.Name,
		domain.CreateSecretInput{
			Value:         req.Value,
			ExpiresAt:     req.ExpiresAt,
			CreatedBy:     actorID,
			CreatedByType: actorType,
		})
	if err != nil {
		return handleError(c, err)
	}

	h.logSecretActivity(c, "secret_rotated", rotated)
	return c.JSON(http.StatusOK, toSecretResponse(rotated))
}

// Delete handles DELETE /secrets/:secret_id. The row is stamped rotated_at
// with no replacement, so the secret stops materializing while its write
// history stays queryable — the same append-only posture as activity_log.
func (h *SecretHandler) Delete(c echo.Context) error {
	workspaceID, current, ok, err := h.resolveCurrentSecret(c)
	if !ok {
		return err
	}

	actorID, actorType := actorctx.FromContext(c.Request().Context())
	if err := h.secretSvc.Delete(c.Request().Context(), workspaceID,
		current.Scope, current.ProjectID, current.AgentID, current.Name, actorID, actorType); err != nil {
		return handleError(c, err)
	}

	h.logSecretActivity(c, "secret_deleted", current)
	return c.NoContent(http.StatusNoContent)
}

// resolveCurrentSecret turns :secret_id into the (scope, name) identity that
// Rotate and Delete operate on, and refuses ids that name a superseded row.
//
// That refusal is the point of doing the lookup at all. Rotate and Delete
// address a secret by identity, not by row id, so handing them the name from
// a superseded row would apply the write to whichever row is CURRENT now —
// succeeding, returning 200, and acting on a different row than the caller
// named. A UI list that went stale between render and click is exactly how
// that happens, so it fails as 409 instead.
//
// The bool, not the error, is what says whether the caller may proceed:
// every refusal path here ends in c.JSON, which returns nil on success, so a
// non-nil error is precisely the case where writing the refusal ITSELF
// failed. Keying the early return on err != nil would let every refusal fall
// straight through into the mutation it had just declined — the first
// version of this file did exactly that, and the two tests below caught it.
func (h *SecretHandler) resolveCurrentSecret(c echo.Context) (uuid.UUID, domain.Secret, bool, error) {
	workspaceID, err := middleware.GetWorkspaceID(c)
	if err != nil {
		return uuid.Nil, domain.Secret{}, false, c.JSON(http.StatusBadRequest, apierror.BadRequest("workspace_id could not be resolved"))
	}

	secretID, err := uuid.Parse(c.Param("secret_id"))
	if err != nil {
		return uuid.Nil, domain.Secret{}, false, c.JSON(http.StatusBadRequest, apierror.BadRequest("secret_id must be a uuid"))
	}

	current, err := h.secretSvc.GetByID(c.Request().Context(), workspaceID, secretID)
	if err != nil {
		return uuid.Nil, domain.Secret{}, false, handleError(c, err)
	}
	if current.RotatedAt != nil {
		return uuid.Nil, domain.Secret{}, false, c.JSON(http.StatusConflict, apierror.Conflict(
			"this secret version has already been superseded — reload the list and act on the current one"))
	}
	return workspaceID, current, true, nil
}

// logSecretActivity writes the AC4 audit entry: who did what to which secret,
// by NAME. The changes payload carries the name, the scope and the value
// fingerprint — never the value, and never an old/new pair, since for this
// entity the "old value" is precisely what nothing is allowed to read.
//
// The fingerprint is included on purpose: it is what makes the log useful for
// answering "did the value actually change" without being able to answer
// "what is it". It is the same sha256[:8] the masked list already shows.
//
// A failed audit write does not fail the request — the mutation has already
// committed, so returning an error would report a failure that did not happen
// and invite a retry that would 409. It is logged loudly instead, matching how
// taskService.logActivity handles the same situation.
func (h *SecretHandler) logSecretActivity(c echo.Context, action string, secret domain.Secret) {
	actorID, actorType := actorctx.FromContext(c.Request().Context())
	changes, err := json.Marshal(map[string]any{
		"name":                secret.Name,
		"scope":               string(secret.Scope),
		"value_sha256_prefix": secret.ValueSHA256Prefix,
	})
	if err != nil {
		log.Printf("[activity] WARNING: failed to encode %s payload for secret %s: %v", action, secret.ID, err)
		return
	}

	// The audit write uses a fresh context rather than the request's: the
	// request context is cancelled the moment the response is written, and an
	// audit entry that vanishes when the client disconnects is the one an
	// investigation would most want.
	ctx, cancel := context.WithTimeout(actorctx.WithActor(context.Background(), actorID, actorType), 5*time.Second)
	defer cancel()

	if err := h.activitySvc.Log(ctx, &domain.ActivityLog{
		WorkspaceID: secret.WorkspaceID,
		EntityType:  "secret",
		EntityID:    secret.ID,
		Action:      action,
		ActorID:     actorID,
		ActorType:   actorType,
		Changes:     changes,
	}); err != nil {
		log.Printf("[activity] WARNING: failed to log %s for secret %s: %v", action, secret.ID, err)
	}
}

// optionalUUIDQueryParam parses a query parameter that may be absent. An
// absent parameter is nil with no error; a present but unparseable one is an
// error, not a silent nil — a typo'd project_id must not quietly widen or
// narrow which secrets come back.
func optionalUUIDQueryParam(c echo.Context, name string) (*uuid.UUID, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
