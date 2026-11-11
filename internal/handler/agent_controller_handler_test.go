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

type fakeSnapshotRepo struct {
	saved []repository.ChatRunnerSnapshot
}

func (f *fakeSnapshotRepo) SaveChatSnapshot(_ context.Context, s repository.ChatRunnerSnapshot) (bool, error) {
	f.saved = append(f.saved, s)
	return true, nil
}

func (f *fakeSnapshotRepo) LatestChatSnapshot(context.Context, uuid.UUID) (*repository.ChatRunnerSnapshot, error) {
	return nil, nil
}

func (f *fakeSnapshotRepo) RunnerChatSnapshots(context.Context, uuid.UUID, string) ([]repository.ChatRunnerSnapshot, error) {
	return nil, nil
}

func TestChatRuntimeSnapshot_AnnounceParsesSnapshot(t *testing.T) {
	snaps := &fakeSnapshotRepo{}
	h := NewAgentControllerHandler(&fakeControllerRepo{cur: map[uuid.UUID]repository.AgentController{}, now: time.Now()}).WithChatSnapshots(snaps)
	agent, thread := uuid.New(), uuid.New()
	id := `"runner_id":"r-a","name":"runner-a","host":"mini-1"`

	require.Equal(t, http.StatusOK, announce(t, h, agent, `{`+id+`}`).Code)
	require.Empty(t, snaps.saved, "an announce without sessions stores nothing")

	body := `{` + id + `,"sessions":[{"thread_id":"` + thread.String() + `","agent":"claude","process_alive":true,"busy":false,"last_end_at":"2026-11-10T19:00:00Z"}],"slots":{"max_per_agent":3,"used":1},"reported_at":"2026-11-10T20:00:00+03:00"}`
	require.Equal(t, http.StatusOK, announce(t, h, agent, body).Code)
	require.Len(t, snaps.saved, 1)
	got := snaps.saved[0]
	assert.Equal(t, agent, got.AgentID)
	assert.Equal(t, "r-a", got.RunnerID)
	require.Len(t, got.Sessions, 1)
	assert.Equal(t, thread, got.Sessions[0].ThreadID)
	assert.True(t, got.Sessions[0].ProcessAlive)
	assert.Equal(t, 3, *got.SlotsMax)
	assert.Equal(t, 1, *got.SlotsUsed)
	assert.Equal(t, time.Date(2026, 11, 10, 17, 0, 0, 0, time.UTC), got.ReportedAt)

	require.Equal(t, http.StatusBadRequest, announce(t, h, agent, `{`+id+`,"sessions":[{"thread_id":"x"}]}`).Code)
	require.Equal(t, http.StatusConflict, announce(t, h, agent, `{"runner_id":"r-b","name":"b","host":"h","sessions":[]}`).Code)
	require.Len(t, snaps.saved, 1, "a refused announce stores nothing")
}

// The freshness window itself, so a removed or widened threshold fails here
// as well as in the DB-backed test.
func TestChatRuntimeSnapshot_FreshWindow(t *testing.T) {
	assert.Equal(t, 120*time.Second, ChatSnapshotFreshFor)
	assert.False(t, chatSnapshotFresh(nil))
	assert.True(t, chatSnapshotFresh(&repository.ChatRunnerSnapshot{Age: 119 * time.Second}))
	assert.True(t, chatSnapshotFresh(&repository.ChatRunnerSnapshot{Age: 120 * time.Second}))
	assert.False(t, chatSnapshotFresh(&repository.ChatRunnerSnapshot{Age: 121 * time.Second}))
}

// Explicit false survives parsing and JSON storage for stopped sessions; older
// snapshot senders without slot_held retain the listed-session default.
func TestChatSnapshotSlotHeldRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		held  bool
	}{
		{"held", `,"slot_held":true`, true},
		{"released", `,"slot_held":false`, false},
		{"legacy", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req announceControllerRequest
			body := `{"sessions":[{"thread_id":"` + uuid.NewString() + `","process_alive":false` + tc.field + `}]}`
			require.NoError(t, json.Unmarshal([]byte(body), &req))
			snapshot, err := req.chatSnapshot(uuid.New())
			require.NoError(t, err)
			data, err := json.Marshal(snapshot.Sessions)
			require.NoError(t, err)
			var stored []map[string]any
			require.NoError(t, json.Unmarshal(data, &stored))
			require.Len(t, stored, 1)
			assert.Equal(t, tc.held, stored[0]["slot_held"])
		})
	}
}
