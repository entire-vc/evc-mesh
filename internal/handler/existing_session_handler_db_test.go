package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

type existingSessionHTTP struct {
	t  *testing.T
	ws uuid.UUID
	h  *ExistingSessionHandler
}

func (x *existingSessionHTTP) do(fn echo.HandlerFunc, method, target, body string, agent uuid.UUID, token string, params map[string]string) *httptest.ResponseRecorder {
	x.t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if token != "" {
		req.Header.Set(ChatRunnerTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
	c.Set(mw.ContextKeyAgentID, agent)
	c.Set(mw.ContextKeyWorkspaceID, x.ws)
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

// The whole wire contract through the HTTP handlers against a real database:
// register, send, replay, list, ack, read the receipt, revoke, and every refusal.
func TestExistingSessionHandlerFlow(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the existing-session handler against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ws, receiver, sender, outsider := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'existing session handler',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	for _, a := range []uuid.UUID{receiver, sender, outsider} {
		_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, a, ws, "es-agent-"+a.String()[:8])
		require.NoError(t, err)
	}
	runners := postgres.NewChatRunnerRepo(db)
	runner, token, err := runners.RegisterRunner(t.Context(), receiver, "mbp")
	require.NoError(t, err)
	x := &existingSessionHTTP{t: t, ws: ws, h: NewExistingSessionHandler(postgres.NewExistingSessionRepo(db), runners)}

	regBody := func(mut func(m map[string]any)) string {
		m := map[string]any{"host_id": "host-1", "hostname": "host-a.example.invalid", "session_id": "native-1",
			"capability": "readonly_metadata", "allowed_sender_agent_ids": []string{sender.String()}}
		if mut != nil {
			mut(m)
		}
		b, _ := json.Marshal(m)
		return string(b)
	}

	// registration refusals: no bearer, a bearer of another agent, a named runner that is not the bearer, bad fields
	require.Equal(t, http.StatusForbidden, x.do(x.h.RegisterReceiver, http.MethodPost, "/", regBody(nil), receiver, "", nil).Code)
	require.Equal(t, http.StatusForbidden, x.do(x.h.RegisterReceiver, http.MethodPost, "/", regBody(nil), sender, token, nil).Code)
	require.Equal(t, http.StatusForbidden, x.do(x.h.RegisterReceiver, http.MethodPost, "/", regBody(func(m map[string]any) { m["runner_id"] = uuid.NewString() }), receiver, token, nil).Code)
	for name, mut := range map[string]func(m map[string]any){
		"host":       func(m map[string]any) { m["host_id"] = "" },
		"capability": func(m map[string]any) { m["capability"] = "exec" },
		"too many":   func(m map[string]any) { m["allowed_sender_agent_ids"] = make([]string, 33) },
		"nil sender": func(m map[string]any) { m["allowed_sender_agent_ids"] = []string{uuid.Nil.String()} },
		"foreign":    func(m map[string]any) { m["allowed_sender_agent_ids"] = []string{uuid.NewString()} },
	} {
		require.Equal(t, http.StatusBadRequest, x.do(x.h.RegisterReceiver, http.MethodPost, "/", regBody(mut), receiver, token, nil).Code, name)
	}

	rec := x.do(x.h.RegisterReceiver, http.MethodPost, "/", regBody(func(m map[string]any) { m["runner_id"] = runner.ID.String() }), receiver, token, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var reg struct {
		ReceiverID uuid.UUID `json:"receiver_id"`
		Revision   int64     `json:"admission_revision"`
		RunnerID   uuid.UUID `json:"runner_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reg))
	require.EqualValues(t, 1, reg.Revision)
	require.Equal(t, runner.ID, reg.RunnerID)

	target := map[string]any{"workspace_id": ws.String(), "agent_id": receiver.String(), "runner_id": runner.ID.String(),
		"host_id": "host-1", "hostname": "host-a.example.invalid", "session_id": "native-1"}
	reqID := uuid.NewString()
	send := func(agent uuid.UUID, mut func(m map[string]any)) *httptest.ResponseRecorder {
		return x.do(x.h.SendDelivery, http.MethodPost, "/", sendBody(t, func(m map[string]any) {
			m["request_id"], m["target"] = reqID, target
			if mut != nil {
				mut(m)
			}
		}), agent, "", nil)
	}
	require.Equal(t, http.StatusForbidden, send(outsider, nil).Code, "an agent the receiver did not list is refused")
	created := send(sender, nil)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	replay := send(sender, nil)
	require.Equal(t, http.StatusOK, replay.Code)
	var rc struct {
		DeliveryID uuid.UUID `json:"delivery_id"`
		State      string    `json:"state"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &rc))
	var rc2 struct {
		DeliveryID uuid.UUID `json:"delivery_id"`
	}
	require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &rc2))
	require.Equal(t, rc.DeliveryID, rc2.DeliveryID)
	require.Equal(t, http.StatusConflict, send(sender, func(m map[string]any) {
		sum := sha256.Sum256([]byte("other"))
		m["text"], m["text_sha256"] = "other", hex.EncodeToString(sum[:])
	}).Code, "a changed hash conflicts")
	require.NotContains(t, created.Body.String(), "read two files", "the receipt carries no text")

	// the receiver lists it only for its exact host and session
	list := func(host, session string) *httptest.ResponseRecorder {
		return x.do(x.h.ListDeliveries, http.MethodGet, "/?host_id="+host+"&session_id="+session, "", receiver, token, nil)
	}
	require.Equal(t, http.StatusBadRequest, list("", "").Code)
	require.Equal(t, http.StatusForbidden, list("host-2", "native-1").Code)
	require.Equal(t, http.StatusForbidden, x.do(x.h.ListDeliveries, http.MethodGet, "/?host_id=host-1&session_id=native-1", "", receiver, "", nil).Code)
	got := list("host-1", "native-1")
	require.Equal(t, http.StatusOK, got.Code)
	require.Contains(t, got.Body.String(), "read two files")

	ack := func(state, extra string) *httptest.ResponseRecorder {
		body := `{"host_id":"host-1","session_id":"native-1","admission_revision":1,"state":"` + state + `"` + extra + `}`
		return x.do(x.h.AckDelivery, http.MethodPost, "/", body, receiver, token, map[string]string{"delivery_id": rc.DeliveryID.String()})
	}
	require.Equal(t, http.StatusBadRequest, ack("running", "").Code)
	require.Equal(t, http.StatusBadRequest, ack("failed", "").Code, "failed needs an error_code")
	require.Equal(t, http.StatusBadRequest, ack("received", `,"error_code":"x"`).Code)
	require.Equal(t, http.StatusBadRequest, ack("acknowledged", `,"result_comment_url":"http://x/y"`).Code)
	require.Equal(t, http.StatusBadRequest, ack("failed", `,"error_code":"Bad Code"`).Code)
	require.Equal(t, http.StatusConflict, ack("acknowledged", "").Code, "queued cannot jump to acknowledged")
	require.Equal(t, http.StatusOK, ack("received", "").Code)
	var left struct {
		Deliveries []json.RawMessage `json:"deliveries"`
	}
	require.NoError(t, json.Unmarshal(list("host-1", "native-1").Body.Bytes(), &left))
	require.Empty(t, left.Deliveries, "received is not listed again")
	done := ack("acknowledged", `,"result_comment_url":"https://mesh.entire.host/t/x#c"`)
	require.Equal(t, http.StatusOK, done.Code, done.Body.String())
	require.Contains(t, done.Body.String(), `"acknowledged"`)
	require.Equal(t, http.StatusBadRequest, x.do(x.h.AckDelivery, http.MethodPost, "/", `{}`, receiver, token, map[string]string{"delivery_id": "nope"}).Code)

	// the sender reads the receipt; another sender and an invalid id do not
	param := map[string]string{"delivery_id": rc.DeliveryID.String()}
	require.Equal(t, http.StatusOK, x.do(x.h.GetDelivery, http.MethodGet, "/", "", sender, "", param).Code)
	require.Equal(t, http.StatusNotFound, x.do(x.h.GetDelivery, http.MethodGet, "/", "", outsider, "", param).Code)
	require.Equal(t, http.StatusBadRequest, x.do(x.h.GetDelivery, http.MethodGet, "/", "", sender, "", map[string]string{"delivery_id": "x"}).Code)

	// revoke: bad id, then the registration ends and sending is refused
	require.Equal(t, http.StatusBadRequest, x.do(x.h.RevokeReceiver, http.MethodDelete, "/", "", receiver, token, map[string]string{"receiver_id": "x"}).Code)
	require.Equal(t, http.StatusNoContent, x.do(x.h.RevokeReceiver, http.MethodDelete, "/", "", receiver, token, map[string]string{"receiver_id": reg.ReceiverID.String()}).Code)
	reqID = uuid.NewString()
	require.Equal(t, http.StatusForbidden, send(sender, nil).Code)

	// an empty allow-list registers (receive-only) and admits nobody
	empty := x.do(x.h.RegisterReceiver, http.MethodPost, "/", regBody(func(m map[string]any) { m["allowed_sender_agent_ids"] = []string{} }), receiver, token, nil)
	require.Equal(t, http.StatusCreated, empty.Code, empty.Body.String())
	reqID = uuid.NewString()
	require.Equal(t, http.StatusForbidden, send(sender, func(m map[string]any) { m["expected_admission_revision"] = 2 }).Code)
}
