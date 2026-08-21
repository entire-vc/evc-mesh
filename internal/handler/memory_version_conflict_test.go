package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Acceptance 1 of task #0ba5e66a, transport half: a lost conditional write must
// reach the caller as a 409 naming both versions.
//
// The service-level test proves the write is refused; this proves the refusal
// survives serialisation. Without it the service could be correct while the
// handler mapped the conflict onto a generic 500 — which reads to a caller as
// "the server broke, retry", the opposite of "someone else edited this, re-read
// before you retry". The two produce different client behaviour, so the status
// code is part of the contract and not a detail.
func TestRemember_VersionConflictReachesTheCallerAs409(t *testing.T) {
	ws := uuid.New()

	ms := &MockMemoryService{
		RememberIntentFunc: func(_ context.Context, _ *domain.Memory, intent domain.MemoryWriteIntent) (service.RememberResult, error) {
			if intent.ExpectedVersion == nil {
				t.Fatalf("handler did not forward expected_version to the service")
			}
			return service.RememberResult{}, &domain.MemoryVersionConflictError{
				Key: "deploy-ordering", Expected: *intent.ExpectedVersion, Actual: 7,
			}
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	body := fmt.Sprintf(
		`{"workspace_id":%q,"key":"deploy-ordering","content":"new text","scope":"workspace","expected_version":3,"reason":"correcting the host"}`,
		ws)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ws)

	if err := h.Remember(c); err != nil {
		t.Fatalf("Remember returned a transport error: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409 — a lost race is not a server fault", rec.Code)
	}

	var got struct {
		Error           string `json:"error"`
		Key             string `json:"key"`
		ExpectedVersion int    `json:"expected_version"`
		ActualVersion   int    `json:"actual_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not JSON: %v", err)
	}
	if got.ExpectedVersion != 3 || got.ActualVersion != 7 {
		t.Errorf("body must carry BOTH versions so the caller can re-read without "+
			"another round trip; got expected=%d actual=%d", got.ExpectedVersion, got.ActualVersion)
	}
	if got.Key != "deploy-ordering" {
		t.Errorf("body must name the key that conflicted, got %q", got.Key)
	}
	if !strings.Contains(got.Error, "modified by someone else") {
		t.Errorf("the message must say what happened, got %q", got.Error)
	}
}

// The reason and expected_version fields have to actually leave the handler, or
// the whole feature is a no-op behind a correct-looking API.
func TestRemember_ForwardsReasonAndExpectedVersion(t *testing.T) {
	ws := uuid.New()
	var seen domain.MemoryWriteIntent
	ms := &MockMemoryService{
		RememberIntentFunc: func(_ context.Context, _ *domain.Memory, intent domain.MemoryWriteIntent) (service.RememberResult, error) {
			seen = intent
			return service.RememberResult{Outcome: "updated", Version: 4}, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	body := fmt.Sprintf(
		`{"workspace_id":%q,"key":"k","content":"c","scope":"workspace","expected_version":3,"reason":"because the host moved"}`,
		ws)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ws)

	if err := h.Remember(c); err != nil {
		t.Fatalf("Remember returned a transport error: %v", err)
	}
	if seen.Reason != "because the host moved" {
		t.Errorf("reason not forwarded, got %q", seen.Reason)
	}
	if seen.ExpectedVersion == nil || *seen.ExpectedVersion != 3 {
		t.Errorf("expected_version not forwarded, got %v", seen.ExpectedVersion)
	}

	// The version the write produced comes back, so a caller can chain a
	// conditional write without re-reading.
	var got struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not JSON: %v", err)
	}
	if got.Version != 4 {
		t.Errorf("response must carry the new version, got %d", got.Version)
	}
}

// The revisions endpoint must exist and must be scoped to the memory's own
// workspace. Revision rows carry full prior CONTENT, so an unscoped read here
// would leak the text of memories the caller cannot read through GetByID — a
// tenant hole opened by the audit trail rather than by the table it audits.
func TestRevisions_ScopedToOwningWorkspace(t *testing.T) {
	ws := uuid.New()
	other := uuid.New()
	memID := uuid.New()
	reason := "corrected after the cutover"

	newHandler := func(memWorkspace uuid.UUID, called *bool) *MemoryHandler {
		return NewMemoryHandler(&MockMemoryService{
			GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
				return &domain.Memory{ID: id, WorkspaceID: memWorkspace}, nil
			},
			ListRevisionsFunc: func(_ context.Context, id uuid.UUID, limit int) ([]domain.MemoryRevision, error) {
				*called = true
				return []domain.MemoryRevision{{
					MemoryID: id, Version: 2, Content: "prod runs in Helsinki",
					Action: domain.MemoryActionUpdated, Reason: &reason,
				}}, nil
			},
		}, &mockWorkspaceMemberRepo{})
	}

	call := func(h *MemoryHandler, caller uuid.UUID) *httptest.ResponseRecorder {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/"+memID.String()+"/revisions", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(memID.String())
		asAgent(c, uuid.New(), caller)
		_ = h.Revisions(c)
		return rec
	}

	t.Run("caller in the owning workspace sees the history", func(t *testing.T) {
		listed := false
		rec := call(newHandler(ws, &listed), ws)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rec.Code)
		}
		var got struct {
			Items []domain.MemoryRevision `json:"items"`
			Count int                     `json:"count"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if got.Count != 1 || len(got.Items) != 1 {
			t.Fatalf("expected one revision, got %d", got.Count)
		}
		if got.Items[0].Reason == nil || *got.Items[0].Reason != reason {
			t.Errorf("the reason must reach the caller; got %v", got.Items[0].Reason)
		}
		if got.Items[0].Content != "prod runs in Helsinki" {
			t.Errorf("prior content must be readable, got %q", got.Items[0].Content)
		}
	})

	t.Run("caller from another workspace gets 404 and no history is read", func(t *testing.T) {
		listed := false
		rec := call(newHandler(ws, &listed), other)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("got %d, want 404 — a foreign workspace must not read revisions", rec.Code)
		}
		if listed {
			t.Error("ListRevisions must not even be reached for a foreign caller")
		}
		if strings.Contains(rec.Body.String(), "Helsinki") {
			t.Error("prior content leaked into a denial response")
		}
	})
}

func TestRevisions_EdgeCases(t *testing.T) {
	ws := uuid.New()
	memID := uuid.New()

	t.Run("a malformed id is a 400, not a lookup", func(t *testing.T) {
		looked := false
		h := NewMemoryHandler(&MockMemoryService{
			GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Memory, error) {
				looked = true
				return nil, nil
			},
		}, &mockWorkspaceMemberRepo{})
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetParamNames("id")
		c.SetParamValues("not-a-uuid")
		asAgent(c, uuid.New(), ws)
		_ = h.Revisions(c)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400", rec.Code)
		}
		if looked {
			t.Error("a malformed id must be rejected before any lookup")
		}
	})

	t.Run("a memory with no history returns an empty array, not null", func(t *testing.T) {
		h := NewMemoryHandler(&MockMemoryService{
			GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
				return &domain.Memory{ID: id, WorkspaceID: ws}, nil
			},
			ListRevisionsFunc: func(_ context.Context, _ uuid.UUID, _ int) ([]domain.MemoryRevision, error) {
				return nil, nil // the pre-migration case: the row predates history
			},
		}, &mockWorkspaceMemberRepo{})
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetParamNames("id")
		c.SetParamValues(memID.String())
		asAgent(c, uuid.New(), ws)
		_ = h.Revisions(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200 — no history is a valid answer", rec.Code)
		}
		// `"items":null` forces every client to special-case nil before ranging.
		if !strings.Contains(rec.Body.String(), `"items":[]`) {
			t.Errorf("empty history must serialise as [], got %s", rec.Body.String())
		}
	})

	t.Run("limit is forwarded, and junk falls back to the default", func(t *testing.T) {
		for _, tc := range []struct {
			query string
			want  int
		}{
			{"?limit=3", 3},
			{"?limit=abc", 50},
			{"?limit=0", 50},
			{"?limit=-4", 50},
			{"", 50},
		} {
			var seen int
			h := NewMemoryHandler(&MockMemoryService{
				GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
					return &domain.Memory{ID: id, WorkspaceID: ws}, nil
				},
				ListRevisionsFunc: func(_ context.Context, _ uuid.UUID, limit int) ([]domain.MemoryRevision, error) {
					seen = limit
					return []domain.MemoryRevision{}, nil
				},
			}, &mockWorkspaceMemberRepo{})
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/"+tc.query, http.NoBody), rec)
			c.SetParamNames("id")
			c.SetParamValues(memID.String())
			asAgent(c, uuid.New(), ws)
			_ = h.Revisions(c)
			if seen != tc.want {
				t.Errorf("limit%q: service saw %d, want %d", tc.query, seen, tc.want)
			}
		}
	})
}
