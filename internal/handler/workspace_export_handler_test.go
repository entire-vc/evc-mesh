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
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type fakeHierarchyReader struct {
	snap *domain.HierarchyExport
	ok   bool
}

func (f *fakeHierarchyReader) Hierarchy(context.Context, uuid.UUID) (*domain.HierarchyExport, bool, error) {
	return f.snap, f.ok, nil
}

func hierarchyCall(h *WorkspaceExportHandler, ws uuid.UUID, caps []string, inm string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	if inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(ws.String())
	if caps != nil {
		c.Set(middleware.ContextKeyExtensionPrincipal, &service.ExtensionPrincipal{
			WorkspaceID: ws, ExtensionID: "keep", Capabilities: map[string][]string{"api": caps},
		})
	}
	_ = h.Hierarchy(c)
	return rec
}

func TestWorkspaceExportHandler_Hierarchy(t *testing.T) {
	ws := uuid.New()
	sponsor := uuid.New()
	snap := &domain.HierarchyExport{
		Workspace: domain.HierarchyWorkspace{ID: ws, Slug: "w", Name: "W"},
		Projects:  []domain.HierarchyProject{{ID: uuid.New(), Slug: "p", Name: "P"}},
		Members:   []domain.HierarchyMember{{UserID: sponsor, Email: "a@example.com", Role: "owner"}},
		Agents:    []domain.HierarchyAgent{{ID: uuid.New(), Slug: "bot", SponsorUserID: &sponsor, ActiveKeys: []domain.HierarchyAgentKey{{KeyID: "primary", KeyPrefix: "ab12"}}}},
	}
	h := NewWorkspaceExportHandler(&fakeHierarchyReader{snap: snap, ok: true})
	all := []string{"workspaces:read", "projects:read", "members:read", "agents:read"}

	// An extension holding all four reads the export; counts and sponsor are there.
	rec := hierarchyCall(h, ws, all, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, map[string]any{"projects": float64(1), "members": float64(1), "agents": float64(1)}, out["counts"])
	assert.Equal(t, sponsor.String(), out["agents"].([]any)[0].(map[string]any)["sponsor_user_id"])
	assert.NotContains(t, rec.Body.String(), "key_sha256", "no key material")
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	assert.Equal(t, `"`+out["cursor"].(string)+`"`, etag)

	// Same data + If-None-Match: 304, empty body.
	rec = hierarchyCall(h, ws, all, etag)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.String())

	// A member removed: the next export has another cursor and no longer answers 304.
	snap.Members = nil
	rec = hierarchyCall(h, ws, all, etag)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotEqual(t, etag, rec.Header().Get("ETag"))

	// Red control: each missing capability is a 403 naming it, including members:read.
	for _, missing := range all {
		var caps []string
		for _, c := range all {
			if c != missing {
				caps = append(caps, c)
			}
		}
		rec = hierarchyCall(h, ws, caps, "")
		require.Equal(t, http.StatusForbidden, rec.Code, "without %s", missing)
		assert.Contains(t, rec.Body.String(), missing)
	}

	// Unknown workspace and a bad id.
	h404 := NewWorkspaceExportHandler(&fakeHierarchyReader{ok: false})
	assert.Equal(t, http.StatusNotFound, hierarchyCall(h404, ws, all, "").Code)
}

func TestEtagMatches(t *testing.T) {
	assert.True(t, etagMatches(`"a"`, `"a"`))
	assert.True(t, etagMatches(`W/"a"`, `"a"`))
	assert.True(t, etagMatches(`"b", "a"`, `"a"`))
	assert.True(t, etagMatches(`*`, `"a"`))
	assert.False(t, etagMatches(`"b"`, `"a"`))
}
