package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// mockTeamRelayMountService is a hand-written double for the two-method
// TeamRelayMountService interface.
type mockTeamRelayMountService struct {
	syncFunc  func(ctx context.Context, projectID uuid.UUID) (*service.MountResult, error)
	syncCalls int
}

func (m *mockTeamRelayMountService) SyncMount(ctx context.Context, projectID uuid.UUID) (*service.MountResult, error) {
	m.syncCalls++
	if m.syncFunc != nil {
		return m.syncFunc(ctx, projectID)
	}
	return &service.MountResult{Status: service.MountStatusOK}, nil
}

func (m *mockTeamRelayMountService) RefreshIfStale(_ context.Context, _ *domain.Document) error {
	return nil
}

// WriteBack is part of the service's interface but not of this handler's job —
// mounting a share never writes back. Present only to satisfy the interface,
// and it returns an error rather than a plausible hash so that a handler which
// somehow started calling it would fail loudly instead of appearing to work.
func (m *mockTeamRelayMountService) WriteBack(_ context.Context, _ *domain.Document, _ string) (string, error) {
	return "", errors.New("mockTeamRelayMountService: WriteBack must not be called by the mount handler")
}

var _ service.TeamRelayMountService = (*mockTeamRelayMountService)(nil)

func trMountRequest(t *testing.T, h *TrMountHandler, projIDParam string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tr/mount")
	c.SetParamNames("proj_id")
	c.SetParamValues(projIDParam)

	require.NoError(t, h.Sync(c))
	return rec
}

// AC-4 lives or dies here. The service classifies four different failures into
// four named sentinels; if the handler flattens any two of them onto the same
// HTTP code, an operator staring at the response cannot tell "your key expired"
// from "the relay is down" — and a share that answers with an empty tree reads
// as "this share has no documents", which is exactly the state AC-4 forbids.
//
// Asserting the mapping table by iterating the table itself would be circular,
// so every expectation below is written out literally.
func TestTrMountHandler_EachStatusGetsItsOwnHTTPCode(t *testing.T) {
	cases := []struct {
		name       string
		status     service.MountStatus
		detail     error
		wantCode   int
		wantDetail string
	}{
		{"ok", service.MountStatusOK, nil, http.StatusOK, ""},
		{"not configured is not a failure", service.MountStatusNotConfigured, nil, http.StatusOK, ""},
		{"key rejected", service.MountStatusKeyRejected, errors.New("agent key rejected"), http.StatusUnauthorized, "agent key rejected"},
		{"foreign share", service.MountStatusForeignShare, errors.New("key not valid for share"), http.StatusForbidden, "key not valid for share"},
		{"unreachable", service.MountStatusUnreachable, errors.New("dial tcp: timeout"), http.StatusBadGateway, "dial tcp: timeout"},
		{"share not found", service.MountStatusShareNotFound, errors.New("no such share"), http.StatusNotFound, "no such share"},
		{"unclassified error", service.MountStatusError, errors.New("boom"), http.StatusInternalServerError, "boom"},
	}

	seenCodes := map[int][]string{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockTeamRelayMountService{
				syncFunc: func(_ context.Context, _ uuid.UUID) (*service.MountResult, error) {
					return &service.MountResult{Status: tc.status, Mounted: 3, Skipped: 4, Err: tc.detail}, nil
				},
			}
			rec := trMountRequest(t, NewTrMountHandler(mock), uuid.New().String())

			assert.Equal(t, tc.wantCode, rec.Code, "status %q must map to its own HTTP code", tc.status)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, string(tc.status), body["status"], "the named status must survive to the body, not just the code")
			assert.EqualValues(t, 3, body["mounted"])
			assert.EqualValues(t, 4, body["skipped"])

			if tc.wantDetail == "" {
				_, hasDetail := body["detail"]
				assert.False(t, hasDetail, "no underlying error means no detail key")
			} else {
				assert.Equal(t, tc.wantDetail, body["detail"], "the operator needs WHY, not only the classification")
			}
		})
		seenCodes[tc.wantCode] = append(seenCodes[tc.wantCode], string(tc.status))
	}

	// The four *failure* sentinels must be mutually distinguishable by code
	// alone. (ok/not_configured deliberately share 200 — neither is a failure.)
	assert.Equal(t, []string{"ok", "not_configured"}, seenCodes[http.StatusOK])
	for code, statuses := range seenCodes {
		if code == http.StatusOK {
			continue
		}
		assert.Len(t, statuses, 1, "HTTP %d is shared by %v — two different failures became indistinguishable", code, statuses)
	}
}

// An unknown status must not fall through as a 200. A future status added to
// the service without a mapping entry here would otherwise be reported as
// success, which is the silent-wrong-answer direction.
func TestTrMountHandler_UnmappedStatusFailsClosed(t *testing.T) {
	mock := &mockTeamRelayMountService{
		syncFunc: func(_ context.Context, _ uuid.UUID) (*service.MountResult, error) {
			return &service.MountResult{Status: service.MountStatus("some_future_status")}, nil
		},
	}
	rec := trMountRequest(t, NewTrMountHandler(mock), uuid.New().String())
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"an unmapped status must fail closed, never be reported as success")
}

func TestTrMountHandler_InvalidProjectID_IsRejectedWithoutCallingTheService(t *testing.T) {
	mock := &mockTeamRelayMountService{}
	rec := trMountRequest(t, NewTrMountHandler(mock), "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, mock.syncCalls, "a malformed id must be rejected before any share is walked")
}

func TestTrMountHandler_PassesTheParsedProjectIDThrough(t *testing.T) {
	projectID := uuid.New()
	var got uuid.UUID
	mock := &mockTeamRelayMountService{
		syncFunc: func(_ context.Context, p uuid.UUID) (*service.MountResult, error) {
			got = p
			return &service.MountResult{Status: service.MountStatusOK}, nil
		},
	}
	rec := trMountRequest(t, NewTrMountHandler(mock), projectID.String())

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, projectID, got, "the handler must mount the project that was asked for")
}

func TestTrMountHandler_ServiceError_IsSurfaced(t *testing.T) {
	mock := &mockTeamRelayMountService{
		syncFunc: func(_ context.Context, _ uuid.UUID) (*service.MountResult, error) {
			return nil, errors.New("settings lookup exploded")
		},
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/projects/:proj_id/tr/mount")
	c.SetParamNames("proj_id")
	c.SetParamValues(uuid.New().String())

	err := NewTrMountHandler(mock).Sync(c)

	// handleError either writes a response or returns the error for echo's
	// handler; either way it must NOT be a 200.
	if err == nil {
		assert.NotEqual(t, http.StatusOK, rec.Code, "a service failure must never read as success")
	}
}
