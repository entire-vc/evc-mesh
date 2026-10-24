package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type wsCapture struct {
	mu     sync.Mutex
	byChan map[string][]map[string]any
}

func (c *wsCapture) Publish(_ context.Context, channel string, event any) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChan == nil {
		c.byChan = map[string][]map[string]any{}
	}
	c.byChan[channel] = append(c.byChan[channel], m)
	return nil
}

func (c *wsCapture) channels() map[string][]map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string][]map[string]any{}
	for k, v := range c.byChan {
		out[k] = append([]map[string]any(nil), v...)
	}
	return out
}

type seqCounter struct{ n atomic.Int64 }

func (s *seqCounter) NextSeq(context.Context, uuid.UUID) (int64, error) { return s.n.Add(1), nil }

// deltasBeforeCompleted is the ordering rule of the final: no chat.message.delta
// may follow the chat.message.completed of its message.
func deltasBeforeCompleted(events []map[string]any) bool {
	completedAt := int64(-1)
	for _, e := range events {
		seq := int64(e["seq"].(float64))
		switch e["event"] {
		case service.ChatEventMessageCompleted:
			completedAt = seq
		case service.ChatEventMessageDelta:
			if completedAt >= 0 && seq > completedAt {
				return false
			}
		}
	}
	return true
}

type runnerEnv struct {
	db     *sqlx.DB
	srv    *httptest.Server
	ws     *wsCapture
	svc    *service.ChatTurnEventsService
	owner  uuid.UUID
	other  uuid.UUID
	ws_    uuid.UUID
	proj   uuid.UUID
	agent  uuid.UUID
	thread uuid.UUID
	runner uuid.UUID
	token  string
}

func newRunnerEnv(t *testing.T) *runnerEnv {
	t.Helper()
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	w := &domain.Workspace{ID: uuid.New(), Name: "Runner events", Slug: "re-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, w))
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, w.ID) })
	p := &domain.Project{ID: uuid.New(), WorkspaceID: w.ID, Name: "P", Slug: "re-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, p))
	mkUser := func() uuid.UUID {
		id := uuid.New()
		_, err := db.Exec(`INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1, $2, $3, 'x', 'u')`, id, id.String()+"@re.test", "re-"+id.String()[:30])
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, id) })
		return id
	}
	env := &runnerEnv{db: db, ws: &wsCapture{}, owner: mkUser(), other: mkUser(), ws_: w.ID, proj: p.ID,
		agent: uuid.New(), thread: uuid.New(), runner: uuid.New(), token: "claim-" + uuid.NewString()}
	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, 'a', $3, 'h', 'p')`,
		env.agent, w.ID, "re-agent-"+uuid.NewString()[:8])
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, title) VALUES ($1,$2,$3,$4,$5,'t')`,
		env.thread, w.ID, p.ID, env.agent, env.owner)
	require.NoError(t, err)

	env.svc = service.NewChatTurnEventsService(postgres.NewChatTurnEventsRepo(db),
		service.NewChatEventPublisher(env.ws, &seqCounter{}))
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	// Stands in for the agent-key middleware: the calling agent is the turn's own
	// unless a test names another one; X-Test-Anon drops the identity altogether.
	asAgent := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("X-Test-Anon") == "" {
				agent := env.agent
				if v, err := uuid.Parse(c.Request().Header.Get("X-Test-Agent")); err == nil {
					agent = v
				}
				c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
				c.Set(mw.ContextKeyAgentID, agent)
				c.Set(mw.ContextKeyWorkspaceID, env.ws_)
			}
			return next(c)
		}
	}
	e.POST("/api/v1/runner/chat/turns/:id/events", NewChatRunnerEventsHandler(env.svc).Events, asAgent)
	env.srv = httptest.NewServer(e)
	t.Cleanup(env.srv.Close)
	return env
}

func (env *runnerEnv) newTurn(t *testing.T, state string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := env.db.Exec(`INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, state, runner_id, claim_token, execution_epoch,
			lease_until, expires_at, first_claimed_at, started_at, attempt)
		VALUES ($1,$2,$3,$4,$5,$6,$7,1, now() + interval '60 seconds', now() + interval '24 hours', now(), now(), 1)`,
		id, env.ws_, env.thread, env.agent, state, env.runner, env.token)
	require.NoError(t, err)
	return id
}

func (env *runnerEnv) post(t *testing.T, turn uuid.UUID, hdr map[string]string, body any) (status int, body2 map[string]any, hdr2 http.Header) {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case []byte:
		raw = b
	default:
		var err error
		raw, err = json.Marshal(b)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/runner/chat/turns/"+turn.String()+"/events", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chat-Protocol", "1")
	req.Header.Set(chatRunnerIDHeader, env.runner.String())
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (env *runnerEnv) batch(events ...map[string]any) map[string]any {
	return map[string]any{"claim_token": env.token, "events": events}
}

func TestChatRunnerEvents_DeliveredToOwnerOnly_DeltasNeverPassFinal(t *testing.T) {
	env := newRunnerEnv(t)
	turn := env.newTurn(t, "streaming")

	code, out, _ := env.post(t, turn, nil, env.batch(
		map[string]any{"seq": 1, "type": "text_delta", "text": "Hel"},
		map[string]any{"seq": 2, "type": "tool_status", "tool_call_id": "t1", "label": "Reading settings", "status": "started"}))
	require.Equal(t, http.StatusOK, code, out)
	code, out, _ = env.post(t, turn, nil, env.batch(
		map[string]any{"seq": 3, "type": "text_delta", "text": "lo"},
		map[string]any{"seq": 4, "type": "usage", "tokens_in": 10, "tokens_out": 5, "cost_usd": 0.001, "model": "m"},
		map[string]any{"seq": 5, "type": "final", "stop_reason": "end_turn"}))
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "done", out["state"])
	assert.EqualValues(t, 5, out["applied_seq"])

	chans := env.ws.channels()
	require.Len(t, chans, 1, "events go to one channel and no other")
	events, ok := chans["ws:user:"+env.owner.String()]
	require.True(t, ok, "the thread owner's channel")
	assert.NotContains(t, chans, "ws:user:"+env.other.String())

	var kinds []string
	var text strings.Builder
	var completed map[string]any
	for _, e := range events {
		kinds = append(kinds, e["event"].(string))
		if e["event"] == service.ChatEventMessageDelta {
			text.WriteString(e["text"].(string))
		}
		if e["event"] == service.ChatEventMessageCompleted {
			completed = e
		}
	}
	assert.True(t, deltasBeforeCompleted(events), "kinds: %v", kinds)
	assert.Equal(t, "Hello", text.String())
	require.NotNil(t, completed)
	assert.Equal(t, "Hello", completed["message"].(map[string]any)["body"])
	assert.Contains(t, kinds, service.ChatEventTurnTool)
	assert.Equal(t, service.ChatEventTurnState, kinds[len(kinds)-1], "terminal state is announced last")
	last := events[len(events)-1]
	assert.Equal(t, "done", last["turn"].(map[string]any)["state"])
}

// Red control for the order: the check must fail on a final published the wrong
// way round (completed first, the trailing delta flushed by Close afterwards).
func TestChatRunnerEvents_OrderCheckFailsOnDeltaAfterCompleted(t *testing.T) {
	ws := &wsCapture{}
	pub := service.NewChatEventPublisher(ws, &seqCounter{})
	owner := uuid.New()
	th := service.ChatThreadRef{WorkspaceID: uuid.New(), ProjectID: uuid.New(), ThreadID: uuid.New(), OwnerID: owner}
	msg := service.ChatMessagePayload{ID: uuid.New(), ThreadID: th.ThreadID, Role: "agent", Status: "done"}
	stream := pub.NewDeltaStream(th, msg.ID, uuid.New())
	stream.Append("tail")
	require.NoError(t, pub.MessageCompleted(context.Background(), th, msg)) // the bug: completed before Close
	require.NoError(t, stream.Close(context.Background()))
	events := ws.channels()["ws:user:"+owner.String()]
	require.Len(t, events, 2)
	assert.False(t, deltasBeforeCompleted(events), "a delta after completed must be caught")
}

func TestChatRunnerEvents_ReplayAndCancelOverHTTP(t *testing.T) {
	env := newRunnerEnv(t)
	turn := env.newTurn(t, "streaming")
	b := env.batch(map[string]any{"seq": 1, "type": "text_delta", "text": "ab"})

	code, out, _ := env.post(t, turn, nil, b)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, out["cancel_requested"])
	sent := len(env.ws.channels()["ws:user:"+env.owner.String()])

	code, out, _ = env.post(t, turn, nil, b)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, out["applied_seq"])
	var body string
	require.NoError(t, env.db.Get(&body, `SELECT m.body FROM ext_agent_chat_messages m JOIN ext_agent_chat_turns t ON t.agent_message_id = m.id WHERE t.id = $1`, turn))
	assert.Equal(t, "ab", body, "a repeated batch does not append twice")
	assert.Equal(t, sent, len(env.ws.channels()["ws:user:"+env.owner.String()]), "nor publish twice")

	state, err := env.svc.RequestCancel(context.Background(), env.owner, env.proj, env.thread)
	require.NoError(t, err)
	assert.Equal(t, "streaming", state)
	code, out, _ = env.post(t, turn, nil, env.batch(map[string]any{"seq": 2, "type": "text_delta", "text": "c"}))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, out["cancel_requested"], "the runner learns of Stop from the events answer")
}

func TestChatRunnerEvents_HTTPRefusals(t *testing.T) {
	env := newRunnerEnv(t)
	turn := env.newTurn(t, "streaming")
	ok := env.batch(map[string]any{"seq": 1, "type": "text_delta", "text": "x"})

	code, out, h := env.post(t, turn, map[string]string{"X-Chat-Protocol": "2"}, ok)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "chat_protocol_unsupported", out["error_code"])
	assert.Equal(t, "1", h.Get("X-Chat-Protocol-Supported"))

	code, _, _ = env.post(t, turn, map[string]string{"X-Test-Anon": "1"}, ok)
	assert.Equal(t, http.StatusUnauthorized, code, "only an agent key is a runner")

	code, out, _ = env.post(t, turn, map[string]string{"X-Test-Agent": uuid.NewString()}, ok)
	assert.Equal(t, http.StatusNotFound, code, "a runner of another agent sees the same answer as for an unknown turn")
	assert.Equal(t, "chat_turn_not_found", out["error_code"])

	code, out, _ = env.post(t, turn, map[string]string{chatRunnerIDHeader: uuid.NewString()}, ok)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "chat_not_claim_holder", out["error_code"])

	code, out, _ = env.post(t, turn, nil, map[string]any{"claim_token": "stale", "events": ok["events"]})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "chat_stale_claim", out["error_code"])

	code, out, _ = env.post(t, uuid.New(), nil, ok)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "chat_turn_not_found", out["error_code"])

	code, out, _ = env.post(t, turn, nil, env.batch(map[string]any{"seq": 4, "type": "text_delta", "text": "x"}))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "chat_seq_gap", out["error_code"])
	assert.EqualValues(t, 1, out["context"].(map[string]any)["expected_seq"])

	many := make([]map[string]any, 201)
	for i := range many {
		many[i] = map[string]any{"seq": i + 1, "type": "text_delta", "text": "x"}
	}
	code, out, _ = env.post(t, turn, nil, env.batch(many...))
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "chat_batch_too_large", out["error_code"])

	huge := []byte(fmt.Sprintf(`{"claim_token":%q,"events":[{"seq":1,"type":"text_delta","text":%q}]}`, env.token, strings.Repeat("x", 300<<10)))
	code, out, _ = env.post(t, turn, nil, huge)
	assert.Equal(t, http.StatusRequestEntityTooLarge, code)
	assert.Equal(t, "chat_batch_too_large", out["error_code"])
}

func TestChatRunnerEvents_RateLimitedPerTurn(t *testing.T) {
	env := newRunnerEnv(t)
	turn := env.newTurn(t, "streaming")
	b := env.batch(map[string]any{"seq": 1, "type": "text_delta", "text": "x"})
	limited := 0
	for i := 0; i < 40; i++ {
		code, out, h := env.post(t, turn, nil, b)
		if code == http.StatusTooManyRequests {
			limited++
			assert.Equal(t, "chat_rate_limited", out["error_code"])
			assert.NotEmpty(t, h.Get("Retry-After"))
		}
	}
	assert.Positive(t, limited, "more than 20 batches in a second must be refused")
}
