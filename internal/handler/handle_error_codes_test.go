package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// Every ad-hoc error shape handleError writes carries a stable machine code
// twice: `code` (legacy, read by agents) and `error_code` (the contract the
// interface translates by). The two must agree, and the message stays English.
func TestHandleError_AdHocShapesCarryErrorCode(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"review evidence", &service.ReviewEvidenceError{Strict: true}, http.StatusUnprocessableEntity, "review_evidence_required"},
		{"foreign assignee", &service.AssigneeNotInWorkspaceError{PrincipalID: uuid.New(), AssigneeType: domain.AssigneeTypeAgent, ProjectID: uuid.New(), Reason: "other workspace"}, http.StatusUnprocessableEntity, "assignee_not_in_workspace"},
		{"mention handoff", &service.MentionHandoffRequiredError{TaskID: uuid.New(), Slugs: []string{"bob", "linus"}}, http.StatusUnprocessableEntity, "mention_handoff_required"},
		{"assignee unresolved", &service.AssigneeUnresolvedError{PrincipalID: uuid.New(), ProjectID: uuid.New()}, http.StatusUnprocessableEntity, "assignee_unresolved"},
		{"assignee refuses", &service.AssigneeRefusesAssignmentsError{PrincipalID: uuid.New(), ProjectID: uuid.New(), AgentSlug: "scout"}, http.StatusUnprocessableEntity, "assignee_refuses_assignments"},
		{"done evidence", &service.DoneEvidenceError{}, http.StatusUnprocessableEntity, "done_evidence_required"},
		{"dod gate", &service.DodGateBlockedError{BlockingGates: []string{"lint"}}, http.StatusUnprocessableEntity, "dod_gate_blocked"},
		{"triage entry", &service.TriageEntryError{}, http.StatusUnprocessableEntity, "triage_entry_requires_human_gate"},
		{"park alarm", &service.ParkAlarmError{Label: "waiting"}, http.StatusUnprocessableEntity, "park_requires_wakeup"},
		{"human gate frozen", &service.HumanGateFrozenError{TargetCategory: domain.StatusCategoryDone}, http.StatusUnprocessableEntity, "human_gate_frozen"},
		{"task shipped", &service.TaskShippedError{}, http.StatusUnprocessableEntity, "task_shipped"},
		{"ambiguous quote", &mdoc.AmbiguousQuoteError{Quote: "x", Matches: 2}, http.StatusBadRequest, "ambiguous_quote"},
		{"document version", &service.DocumentVersionConflictError{CurrentVersion: 3}, http.StatusConflict, "document_version_conflict"},
		{"external conflict", &service.ExternalSourceConflictError{SourcePath: "a.md", BaseSHA256: "abc"}, http.StatusConflict, "external_source_conflict"},
		{"external landed", &service.ExternalSourceWriteLandedError{SourcePath: "a.md", SHA256: "abc", CurrentVersion: 4}, http.StatusConflict, "external_source_write_landed"},
		{"cas conflict", &service.CASConflictError{}, http.StatusConflict, "cas_conflict"},
		{"rule violation", &service.RuleViolationError{Violations: []domain.RuleViolation{{RuleName: "r", Message: "m"}}}, http.StatusUnprocessableEntity, "rule_violation"},
		{"list revision", &service.ListRevisionStaleError{Requested: 1, Current: 2}, http.StatusGone, "list_revision_stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), rec)
			require.NoError(t, handleError(c, tc.err))

			assert.Equal(t, tc.status, rec.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body["error_code"])
			if _, legacy := body["code"]; legacy {
				assert.Equal(t, tc.code, body["code"])
			} else {
				assert.Equal(t, tc.code, body["error"], "shapes without a legacy code key use the error key")
			}
			assert.NotEmpty(t, body["message"])
		})
	}
}
