package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fakeTurnStore struct {
	started    *postgres.ChatStarted
	startErr   error
	session    *postgres.ChatSessionContext
	sessionErr error
	gotEpoch   int64
	gotToken   string
}

func (f *fakeTurnStore) StartTurn(_ context.Context, _ uuid.UUID, _ []uuid.UUID, _, _ uuid.UUID, token string, _ int) (*postgres.ChatStarted, error) {
	f.gotToken = token
	return f.started, f.startErr
}

func (f *fakeTurnStore) SessionContext(_ context.Context, _ uuid.UUID, _ []uuid.UUID, _, _ uuid.UUID, _ string, epoch int64) (*postgres.ChatSessionContext, error) {
	f.gotEpoch = epoch
	return f.session, f.sessionErr
}

func (f *fakeTurnStore) SessionGeneration(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 2, nil
}

type fakeSessions struct {
	service.ChatSessionService
	binding service.ChatSessionBinding
	err     error
}

func (f *fakeSessions) Issue(_ context.Context, b service.ChatSessionBinding) (*service.ChatSessionIssued, error) {
	f.binding = b
	if f.err != nil {
		return nil, f.err
	}
	return &service.ChatSessionIssued{Token: "cst_x", ExpiresAt: time.Now().Add(5 * time.Minute), Binding: b}, nil
}

// turnCall drives one start / session-token handler as a runner of a fresh agent.
func turnCall(t *testing.T, fn echo.HandlerFunc, body, epoch, turnParam string) (rec *httptest.ResponseRecorder, out map[string]any) {
	t.Helper()
	c, rec := runnerCtx(http.MethodPost, "/runner/chat/turns/x", body, uuid.New())
	c.SetParamNames("id")
	c.SetParamValues(turnParam)
	if epoch != "" {
		c.Request().Header.Set(chatEpochHeader, epoch)
	}
	require.NoError(t, fn(c))
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestChatTurnStart_ReturnsTheStreamingState(t *testing.T) {
	store := &fakeTurnStore{started: &postgres.ChatStarted{State: "streaming", LeaseUntil: time.Now().UTC(), LastSeq: 0, ExecutionEpoch: 4}}
	h := NewChatTurnStartHandler(store, nil)

	rec, out := turnCall(t, h.Start, `{"claim_token":"ct_1"}`, "", uuid.NewString())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "streaming", out["state"])
	assert.EqualValues(t, 4, out["execution_epoch"])
	assert.Equal(t, false, out["cancel_requested"])
	assert.Equal(t, "ct_1", store.gotToken)
}

func TestChatTurnStart_RefusalsReachTheWire(t *testing.T) {
	store := &fakeTurnStore{startErr: apierror.ChatRunner(http.StatusConflict, "chat_stale_claim", "stale", nil)}
	h := NewChatTurnStartHandler(store, nil)

	rec, out := turnCall(t, h.Start, `{"claim_token":"old"}`, "", uuid.NewString())
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "chat_stale_claim", out["error_code"])

	rec, _ = turnCall(t, h.Start, `{}`, "", uuid.NewString())
	assert.Equal(t, http.StatusBadRequest, rec.Code, "claim_token is required")
	rec, _ = turnCall(t, h.Start, `{"claim_token":"ct"}`, "", "not-a-uuid")
	assert.Equal(t, http.StatusNotFound, rec.Code, "an unparsable turn id looks like an unknown turn")
}

func TestChatTurnSessionToken_IssuesFromTheTurnsOwnBinding(t *testing.T) {
	sc := &postgres.ChatSessionContext{UserID: uuid.New(), AgentID: uuid.New(), WorkspaceID: uuid.New(), ProjectID: uuid.New(),
		ThreadID: uuid.New(), TurnID: uuid.New(), ExecutionEpoch: 4}
	store := &fakeTurnStore{session: sc}
	sessions := &fakeSessions{}
	h := NewChatTurnStartHandler(store, sessions)

	rec, out := turnCall(t, h.SessionToken, `{"claim_token":"ct_1"}`, "4", uuid.NewString())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "cst_x", out["token"])
	assert.Equal(t, "chat_session", out["kind"])
	assert.EqualValues(t, 2, out["generation"])
	assert.EqualValues(t, 4, out["execution_epoch"])
	assert.EqualValues(t, 4, store.gotEpoch)
	assert.Equal(t, sc.UserID, sessions.binding.UserID, "the user comes from the turn, not the request")
	assert.Equal(t, sc.TurnID, sessions.binding.ChatSessionID)
	assert.Equal(t, sc.ThreadID, sessions.binding.ChatThreadID)
}

func TestChatTurnSessionToken_NothingIsIssuedOnRefusal(t *testing.T) {
	sessions := &fakeSessions{}
	store := &fakeTurnStore{sessionErr: apierror.ChatRunner(http.StatusConflict, "chat_not_started", "not started", nil)}
	h := NewChatTurnStartHandler(store, sessions)

	rec, out := turnCall(t, h.SessionToken, `{"claim_token":"ct"}`, "1", uuid.NewString())
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "chat_not_started", out["error_code"])
	assert.NotContains(t, rec.Body.String(), "cst_")
	assert.Equal(t, uuid.Nil, sessions.binding.UserID, "Issue was never reached")

	rec, _ = turnCall(t, h.SessionToken, `{"claim_token":"ct"}`, "", uuid.NewString())
	assert.Equal(t, http.StatusBadRequest, rec.Code, "X-Chat-Epoch is required")
	rec, _ = turnCall(t, h.SessionToken, `{}`, "1", uuid.NewString())
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	store.sessionErr, store.session = nil, &postgres.ChatSessionContext{ExecutionEpoch: 1}
	sessions.err = apierror.Forbidden("user is not a project member")
	rec, _ = turnCall(t, h.SessionToken, `{"claim_token":"ct"}`, "1", uuid.NewString())
	assert.Equal(t, http.StatusForbidden, rec.Code, "a failed liveness check on the user or agent issues no token")
}

// D3 at the handler: without the holder's runner id the history is refused
// before the database is touched (no query is expected on the mock).
func TestChatRunnerHistory_NoRunnerIdentityIsForbiddenBeforeAnyRead(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer raw.Close()
	h := NewChatRunnerHandler(postgres.NewChatRunnerRepo(sqlx.NewDb(raw, "postgres")))

	for name, runner := range map[string]string{"missing": "", "malformed": "nope"} {
		c, rec := runnerCtx(http.MethodGet, "/history", "", uuid.New())
		c.Request().Header.Del(chatRunnerIDHeader)
		if runner != "" {
			c.Request().Header.Set(chatRunnerIDHeader, runner)
		}
		c.SetParamNames("thread_id")
		c.SetParamValues(uuid.NewString())
		require.NoError(t, h.History(c), name)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Contains(t, rec.Body.String(), "chat_history_forbidden", name)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
