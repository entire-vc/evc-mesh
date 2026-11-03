package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/hostlocalqa"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// qaStack runs the real handlers and repositories against a real database. The
// policy fixes the workspace and agent, so the test owns those two ids for its
// duration and removes the workspace afterwards.
type qaStack struct {
	t        *testing.T
	db       *sqlx.DB
	policy   *hostlocalqa.Policy
	ws       uuid.UUID
	receiver uuid.UUID
	outsider uuid.UUID
	runner   uuid.UUID
	token    string
	offset   atomic.Int64 // server clock shift in seconds
	on       *HostLocalQAHandler
	off      *HostLocalQAHandler
	legacy   *ExistingSessionHandler
}

func (s *qaStack) now() time.Time {
	return time.Now().Add(time.Duration(s.offset.Load()) * time.Second)
}

type qaCaller struct {
	agent uuid.UUID
	ws    uuid.UUID
	token string
	set   func(c echo.Context)
}

func (s *qaStack) call(fn echo.HandlerFunc, method, target, body string, who qaCaller, params map[string]string) *httptest.ResponseRecorder {
	s.t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if who.token != "" {
		req.Header.Set(ChatRunnerTokenHeader, who.token)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
	c.Set(mw.ContextKeyAgentID, who.agent)
	c.Set(mw.ContextKeyWorkspaceID, who.ws)
	if who.set != nil {
		who.set(c)
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

func (s *qaStack) rcv() qaCaller { return qaCaller{agent: s.receiver, ws: s.ws, token: s.token} }
func (s *qaStack) snd() qaCaller { return qaCaller{agent: s.receiver, ws: s.ws} }

func (s *qaStack) count(table string) int {
	var n int
	require.NoError(s.t, s.db.Get(&n, `SELECT count(*) FROM `+table))
	return n
}

func (s *qaStack) target() map[string]any {
	return map[string]any{"agent_id": s.receiver.String(), "host_id": "host-1", "hostname": s.policy.Hostname,
		"runner_id": s.runner.String(), "session_id": "native-1", "workspace_id": s.ws.String()}
}

func qaProducer() map[string]any {
	return map[string]any{"producer_pid": 4242, "producer_started": "2026-11-03T00:00:00Z",
		"terminal_session_id": "w0t0p0:ABC", "transport": "iterm", "tty": "/dev/ttys004"}
}

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func js(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }

func (s *qaStack) regBody(mut func(m map[string]any)) string {
	m := map[string]any{"schema_version": 1, "capability": hostlocalqa.Capability, "policy_sha256": s.policy.SHA256,
		"target": s.target(), "producer": qaProducer(), "expires": s.now().Add(8 * time.Hour).Unix(),
		"allowed_sender_agent_ids": []string{s.receiver.String()}}
	if mut != nil {
		mut(m)
	}
	return js(m)
}

type qaReceiver struct {
	ID  uuid.UUID `json:"receiver_id"`
	Rev int64     `json:"admission_revision"`
}

func (s *qaStack) register() qaReceiver {
	s.t.Helper()
	rec := s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), s.rcv(), nil)
	require.Equal(s.t, http.StatusCreated, rec.Code, rec.Body.String())
	var r qaReceiver
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &r))
	return r
}

func (s *qaStack) sendMap(r qaReceiver, req uuid.UUID) map[string]any {
	p := s.policy
	return map[string]any{
		"artifact_id": p.ArtifactID, "candidate": p.Candidate, "capability": hostlocalqa.Capability,
		"expected_admission_revision": r.Rev, "expires": s.now().Add(time.Hour).Unix(),
		"operation_manifest_sha256": p.OperationManifestSHA, "policy_sha256": p.SHA256, "producer": qaProducer(),
		"qa_package_sha256": p.QAPackageSHA, "receiver_id": r.ID.String(), "request_id": req.String(),
		"schema_version": 1, "source_task_id": p.SourceTaskID.String(), "target": s.target(),
		"text": p.Text, "text_sha256": p.TextSHA256,
	}
}

func (s *qaStack) send(m map[string]any) *httptest.ResponseRecorder {
	return s.call(s.on.SendDelivery, http.MethodPost, "/", js(m), s.snd(), nil)
}

func (s *qaStack) mustSend(r qaReceiver) (delivery uuid.UUID, env map[string]any) {
	s.t.Helper()
	env = s.sendMap(r, uuid.New())
	rec := s.send(env)
	require.Equal(s.t, http.StatusCreated, rec.Code, rec.Body.String())
	var out struct {
		ID uuid.UUID `json:"delivery_id"`
	}
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.ID, env
}

func qaParam(id uuid.UUID) map[string]string { return map[string]string{"delivery_id": id.String()} }

func binding(env map[string]any, delivery uuid.UUID) map[string]any {
	return map[string]any{"receiver_id": env["receiver_id"], "admission_revision": env["expected_admission_revision"],
		"delivery_id": delivery.String(), "request_id": env["request_id"], "capability": hostlocalqa.Capability,
		"policy_sha256": env["policy_sha256"], "text_sha256": env["text_sha256"], "target": env["target"],
		"producer": env["producer"], "expires": env["expires"]}
}

func ackBody(env map[string]any, delivery uuid.UUID, state string, extra map[string]any) string {
	m := binding(env, delivery)
	m["schema_version"], m["state"], m["observed_at"] = 1, state, time.Now().UTC().Format(time.RFC3339)
	for k, v := range extra {
		m[k] = v
	}
	return js(m)
}

func (s *qaStack) ack(env map[string]any, delivery uuid.UUID, state string, extra map[string]any) *httptest.ResponseRecorder {
	return s.call(s.on.AckDelivery, http.MethodPost, "/", ackBody(env, delivery, state, extra), s.rcv(), qaParam(delivery))
}

func (s *qaStack) check(body map[string]any, delivery uuid.UUID) *httptest.ResponseRecorder {
	return s.call(s.on.CheckDelivery, http.MethodPost, "/", js(body), s.rcv(), qaParam(delivery))
}

func (s *qaStack) get(delivery uuid.UUID) map[string]any {
	s.t.Helper()
	rec := s.call(s.on.GetDelivery, http.MethodGet, "/", "", s.snd(), qaParam(delivery))
	require.Equal(s.t, http.StatusOK, rec.Code, rec.Body.String())
	var m map[string]any
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func (s *qaStack) list() []map[string]any {
	s.t.Helper()
	rec := s.call(s.on.ListDeliveries, http.MethodGet, "/?host_id=host-1&session_id=native-1", "", s.rcv(), nil)
	require.Equal(s.t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Deliveries []map[string]any `json:"deliveries"`
	}
	require.NoError(s.t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Deliveries
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func details(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m struct {
		Details string `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m.Details
}

func newQAStack(t *testing.T) *qaStack {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the host-local QA assignment handlers against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	pol, err := hostlocalqa.LoadPolicy()
	require.NoError(t, err)
	s := &qaStack{t: t, db: db, policy: pol, ws: pol.WorkspaceID, receiver: pol.AgentID, outsider: uuid.New()}
	_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, s.ws)
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'host local qa',$2,$3)`, s.ws, s.ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, s.ws) })
	for _, a := range []uuid.UUID{s.receiver, s.outsider} {
		_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'a',$3,'h','p')`, a, s.ws, "qa-agent-"+a.String()[:8])
		require.NoError(t, err)
	}
	runners := postgres.NewChatRunnerRepo(db)
	runner, token, err := runners.RegisterRunner(t.Context(), s.receiver, "mbp")
	require.NoError(t, err)
	s.runner, s.token = runner.ID, token
	repo := postgres.NewHostLocalQARepo(db, pol).WithClock(s.now)
	s.on = NewHostLocalQAHandler(repo, runners, pol, true)
	s.off = NewHostLocalQAHandler(repo, runners, pol, false)
	s.legacy = NewExistingSessionHandler(postgres.NewExistingSessionRepo(db), runners)
	return s
}

var receiptKeys = sortedStrs([]string{"acknowledged_at", "admission_revision", "capability", "delivery_id", "error_code", "expires",
	"policy_sha256", "producer", "receiver_id", "received_at", "request_id", "result_comment_url", "schema_version",
	"state", "target", "text_sha256"})

func sortedStrs(k []string) []string { sort.Strings(k); return k }

func TestHostLocalQAAssignmentFlow(t *testing.T) {
	s := newQAStack(t)
	deliveries, receivers := "host_local_qa_deliveries", "host_local_qa_receivers"

	t.Run("off", func(t *testing.T) {
		id := uuid.New()
		env := s.sendMap(qaReceiver{ID: uuid.New(), Rev: 1}, uuid.New())
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"register": s.call(s.off.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), s.rcv(), nil),
			"send":     s.call(s.off.SendDelivery, http.MethodPost, "/", js(env), s.snd(), nil),
			"get":      s.call(s.off.GetDelivery, http.MethodGet, "/", "", s.snd(), qaParam(id)),
			"list":     s.call(s.off.ListDeliveries, http.MethodGet, "/?host_id=h&session_id=s", "", s.rcv(), nil),
			"ack":      s.call(s.off.AckDelivery, http.MethodPost, "/", ackBody(env, id, "received", nil), s.rcv(), qaParam(id)),
			"check":    s.call(s.off.CheckDelivery, http.MethodPost, "/", js(binding(env, id)), s.rcv(), qaParam(id)),
		} {
			require.Equal(t, http.StatusForbidden, rec.Code, name)
			require.Equal(t, "host_local_qa_disabled", details(t, rec), name)
		}
		require.Zero(t, s.count(receivers))
		require.Zero(t, s.count(deliveries))
		// positive control: the identical register body is accepted once the capability is ON
		r := s.register()
		require.EqualValues(t, 1, r.Rev)
		require.Equal(t, 1, s.count(receivers))
		// an unauthenticated-style caller never gets past authentication, ON or OFF
		rec := s.call(s.off.SendDelivery, http.MethodPost, "/", js(env), qaCaller{agent: uuid.Nil, ws: s.ws, set: func(c echo.Context) { c.Set(mw.ContextKeyAuthType, "") }}, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("exact", func(t *testing.T) {
		r := s.register()
		delivery, env := s.mustSend(r)
		rc := s.get(delivery)
		require.Equal(t, receiptKeys, keysOf(rc), "the receipt is exactly the contract fields")
		require.Equal(t, "queued", rc["state"])
		require.NotContains(t, js(rc), "Host-local QA assignment v1", "the receipt carries no text")

		queued := s.list()
		require.Len(t, queued, 1)
		want := append(keysOf(env), "created_at", "delivery_id")
		sort.Strings(want)
		require.Equal(t, want, keysOf(queued[0]), "poll = sender envelope + delivery_id + created_at, nothing else")
		require.Equal(t, s.policy.Text, queued[0]["text"], "the text is exactly brief.txt, final LF included")
		require.Equal(t, delivery.String(), queued[0]["delivery_id"])

		got := s.ack(env, delivery, "received", nil)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		require.Empty(t, s.list(), "received is never listed again")
		done := s.ack(env, delivery, "acknowledged", map[string]any{"result_comment_url": "https://mesh.example.invalid/t/x#c"})
		require.Equal(t, http.StatusOK, done.Code, done.Body.String())
		require.Equal(t, "acknowledged", s.get(delivery)["state"])
	})

	t.Run("ack-is-not-install", func(t *testing.T) {
		r := s.register()
		delivery, env := s.mustSend(r)
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "received", nil).Code)
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "acknowledged", nil).Code)
		rc := s.get(delivery)
		require.Equal(t, "acknowledged", rc["state"])
		for k := range rc {
			for _, bad := range []string{"install", "loaded", "matrix", "field", "mini"} {
				require.NotContains(t, k, bad)
			}
		}
		require.Equal(t, receiptKeys, keysOf(rc))
		// the guard still answers for an acknowledged delivery and marks nothing
		require.Equal(t, http.StatusOK, s.check(binding(env, delivery), delivery).Code)
		require.Equal(t, rc, s.get(delivery), "checking changes no state")
	})

	t.Run("ack", func(t *testing.T) {
		r := s.register()
		delivery, env := s.mustSend(r)
		rec := s.ack(env, delivery, "acknowledged", nil)
		require.Equal(t, http.StatusConflict, rec.Code, "queued cannot jump to acknowledged")
		require.Equal(t, "delivery_state_conflict", details(t, rec))
		require.Equal(t, "queued", s.get(delivery)["state"])
		for name, mut := range map[string]func(m map[string]any){
			"receiver":   func(m map[string]any) { m["receiver_id"] = uuid.NewString() },
			"revision":   func(m map[string]any) { m["admission_revision"] = r.Rev + 1 },
			"request":    func(m map[string]any) { m["request_id"] = uuid.NewString() },
			"policy":     func(m map[string]any) { m["policy_sha256"] = s.policy.TextSHA256 },
			"text":       func(m map[string]any) { m["text_sha256"] = s.policy.SHA256 },
			"expires":    func(m map[string]any) { m["expires"] = env["expires"].(int64) + 1 },
			"target":     func(m map[string]any) { m["target"].(map[string]any)["session_id"] = "other" },
			"producer":   func(m map[string]any) { m["producer"].(map[string]any)["producer_pid"] = 1 },
			"delivery":   func(m map[string]any) { m["delivery_id"] = uuid.NewString() },
			"agent":      func(m map[string]any) { m["target"].(map[string]any)["agent_id"] = s.outsider.String() },
			"workspace":  func(m map[string]any) { m["target"].(map[string]any)["workspace_id"] = uuid.NewString() },
			"runner":     func(m map[string]any) { m["target"].(map[string]any)["runner_id"] = uuid.NewString() },
			"host":       func(m map[string]any) { m["target"].(map[string]any)["host_id"] = "host-2" },
			"hostname":   func(m map[string]any) { m["target"].(map[string]any)["hostname"] = "other.example.invalid" },
			"tty":        func(m map[string]any) { m["producer"].(map[string]any)["tty"] = "/dev/ttys005" },
			"terminal":   func(m map[string]any) { m["producer"].(map[string]any)["terminal_session_id"] = "x" },
			"started":    func(m map[string]any) { m["producer"].(map[string]any)["producer_started"] = "x" },
			"capability": nil,
		} {
			if mut == nil {
				continue
			}
			m := binding(env, delivery)
			m["schema_version"], m["state"], m["observed_at"] = 1, "received", time.Now().UTC().Format(time.RFC3339)
			m["target"], m["producer"] = clone(m["target"].(map[string]any)), clone(m["producer"].(map[string]any))
			m["expires"] = env["expires"]
			mut(m)
			got := s.call(s.on.AckDelivery, http.MethodPost, "/", js(m), s.rcv(), qaParam(delivery))
			require.Equal(t, http.StatusForbidden, got.Code, name+": "+got.Body.String())
			require.Equal(t, "host_local_qa_target_denied", details(t, got), name)
		}
		require.Equal(t, "queued", s.get(delivery)["state"], "no mismatched ack changed anything")
		for name, body := range map[string]string{
			"state":       ackBody(env, delivery, "running", nil),
			"failed code": ackBody(env, delivery, "failed", nil),
			"code on ok":  ackBody(env, delivery, "received", map[string]any{"error_code": "x"}),
			"http url":    ackBody(env, delivery, "received", map[string]any{"result_comment_url": "http://x/y"}),
			"observed":    ackBody(env, delivery, "received", map[string]any{"observed_at": "yesterday"}),
			"extra":       ackBody(env, delivery, "received", map[string]any{"transcript": "x"}),
		} {
			require.Equal(t, http.StatusBadRequest, s.call(s.on.AckDelivery, http.MethodPost, "/", body, s.rcv(), qaParam(delivery)).Code, name)
		}
		require.Equal(t, http.StatusBadRequest, s.call(s.on.AckDelivery, http.MethodPost, "/", "{}", s.rcv(), map[string]string{"delivery_id": "nope"}).Code)
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "received", nil).Code)
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "received", nil).Code, "repeating the current state is a no-op")
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "failed", map[string]any{"error_code": "paste_failed"}).Code)
		require.Equal(t, "failed", s.get(delivery)["state"])
		require.Equal(t, http.StatusConflict, s.ack(env, delivery, "acknowledged", nil).Code, "failed is terminal")
		d2, env2 := s.mustSend(r)
		require.Equal(t, http.StatusOK, s.ack(env2, d2, "uncertain", map[string]any{"error_code": "partial_paste"}).Code, "queued -> uncertain")
		require.Equal(t, "uncertain", s.get(d2)["state"])
		require.Empty(t, s.list(), "failed and uncertain are never listed again")
	})

	t.Run("duplicate", func(t *testing.T) {
		r := s.register()
		delivery, env := s.mustSend(r)
		before := s.count(deliveries)
		rec := s.send(env)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var again struct {
			ID uuid.UUID `json:"delivery_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &again))
		require.Equal(t, delivery, again.ID, "an identical replay returns the same delivery")
		require.Equal(t, before, s.count(deliveries), "and queues nothing")
		require.Len(t, s.list(), 1, "one injection total")
	})

	t.Run("conflict", func(t *testing.T) {
		r := s.register()
		delivery, env := s.mustSend(r)
		original := s.get(delivery)
		before := s.count(deliveries)
		for name, mut := range map[string]func(m map[string]any){
			"expires":  func(m map[string]any) { m["expires"] = env["expires"].(int64) + 1 },
			"revision": func(m map[string]any) { m["expected_admission_revision"] = r.Rev + 1 },
			"producer": func(m map[string]any) { m["producer"].(map[string]any)["producer_pid"] = 7 },
			"session":  func(m map[string]any) { m["target"].(map[string]any)["session_id"] = "other" },
			"receiver": func(m map[string]any) { m["receiver_id"] = uuid.NewString() },
			"host":     func(m map[string]any) { m["target"].(map[string]any)["host_id"] = "host-9" },
		} {
			m := clone(env)
			m["expires"] = env["expires"]
			mut(m)
			rec := s.send(m)
			require.Equal(t, http.StatusConflict, rec.Code, name+": "+rec.Body.String())
			require.Equal(t, "request_conflict", details(t, rec), name)
		}
		require.Equal(t, before, s.count(deliveries))
		require.Equal(t, original, s.get(delivery), "the original is unchanged")
	})

	t.Run("cross-capability", func(t *testing.T) {
		r := s.register()
		// a legacy admission for the same agent and runner
		lreg := s.call(s.legacy.RegisterReceiver, http.MethodPost, "/", js(map[string]any{"host_id": "host-1", "hostname": s.policy.Hostname,
			"session_id": "legacy-1", "capability": "readonly_metadata", "allowed_sender_agent_ids": []string{s.receiver.String()}}), s.rcv(), nil)
		require.Equal(t, http.StatusCreated, lreg.Code, lreg.Body.String())
		legacySend := func(req uuid.UUID) *httptest.ResponseRecorder {
			return s.call(s.legacy.SendDelivery, http.MethodPost, "/", sendBody(t, func(m map[string]any) {
				m["request_id"] = req.String()
				m["target"] = map[string]any{"workspace_id": s.ws.String(), "agent_id": s.receiver.String(), "runner_id": s.runner.String(),
					"host_id": "host-1", "hostname": s.policy.Hostname, "session_id": "legacy-1"}
			}), s.snd(), nil)
		}
		// legacy request id reused by the QA route
		legacyReq := uuid.New()
		require.Equal(t, http.StatusCreated, legacySend(legacyReq).Code)
		rec := s.send(s.sendMap(r, legacyReq))
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, "request_conflict", details(t, rec))
		// QA request id reused by the legacy route
		_, env := s.mustSend(r)
		qaReq, _ := uuid.Parse(env["request_id"].(string))
		before := s.count("existing_session_deliveries")
		rec = legacySend(qaReq)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, before, s.count("existing_session_deliveries"))
	})

	t.Run("legacy-rejects-qa", func(t *testing.T) {
		r := s.register()
		beforeL, beforeQ := s.count("existing_session_deliveries"), s.count(deliveries)
		beforeLR := s.count("existing_session_receivers")
		qa := s.sendMap(r, uuid.New())
		rec := s.call(s.legacy.SendDelivery, http.MethodPost, "/", js(qa), s.snd(), nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		legacyShaped := map[string]any{"host_id": "host-1", "hostname": s.policy.Hostname, "session_id": "native-1",
			"capability": hostlocalqa.Capability, "allowed_sender_agent_ids": []string{s.receiver.String()}}
		rec = s.call(s.legacy.RegisterReceiver, http.MethodPost, "/", js(legacyShaped), s.rcv(), nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, beforeL, s.count("existing_session_deliveries"))
		require.Equal(t, beforeLR, s.count("existing_session_receivers"))
		require.Equal(t, beforeQ, s.count(deliveries))
		// positive control: the legacy shape on the legacy route still works
		legacyShaped["capability"] = "readonly_metadata"
		require.Equal(t, http.StatusCreated, s.call(s.legacy.RegisterReceiver, http.MethodPost, "/", js(legacyShaped), s.rcv(), nil).Code)
	})

	t.Run("qa-rejects-legacy", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		beforeR := s.count(receivers)
		legacyEnv := s.sendMap(r, uuid.New())
		legacyEnv["capability"] = "readonly_metadata"
		require.Equal(t, http.StatusBadRequest, s.send(legacyEnv).Code)
		// the legacy wire shape (no producer, no policy, ...) on the QA route
		legacyShape := map[string]any{"request_id": uuid.NewString(), "target": s.target(), "expected_admission_revision": r.Rev,
			"capability": "readonly_metadata", "text": "read two files", "text_sha256": strings.Repeat("a", 64)}
		require.Equal(t, http.StatusBadRequest, s.send(legacyShape).Code)
		require.Equal(t, http.StatusBadRequest, s.call(s.on.RegisterReceiver, http.MethodPost, "/",
			s.regBody(func(m map[string]any) { m["capability"] = "readonly_metadata" }), s.rcv(), nil).Code)
		delivery, env := s.mustSend(r)
		before++
		bad := binding(env, delivery)
		bad["capability"] = "readonly_metadata"
		require.Equal(t, http.StatusBadRequest, s.check(bad, delivery).Code)
		require.Equal(t, before, s.count(deliveries))
		require.Equal(t, beforeR, s.count(receivers), "a legacy admission is never a QA admission")
	})

	t.Run("unknown-fields", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		base := s.sendMap(r, uuid.New())
		for name, mut := range map[string]func(m map[string]any){
			"command":         func(m map[string]any) { m["command"] = "ls" },
			"argv":            func(m map[string]any) { m["argv"] = []string{"ls"} },
			"cwd":             func(m map[string]any) { m["cwd"] = "/" },
			"tool":            func(m map[string]any) { m["tool"] = "bash" },
			"url":             func(m map[string]any) { m["url"] = "https://x" },
			"nested target":   func(m map[string]any) { m["target"].(map[string]any)["cwd"] = "/" },
			"nested producer": func(m map[string]any) { m["producer"].(map[string]any)["argv"] = "x" },
		} {
			m := clone(base)
			mut(m)
			m["expires"] = base["expires"]
			rec := s.send(m)
			require.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
		}
		dup := js(base)
		dup = dup[:len(dup)-1] + `,"request_id":"` + uuid.NewString() + `"}`
		require.Equal(t, http.StatusBadRequest, s.call(s.on.SendDelivery, http.MethodPost, "/", dup, s.snd(), nil).Code, "duplicate key")
		require.Equal(t, http.StatusBadRequest, s.call(s.on.SendDelivery, http.MethodPost, "/", js(base)+" {}", s.snd(), nil).Code, "trailing data")
		require.Equal(t, before, s.count(deliveries), "zero enqueue")
		require.Equal(t, http.StatusCreated, s.send(base).Code, "positive control: the clean body is accepted")
	})

	t.Run("hash", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		base := s.sendMap(r, uuid.New())
		for name, mut := range map[string]func(m map[string]any){
			"text byte":   func(m map[string]any) { m["text"] = s.policy.Text + " " },
			"no final LF": func(m map[string]any) { m["text"] = strings.TrimSuffix(s.policy.Text, "\n") },
			"text sha":    func(m map[string]any) { m["text_sha256"] = s.policy.SHA256 },
			"policy sha":  func(m map[string]any) { m["policy_sha256"] = s.policy.TextSHA256 },
			"package":     func(m map[string]any) { m["qa_package_sha256"] = s.policy.SHA256 },
			"manifest":    func(m map[string]any) { m["operation_manifest_sha256"] = s.policy.SHA256 },
			"candidate":   func(m map[string]any) { m["candidate"] = strings.Repeat("0", 40) },
		} {
			m := clone(base)
			m["expires"], m["artifact_id"], m["expected_admission_revision"] = base["expires"], base["artifact_id"], base["expected_admission_revision"]
			mut(m)
			require.Equal(t, http.StatusBadRequest, s.send(m).Code, name)
		}
		require.Equal(t, before, s.count(deliveries))
	})

	t.Run("identity", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		var bodies []string
		for _, field := range []string{"workspace_id", "agent_id", "runner_id", "host_id", "hostname", "session_id"} {
			m := s.sendMap(r, uuid.New())
			tg := clone(s.target())
			switch field {
			case "workspace_id", "agent_id", "runner_id":
				tg[field] = uuid.NewString()
			default:
				tg[field] = "other-" + field
			}
			m["target"] = tg
			rec := s.send(m)
			require.Equal(t, http.StatusForbidden, rec.Code, field+": "+rec.Body.String())
			bodies = append(bodies, rec.Body.String())
		}
		for _, b := range bodies[1:] {
			require.Equal(t, bodies[0], b, "every identity mismatch is refused with the same answer")
		}
		wrong := s.sendMap(r, uuid.New())
		wrong["receiver_id"] = uuid.NewString()
		require.Equal(t, http.StatusForbidden, s.send(wrong).Code)
		require.Equal(t, before, s.count(deliveries), "zero enqueue")
	})

	t.Run("producer", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		for field, v := range map[string]any{"producer_pid": 1, "producer_started": "other", "terminal_session_id": "other", "tty": "/dev/ttys099"} {
			m := s.sendMap(r, uuid.New())
			m["producer"].(map[string]any)[field] = v
			require.Equal(t, http.StatusForbidden, s.send(m).Code, field)
		}
		require.Equal(t, before, s.count(deliveries))
	})

	t.Run("revision", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		for name, v := range map[string]any{"zero": 0, "bool": true, "string": "1", "float": 1.5, "negative": -1} {
			m := s.sendMap(r, uuid.New())
			m["expected_admission_revision"] = v
			require.Equal(t, http.StatusBadRequest, s.send(m).Code, name)
		}
		m := s.sendMap(r, uuid.New())
		delete(m, "expected_admission_revision")
		require.Equal(t, http.StatusBadRequest, s.send(m).Code, "missing")
		m = s.sendMap(r, uuid.New())
		m["expected_admission_revision"] = r.Rev + 1
		rec := s.send(m)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "admission_stale", details(t, rec))
		require.Equal(t, before, s.count(deliveries))
		// re-admission bumps the revision and fails what was queued under the old one
		stale, _ := s.mustSend(r)
		r2 := s.register()
		require.Equal(t, r.Rev+1, r2.Rev)
		require.Equal(t, r.ID, r2.ID, "same identity, same admission row")
		require.Empty(t, s.list())
		rc := s.get(stale)
		require.Equal(t, "failed", rc["state"])
		require.Equal(t, "admission_stale", rc["error_code"])
	})

	t.Run("expired", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		for name, exp := range map[string]int64{"now": s.now().Unix(), "past": s.now().Unix() - 10,
			"over 8h": s.now().Add(8*time.Hour).Unix() + 2} {
			m := s.sendMap(r, uuid.New())
			m["expires"] = exp
			rec := s.send(m)
			require.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
		}
		require.Equal(t, before, s.count(deliveries))
		for name, exp := range map[string]int64{"reg past": s.now().Unix() - 1, "reg over 8h": s.now().Add(8*time.Hour).Unix() + 5} {
			rec := s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(func(m map[string]any) { m["expires"] = exp }), s.rcv(), nil)
			require.Equal(t, http.StatusBadRequest, rec.Code, name)
		}
		m := s.sendMap(r, uuid.New())
		m["expires"] = s.now().Add(8*time.Hour).Unix() - 5
		require.Equal(t, http.StatusCreated, s.send(m).Code, "positive control: just under 8h is accepted")

		// queued item that expires before delivery: never listed, not receivable, not checkable
		short := s.sendMap(r, uuid.New())
		short["expires"] = s.now().Unix() + 30
		rec := s.send(short)
		require.Equal(t, http.StatusCreated, rec.Code)
		var d struct {
			ID uuid.UUID `json:"delivery_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
		require.Equal(t, http.StatusOK, s.check(binding(short, d.ID), d.ID).Code, "valid while unexpired")
		s.offset.Add(45)
		t.Cleanup(func() { s.offset.Add(-45) })
		for _, q := range s.list() {
			require.NotEqual(t, d.ID.String(), q["delivery_id"], "an expired delivery is not forwarded")
		}
		rc := s.get(d.ID)
		require.Equal(t, "failed", rc["state"])
		require.Equal(t, "expired", rc["error_code"])
		require.Equal(t, http.StatusConflict, s.ack(short, d.ID, "received", nil).Code)
		require.Equal(t, http.StatusForbidden, s.check(binding(short, d.ID), d.ID).Code)
	})

	t.Run("check", func(t *testing.T) {
		r := s.register()
		delivery, env := s.mustSend(r)
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "received", nil).Code)
		before := s.get(delivery)
		rec := s.check(binding(env, delivery), delivery)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, []string{"active", "admission_revision", "capability", "checked_at", "delivery_id", "expires", "policy_sha256",
			"producer", "receiver_id", "request_id", "target", "text_sha256"}, keysOf(got))
		require.Equal(t, true, got["active"])
		require.Equal(t, delivery.String(), got["delivery_id"])
		require.Equal(t, env["request_id"], got["request_id"])
		require.JSONEq(t, js(env["target"].(map[string]any)), js(got["target"].(map[string]any)))
		require.JSONEq(t, js(env["producer"].(map[string]any)), js(got["producer"].(map[string]any)))
		_, err := time.Parse(time.RFC3339, got["checked_at"].(string))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, s.check(binding(env, delivery), delivery).Code, "replay-safe")
		require.Equal(t, before, s.get(delivery), "checking writes nothing")

		var denied []string
		for name, mut := range map[string]func(m map[string]any){
			"receiver":  func(m map[string]any) { m["receiver_id"] = uuid.NewString() },
			"revision":  func(m map[string]any) { m["admission_revision"] = r.Rev + 1 },
			"delivery":  func(m map[string]any) { m["delivery_id"] = uuid.NewString() },
			"request":   func(m map[string]any) { m["request_id"] = uuid.NewString() },
			"policy":    func(m map[string]any) { m["policy_sha256"] = s.policy.TextSHA256 },
			"text":      func(m map[string]any) { m["text_sha256"] = s.policy.SHA256 },
			"expires":   func(m map[string]any) { m["expires"] = env["expires"].(int64) + 1 },
			"workspace": func(m map[string]any) { m["target"].(map[string]any)["workspace_id"] = uuid.NewString() },
			"agent":     func(m map[string]any) { m["target"].(map[string]any)["agent_id"] = s.outsider.String() },
			"runner":    func(m map[string]any) { m["target"].(map[string]any)["runner_id"] = uuid.NewString() },
			"host":      func(m map[string]any) { m["target"].(map[string]any)["host_id"] = "host-2" },
			"hostname":  func(m map[string]any) { m["target"].(map[string]any)["hostname"] = "other.example.invalid" },
			"session":   func(m map[string]any) { m["target"].(map[string]any)["session_id"] = "other" },
			"pid":       func(m map[string]any) { m["producer"].(map[string]any)["producer_pid"] = 1 },
			"tty":       func(m map[string]any) { m["producer"].(map[string]any)["tty"] = "/dev/ttys099" },
		} {
			m := clone(binding(env, delivery))
			m["expires"] = env["expires"]
			mut(m)
			rec := s.check(m, delivery)
			require.Equal(t, http.StatusForbidden, rec.Code, name+": "+rec.Body.String())
			require.Equal(t, "host_local_qa_check_denied", details(t, rec), name)
			denied = append(denied, rec.Body.String())
		}
		for _, d := range denied[1:] {
			require.Equal(t, denied[0], d, "every mismatch is the same refusal")
		}
		// malformed: tokens and text never travel in the body
		for name, mut := range map[string]func(m map[string]any){
			"token":   func(m map[string]any) { m["token"] = "x" },
			"text":    func(m map[string]any) { m["text"] = s.policy.Text },
			"bool":    func(m map[string]any) { m["admission_revision"] = true },
			"missing": func(m map[string]any) { delete(m, "request_id") },
		} {
			m := clone(binding(env, delivery))
			m["expires"] = env["expires"]
			mut(m)
			require.Equal(t, http.StatusBadRequest, s.check(m, delivery).Code, name)
		}
		// the credential: no runner token, another agent's runner token
		noTok := s.call(s.on.CheckDelivery, http.MethodPost, "/", js(binding(env, delivery)), qaCaller{agent: s.receiver, ws: s.ws}, qaParam(delivery))
		require.Equal(t, http.StatusForbidden, noTok.Code)
		other, otherTok, err := postgres.NewChatRunnerRepo(s.db).RegisterRunner(t.Context(), s.outsider, "other")
		require.NoError(t, err)
		_ = other
		require.Equal(t, http.StatusForbidden, s.call(s.on.CheckDelivery, http.MethodPost, "/", js(binding(env, delivery)),
			qaCaller{agent: s.receiver, ws: s.ws, token: otherTok}, qaParam(delivery)).Code, "a runner credential of another agent")
		// failed deliveries are not active
		require.Equal(t, http.StatusOK, s.ack(env, delivery, "failed", map[string]any{"error_code": "paste_failed"}).Code)
		require.Equal(t, http.StatusForbidden, s.check(binding(env, delivery), delivery).Code)
	})

	t.Run("revoked", func(t *testing.T) {
		r := s.register()
		queued, qEnv := s.mustSend(r)
		received, rEnv := s.mustSend(r)
		require.Equal(t, http.StatusOK, s.ack(rEnv, received, "received", nil).Code)
		require.Equal(t, http.StatusOK, s.check(binding(qEnv, queued), queued).Code, "active before revoke")
		rev := func(id uuid.UUID) *httptest.ResponseRecorder {
			return s.call(s.on.RevokeReceiver, http.MethodDelete, "/", "", s.rcv(), map[string]string{"receiver_id": id.String()})
		}
		require.Equal(t, http.StatusBadRequest, s.call(s.on.RevokeReceiver, http.MethodDelete, "/", "", s.rcv(), map[string]string{"receiver_id": "x"}).Code)
		require.Equal(t, http.StatusNoContent, rev(r.ID).Code)
		require.Equal(t, http.StatusNotFound, rev(r.ID).Code, "already revoked")
		require.Equal(t, "failed", s.get(queued)["state"])
		require.Equal(t, "receiver_revoked", s.get(queued)["error_code"])
		require.Equal(t, "uncertain", s.get(received)["state"], "a received delivery may have been injected")
		require.Equal(t, "delivery_uncertain", s.get(received)["error_code"])
		before := s.count(deliveries)
		require.Equal(t, http.StatusForbidden, s.call(s.on.ListDeliveries, http.MethodGet, "/?host_id=host-1&session_id=native-1", "", s.rcv(), nil).Code)
		require.Equal(t, http.StatusForbidden, s.send(s.sendMap(r, uuid.New())).Code, "no new work for a revoked admission")
		require.Equal(t, http.StatusForbidden, s.check(binding(qEnv, queued), queued).Code, "the guard refuses at the next boundary")
		require.Equal(t, http.StatusForbidden, s.check(binding(rEnv, received), received).Code)
		require.Equal(t, http.StatusForbidden, s.ack(rEnv, received, "acknowledged", nil).Code)
		require.Equal(t, before, s.count(deliveries))
		// revoke is refused while the capability is OFF like every other route, and
		// changes nothing; the same call is accepted once it is ON (positive control)
		r2 := s.register()
		param := map[string]string{"receiver_id": r2.ID.String()}
		off := s.call(s.off.RevokeReceiver, http.MethodDelete, "/", "", s.rcv(), param)
		require.Equal(t, http.StatusForbidden, off.Code)
		require.Equal(t, "host_local_qa_disabled", details(t, off))
		var revokedRows int
		require.NoError(t, s.db.Get(&revokedRows, `SELECT count(*) FROM host_local_qa_receivers WHERE id=$1 AND revoked_at IS NOT NULL`, r2.ID))
		require.Zero(t, revokedRows)
		require.Equal(t, http.StatusNoContent, s.call(s.on.RevokeReceiver, http.MethodDelete, "/", "", s.rcv(), param).Code)
	})

	t.Run("disable", func(t *testing.T) {
		// a legacy admission and delivery exist; disabling QA touches neither
		legacyReg := s.call(s.legacy.RegisterReceiver, http.MethodPost, "/", js(map[string]any{"host_id": "host-1", "hostname": s.policy.Hostname,
			"session_id": "legacy-2", "capability": "readonly_metadata", "allowed_sender_agent_ids": []string{s.receiver.String()}}), s.rcv(), nil)
		require.Equal(t, http.StatusCreated, legacyReg.Code, legacyReg.Body.String())
		r := s.register()
		beforeLegacy := s.count("existing_session_receivers")
		require.Equal(t, http.StatusNoContent, s.call(s.on.RevokeReceiver, http.MethodDelete, "/", "", s.rcv(), map[string]string{"receiver_id": r.ID.String()}).Code)
		require.Equal(t, beforeLegacy, s.count("existing_session_receivers"))
		var revoked int
		require.NoError(t, s.db.Get(&revoked, `SELECT count(*) FROM existing_session_receivers WHERE revoked_at IS NOT NULL AND workspace_id=$1`, s.ws))
		require.Zero(t, revoked, "legacy admissions are not revoked by a QA revoke")
		legacySend := s.call(s.legacy.SendDelivery, http.MethodPost, "/", sendBody(t, func(m map[string]any) {
			m["request_id"] = uuid.NewString()
			m["target"] = map[string]any{"workspace_id": s.ws.String(), "agent_id": s.receiver.String(), "runner_id": s.runner.String(),
				"host_id": "host-1", "hostname": s.policy.Hostname, "session_id": "legacy-2"}
		}), s.snd(), nil)
		require.Equal(t, http.StatusCreated, legacySend.Code, "legacy unaffected: "+legacySend.Body.String())
	})

	t.Run("auth", func(t *testing.T) {
		r := s.register()
		before := s.count(deliveries)
		env := s.sendMap(r, uuid.New())
		chat := func(c echo.Context) { c.Set(mw.ContextKeyChatSession, &service.ChatSessionPrincipal{}) }
		connector := func(c echo.Context) { c.Set(mw.ContextKeyOAuthConnectorUserID, uuid.New()) }
		require.Equal(t, http.StatusUnauthorized, s.call(s.on.SendDelivery, http.MethodPost, "/", js(env), qaCaller{agent: s.receiver, ws: s.ws, set: chat}, nil).Code)
		require.Equal(t, http.StatusForbidden, s.call(s.on.SendDelivery, http.MethodPost, "/", js(env), qaCaller{agent: s.receiver, ws: s.ws, set: connector}, nil).Code)
		require.Equal(t, http.StatusUnauthorized, s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), qaCaller{agent: s.receiver, ws: s.ws, token: s.token, set: chat}, nil).Code)
		// another agent: not in the fixed sender allowlist, not the policy agent
		require.Equal(t, http.StatusForbidden, s.call(s.on.SendDelivery, http.MethodPost, "/", js(env), qaCaller{agent: s.outsider, ws: s.ws}, nil).Code)
		_, otherTok, err := postgres.NewChatRunnerRepo(s.db).RegisterRunner(t.Context(), s.outsider, "other2")
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), qaCaller{agent: s.outsider, ws: s.ws, token: otherTok}, nil).Code,
			"the receiver must be the fixed policy agent")
		require.Equal(t, http.StatusForbidden, s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), qaCaller{agent: s.receiver, ws: s.ws}, nil).Code, "no runner credential")
		require.Equal(t, http.StatusForbidden, s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), qaCaller{agent: s.receiver, ws: s.ws, token: otherTok}, nil).Code, "another agent's runner credential")
		// the body cannot choose the workspace: a key of another workspace is refused
		require.Equal(t, http.StatusForbidden, s.call(s.on.SendDelivery, http.MethodPost, "/", js(env), qaCaller{agent: s.receiver, ws: uuid.New()}, nil).Code)
		require.Equal(t, http.StatusForbidden, s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(nil), qaCaller{agent: s.receiver, ws: uuid.New(), token: s.token}, nil).Code)
		// the body cannot choose the runner: a registration naming a runner other than the credential's
		rec := s.call(s.on.RegisterReceiver, http.MethodPost, "/", s.regBody(func(m map[string]any) { m["target"].(map[string]any)["runner_id"] = uuid.NewString() }), s.rcv(), nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, before, s.count(deliveries))
		require.Equal(t, http.StatusCreated, s.send(env).Code, "positive control: the proper caller succeeds")
	})

	t.Run("races", func(t *testing.T) {
		// identical concurrent sends: one row
		r := s.register()
		env := s.sendMap(r, uuid.New())
		var wg sync.WaitGroup
		codes := make([]int, 8)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = s.send(env).Code
			}()
		}
		wg.Wait()
		created := 0
		for _, c := range codes {
			require.Contains(t, []int{http.StatusCreated, http.StatusOK}, c)
			if c == http.StatusCreated {
				created++
			}
		}
		require.Equal(t, 1, created)

		// a QA and a legacy send of the same request id: exactly one wins
		require.Equal(t, http.StatusCreated, s.call(s.legacy.RegisterReceiver, http.MethodPost, "/", js(map[string]any{"host_id": "host-1", "hostname": s.policy.Hostname,
			"session_id": "legacy-3", "capability": "readonly_metadata", "allowed_sender_agent_ids": []string{s.receiver.String()}}), s.rcv(), nil).Code)
		for i := 0; i < 5; i++ {
			req := uuid.New()
			var qa, lg int
			wg.Add(2)
			go func() { defer wg.Done(); qa = s.send(s.sendMap(r, req)).Code }()
			go func() {
				defer wg.Done()
				lg = s.call(s.legacy.SendDelivery, http.MethodPost, "/", sendBody(t, func(m map[string]any) {
					m["request_id"] = req.String()
					m["target"] = map[string]any{"workspace_id": s.ws.String(), "agent_id": s.receiver.String(), "runner_id": s.runner.String(),
						"host_id": "host-1", "hostname": s.policy.Hostname, "session_id": "legacy-3"}
				}), s.snd(), nil).Code
			}()
			wg.Wait()
			ok := 0
			for _, c := range []int{qa, lg} {
				if c == http.StatusCreated {
					ok++
				} else {
					require.Equal(t, http.StatusConflict, c)
				}
			}
			require.Equal(t, 1, ok, fmt.Sprintf("round %d: qa=%d legacy=%d", i, qa, lg))
		}

		// send racing revoke: whatever is queued ends failed, never queued under a revoked admission
		for i := 0; i < 6; i++ {
			rr := s.register()
			var rec *httptest.ResponseRecorder
			wg.Add(2)
			go func() { defer wg.Done(); rec = s.send(s.sendMap(rr, uuid.New())) }()
			go func() {
				defer wg.Done()
				s.call(s.on.RevokeReceiver, http.MethodDelete, "/", "", s.rcv(), map[string]string{"receiver_id": rr.ID.String()})
			}()
			wg.Wait()
			if rec.Code == http.StatusCreated {
				var d struct {
					ID uuid.UUID `json:"delivery_id"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
				rc := s.get(d.ID)
				require.Equal(t, "failed", rc["state"], "round %d", i)
				require.Equal(t, "receiver_revoked", rc["error_code"])
			} else {
				require.Equal(t, http.StatusForbidden, rec.Code, "round %d: %s", i, rec.Body.String())
			}
		}
	})
}
