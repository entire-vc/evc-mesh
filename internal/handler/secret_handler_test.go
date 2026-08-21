package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type mockSecretService struct {
	create   func(ctx context.Context, input domain.CreateSecretInput) (domain.Secret, error)
	rotate   func(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error)
	del      func(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, by uuid.UUID, byType domain.ActorType) error
	list     func(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Secret, error)
	getByID  func(ctx context.Context, workspaceID, id uuid.UUID) (domain.Secret, error)
	lastCall string
}

func (m *mockSecretService) Create(ctx context.Context, input domain.CreateSecretInput) (domain.Secret, error) {
	m.lastCall = "create"
	return m.create(ctx, input)
}

func (m *mockSecretService) Rotate(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error) {
	m.lastCall = "rotate"
	return m.rotate(ctx, workspaceID, scope, projectID, agentID, name, input)
}

func (m *mockSecretService) Delete(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, by uuid.UUID, byType domain.ActorType) error {
	m.lastCall = "delete"
	return m.del(ctx, workspaceID, scope, projectID, agentID, name, by, byType)
}

func (m *mockSecretService) List(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Secret, error) {
	m.lastCall = "list"
	return m.list(ctx, workspaceID, projectID, agentID)
}

func (m *mockSecretService) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (domain.Secret, error) {
	return m.getByID(ctx, workspaceID, id)
}

// recordingActivityLog captures entries synchronously. logSecretActivity runs
// inline (not in a goroutine) so a call has always landed by the time the
// handler returns — the audit entry is part of the mutation, not a
// best-effort afterthought scheduled behind it.
type recordingActivityLog struct {
	MockActivityLogService
	entries []domain.ActivityLog
	err     error
}

func (r *recordingActivityLog) Log(_ context.Context, entry *domain.ActivityLog) error {
	r.entries = append(r.entries, *entry)
	return r.err
}

func sampleSecret(wsID uuid.UUID) domain.Secret {
	return domain.Secret{
		ID:                uuid.New(),
		WorkspaceID:       wsID,
		Scope:             domain.SecretScopeWorkspace,
		Name:              "GITHUB_TOKEN",
		ValueSHA256Prefix: "deadbeef",
		ValueLength:       40,
		ValueCharClass:    "a-z+A-Z+0-9",
		CreatedBy:         uuid.New(),
		CreatedByType:     domain.ActorTypeUser,
		CreatedAt:         time.Now(),
	}
}

// newSecretCtx builds a request context the way the auth + workspace
// middlewares leave it: workspace in the Echo context, actor in the Go
// context.
func newSecretCtx(t *testing.T, method, target, body string, wsID, actorID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	var rdr *bytes.Buffer
	if body == "" {
		rdr = bytes.NewBufferString("")
	} else {
		rdr = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(actorctx.WithActor(req.Context(), actorID, domain.ActorTypeUser))
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.ContextKeyWorkspaceID, wsID)
	return c, rec
}

// TestSecretHandler_CreateNeverEchoesTheValue is the handler-level half of
// AC2. The service is handed a real plaintext and the response is searched
// for it as a raw substring rather than by decoding into a struct — decoding
// would only prove that the field NAMES we know about are absent, which is
// exactly the check that would still pass if a value leaked under a name
// nobody thought to look for.
func TestSecretHandler_CreateNeverEchoesTheValue(t *testing.T) {
	wsID, actorID := uuid.New(), uuid.New()
	const plaintext = "ghp_ThisMustNeverAppearInAResponse"

	var gotInput domain.CreateSecretInput
	svc := &mockSecretService{create: func(_ context.Context, input domain.CreateSecretInput) (domain.Secret, error) {
		gotInput = input
		return sampleSecret(wsID), nil
	}}
	act := &recordingActivityLog{}
	h := NewSecretHandler(svc, act)

	c, rec := newSecretCtx(t, http.MethodPost, "/api/v1/workspaces/x/secrets",
		`{"name":"GITHUB_TOKEN","scope":"workspace","value":"`+plaintext+`"}`, wsID, actorID)
	require.NoError(t, h.Create(c))

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, plaintext, gotInput.Value, "the service must still receive the value")
	assert.Equal(t, wsID, gotInput.WorkspaceID, "workspace comes from the guarded context, not the body")
	assert.Equal(t, actorID, gotInput.CreatedBy)
	assert.NotContains(t, rec.Body.String(), plaintext, "the create response echoed the secret back")
	assert.NotContains(t, rec.Body.String(), `"value"`, "the response carries a value field")
}

// TestSecretHandler_ActivityEntryNamesTheSecretNotItsValue is AC4: an audit
// row per mutation, carrying the name and never the value.
func TestSecretHandler_ActivityEntryNamesTheSecretNotItsValue(t *testing.T) {
	wsID, actorID := uuid.New(), uuid.New()
	const plaintext = "ghp_AuditMustNotCarryThis"
	secret := sampleSecret(wsID)

	svc := &mockSecretService{create: func(_ context.Context, _ domain.CreateSecretInput) (domain.Secret, error) {
		return secret, nil
	}}
	act := &recordingActivityLog{}
	h := NewSecretHandler(svc, act)

	c, _ := newSecretCtx(t, http.MethodPost, "/api/v1/workspaces/x/secrets",
		`{"name":"GITHUB_TOKEN","scope":"workspace","value":"`+plaintext+`"}`, wsID, actorID)
	require.NoError(t, h.Create(c))

	require.Len(t, act.entries, 1)
	entry := act.entries[0]
	assert.Equal(t, "secret", entry.EntityType)
	assert.Equal(t, "secret_created", entry.Action)
	assert.Equal(t, secret.ID, entry.EntityID)
	assert.Equal(t, wsID, entry.WorkspaceID)
	assert.Equal(t, actorID, entry.ActorID)
	assert.Contains(t, string(entry.Changes), "GITHUB_TOKEN", "the audit entry must name the secret")
	assert.NotContains(t, string(entry.Changes), plaintext, "the audit entry carried the secret's value")
}

// A failed audit write must not fail a mutation that already committed —
// reporting a failure that did not happen invites a retry that would 409.
func TestSecretHandler_CreateSucceedsWhenTheAuditWriteFails(t *testing.T) {
	wsID := uuid.New()
	svc := &mockSecretService{create: func(_ context.Context, _ domain.CreateSecretInput) (domain.Secret, error) {
		return sampleSecret(wsID), nil
	}}
	act := &recordingActivityLog{err: assert.AnError}
	h := NewSecretHandler(svc, act)

	c, rec := newSecretCtx(t, http.MethodPost, "/api/v1/workspaces/x/secrets",
		`{"name":"GITHUB_TOKEN","scope":"workspace","value":"v"}`, wsID, uuid.New())
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// A malformed create body must not be quoted back. On this route the most
// likely malformed body is a well-formed secret with a stray character in it.
func TestSecretHandler_CreateDoesNotQuoteAMalformedBody(t *testing.T) {
	wsID := uuid.New()
	h := NewSecretHandler(&mockSecretService{}, &recordingActivityLog{})

	c, rec := newSecretCtx(t, http.MethodPost, "/api/v1/workspaces/x/secrets",
		`{"name":"GITHUB_TOKEN","value":"ghp_MalformedButStillASecret`, wsID, uuid.New())
	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.NotContains(t, rec.Body.String(), "ghp_MalformedButStillASecret")
}

func TestSecretHandler_ListReturnsMaskedFieldsOnly(t *testing.T) {
	wsID := uuid.New()
	svc := &mockSecretService{list: func(_ context.Context, _ uuid.UUID, _, _ *uuid.UUID) ([]domain.Secret, error) {
		return []domain.Secret{sampleSecret(wsID)}, nil
	}}
	h := NewSecretHandler(svc, &recordingActivityLog{})

	c, rec := newSecretCtx(t, http.MethodGet, "/api/v1/workspaces/x/secrets", "", wsID, uuid.New())
	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var out []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)

	// Only name, value_sha256_prefix, value_length and value_char_class are
	// 1:1 with what scripts/env-inventory.py prints (NAME / FP / LEN / CHARS).
	// scope, created_by and created_at asserted below are Mesh's own audit
	// fields with no counterpart in the script — it scans static
	// .env/plist/JSON files, which carry no such metadata — so this list is
	// a superset of the script's output, not a copy of it.
	for _, field := range []string{"name", "scope", "value_sha256_prefix", "value_length", "value_char_class", "created_by", "created_at"} {
		assert.Contains(t, out[0], field, "masked list is missing %s", field)
	}
	assert.NotContains(t, out[0], "value")
	assert.NotContains(t, out[0], "encrypted_value")
}

func TestSecretHandler_ListPassesScopeFiltersThrough(t *testing.T) {
	wsID, projID, agentID := uuid.New(), uuid.New(), uuid.New()
	var gotProj, gotAgent *uuid.UUID
	svc := &mockSecretService{list: func(_ context.Context, _ uuid.UUID, p, a *uuid.UUID) ([]domain.Secret, error) {
		gotProj, gotAgent = p, a
		return nil, nil
	}}
	h := NewSecretHandler(svc, &recordingActivityLog{})

	c, rec := newSecretCtx(t, http.MethodGet,
		"/api/v1/workspaces/x/secrets?project_id="+projID.String()+"&agent_id="+agentID.String(), "", wsID, uuid.New())
	require.NoError(t, h.List(c))

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, gotProj)
	require.NotNil(t, gotAgent)
	assert.Equal(t, projID, *gotProj)
	assert.Equal(t, agentID, *gotAgent)
}

// An unparseable filter must be an error, not a silent nil: silently dropping
// it would answer a narrower question than the caller asked while looking
// like a successful answer to the one they did ask.
func TestSecretHandler_ListRejectsAMalformedFilter(t *testing.T) {
	h := NewSecretHandler(&mockSecretService{}, &recordingActivityLog{})
	c, rec := newSecretCtx(t, http.MethodGet, "/api/v1/workspaces/x/secrets?project_id=not-a-uuid", "", uuid.New(), uuid.New())
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSecretHandler_RotateUsesTheStoredIdentityNotTheBody(t *testing.T) {
	wsID, actorID := uuid.New(), uuid.New()
	projID := uuid.New()
	current := sampleSecret(wsID)
	current.Scope = domain.SecretScopeProject
	current.ProjectID = &projID
	current.Name = "DEPLOY_KEY"

	var gotScope domain.SecretScope
	var gotName string
	var gotProj *uuid.UUID
	svc := &mockSecretService{
		getByID: func(_ context.Context, _, _ uuid.UUID) (domain.Secret, error) { return current, nil },
		rotate: func(_ context.Context, _ uuid.UUID, scope domain.SecretScope, p, _ *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error) {
			gotScope, gotName, gotProj = scope, name, p
			assert.Equal(t, "new-value", input.Value)
			return current, nil
		},
	}
	act := &recordingActivityLog{}
	h := NewSecretHandler(svc, act)

	// The body tries to rename the secret and move it to workspace scope.
	// Neither field exists on rotateSecretRequest, so both are ignored.
	c, rec := newSecretCtx(t, http.MethodPost, "/api/v1/secrets/x/rotate",
		`{"value":"new-value","name":"ATTACKER_CHOSEN","scope":"workspace"}`, wsID, actorID)
	c.SetParamNames("secret_id")
	c.SetParamValues(current.ID.String())
	require.NoError(t, h.Rotate(c))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.SecretScopeProject, gotScope, "rotate changed the secret's scope")
	assert.Equal(t, "DEPLOY_KEY", gotName, "rotate renamed the secret")
	require.NotNil(t, gotProj)
	assert.Equal(t, projID, *gotProj)
	require.Len(t, act.entries, 1)
	assert.Equal(t, "secret_rotated", act.entries[0].Action)
}

// The stale-list case: a superseded id must fail loudly rather than quietly
// redirecting the write to whichever row is current now.
func TestSecretHandler_RotateRefusesASupersededVersion(t *testing.T) {
	wsID := uuid.New()
	rotatedAt := time.Now().Add(-time.Hour)
	superseded := sampleSecret(wsID)
	superseded.RotatedAt = &rotatedAt

	rotateCalled := false
	svc := &mockSecretService{
		getByID: func(_ context.Context, _, _ uuid.UUID) (domain.Secret, error) { return superseded, nil },
		rotate: func(_ context.Context, _ uuid.UUID, _ domain.SecretScope, _, _ *uuid.UUID, _ string, _ domain.CreateSecretInput) (domain.Secret, error) {
			rotateCalled = true
			return domain.Secret{}, nil
		},
	}
	h := NewSecretHandler(svc, &recordingActivityLog{})

	c, rec := newSecretCtx(t, http.MethodPost, "/api/v1/secrets/x/rotate", `{"value":"v"}`, wsID, uuid.New())
	c.SetParamNames("secret_id")
	c.SetParamValues(superseded.ID.String())
	require.NoError(t, h.Rotate(c))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.False(t, rotateCalled, "a superseded id was allowed through to rotate")
}

func TestSecretHandler_DeleteStampsAndAudits(t *testing.T) {
	wsID, actorID := uuid.New(), uuid.New()
	current := sampleSecret(wsID)

	deleted := false
	svc := &mockSecretService{
		getByID: func(_ context.Context, _, _ uuid.UUID) (domain.Secret, error) { return current, nil },
		del: func(_ context.Context, ws uuid.UUID, scope domain.SecretScope, _, _ *uuid.UUID, name string, by uuid.UUID, _ domain.ActorType) error {
			deleted = true
			assert.Equal(t, wsID, ws)
			assert.Equal(t, current.Name, name)
			assert.Equal(t, actorID, by)
			return nil
		},
	}
	act := &recordingActivityLog{}
	h := NewSecretHandler(svc, act)

	c, rec := newSecretCtx(t, http.MethodDelete, "/api/v1/secrets/x", "", wsID, actorID)
	c.SetParamNames("secret_id")
	c.SetParamValues(current.ID.String())
	require.NoError(t, h.Delete(c))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, deleted)
	require.Len(t, act.entries, 1)
	assert.Equal(t, "secret_deleted", act.entries[0].Action)
	assert.Contains(t, string(act.entries[0].Changes), current.Name)
}

// A foreign or nonexistent id gets one answer, so the endpoint cannot be used
// to learn that a secret exists in another workspace.
func TestSecretHandler_RotateOnAForeignIDIsNotFound(t *testing.T) {
	svc := &mockSecretService{
		getByID: func(_ context.Context, _, _ uuid.UUID) (domain.Secret, error) {
			return domain.Secret{}, apierror.NotFound("Secret")
		},
	}
	h := NewSecretHandler(svc, &recordingActivityLog{})

	c, rec := newSecretCtx(t, http.MethodPost, "/api/v1/secrets/x/rotate", `{"value":"v"}`, uuid.New(), uuid.New())
	c.SetParamNames("secret_id")
	c.SetParamValues(uuid.New().String())
	require.NoError(t, h.Rotate(c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "workspace",
		"the not-found body should not hint at whose workspace the id belongs to")
}

func TestSecretHandler_RejectsAMalformedSecretID(t *testing.T) {
	h := NewSecretHandler(&mockSecretService{}, &recordingActivityLog{})
	c, rec := newSecretCtx(t, http.MethodDelete, "/api/v1/secrets/x", "", uuid.New(), uuid.New())
	c.SetParamNames("secret_id")
	c.SetParamValues("not-a-uuid")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
