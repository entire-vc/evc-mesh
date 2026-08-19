package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// mockDocumentMentionService records what the handler asked for.
type mockDocumentMentionService struct {
	gotID     uuid.UUID
	gotKind   string
	gotFilter repository.MentionFilter
	gotSeen   [2]uuid.UUID

	views []domain.DocumentCommentMentionView
	count int64
	err   error
}

func (m *mockDocumentMentionService) List(
	_ context.Context, mentionedID uuid.UUID, mentionedKind string, filter repository.MentionFilter,
) ([]domain.DocumentCommentMentionView, error) {
	m.gotID, m.gotKind, m.gotFilter = mentionedID, mentionedKind, filter
	return m.views, m.err
}

func (m *mockDocumentMentionService) MarkSeen(_ context.Context, commentID, mentionedID uuid.UUID) error {
	m.gotSeen = [2]uuid.UUID{commentID, mentionedID}
	return m.err
}

func (m *mockDocumentMentionService) CountUnseen(
	_ context.Context, mentionedID uuid.UUID, mentionedKind string,
) (int64, error) {
	m.gotID, m.gotKind = mentionedID, mentionedKind
	return m.count, m.err
}

// callerIs builds a request whose context carries an authenticated actor, which
// is what every one of these routes scopes itself to.
func callerIs(t *testing.T, actorID uuid.UUID, kind domain.ActorType, target string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	req = req.WithContext(actorctx.WithActor(req.Context(), actorID, kind))
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestDocumentMentionHandler_List_ScopesToTheCaller(t *testing.T) {
	svc := &mockDocumentMentionService{views: []domain.DocumentCommentMentionView{
		{CommentID: uuid.New(), MentionedSlug: "pavel", DocumentTitle: "Runbook"},
	}}
	h := NewDocumentMentionHandler(svc)
	userID := uuid.New()
	c, rec := callerIs(t, userID, domain.ActorTypeUser, "/me/document-mentions")

	require.NoError(t, h.List(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, userID, svc.gotID, "the inbox is the caller's own, never a caller-supplied id")
	assert.Equal(t, "user", svc.gotKind)
	assert.Equal(t, 50, svc.gotFilter.Limit)

	var got []domain.DocumentCommentMentionView
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "Runbook", got[0].DocumentTitle)
}

// TestDocumentMentionHandler_List_AnAgentReadsItsOwnInbox: the kind comes from
// the authenticated actor, so an agent key reads agent rows and a JWT reads user
// rows — the two id spaces are separate and must not be crossed.
func TestDocumentMentionHandler_List_AnAgentReadsItsOwnInbox(t *testing.T) {
	svc := &mockDocumentMentionService{}
	h := NewDocumentMentionHandler(svc)
	agentID := uuid.New()
	c, _ := callerIs(t, agentID, domain.ActorTypeAgent, "/me/document-mentions")

	require.NoError(t, h.List(c))

	assert.Equal(t, agentID, svc.gotID)
	assert.Equal(t, "agent", svc.gotKind)
}

func TestDocumentMentionHandler_List_RequiresAuthentication(t *testing.T) {
	h := NewDocumentMentionHandler(&mockDocumentMentionService{})
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/me/document-mentions", http.NoBody), httptest.NewRecorder())

	err := h.List(c)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode())
}

func TestDocumentMentionHandler_List_ParsesEveryFilter(t *testing.T) {
	svc := &mockDocumentMentionService{}
	h := NewDocumentMentionHandler(svc)
	projectID := uuid.New()
	since := time.Now().UTC().Truncate(time.Second)
	target := "/me/document-mentions?seen=false&limit=7&project_id=" + projectID.String() +
		"&since=" + since.Format(time.RFC3339)
	c, _ := callerIs(t, uuid.New(), domain.ActorTypeUser, target)

	require.NoError(t, h.List(c))

	require.NotNil(t, svc.gotFilter.Seen)
	assert.False(t, *svc.gotFilter.Seen)
	assert.Equal(t, 7, svc.gotFilter.Limit)
	require.NotNil(t, svc.gotFilter.ProjectID)
	assert.Equal(t, projectID, *svc.gotFilter.ProjectID)
	require.NotNil(t, svc.gotFilter.Since)
	assert.True(t, since.Equal(*svc.gotFilter.Since))
}

func TestDocumentMentionHandler_List_RejectsMalformedFilters(t *testing.T) {
	cases := []struct{ name, query, field string }{
		{"seen", "?seen=maybe", "seen"},
		{"since", "?since=yesterday", "since"},
		{"project_id", "?project_id=not-a-uuid", "project_id"},
		{"limit below range", "?limit=0", "limit"},
		{"limit above range", "?limit=101", "limit"},
		{"limit not a number", "?limit=many", "limit"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewDocumentMentionHandler(&mockDocumentMentionService{})
			c, _ := callerIs(t, uuid.New(), domain.ActorTypeUser, "/me/document-mentions"+tc.query)

			err := h.List(c)

			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
			assert.Contains(t, apiErr.Validation, tc.field)
		})
	}
}

func TestDocumentMentionHandler_List_SurfacesAServiceFailure(t *testing.T) {
	boom := errors.New("db down")
	h := NewDocumentMentionHandler(&mockDocumentMentionService{err: boom})
	c, _ := callerIs(t, uuid.New(), domain.ActorTypeUser, "/me/document-mentions")

	assert.ErrorIs(t, h.List(c), boom)
}

// TestDocumentMentionHandler_MarkSeen_MarksTheCallersOwnRow: the comment is
// named by the path and the recipient by the session, so there is no request in
// which a caller marks somebody else's mention.
func TestDocumentMentionHandler_MarkSeen_MarksTheCallersOwnRow(t *testing.T) {
	svc := &mockDocumentMentionService{}
	h := NewDocumentMentionHandler(svc)
	userID, commentID := uuid.New(), uuid.New()
	c, rec := callerIs(t, userID, domain.ActorTypeUser, "/me/document-mentions/"+commentID.String()+"/seen")
	c.SetPath("/me/document-mentions/:dcom_id/seen")
	c.SetParamNames("dcom_id")
	c.SetParamValues(commentID.String())

	require.NoError(t, h.MarkSeen(c))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, [2]uuid.UUID{commentID, userID}, svc.gotSeen)
}

func TestDocumentMentionHandler_MarkSeen_RejectsAMalformedID(t *testing.T) {
	h := NewDocumentMentionHandler(&mockDocumentMentionService{})
	c, _ := callerIs(t, uuid.New(), domain.ActorTypeUser, "/me/document-mentions/nope/seen")
	c.SetParamNames("dcom_id")
	c.SetParamValues("nope")

	err := h.MarkSeen(c)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation, "dcom_id")
}

func TestDocumentMentionHandler_MarkSeen_RequiresAuthentication(t *testing.T) {
	h := NewDocumentMentionHandler(&mockDocumentMentionService{})
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), httptest.NewRecorder())

	err := h.MarkSeen(c)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode())
}

func TestDocumentMentionHandler_MarkSeen_SurfacesAServiceFailure(t *testing.T) {
	boom := errors.New("db down")
	h := NewDocumentMentionHandler(&mockDocumentMentionService{err: boom})
	c, _ := callerIs(t, uuid.New(), domain.ActorTypeUser, "/")
	c.SetParamNames("dcom_id")
	c.SetParamValues(uuid.New().String())

	assert.ErrorIs(t, h.MarkSeen(c), boom)
}

func TestDocumentMentionHandler_UnseenCount(t *testing.T) {
	svc := &mockDocumentMentionService{count: 4}
	h := NewDocumentMentionHandler(svc)
	agentID := uuid.New()
	c, rec := callerIs(t, agentID, domain.ActorTypeAgent, "/me/document-mentions/unseen_count")

	require.NoError(t, h.UnseenCount(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "max-age=10", rec.Header().Get("Cache-Control"))
	assert.Equal(t, agentID, svc.gotID)
	assert.Equal(t, "agent", svc.gotKind)

	var got map[string]int64
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, int64(4), got["count"])
}

func TestDocumentMentionHandler_UnseenCount_RequiresAuthentication(t *testing.T) {
	h := NewDocumentMentionHandler(&mockDocumentMentionService{})
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())

	err := h.UnseenCount(c)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode())
}

func TestDocumentMentionHandler_UnseenCount_SurfacesAServiceFailure(t *testing.T) {
	boom := errors.New("db down")
	h := NewDocumentMentionHandler(&mockDocumentMentionService{err: boom})
	c, _ := callerIs(t, uuid.New(), domain.ActorTypeUser, "/")

	assert.ErrorIs(t, h.UnseenCount(c), boom)
}

// TestDocumentMentionedIsSubscribable closes the gap that would make every other
// test here meaningless: notificationService.dispatch drops an event whose type
// is not in the recipient's stored `events` array, and the settings page can only
// offer what dispatchableEvents allows. An event nobody can subscribe to is
// delivered to nobody, silently — the exact failure mode this feature exists to
// prevent.
func TestDocumentMentionedIsSubscribable(t *testing.T) {
	assert.True(t, dispatchableEvents["document.mentioned"],
		"document.mentioned must be subscribable, or the notification is dropped by the fan-out with no log line")
	assert.Contains(t, defaultSubscribedEvents(), "document.mentioned",
		"a new preference row must include it by default, as it does every other mention event")
}
