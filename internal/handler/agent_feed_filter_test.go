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
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// GET /agents/me/tasks used to read status_category and then apply it in Go over
// a feed the repository had already returned in full, and to ignore limit /
// page_size / project_id entirely — even though the MCP client has always sent
// page_size. These pin that all three now reach the repository as a filter.

// feedHandler returns a handler whose task service records the filter it was
// handed and answers with tasks/total.
func feedHandler(tasks []domain.Task, total int) (*AgentHandler, *repository.AssigneeTaskFilter) {
	got := &repository.AssigneeTaskFilter{}
	svc := &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType,
			filter repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
			*got = filter
			return tasks, total, nil
		},
	}
	return NewAgentHandlerWithTaskService(nil, svc), got
}

// feedRequest drives GetMyTasks with an authenticated agent + workspace.
func feedRequest(t *testing.T, h *AgentHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?"+query, http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, uuid.New())
	c.Set(mw.ContextKeyWorkspaceID, uuid.New())
	require.NoError(t, h.GetMyTasks(c))
	return rec
}

func TestAgentFeed_StatusCategoryReachesTheRepository(t *testing.T) {
	h, got := feedHandler([]domain.Task{}, 0)
	rec := feedRequest(t, h, "status_category=in_progress")

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got.StatusCategory,
		"the category must be pushed into the query, not applied to the rows afterwards")
	assert.Equal(t, domain.StatusCategoryInProgress, *got.StatusCategory)
}

func TestAgentFeed_EveryKnownCategoryIsAccepted(t *testing.T) {
	for _, cat := range []domain.StatusCategory{
		domain.StatusCategoryBacklog, domain.StatusCategoryTodo, domain.StatusCategoryInProgress,
		domain.StatusCategoryReview, domain.StatusCategoryDone, domain.StatusCategoryCancelled,
		domain.StatusCategoryTriage,
	} {
		h, got := feedHandler([]domain.Task{}, 0)
		rec := feedRequest(t, h, "status_category="+string(cat))
		assert.Equal(t, http.StatusOK, rec.Code, "category %q must be accepted", cat)
		require.NotNil(t, got.StatusCategory)
		assert.Equal(t, cat, *got.StatusCategory)
	}
}

// A mistyped category used to match nothing and answer 200 with an empty array,
// which an agent reads as "no work assigned to me".
func TestAgentFeed_UnknownCategoryIsRejectedNotSilentlyEmpty(t *testing.T) {
	h, got := feedHandler([]domain.Task{}, 0)
	rec := feedRequest(t, h, "status_category=in-progress")

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"an unknown category must not be answered with an empty feed — that is "+
			"indistinguishable from having no work")
	assert.Nil(t, got.StatusCategory)
}

func TestAgentFeed_LimitReachesTheRepository(t *testing.T) {
	h, got := feedHandler([]domain.Task{}, 0)
	rec := feedRequest(t, h, "limit=7")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 7, got.Limit)
}

// The MCP get_my_tasks tool sends page_size, and has done so all along while the
// server ignored it.
func TestAgentFeed_PageSizeIsAcceptedAsTheLimit(t *testing.T) {
	h, got := feedHandler([]domain.Task{}, 0)
	rec := feedRequest(t, h, "page_size=25")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 25, got.Limit,
		"page_size is what the MCP client sends; ignoring it served the whole feed to a "+
			"caller that asked for a page")
}

func TestAgentFeed_LimitWinsOverPageSizeWhenBothArePresent(t *testing.T) {
	h, got := feedHandler([]domain.Task{}, 0)
	feedRequest(t, h, "limit=3&page_size=99")
	assert.Equal(t, 3, got.Limit)
}

func TestAgentFeed_NoLimitMeansTheWholeFeed(t *testing.T) {
	h, got := feedHandler([]domain.Task{}, 0)
	feedRequest(t, h, "")
	assert.Zero(t, got.Limit,
		"the feed is how an agent discovers work, so truncation must stay opt-in")
}

func TestAgentFeed_MalformedLimitIsRejected(t *testing.T) {
	for _, q := range []string{"limit=0", "limit=-4", "limit=many", "page_size=0"} {
		h, _ := feedHandler([]domain.Task{}, 0)
		rec := feedRequest(t, h, q)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "query %q must be rejected", q)
	}
}

func TestAgentFeed_ProjectIDReachesTheRepository(t *testing.T) {
	projID := uuid.New()
	h, got := feedHandler([]domain.Task{}, 0)
	rec := feedRequest(t, h, "project_id="+projID.String())

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got.ProjectID)
	assert.Equal(t, projID, *got.ProjectID)
}

func TestAgentFeed_MalformedProjectIDIsRejected(t *testing.T) {
	h, _ := feedHandler([]domain.Task{}, 0)
	rec := feedRequest(t, h, "project_id=not-a-uuid")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// A truncated feed must announce itself, or an agent stops at the cut and
// reports itself idle.
func TestAgentFeed_TruncationIsReportedInTheResponse(t *testing.T) {
	h, _ := feedHandler([]domain.Task{{ID: uuid.New()}, {ID: uuid.New()}}, 40)
	rec := feedRequest(t, h, "limit=2")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, 2, body["count"])
	assert.EqualValues(t, 40, body["total_count"])
	assert.Equal(t, true, body["has_more"],
		"a caller must be able to tell 2-of-40 from 2-of-2")
}

func TestAgentFeed_CompleteFeedIsNotFlaggedAsTruncated(t *testing.T) {
	h, _ := feedHandler([]domain.Task{{ID: uuid.New()}}, 1)
	rec := feedRequest(t, h, "")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.EqualValues(t, 1, body["total_count"])
	assert.Equal(t, false, body["has_more"])
}

// The long-poll twin shares the parser, and must reject a bad filter up front
// rather than after parking the caller for the poll timeout.
func TestPollTasks_RejectsABadFilterBeforeParking(t *testing.T) {
	h := pollHandlerWithUnusedRedis(&MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType,
			_ repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
			return nil, 0, nil
		},
	})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?status_category=nope&timeout=120", http.NoBody), rec)
	c.Set(mw.ContextKeyAgentID, uuid.New())
	c.Set(mw.ContextKeyWorkspaceID, uuid.New())

	require.NoError(t, h.PollTasks(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"a client-side filter error must be reported immediately, not two minutes later "+
			"as an empty timeout")
}
