package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func patchMetadataRequest(h *VCSLinkHandler, taskID, linkID, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id", "link_id")
	c.SetParamValues(taskID, linkID)
	_ = h.PatchMetadata(c)
	return rec
}

func TestVCSLinkHandler_PatchMetadata_PassesIDsAndPatchAndReturnsLink(t *testing.T) {
	stub := &stubVCSLinkService{}
	h := NewVCSLinkHandler(stub)
	taskID, linkID := uuid.New(), uuid.New()

	rec := patchMetadataRequest(h, taskID.String(), linkID.String(), `{"ship_sha":"abc"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, stub.patchCalls, 1)
	assert.Equal(t, taskID, stub.patchCalls[0].taskID)
	assert.Equal(t, linkID, stub.patchCalls[0].linkID)
	assert.Equal(t, map[string]any{"ship_sha": "abc"}, stub.patchCalls[0].patch)
	assert.Contains(t, rec.Body.String(), linkID.String())
	assert.Contains(t, rec.Body.String(), `"ship_sha":"merged"`)
}

func TestVCSLinkHandler_PatchMetadata_BadInputIs400AndNeverReachesService(t *testing.T) {
	stub := &stubVCSLinkService{}
	h := NewVCSLinkHandler(stub)
	good := uuid.New().String()

	for name, tc := range map[string]struct{ task, link, body string }{
		"bad task_id": {"nope", good, `{"ship_sha":"x"}`},
		"bad link_id": {good, "nope", `{"ship_sha":"x"}`},
		"not json":    {good, good, `not json`},
		"json null":   {good, good, `null`},
		"json array":  {good, good, `[1]`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := patchMetadataRequest(h, tc.task, tc.link, tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
	assert.Empty(t, stub.patchCalls)
}

func TestVCSLinkHandler_PatchMetadata_ServiceErrorsKeepTheirStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"bad request": {apierror.BadRequest("nope"), http.StatusBadRequest},
		"not found":   {apierror.NotFound("VCSLink"), http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewVCSLinkHandler(&stubVCSLinkService{patchReturnErr: tc.err})
			rec := patchMetadataRequest(h, uuid.New().String(), uuid.New().String(), `{"ship_sha":"x"}`)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func shipSHAByURLRequest(h *VCSLinkHandler, ws *uuid.UUID, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if ws != nil {
		c.Set(mw.ContextKeyWorkspaceID, *ws)
	}
	_ = h.PatchShipSHAByURL(c)
	return rec
}

func TestVCSLinkHandler_PatchShipSHAByURL_PassesWorkspaceURLAndSHA(t *testing.T) {
	stub := &stubVCSLinkService{}
	ws := uuid.New()

	rec := shipSHAByURLRequest(NewVCSLinkHandler(stub), &ws, `{"url":"https://h/p/-/merge_requests/5","ship_sha":"abc"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, stub.shipCalls, 1)
	assert.Equal(t, shipCall{ws: ws, url: "https://h/p/-/merge_requests/5", sha: "abc"}, stub.shipCalls[0])
	assert.Contains(t, rec.Body.String(), "vcs_links")
}

func TestVCSLinkHandler_PatchShipSHAByURL_RejectsMissingWorkspaceAndBadBody(t *testing.T) {
	stub := &stubVCSLinkService{}
	ws := uuid.New()

	assert.Equal(t, http.StatusForbidden, shipSHAByURLRequest(NewVCSLinkHandler(stub), nil, `{}`).Code)
	assert.Equal(t, http.StatusBadRequest, shipSHAByURLRequest(NewVCSLinkHandler(stub), &ws, `not json`).Code)
	assert.Empty(t, stub.shipCalls)
}

func TestVCSLinkHandler_PatchShipSHAByURL_ServiceErrorsKeepTheirStatus(t *testing.T) {
	ws := uuid.New()
	h := NewVCSLinkHandler(&stubVCSLinkService{shipReturnErr: apierror.NotFound("VCSLink")})
	assert.Equal(t, http.StatusNotFound, shipSHAByURLRequest(h, &ws, `{"url":"u","ship_sha":"x"}`).Code)
}
