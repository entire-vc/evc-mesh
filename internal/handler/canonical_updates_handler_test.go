package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// setupCanonicalUpdatesTest is a helper that wires up the handler, Echo context,
// and sets up valid agent_id / workspace_id context values (required by GetAgentID / GetWorkspaceID).
func setupCanonicalUpdatesTest(
	ms *MockMemoryService,
	sr *MockAgentSessionRepository,
	as *MockAgentService,
	agentID uuid.UUID,
	workspaceID uuid.UUID,
	url string,
) (echo.Context, *httptest.ResponseRecorder, *CanonicalUpdatesHandler) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, url, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("agent_id", agentID)
	c.Set("workspace_id", workspaceID)

	h := NewCanonicalUpdatesHandler(ms, sr, as)
	return c, rec, h
}

// TestGetCanonicalUpdates_SinceCursor verifies that an explicit ?since param is
// passed through to ListMemories as the Since filter.
func TestGetCanonicalUpdates_SinceCursor(t *testing.T) {
	agentID := uuid.New()
	workspaceID := uuid.New()

	sinceTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sinceStr := sinceTime.Format(time.RFC3339)

	var capturedFilter domain.MemoryListFilter
	ms := &MockMemoryService{
		ListMemoriesFunc: func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
			capturedFilter = filter
			return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
		},
	}
	sr := &MockAgentSessionRepository{}
	as := &MockAgentService{}

	c, rec, h := setupCanonicalUpdatesTest(ms, sr, as, agentID, workspaceID,
		"/?since="+sinceStr)

	err := h.GetCanonicalUpdates(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify Since was correctly passed.
	require.NotNil(t, capturedFilter.Since)
	assert.Equal(t, sinceTime.UTC(), capturedFilter.Since.UTC())
}

// TestGetCanonicalUpdates_PrivacyFilter verifies that "privacy:public" is always
// present in the Tags (AND) filter — enforced at query level, not post-hoc.
func TestGetCanonicalUpdates_PrivacyFilter(t *testing.T) {
	agentID := uuid.New()
	workspaceID := uuid.New()

	var capturedFilter domain.MemoryListFilter
	ms := &MockMemoryService{
		ListMemoriesFunc: func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
			capturedFilter = filter
			return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
		},
	}
	sr := &MockAgentSessionRepository{}
	as := &MockAgentService{}

	c, rec, h := setupCanonicalUpdatesTest(ms, sr, as, agentID, workspaceID, "/")

	err := h.GetCanonicalUpdates(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Privacy:public must be in the AND Tags filter.
	assert.Contains(t, capturedFilter.Tags, "privacy:public",
		"privacy:public must be in Tags AND filter")

	// kind:canonical-decision must also be in the AND Tags filter.
	assert.Contains(t, capturedFilter.Tags, "kind:canonical-decision",
		"kind:canonical-decision must be in Tags AND filter")
}

// TestGetCanonicalUpdates_AgentFilter verifies that propagate_to tags are placed
// in TagsAny (OR filter). When ?agent=<slug> is provided, propagate_to:<slug>
// is included alongside propagate_to:all.
func TestGetCanonicalUpdates_AgentFilter(t *testing.T) {
	agentID := uuid.New()
	workspaceID := uuid.New()
	targetAgentID := uuid.New()
	targetSlug := "linus"

	var capturedFilter domain.MemoryListFilter
	ms := &MockMemoryService{
		ListMemoriesFunc: func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
			capturedFilter = filter
			return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
		},
	}
	sr := &MockAgentSessionRepository{}
	as := &MockAgentService{
		GetBySlugFunc: func(ctx context.Context, wsID uuid.UUID, slug string) (*domain.Agent, error) {
			assert.Equal(t, workspaceID, wsID)
			assert.Equal(t, targetSlug, slug)
			return &domain.Agent{ID: targetAgentID, Slug: targetSlug}, nil
		},
	}

	c, rec, h := setupCanonicalUpdatesTest(ms, sr, as, agentID, workspaceID,
		"/?agent="+targetSlug)

	err := h.GetCanonicalUpdates(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// TagsAny must contain both propagate_to:all and propagate_to:<slug>.
	assert.Contains(t, capturedFilter.TagsAny, "propagate_to:all",
		"propagate_to:all must always be in TagsAny")
	assert.Contains(t, capturedFilter.TagsAny, "propagate_to:"+targetSlug,
		"propagate_to:<slug> must be in TagsAny when ?agent is given")
}

// TestGetCanonicalUpdates_DefaultSince verifies that when ?since is omitted,
// the handler calls GetPreviousStartedAt on the session repo and uses that value.
// When GetPreviousStartedAt returns nil, it should default to 24h ago.
func TestGetCanonicalUpdates_DefaultSince(t *testing.T) {
	t.Run("uses previous session started_at when available", func(t *testing.T) {
		agentID := uuid.New()
		workspaceID := uuid.New()

		prevSession := time.Date(2026, 5, 31, 8, 0, 0, 0, time.UTC)

		var capturedFilter domain.MemoryListFilter
		ms := &MockMemoryService{
			ListMemoriesFunc: func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
				capturedFilter = filter
				return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
			},
		}
		sr := &MockAgentSessionRepository{
			GetPreviousStartedAtFunc: func(ctx context.Context, id uuid.UUID) (*time.Time, error) {
				assert.Equal(t, agentID, id)
				return &prevSession, nil
			},
		}
		as := &MockAgentService{}

		c, rec, h := setupCanonicalUpdatesTest(ms, sr, as, agentID, workspaceID, "/")

		err := h.GetCanonicalUpdates(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)

		require.NotNil(t, capturedFilter.Since)
		assert.Equal(t, prevSession.UTC(), capturedFilter.Since.UTC())
	})

	t.Run("falls back to 24h ago when no prior session", func(t *testing.T) {
		agentID := uuid.New()
		workspaceID := uuid.New()
		before := time.Now().Add(-24 * time.Hour)

		var capturedFilter domain.MemoryListFilter
		ms := &MockMemoryService{
			ListMemoriesFunc: func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
				capturedFilter = filter
				return &service.RecallResult{Items: []domain.ScoredMemory{}}, nil
			},
		}
		sr := &MockAgentSessionRepository{
			GetPreviousStartedAtFunc: func(ctx context.Context, id uuid.UUID) (*time.Time, error) {
				return nil, nil // no prior session
			},
		}
		as := &MockAgentService{}

		c, rec, h := setupCanonicalUpdatesTest(ms, sr, as, agentID, workspaceID, "/")

		err := h.GetCanonicalUpdates(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)

		require.NotNil(t, capturedFilter.Since)
		// Should be approximately 24h ago — allow 5 second drift.
		assert.WithinDuration(t, before, *capturedFilter.Since, 5*time.Second)
	})
}

// TestGetCanonicalUpdates_ResponseShape verifies that a memory record returned by
// ListMemories is correctly mapped to the response shape.
func TestGetCanonicalUpdates_ResponseShape(t *testing.T) {
	agentID := uuid.New()
	workspaceID := uuid.New()
	memID := uuid.New()
	projID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	mem := domain.Memory{
		ID:          memID,
		WorkspaceID: workspaceID,
		ProjectID:   &projID,
		Key:         "API rate limit decision",
		Content:     "All external API calls capped at 100 req/min.",
		Tags: []string{
			"privacy:public",
			"kind:canonical-decision",
			"propagate_to:all",
			"source:garfield",
			"ref:task-abc123",
			"affects:linus",
		},
		CreatedAt: now,
	}

	ms := &MockMemoryService{
		ListMemoriesFunc: func(ctx context.Context, filter domain.MemoryListFilter) (*service.RecallResult, error) {
			return &service.RecallResult{
				Items: []domain.ScoredMemory{{Memory: mem}},
			}, nil
		},
	}
	sr := &MockAgentSessionRepository{}
	as := &MockAgentService{}

	c, rec, h := setupCanonicalUpdatesTest(ms, sr, as, agentID, workspaceID, "/")
	err := h.GetCanonicalUpdates(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.EqualValues(t, 1, resp["count"])
	updates := resp["updates"].([]interface{})
	require.Len(t, updates, 1)
	item := updates[0].(map[string]interface{})

	assert.Equal(t, memID.String(), item["id"])
	assert.Equal(t, "canonical-decision", item["kind"])
	assert.Equal(t, "garfield", item["source"])
	assert.Equal(t, "task-abc123", item["ref"])
	assert.Equal(t, "API rate limit decision", item["summary"])
	assert.Equal(t, "All external API calls capped at 100 req/min.", item["text"])
	// affects should include linus (from affects: tag)
	affects := item["affects"].([]interface{})
	assert.Contains(t, affects, "linus")
}
