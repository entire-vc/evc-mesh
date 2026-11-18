package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// TestUnseenCount_IsNeverServedFromTheBrowserCache pins the header of both
// unseen_count sources the client sums for the badge and the mentions_unseen
// tile. With max-age the browser answered the refetch after a live mention or
// after POST .../seen from its disk cache, so the tile kept the old number
// for the whole TTL while the inbox list next to it was already fresh.
func TestUnseenCount_IsNeverServedFromTheBrowserCache(t *testing.T) {
	workspaceID := uuid.NewString()
	sources := map[string]func(echo.Context) error{
		"task":     NewMentionHandler(&viewerCapturingMentionService{}).UnseenCount,
		"document": NewDocumentMentionHandler(&mockDocumentMentionService{count: 1}).UnseenCount,
	}
	for name, handle := range sources {
		t.Run(name, func(t *testing.T) {
			c, rec := callerIs(t, uuid.New(), domain.ActorTypeUser, "/me/x/unseen_count?workspace_id="+workspaceID)
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleOwner)

			require.NoError(t, handle(c))

			require.Equal(t, http.StatusOK, rec.Code)
			cc := rec.Header().Get("Cache-Control")
			assert.Equal(t, "private, no-cache", cc)
			assert.NotContains(t, cc, "max-age", "a freshness lifetime lets the browser skip the refetch")
		})
	}
}
