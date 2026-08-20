package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// leakedSecret stands in for the class of thing that legitimately ends up in
// Changes.old — a prior revision of a field that has since been edited. Task
// cab1e85f's live repro was exactly this: a task description edit left a prod
// password in Changes.old, readable by anyone who could reach this route.
const leakedSecret = "prod-password-hunter2"

func newActivityTestEntry() domain.ActivityLog {
	return domain.ActivityLog{
		ID:         uuid.New(),
		EntityType: "task",
		EntityID:   uuid.New(),
		Action:     "task.updated",
		ActorID:    uuid.New(),
		ActorType:  domain.ActorTypeUser,
		Changes:    json.RawMessage(`{"description":{"old":"` + leakedSecret + `","new":"redacted"}}`),
	}
}

func setupActivityTest(mockSvc *MockActivityLogService) (*ActivityHandler, *echo.Echo) {
	e := echo.New()
	h := NewActivityHandler(mockSvc)
	return h, e
}

// doListByTask drives ListByTask as auth middleware would have set up the
// context: agent auth sets ContextKeyAuthType, user auth sets
// ContextKeyWorkspaceRole to whatever WorkspaceRLS resolved for that caller in
// that workspace. Empty role models "no role resolved" (e.g. the fallback
// path never ran) — must fail closed, not open.
func doListByTask(t *testing.T, h *ActivityHandler, taskID uuid.UUID, isAgent bool, wsRole string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/activity")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	if isAgent {
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
	} else {
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
		if wsRole != "" {
			c.Set(mw.ContextKeyWorkspaceRole, wsRole)
		}
	}

	require.NoError(t, h.ListByTask(c))
	return rec
}

// TestActivityHandler_ListByTask_RedactsChangesWithoutPermission is the
// regression test for task cab1e85f: GET /tasks/:task_id/activity must never
// return Changes to a caller who does not hold PermExportAuditLog, because
// Changes carries prior field values verbatim and those can contain a secret
// pasted into a task description and later edited out.
//
// It is a negative control by construction: run it against the pre-fix
// ListByTask (which returned h.activityService's page unmodified) and every
// case below except "owner"/"admin" turns red, because Changes was never
// stripped for anyone.
func TestActivityHandler_ListByTask_RedactsChangesWithoutPermission(t *testing.T) {
	taskID := uuid.New()
	entry := newActivityTestEntry()
	mockSvc := &MockActivityLogService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
			assert.Equal(t, taskID, tid)
			return pagination.NewPage([]domain.ActivityLog{entry}, 1, pg), nil
		},
	}
	h, _ := setupActivityTest(mockSvc)

	cases := []struct {
		name    string
		isAgent bool
		wsRole  string
	}{
		{"agent caller", true, ""},
		{"member role", false, domain.RoleMember},
		{"viewer role", false, domain.RoleViewer},
		{"no role resolved", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doListByTask(t, h, taskID, tc.isAgent, tc.wsRole)
			require.Equal(t, http.StatusOK, rec.Code)

			var page pagination.Page[domain.ActivityLog]
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
			require.Len(t, page.Items, 1)
			// A nil json.RawMessage marshals to the JSON literal `null`; decoding
			// `null` back into a RawMessage stores the 4 bytes "null" rather than a
			// nil slice (encoding/json's documented RawMessage behavior) — so the
			// on-the-wire assertion is the string "null", not a Go-nil check.
			assert.JSONEq(t, "null", string(page.Items[0].Changes),
				"Changes must be redacted for a caller without PermExportAuditLog")
			assert.NotContains(t, rec.Body.String(), leakedSecret,
				"the raw response body must never contain a redacted secret, regardless of field-level parsing")
			// Metadata survives redaction — the point is to hide the diff,
			// not to break the activity feed for regular workspace members.
			assert.Equal(t, entry.Action, page.Items[0].Action)
			assert.Equal(t, entry.ActorID, page.Items[0].ActorID)
		})
	}
}

// TestActivityHandler_ListByTask_KeepsChangesWithPermission is the positive
// control: owner/admin hold PermExportAuditLog and must keep seeing the full
// diff the task-detail activity feed is built to show. Without this case, a
// version of the handler that always nils Changes would pass the redaction
// test above for the wrong reason.
func TestActivityHandler_ListByTask_KeepsChangesWithPermission(t *testing.T) {
	taskID := uuid.New()
	entry := newActivityTestEntry()
	mockSvc := &MockActivityLogService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
			return pagination.NewPage([]domain.ActivityLog{entry}, 1, pg), nil
		},
	}
	h, _ := setupActivityTest(mockSvc)

	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			rec := doListByTask(t, h, taskID, false, role)
			require.Equal(t, http.StatusOK, rec.Code)

			var page pagination.Page[domain.ActivityLog]
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
			require.Len(t, page.Items, 1)
			require.NotNil(t, page.Items[0].Changes)
			assert.Contains(t, string(page.Items[0].Changes), leakedSecret,
				"owner/admin must still see the full diff PermExportAuditLog is meant to grant")
		})
	}
}

func newTaskMovedTestEntry() domain.ActivityLog {
	return domain.ActivityLog{
		ID:         uuid.New(),
		EntityType: "task",
		EntityID:   uuid.New(),
		Action:     "task.moved",
		ActorID:    uuid.New(),
		ActorType:  domain.ActorTypeAgent,
		Changes:    json.RawMessage(`{"source":"api","status":{"new":"In Progress","old":"Todo"}}`),
	}
}

func newTaskAssignedTestEntry() domain.ActivityLog {
	return domain.ActivityLog{
		ID:         uuid.New(),
		EntityType: "task",
		EntityID:   uuid.New(),
		Action:     "task.assigned",
		ActorID:    uuid.New(),
		ActorType:  domain.ActorTypeAgent,
		Changes:    json.RawMessage(`{"assignee_id":{"new":"` + uuid.New().String() + `","old":null},"assignee_type":{"new":"agent","old":""}}`),
	}
}

// TestActivityHandler_ListByTask_KeepsStatusChangesForAgentWithoutPermission
// is the regression test for task a43785cc: GET /tasks/{id}/activity was
// reporting Changes=null on 820 of 820 task.moved events. The write side
// (MoveTask/logActivity in task_service.go) was never the bug — a prod DB
// read confirmed every task.moved row has always carried a populated
// Changes.status.{old,new}. The bug is this handler's redaction: an agent
// caller never holds PermExportAuditLog (see callerHasExportAuditLogPerm),
// so the blanket redaction added for cab1e85f nils Changes on every action
// for every agent — including task.moved and task.assigned, whose payload
// (status names, assignee UUIDs, a closed set of enum/reason values) was
// never the leak vector cab1e85f closed. review-verify-driver reads exactly
// Changes.status off task.moved to compute how long a card has sat in its
// current status; seeing it always null, it fell back to task.updated_at,
// which the driver's own comments reset, and misjudged fresh cards as stale.
//
// It is a negative control by construction: on the handler as merged for
// cab1e85f (blanket redaction, no per-action allowlist) this fails for both
// cases below, because Changes was nil for task.moved/task.assigned too —
// confirmed by running it against `git checkout origin/main --
// internal/handler/activity_handler.go` before this fix landed.
func TestActivityHandler_ListByTask_KeepsStatusChangesForAgentWithoutPermission(t *testing.T) {
	taskID := uuid.New()
	cases := []struct {
		name  string
		entry domain.ActivityLog
	}{
		{"task.moved", newTaskMovedTestEntry()},
		{"task.assigned", newTaskAssignedTestEntry()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := tc.entry
			mockSvc := &MockActivityLogService{
				ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
					assert.Equal(t, taskID, tid)
					return pagination.NewPage([]domain.ActivityLog{entry}, 1, pg), nil
				},
			}
			h, _ := setupActivityTest(mockSvc)

			// Agent caller — mw.IsAgent(c) is true, so
			// callerHasExportAuditLogPerm always returns false regardless of
			// role, exactly like review-verify-driver's requests.
			rec := doListByTask(t, h, taskID, true, "")
			require.Equal(t, http.StatusOK, rec.Code)

			var page pagination.Page[domain.ActivityLog]
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
			require.Len(t, page.Items, 1)
			require.NotNil(t, page.Items[0].Changes,
				"status/assignee diffs carry no free text — must survive redaction for an agent caller")
			assert.JSONEq(t, string(entry.Changes), string(page.Items[0].Changes),
				"the full status/assignee diff must reach the caller unmodified")
		})
	}
}

// TestActivityHandler_ListByTask_StillRedactsTaskUpdatedForAgent pins that
// widening the allowlist did not widen too far: task.updated carries
// title/description diffs (the actual cab1e85f leak vector) and must stay
// redacted for an agent caller exactly as before.
func TestActivityHandler_ListByTask_StillRedactsTaskUpdatedForAgent(t *testing.T) {
	taskID := uuid.New()
	entry := newActivityTestEntry() // Action: "task.updated", carries leakedSecret
	mockSvc := &MockActivityLogService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, pg pagination.Params) (*pagination.Page[domain.ActivityLog], error) {
			return pagination.NewPage([]domain.ActivityLog{entry}, 1, pg), nil
		},
	}
	h, _ := setupActivityTest(mockSvc)

	rec := doListByTask(t, h, taskID, true, "")
	require.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.ActivityLog]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.JSONEq(t, "null", string(page.Items[0].Changes))
	assert.NotContains(t, rec.Body.String(), leakedSecret)
}

// TestActivityHandler_ListByTask_InvalidTaskID pins the existing 400 path so
// the redaction change above it in the handler cannot have altered it.
func TestActivityHandler_ListByTask_InvalidTaskID(t *testing.T) {
	mockSvc := &MockActivityLogService{}
	h, e := setupActivityTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/activity")
	c.SetParamNames("task_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.ListByTask(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
