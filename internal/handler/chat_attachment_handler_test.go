package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// These cases are refused before any query runs, so the repository has no DB.
func chatAttachmentUnitHandler(store ChatAttachmentStore) *ChatAttachmentHandler {
	return NewChatAttachmentHandler(postgres.NewChatAttachmentRepo(nil), store)
}

func chatUnitCall(fn echo.HandlerFunc, body string, user, agent uuid.UUID, params map[string]string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != uuid.Nil {
		c.Set(mw.ContextKeyUserID, user)
	}
	if agent != uuid.Nil {
		c.Set(mw.ContextKeyAgentID, agent)
	}
	var names, values []string
	for k, v := range params {
		names, values = append(names, k), append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := fn(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func TestChatAttachmentHandler_PresignRefusals(t *testing.T) {
	h := chatAttachmentUnitHandler(&fakeChatStore{})
	user, project, thread := uuid.New(), uuid.New(), uuid.New()
	ok := presignBody(thread, 100)
	params := map[string]string{"proj_id": project.String()}

	require.Equal(t, http.StatusServiceUnavailable,
		chatUnitCall(chatAttachmentUnitHandler(nil).Presign, ok, user, uuid.Nil, params).Code, "no object storage")
	require.Equal(t, http.StatusBadRequest,
		chatUnitCall(h.Presign, ok, user, uuid.Nil, map[string]string{"proj_id": "nope"}).Code)
	require.Equal(t, http.StatusBadRequest, chatUnitCall(h.Presign, "{not json", user, uuid.Nil, params).Code)

	for name, body := range map[string]string{
		"no thread":       `{"file_name":"a","mime":"text/plain","size":1}`,
		"no file name":    `{"thread_id":"` + thread.String() + `","mime":"text/plain","size":1}`,
		"long file name":  `{"thread_id":"` + thread.String() + `","file_name":"` + strings.Repeat("a", 256) + `","mime":"text/plain","size":1}`,
		"no mime":         `{"thread_id":"` + thread.String() + `","file_name":"a","size":1}`,
		"header injected": `{"thread_id":"` + thread.String() + `","file_name":"a","mime":"text/plain\r\nX: y","size":1}`,
		"zero size":       `{"thread_id":"` + thread.String() + `","file_name":"a","mime":"text/plain","size":0}`,
		"zero width":      `{"thread_id":"` + thread.String() + `","file_name":"a","mime":"image/png","size":1,"width":0,"height":5}`,
	} {
		rec := chatUnitCall(h.Presign, body, user, uuid.Nil, params)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name+": "+rec.Body.String())
	}

	// Red control: the same well-formed body passes validation and reaches the
	// user-only check, which an agent key fails.
	require.Equal(t, http.StatusForbidden, chatUnitCall(h.Presign, ok, uuid.Nil, uuid.New(), params).Code)
}

func TestChatAttachmentHandler_DownloadRefusals(t *testing.T) {
	h := chatAttachmentUnitHandler(&fakeChatStore{})
	user, project, id := uuid.New(), uuid.New(), uuid.New()
	params := map[string]string{"proj_id": project.String(), "attachment_id": id.String()}

	require.Equal(t, http.StatusServiceUnavailable,
		chatUnitCall(chatAttachmentUnitHandler(nil).Download, "", user, uuid.Nil, params).Code)
	for _, p := range []map[string]string{
		{"proj_id": "nope", "attachment_id": id.String()},
		{"proj_id": project.String(), "attachment_id": "nope"},
		{"proj_id": project.String(), "attachment_id": uuid.Nil.String()},
	} {
		rec := chatUnitCall(h.Download, "", user, uuid.Nil, p)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, "attachment_not_found", chatDetails(rec))
	}
	require.Equal(t, http.StatusForbidden, chatUnitCall(h.Download, "", uuid.Nil, uuid.New(), params).Code)
}
