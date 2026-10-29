package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TurnCommandHandler serves the turn-ending commands wait_for, continue_next,
// hand_off and add_note (docs/api/typed-outcomes.md §Commands 1, 2, 6, 7).
type TurnCommandHandler struct {
	svc   *service.TurnCommandService
	tasks taskIDResolver
}

// NewTurnCommandHandler creates the handler.
func NewTurnCommandHandler(svc *service.TurnCommandService, tasks taskIDResolver) *TurnCommandHandler {
	return &TurnCommandHandler{svc: svc, tasks: tasks}
}

type turnCommonBody struct {
	SessionID *string `json:"session_id"`
	TurnID    *string `json:"turn_id"`
}

func (b turnCommonBody) common() service.TurnCommon {
	return service.TurnCommon{SessionID: b.SessionID, TurnID: b.TurnID}
}

type waitForBody struct {
	turnCommonBody
	On              string      `json:"on"`
	TargetTaskID    *uuid.UUID  `json:"target_task_id"`
	TargetCondition *string     `json:"target_condition"`
	VCSLinkID       *uuid.UUID  `json:"vcs_link_id"`
	Provider        *string     `json:"provider"`
	ProjectPath     *string     `json:"project_path"`
	MRIID           *int        `json:"mr_iid"`
	PipelineID      *int64      `json:"pipeline_id"`
	RequiredJobs    []string    `json:"required_jobs"`
	Until           *string     `json:"until"`
	UntilAt         untilAtJSON `json:"until_at"`
	EventRef        *string     `json:"event_ref"`
	Source          *string     `json:"source"`
	Why             *string     `json:"why"`
	Note            string      `json:"note"`
}

// untilAtJSON is until_at in a wait_for body. Anything but an RFC 3339 time
// with Z or an offset ("6h", "2026-10-30T03:00:00") is a refused time wait,
// 422 naive_time, not a malformed body.
type untilAtJSON struct{ Time *time.Time }

type untilAtTimeError struct{ value string }

func (e *untilAtTimeError) Error() string {
	return "until_at must be an absolute RFC 3339 time with Z or an offset, not " + e.value
}

func (u *untilAtJSON) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		u.Time = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return &untilAtTimeError{value: string(b)}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return &untilAtTimeError{value: string(b)}
	}
	u.Time = &t
	return nil
}

// bindWaitFor binds a wait_for body; a naive or relative until_at is 422.
func bindWaitFor(c echo.Context, b *waitForBody) *apierror.Error {
	err := c.Bind(b)
	if err == nil {
		return nil
	}
	var uaErr *untilAtTimeError
	if errors.As(err, &uaErr) {
		return &apierror.Error{Code: http.StatusUnprocessableEntity, ErrorCode: domain.CodeNaiveTime, Message: uaErr.Error()}
	}
	return apierror.BadRequest("invalid request body")
}

// turnError renders a command refusal: an OutcomeError is 422 with its code,
// everything else keeps the usual mapping.
func turnError(c echo.Context, err error) error {
	var oe *domain.OutcomeError
	if errors.As(err, &oe) {
		return c.JSON(http.StatusUnprocessableEntity, &apierror.Error{
			Code: http.StatusUnprocessableEntity, ErrorCode: oe.Code, Message: oe.Message,
		})
	}
	return handleError(c, err)
}

func (h *TurnCommandHandler) taskID(c echo.Context) (uuid.UUID, error) {
	return resolveTaskID(c.Request().Context(), c.Param("task_id"), h.tasks)
}

// WaitFor handles POST /tasks/:task_id/wait.
func (h *TurnCommandHandler) WaitFor(c echo.Context) error {
	id, err := h.taskID(c)
	if err != nil {
		return handleError(c, err)
	}
	var b waitForBody
	if apiErr := bindWaitFor(c, &b); apiErr != nil {
		return c.JSON(apiErr.Code, apiErr)
	}
	res, err := h.svc.WaitFor(c.Request().Context(), id, service.WaitForInput{
		TurnCommon: b.common(), On: b.On, TargetTaskID: b.TargetTaskID, TargetCondition: b.TargetCondition,
		VCSLinkID: b.VCSLinkID, Provider: b.Provider, ProjectPath: b.ProjectPath, MRIID: b.MRIID,
		PipelineID: b.PipelineID, RequiredJobs: b.RequiredJobs, Until: b.Until, UntilAt: b.UntilAt.Time,
		EventRef: b.EventRef, Source: b.Source, Why: b.Why, Note: b.Note,
	})
	if err != nil {
		return turnError(c, err)
	}
	return c.JSON(http.StatusCreated, res)
}

// ContinueNext handles POST /tasks/:task_id/next.
func (h *TurnCommandHandler) ContinueNext(c echo.Context) error {
	id, err := h.taskID(c)
	if err != nil {
		return handleError(c, err)
	}
	var b struct {
		turnCommonBody
		Note string `json:"note"`
	}
	err = c.Bind(&b)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	res, err := h.svc.ContinueNext(c.Request().Context(), id, b.common(), b.Note)
	if err != nil {
		return turnError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// HandOff handles POST /tasks/:task_id/handoff.
func (h *TurnCommandHandler) HandOff(c echo.Context) error {
	id, err := h.taskID(c)
	if err != nil {
		return handleError(c, err)
	}
	var b struct {
		turnCommonBody
		To  string `json:"to"`
		Why string `json:"why"`
	}
	err = c.Bind(&b)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	res, err := h.svc.HandOff(c.Request().Context(), id, b.common(), b.To, b.Why)
	if err != nil {
		return turnError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// AddNote handles POST /tasks/:task_id/notes.
func (h *TurnCommandHandler) AddNote(c echo.Context) error {
	id, err := h.taskID(c)
	if err != nil {
		return handleError(c, err)
	}
	var b struct {
		Text string `json:"text"`
	}
	err = c.Bind(&b)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	commentID, err := h.svc.AddNote(c.Request().Context(), id, b.Text)
	if err != nil {
		return turnError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]uuid.UUID{"comment_id": commentID})
}
