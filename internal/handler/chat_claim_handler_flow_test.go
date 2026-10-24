package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// What the database decides (one winner, fencing, expiry) is pinned by the
// DB-backed repo tests; these cover what the handler adds around it: the
// response shape, the long-poll loop, and how store errors reach the wire.

type fakeRunnerStore struct {
	mu       sync.Mutex
	batches  [][]postgres.ChatClaimedTurn // returned one per call, then empty
	claimErr error
	calls    int
	params   postgres.ChatClaimParams
	lease    *postgres.ChatLease
	leaseErr error
}

func (f *fakeRunnerStore) ClaimTurns(_ context.Context, p postgres.ChatClaimParams) ([]postgres.ChatClaimedTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.params = p
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}

func (f *fakeRunnerStore) RenewLease(context.Context, uuid.UUID, []uuid.UUID, uuid.UUID, uuid.UUID, string, int) (*postgres.ChatLease, error) {
	return f.lease, f.leaseErr
}

type fakePresigner struct{ ChatAttachmentStore }

func (fakePresigner) GetPresignedURL(_ context.Context, key string, _ time.Duration, _, _ string) (string, error) {
	return "https://files.test/" + key, nil
}

func runnerCtx(method, target, body string, agent uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Chat-Protocol", "1")
	req.Header.Set(chatRunnerIDHeader, uuid.NewString())
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
	c.Set(mw.ContextKeyAgentID, agent)
	c.Set(mw.ContextKeyWorkspaceID, uuid.New())
	return c, rec
}

func TestChatRunnerClaim_ReturnsTheClaimedTurnWithEverythingTheRunnerNeeds(t *testing.T) {
	agent, att := uuid.New(), uuid.New()
	ws := uuid.New()
	store := &fakeRunnerStore{batches: [][]postgres.ChatClaimedTurn{{{
		TurnID: uuid.New(), ThreadID: uuid.New(), AgentID: agent, ProjectID: uuid.New(), WorkspaceID: ws,
		Attempt: 2, ClaimToken: "ct_secret", ExecutionEpoch: 7, LeaseUntil: time.Now().UTC(),
		RequestedBy: uuid.New(), DisplayName: "Ann", MessageID: uuid.New(), Body: "hello",
		Attachments: []postgres.ChatClaimedAttachment{{ID: att, FileName: "a.png", Mime: "image/png", Size: 5}},
		Runtime:     []byte(`{"session_id":"s1","harness":"h","secret":"x"}`),
	}}}}
	h := NewChatClaimHandler(store, nil, fakePresigner{})
	c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+agent.String()+"&max=9&timeout=5", "", agent)
	require.NoError(t, h.Claim(c))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "the response carries the claim_token")
	assert.Equal(t, chatClaimMaxTurns, store.params.Max, "max is capped at 5")
	assert.Equal(t, []uuid.UUID{agent}, store.params.AgentIDs)

	var body struct {
		Protocol int              `json:"protocol"`
		Turns    []map[string]any `json:"turns"`
		Commands []any            `json:"commands"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Protocol)
	assert.Empty(t, body.Commands)
	require.Len(t, body.Turns, 1)
	turn := body.Turns[0]
	assert.Equal(t, "ct_secret", turn["claim_token"])
	assert.EqualValues(t, 7, turn["execution_epoch"])
	assert.Equal(t, "hello", turn["message"].(map[string]any)["body"])
	assert.Equal(t, "Ann", turn["requested_by"].(map[string]any)["display_name"])
	atts := turn["message"].(map[string]any)["attachments"].([]any)
	require.Len(t, atts, 1)
	assert.Equal(t, "https://files.test/"+chatAttachmentKey(ws, att), atts[0].(map[string]any)["download_url"])
	rt := turn["thread_runtime"].(map[string]any)
	assert.Equal(t, "s1", rt["session_id"])
	assert.Nil(t, rt["profile"])
	assert.NotContains(t, rt, "secret", "only the four documented runtime fields reach the runner")
}

func TestChatRunnerClaim_ParksThenAnswersWhenATurnAppears(t *testing.T) {
	agent := uuid.New()
	store := &fakeRunnerStore{batches: [][]postgres.ChatClaimedTurn{nil, {{TurnID: uuid.New(), AgentID: agent, ClaimToken: "ct_late"}}}}
	h := NewChatClaimHandler(store, nil, nil)
	c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+agent.String()+"&timeout=30", "", agent)

	start := time.Now()
	require.NoError(t, h.Claim(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.GreaterOrEqual(t, time.Since(start), chatClaimPollNoRedis-100*time.Millisecond, "without Redis the database is re-read on the 2 s tick")
	assert.Equal(t, 2, store.calls)
}

func TestChatRunnerClaim_NothingToDoIs204(t *testing.T) {
	agent := uuid.New()
	t.Run("timeout", func(t *testing.T) {
		c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+agent.String()+"&timeout=1", "", agent)
		require.NoError(t, NewChatClaimHandler(&fakeRunnerStore{}, nil, nil).Claim(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
	t.Run("server shutdown", func(t *testing.T) {
		down := make(chan struct{})
		close(down)
		h := NewChatClaimHandler(&fakeRunnerStore{}, nil, nil)
		h.SetShutdownSignal(down)
		c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+agent.String(), "", agent)
		require.NoError(t, h.Claim(c))
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
	t.Run("client gone", func(t *testing.T) {
		c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+agent.String(), "", agent)
		ctx, cancel := context.WithCancel(c.Request().Context())
		cancel()
		c.SetRequest(c.Request().WithContext(ctx))
		require.NoError(t, NewChatClaimHandler(&fakeRunnerStore{}, nil, nil).Claim(c))
		assert.Zero(t, rec.Body.Len())
	})
}

func TestChatRunnerClaim_StoreFailureIsAServerError(t *testing.T) {
	agent := uuid.New()
	c, rec := runnerCtx(http.MethodGet, "/runner/chat/claim?agents="+agent.String(), "", agent)
	h := NewChatClaimHandler(&fakeRunnerStore{claimErr: errors.New("db down")}, nil, nil)
	if err := h.Claim(c); err != nil {
		c.Echo().HTTPErrorHandler(err, c)
	}
	assert.GreaterOrEqual(t, rec.Code, 500)
}

func TestChatRunnerLease_Answers(t *testing.T) {
	agent, turn := uuid.New(), uuid.New()
	call := func(store *fakeRunnerStore, id, body string) (*httptest.ResponseRecorder, map[string]any) {
		c, rec := runnerCtx(http.MethodPost, "/runner/chat/turns/"+id+"/lease", body, agent)
		c.SetParamNames("id")
		c.SetParamValues(id)
		require.NoError(t, NewChatClaimHandler(store, nil, nil).RenewLease(c))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	until := time.Now().UTC().Add(time.Minute)
	rec, out := call(&fakeRunnerStore{lease: &postgres.ChatLease{LeaseUntil: until, State: "claimed", ExecutionEpoch: 4}}, turn.String(), `{"claim_token":"ct_x"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "claimed", out["state"])
	assert.EqualValues(t, 4, out["execution_epoch"])
	assert.Equal(t, false, out["cancel_requested"])

	rec, out = call(&fakeRunnerStore{leaseErr: &apierror.Error{Code: 409, ErrorCode: postgres.ChatErrStaleClaim, Message: "stale"}}, turn.String(), `{"claim_token":"ct_old"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, postgres.ChatErrStaleClaim, out["error_code"], "the fence answer reaches the runner verbatim")

	rec, out = call(&fakeRunnerStore{}, "not-a-uuid", `{"claim_token":"ct_x"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, postgres.ChatErrTurnNotFound, out["error_code"])

	rec, _ = call(&fakeRunnerStore{}, turn.String(), `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "claim_token is required")
}
