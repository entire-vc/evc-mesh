package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// stubWatchService records what the handler asked for and answers whatever the
// test wants, so the assertions here are about the HTTP surface only.
type stubWatchService struct {
	state *domain.DocumentWatchState
	err   error

	calls    []string
	gotDocID uuid.UUID
	gotWsID  uuid.UUID
}

func (s *stubWatchService) record(name string, docID, wsID uuid.UUID) (*domain.DocumentWatchState, error) {
	s.calls = append(s.calls, name)
	s.gotDocID, s.gotWsID = docID, wsID
	return s.state, s.err
}

func (s *stubWatchService) Watch(_ context.Context, docID, wsID uuid.UUID) (*domain.DocumentWatchState, error) {
	return s.record("watch", docID, wsID)
}

func (s *stubWatchService) Unwatch(_ context.Context, docID, wsID uuid.UUID) (*domain.DocumentWatchState, error) {
	return s.record("unwatch", docID, wsID)
}

func (s *stubWatchService) State(_ context.Context, docID, wsID uuid.UUID) (*domain.DocumentWatchState, error) {
	return s.record("state", docID, wsID)
}

func (s *stubWatchService) AutoSubscribe(context.Context, uuid.UUID, uuid.UUID, string, string) {}
func (s *stubWatchService) RecordChange(context.Context, service.RecordDocumentChangeInput)     {}
func (s *stubWatchService) NotifyComment(context.Context, service.NotifyDocumentCommentInput)   {}
func (s *stubWatchService) NotifyDeleted(context.Context, *domain.Document, uuid.UUID)          {}
func (s *stubWatchService) SweepPendingNotices(context.Context) (int, error)                    { return 0, nil }

var _ service.DocumentWatchService = (*stubWatchService)(nil)

func watchState(overrides func(*domain.DocumentWatchState)) *domain.DocumentWatchState {
	st := &domain.DocumentWatchState{Watching: true, Source: domain.WatchSourceExplicit, WatcherCount: 3}
	if overrides != nil {
		overrides(st)
	}
	return st
}

func decodeWatchState(t *testing.T, body []byte) domain.DocumentWatchState {
	t.Helper()
	var st domain.DocumentWatchState
	require.NoError(t, json.Unmarshal(body, &st))
	return st
}

func TestDocumentWatchHandler_GetReturnsTheState(t *testing.T) {
	svc := &stubWatchService{state: watchState(nil)}
	h := NewDocumentWatchHandler(svc)
	e := echo.New()

	docID, wsID := uuid.New(), uuid.New()
	c, rec := docRequest(e, http.MethodGet, docID.String(), &wsID, "")

	require.NoError(t, h.Get(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"state"}, svc.calls)
	assert.Equal(t, docID, svc.gotDocID)
	assert.Equal(t, wsID, svc.gotWsID, "the workspace must come from the resolved context, never the body")

	got := decodeWatchState(t, rec.Body.Bytes())
	assert.True(t, got.Watching)
	assert.Equal(t, 3, got.WatcherCount)
}

func TestDocumentWatchHandler_WatchAndUnwatchUseTheirOwnVerbs(t *testing.T) {
	// The route is one path with three methods; a handler wired to the wrong
	// service call would subscribe on DELETE and nobody would notice until a
	// user complained about notifications they cancelled.
	for _, tc := range []struct {
		name   string
		method string
		invoke func(*DocumentWatchHandler, echo.Context) error
		want   string
	}{
		{"put subscribes", http.MethodPut, (*DocumentWatchHandler).Watch, "watch"},
		{"delete unsubscribes", http.MethodDelete, (*DocumentWatchHandler).Unwatch, "unwatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubWatchService{state: watchState(nil)}
			h := NewDocumentWatchHandler(svc)
			wsID := uuid.New()
			c, rec := docRequest(echo.New(), tc.method, uuid.New().String(), &wsID, "")

			require.NoError(t, tc.invoke(h, c))

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, []string{tc.want}, svc.calls)
		})
	}
}

func TestDocumentWatchHandler_UnwatchAnswersTheResultingStateNotNoContent(t *testing.T) {
	// 204 cannot carry the one thing the caller needs after unsubscribing: that
	// this is a recorded refusal (muted), not merely an absent subscription —
	// which is what stops the next comment from re-subscribing them.
	svc := &stubWatchService{state: watchState(func(st *domain.DocumentWatchState) {
		st.Watching = false
		st.Muted = true
		st.Source = ""
	})}
	h := NewDocumentWatchHandler(svc)
	wsID := uuid.New()
	c, rec := docRequest(echo.New(), http.MethodDelete, uuid.New().String(), &wsID, "")

	require.NoError(t, h.Unwatch(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	got := decodeWatchState(t, rec.Body.Bytes())
	assert.False(t, got.Watching)
	assert.True(t, got.Muted)
}

func TestDocumentWatchHandler_RejectsAMalformedDocumentID(t *testing.T) {
	svc := &stubWatchService{state: watchState(nil)}
	h := NewDocumentWatchHandler(svc)
	wsID := uuid.New()
	c, rec := docRequest(echo.New(), http.MethodPut, "not-a-uuid", &wsID, "")

	require.NoError(t, h.Watch(c))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.calls, "nothing may be written for an id that does not parse")
}

func TestDocumentWatchHandler_RefusesWhenTheWorkspaceIsNotResolved(t *testing.T) {
	// Without a workspace in context there is no tenant to scope the
	// subscription to, and answering 200 would let a caller subscribe across
	// tenants. Fail closed.
	svc := &stubWatchService{state: watchState(nil)}
	h := NewDocumentWatchHandler(svc)
	c, rec := docRequest(echo.New(), http.MethodPut, uuid.New().String(), nil, "")

	require.NoError(t, h.Watch(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, svc.calls)
}

func TestDocumentWatchHandler_PropagatesAServiceRefusal(t *testing.T) {
	// A document in another workspace, or one that does not exist, is a 404
	// from the service and must stay a 404 — not become a 500 the caller cannot
	// act on.
	svc := &stubWatchService{err: apierror.NotFound("Document")}
	h := NewDocumentWatchHandler(svc)
	wsID := uuid.New()

	for name, invoke := range map[string]func(*DocumentWatchHandler, echo.Context) error{
		"get":     (*DocumentWatchHandler).Get,
		"watch":   (*DocumentWatchHandler).Watch,
		"unwatch": (*DocumentWatchHandler).Unwatch,
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := docRequest(echo.New(), http.MethodPut, uuid.New().String(), &wsID, "")
			require.NoError(t, invoke(h, c))
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

func TestDocumentWatchHandler_UnexpectedFailureIsNotReportedAsSuccess(t *testing.T) {
	svc := &stubWatchService{err: errors.New("database unavailable")}
	h := NewDocumentWatchHandler(svc)
	wsID := uuid.New()
	c, rec := docRequest(echo.New(), http.MethodGet, uuid.New().String(), &wsID, "")

	require.NoError(t, h.Get(c))

	assert.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError)
}

// TestDocumentWatchEventsAreDispatchable pins the three event types to the
// preference whitelist.
//
// An event type a producer emits but no preference row may name is dispatched
// perfectly and delivered to nobody, with no error anywhere — the same silent
// failure the mention path was caught in. This test is what makes adding a
// fourth document event without registering it fail here rather than in
// production a fortnight later.
func TestDocumentWatchEventsAreDispatchable(t *testing.T) {
	for _, ev := range []string{
		service.DocumentChangedEvent,
		service.DocumentCommentedEvent,
		service.DocumentDeletedEvent,
	} {
		assert.True(t, dispatchableEvents[ev],
			"%s is emitted by the watch service but cannot be subscribed to", ev)
	}
}
