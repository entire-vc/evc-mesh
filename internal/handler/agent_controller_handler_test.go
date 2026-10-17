package handler

import (
	"context"
	"encoding/json"
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

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// fakeControllerRepo mirrors the Claim contract (free / same runner / lapsed
// lease succeeds, otherwise held=false); the SQL itself is covered by the
// DATABASE_URL-gated test in the postgres package.
type fakeControllerRepo struct {
	mu  sync.Mutex
	cur map[uuid.UUID]repository.AgentController
	now time.Time
}

func (f *fakeControllerRepo) Claim(_ context.Context, c repository.AgentController, ttl time.Duration) (*repository.AgentController, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h, ok := f.cur[c.AgentID]; ok && h.RunnerID != c.RunnerID && h.LeaseExpiresAt.After(f.now) {
		return &h, false, nil
	}
	c.LeaseExpiresAt = f.now.Add(ttl)
	f.cur[c.AgentID] = c
	return &c, true, nil
}

func (f *fakeControllerRepo) Renew(context.Context, uuid.UUID, string, time.Duration) error {
	return nil
}

func announce(t *testing.T, h *AgentControllerHandler, agent uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/agents/me/controller", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if agent != uuid.Nil {
		c.Set("agent_id", agent)
	}
	require.NoError(t, h.Announce(c))
	return rec
}

func TestAnnounceController_SecondProcessGets409ThenTakeoverAfterLease(t *testing.T) {
	repo := &fakeControllerRepo{cur: map[uuid.UUID]repository.AgentController{}, now: time.Now()}
	h := NewAgentControllerHandler(repo)
	agent := uuid.New()
	a := `{"runner_id":"r-a","name":"runner-a","host":"mini-1","version":"1.0"}`
	b := `{"runner_id":"r-b","name":"runner-b","host":"mini-2","version":"1.0"}`

	assert.Equal(t, http.StatusOK, announce(t, h, agent, a).Code)
	assert.Equal(t, http.StatusOK, announce(t, h, agent, a).Code, "same runner renews")

	rec := announce(t, h, agent, b)
	require.Equal(t, http.StatusConflict, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["message"], "runner-a")
	assert.Contains(t, body["message"], "mini-1")

	repo.now = repo.now.Add(AgentControllerLease + time.Second)
	assert.Equal(t, http.StatusOK, announce(t, h, agent, b).Code, "lapsed lease is taken over")
	assert.Equal(t, http.StatusConflict, announce(t, h, agent, a).Code, "former holder is now locked out")
}

func TestAnnounceController_Validation(t *testing.T) {
	h := NewAgentControllerHandler(&fakeControllerRepo{cur: map[uuid.UUID]repository.AgentController{}, now: time.Now()})
	assert.Equal(t, http.StatusUnauthorized, announce(t, h, uuid.Nil, `{}`).Code)
	assert.Equal(t, http.StatusBadRequest, announce(t, h, uuid.New(), `{"runner_id":"x","name":"n"}`).Code)
}
