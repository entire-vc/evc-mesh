package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func syncRequest(h *VCSLinkHandler, taskID, linkID string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id", "link_id")
	c.SetParamValues(taskID, linkID)
	_ = h.Sync(c)
	return rec
}

func TestVCSLinkHandler_Sync_PassesIDsAndReturnsOutcome(t *testing.T) {
	stub := &stubVCSLinkService{}
	h := NewVCSLinkHandler(stub)
	taskID, linkID := uuid.New(), uuid.New()

	rec := syncRequest(h, taskID.String(), linkID.String())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, stub.syncCalls, 1)
	assert.Equal(t, [2]uuid.UUID{taskID, linkID}, stub.syncCalls[0])
	assert.Contains(t, rec.Body.String(), `"changed":true`)
	assert.Contains(t, rec.Body.String(), linkID.String())
}

func TestVCSLinkHandler_Sync_BadIDsAre400AndNeverReachService(t *testing.T) {
	stub := &stubVCSLinkService{}
	h := NewVCSLinkHandler(stub)
	good := uuid.New().String()
	assert.Equal(t, http.StatusBadRequest, syncRequest(h, "nope", good).Code)
	assert.Equal(t, http.StatusBadRequest, syncRequest(h, good, "nope").Code)
	assert.Empty(t, stub.syncCalls)
}

func TestVCSLinkHandler_Sync_ServiceErrorsKeepTheirStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"not found":   {apierror.NotFound("VCSLink"), http.StatusNotFound},
		"unavailable": {apierror.ServiceUnavailable("gitlab"), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewVCSLinkHandler(&stubVCSLinkService{syncReturnErr: tc.err})
			assert.Equal(t, tc.want, syncRequest(h, uuid.New().String(), uuid.New().String()).Code)
		})
	}
}
