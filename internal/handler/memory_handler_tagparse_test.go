package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Tag params must survive BOTH wire encodings (task #2c087b2a).
//
// The MCP REST client emits repeated params (params.Add per tag) while the handler
// bound `query:"tags_any"` into a plain string — echo fills that from the FIRST
// occurrence only. So recall(tags_any=['pavel-decision','canonical']) reached the
// service as just ['pavel-decision']: a silently narrowed filter, not an error.
// That matters most for the C4 canonical-decision read every agent runs at wake-up.

func recallOptsFrom(t *testing.T, rawQuery string) domain.RecallOpts {
	t.Helper()

	ownWS := uuid.New()
	var got domain.RecallOpts
	ms := &MockMemoryService{
		RecallFunc: func(_ context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
			got = opts
			return []domain.ScoredMemory{}, domain.SearchModeHybrid, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=probe&"+rawQuery, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.Search(c))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return got
}

func TestSearch_TagsAny_RepeatedParams(t *testing.T) {
	opts := recallOptsFrom(t, "tags_any=pavel-decision&tags_any=canonical")
	assert.Equal(t, []string{"pavel-decision", "canonical"}, opts.TagsAny,
		"every repeated tags_any occurrence must reach the service, not just the first")
}

func TestSearch_TagsAny_CommaSeparated(t *testing.T) {
	opts := recallOptsFrom(t, "tags_any=pavel-decision,canonical")
	assert.Equal(t, []string{"pavel-decision", "canonical"}, opts.TagsAny)
}

func TestSearch_TagsAny_MixedEncodings(t *testing.T) {
	opts := recallOptsFrom(t, "tags_any=a,b&tags_any=c")
	assert.Equal(t, []string{"a", "b", "c"}, opts.TagsAny,
		"repeated and comma-separated forms must compose")
}

func TestSearch_Tags_RepeatedParams(t *testing.T) {
	opts := recallOptsFrom(t, "tags=kind:decision&tags=project:mesh")
	assert.Equal(t, []string{"kind:decision", "project:mesh"}, opts.Tags,
		"Tags is an AND filter — dropping one silently widens the result set")
}

func TestSearch_Tags_EmptyMeansNoFilter(t *testing.T) {
	opts := recallOptsFrom(t, "tags=&tags_any=")
	assert.Nil(t, opts.Tags, "an empty tags param must mean no filter, not a filter on \"\"")
	assert.Nil(t, opts.TagsAny)
}

func TestSearch_ScopeReachesService(t *testing.T) {
	opts := recallOptsFrom(t, "scope=agent")
	assert.Equal(t, domain.ScopeAgent, opts.Scope)
}
