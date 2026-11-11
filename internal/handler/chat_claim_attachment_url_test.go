package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Who may read which attachment is decided in SQL (HeldTurnAttachment, pinned by
// the DB-backed repo test); these cover what the handler adds around it.

type failingPresigner struct{ ChatAttachmentStore }

func (failingPresigner) GetPresignedURL(context.Context, string, time.Duration, string, string) (string, error) {
	return "", errors.New("s3 down")
}

type downloadURLCall struct {
	store    *fakeRunnerStore
	objects  ChatAttachmentStore
	turn     string
	att      string
	protocol string
}

func callDownloadURL(t *testing.T, agent uuid.UUID, d downloadURLCall) (rec *httptest.ResponseRecorder, body map[string]any, ws, runner uuid.UUID) {
	t.Helper()
	target := "/runner/chat/turns/" + d.turn + "/attachments/" + d.att + "/download-url"
	c, rec := runnerCtx(http.MethodGet, target, "", agent)
	if d.protocol != "" {
		c.Request().Header.Set("X-Chat-Protocol", d.protocol)
	}
	c.SetParamNames("id", "attachment_id")
	c.SetParamValues(d.turn, d.att)
	ws, _ = c.Get(mw.ContextKeyWorkspaceID).(uuid.UUID)
	runner = uuid.MustParse(c.Request().Header.Get(chatRunnerIDHeader))
	require.NoError(t, NewChatClaimHandler(d.store, nil, d.objects).AttachmentDownloadURL(c))
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body, ws, runner
}

func TestChatRunnerAttachmentURL_HolderGetsAFreshLink(t *testing.T) {
	agent, turn, att, attWS := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeRunnerStore{att: &postgres.ChatTurnAttachment{ID: att, WorkspaceID: attWS, FileName: "a.png", Mime: "image/png", Size: 5}}
	rec, body, ws, runner := callDownloadURL(t, agent, downloadURLCall{store: store, objects: fakePresigner{}, turn: turn.String(), att: att.String()})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "the link is a bearer credential to the file")
	assert.Equal(t, map[string]any{"download_url": "https://files.test/" + chatAttachmentKey(attWS, att)}, body,
		"the same object key the claim presigns, and nothing else in the answer")
	assert.Equal(t, heldAttachmentArgs{workspace: ws, runner: runner, turn: turn, attachment: att, agents: []uuid.UUID{agent}}, store.attArgs,
		"the store is asked with the caller's own workspace, agent and runner id")
}

func TestChatRunnerAttachmentURL_MalformedIDsAnswerLikeAnUnknownTurn(t *testing.T) {
	agent, good := uuid.New(), uuid.NewString()
	nilID := uuid.Nil.String()
	for _, ids := range [][2]string{{"not-a-uuid", good}, {nilID, good}, {good, "not-a-uuid"}, {good, nilID}} {
		store := &fakeRunnerStore{}
		rec, body, _, _ := callDownloadURL(t, agent, downloadURLCall{store: store, objects: fakePresigner{}, turn: ids[0], att: ids[1]})
		assert.Equal(t, http.StatusNotFound, rec.Code, ids)
		assert.Equal(t, postgres.ChatErrTurnNotFound, body["error_code"], ids)
		assert.Zero(t, store.attCalls, "a malformed id never reaches the database")
	}
}

func TestChatRunnerAttachmentURL_StoreRefusalsReachTheRunnerVerbatim(t *testing.T) {
	agent := uuid.New()
	turn, att := uuid.NewString(), uuid.NewString()

	rec, body, _, _ := callDownloadURL(t, agent, downloadURLCall{store: &fakeRunnerStore{
		attErr: &apierror.Error{Code: 404, ErrorCode: postgres.ChatErrTurnNotFound, Message: "turn not found"}}, objects: fakePresigner{}, turn: turn, att: att})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, postgres.ChatErrTurnNotFound, body["error_code"])

	rec, body, _, _ = callDownloadURL(t, agent, downloadURLCall{store: &fakeRunnerStore{
		attErr: apierror.NotFoundWithDetails("attachment", "attachment_not_found")}, objects: fakePresigner{}, turn: turn, att: att})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "attachment_not_found", body["details"])

	rec, _, _, _ = callDownloadURL(t, agent, downloadURLCall{store: &fakeRunnerStore{attErr: errors.New("db down")}, objects: fakePresigner{}, turn: turn, att: att})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestChatRunnerAttachmentURL_ObjectStorageProblemsAre503(t *testing.T) {
	agent := uuid.New()
	turn, att := uuid.NewString(), uuid.New()
	held := &postgres.ChatTurnAttachment{ID: att, WorkspaceID: uuid.New(), FileName: "a.png", Mime: "image/png", Size: 5}

	t.Run("not configured", func(t *testing.T) {
		store := &fakeRunnerStore{att: held}
		rec, body, _, _ := callDownloadURL(t, agent, downloadURLCall{store: store, turn: turn, att: att.String()})
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, apierror.CodeServiceUnavailable, body["error_code"])
		assert.Zero(t, store.attCalls, "nothing to presign with: the database is not asked")
	})
	t.Run("presign fails", func(t *testing.T) {
		rec, body, _, _ := callDownloadURL(t, agent, downloadURLCall{store: &fakeRunnerStore{att: held}, objects: failingPresigner{}, turn: turn, att: att.String()})
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, apierror.CodeServiceUnavailable, body["error_code"])
		assert.NotContains(t, rec.Body.String(), "s3 down", "the storage error stays server-side")
	})
}

func TestChatRunnerAttachmentURL_RunnerIdentityIsCheckedFirst(t *testing.T) {
	store := &fakeRunnerStore{}
	rec, body, _, _ := callDownloadURL(t, uuid.New(), downloadURLCall{store: store, objects: fakePresigner{},
		turn: uuid.NewString(), att: uuid.NewString(), protocol: "2"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "chat_protocol_unsupported", body["error_code"])
	assert.Zero(t, store.attCalls)
}
