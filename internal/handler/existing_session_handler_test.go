package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// The validation and authentication of the existing-session routes all run
// before any repository call, so a handler without repositories proves that a
// refused request never reaches storage.
func existingSessionCall(t *testing.T, fn echo.HandlerFunc, body string, headers map[string]string, set func(echo.Context)) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	set(c)
	if err := fn(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func asAgentKey(c echo.Context) {
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
	c.Set(mw.ContextKeyAgentID, uuid.New())
	c.Set(mw.ContextKeyWorkspaceID, uuid.New())
}

func sendBody(t *testing.T, mut func(m map[string]any)) string {
	t.Helper()
	text := "read two files"
	sum := sha256.Sum256([]byte(text))
	m := map[string]any{
		"request_id": uuid.NewString(),
		"target": map[string]any{"workspace_id": uuid.NewString(), "agent_id": uuid.NewString(), "runner_id": uuid.NewString(),
			"host_id": "h", "hostname": "host-a.example.invalid", "session_id": "s"},
		"expected_admission_revision": 1, "capability": "readonly_metadata",
		"text": text, "text_sha256": hex.EncodeToString(sum[:]),
	}
	mut(m)
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func TestExistingSessionSendRefusesBadInputBeforeStorage(t *testing.T) {
	h := NewExistingSessionHandler(nil, nil)
	cases := map[string]func(m map[string]any){
		"request id":    func(m map[string]any) { m["request_id"] = "nope" },
		"capability":    func(m map[string]any) { m["capability"] = "exec" },
		"empty text":    func(m map[string]any) { m["text"] = "" },
		"oversize text": func(m map[string]any) { m["text"] = strings.Repeat("a", 16*1024+1) },
		"hash":          func(m map[string]any) { m["text_sha256"] = strings.Repeat("0", 64) },
		"revision":      func(m map[string]any) { m["expected_admission_revision"] = 0 },
		"incomplete":    func(m map[string]any) { m["target"] = map[string]any{"agent_id": uuid.NewString()} },
	}
	for name, mut := range cases {
		rec := existingSessionCall(t, h.SendDelivery, sendBody(t, mut), nil, asAgentKey)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
}

func TestExistingSessionRoutesNeedAPlainAgentKey(t *testing.T) {
	h := NewExistingSessionHandler(nil, nil)
	body := sendBody(t, func(map[string]any) {})
	// no identity, a user token, a chat-session token
	for name, set := range map[string]func(echo.Context){
		"anonymous": func(echo.Context) {},
		"user":      func(c echo.Context) { c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser) },
	} {
		require.Equal(t, http.StatusUnauthorized, existingSessionCall(t, h.SendDelivery, body, nil, set).Code, name)
	}
	// a receiver call without the runner bearer, or with a malformed one
	reg := `{"host_id":"h","hostname":"x","session_id":"s","capability":"readonly_metadata","allowed_sender_agent_ids":["` + uuid.NewString() + `"]}`
	require.Equal(t, http.StatusForbidden, existingSessionCall(t, h.RegisterReceiver, reg, nil, asAgentKey).Code)
}
