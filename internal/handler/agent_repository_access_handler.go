package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RepositoryAccessReader loads an agent's configured repository access; nil
// means "not configured".
type RepositoryAccessReader interface {
	Get(ctx context.Context, agentID uuid.UUID) (*domain.AgentRepositoryAccess, error)
}

// AgentRepositoryAccessHandler serves PUT /agents/{id}/repository-access, the
// only write path of an agent's repository access.
type AgentRepositoryAccessHandler struct {
	repo *postgres.AgentRepositoryAccessRepo
}

func NewAgentRepositoryAccessHandler(repo *postgres.AgentRepositoryAccessRepo) *AgentRepositoryAccessHandler {
	return &AgentRepositoryAccessHandler{repo: repo}
}

// Put replaces the list under compare-and-swap on expected_revision:
// 200 {revision, repositories} | 400 malformed | 403 not allowed | 404 | 409 stale.
func (h *AgentRepositoryAccessHandler) Put(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil || agentID == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid agent_id"))
	}
	in, err := decodeRepositoryAccessPut(c)
	if err != nil {
		return handleError(c, err)
	}
	out, err := h.repo.Put(c.Request().Context(), userRuntimeActor(c), agentID, in)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// History serves GET /agents/{id}/repository-access/history?limit=&cursor=:
// the writes newest first. Readable by whoever may read GET /agents/{id}.
func (h *AgentRepositoryAccessHandler) History(c echo.Context) error {
	agentID, err := uuid.Parse(c.Param("agent_id"))
	if err != nil || agentID == uuid.Nil {
		return handleError(c, apierror.BadRequest("invalid agent_id"))
	}
	limit := 0
	if raw := c.QueryParam("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
			return handleError(c, apierror.BadRequest(fmt.Sprintf("limit must be between 1 and %d", postgres.RepositoryAccessHistoryMaxLimit)))
		}
	}
	page, err := h.repo.History(c.Request().Context(), agentID, limit, c.QueryParam("cursor"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

// decodeRepositoryAccessPut is strict: unknown fields, a missing or null
// expected_revision or repositories, and trailing data are refused.
func decodeRepositoryAccessPut(c echo.Context) (postgres.AgentRepositoryAccessPut, error) {
	var in postgres.AgentRepositoryAccessPut
	bad := apierror.BadRequest("body must be {expected_revision: int, repositories: [{provider, host, project_path}]}")
	data, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20))
	if err != nil {
		return in, bad
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		return in, bad
	}
	// Exactly one value: Decoder.More misses a stray ']' or '}', so require EOF.
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return in, bad
	}
	if in.ExpectedRevision == nil || in.Repositories == nil {
		return in, bad
	}
	return in, nil
}
