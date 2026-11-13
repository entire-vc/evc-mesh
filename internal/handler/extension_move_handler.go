package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// CatalogMover moves a repository install to the signed catalog.
type CatalogMover interface {
	PlanMoveToCatalog(ctx context.Context, id, targetVersion, targetScope string) (*catalog.MovePlan, error)
	MoveToCatalog(ctx context.Context, actor uuid.UUID, id string, req catalog.MoveRequest) (*catalog.MoveResult, error)
}

// WithCatalogMove enables POST /extensions/:ext_id/move-to-catalog{,/plan}.
func (h *ExtensionRepoInstallHandler) WithCatalogMove(m CatalogMover) *ExtensionRepoInstallHandler {
	h.mover = m
	return h
}

type moveBody struct {
	TargetVersion     string `json:"target_version"`
	TargetCommitSHA   string `json:"target_commit_sha"`
	TargetScope       string `json:"target_scope"`
	ExpectedVersion   string `json:"expected_version"`
	ExpectedCommitSHA string `json:"expected_commit_sha"`
	ConsentCommitSHA  string `json:"consent_commit_sha"`
}

func (h *ExtensionRepoInstallHandler) bindMove(c echo.Context) (uuid.UUID, moveBody, error) {
	var b moveBody
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return uid, b, err
	}
	if h.mover == nil {
		return uid, b, c.JSON(http.StatusNotImplemented, apierror.BadRequest("moving to the catalog is not enabled on this instance"))
	}
	if err := c.Bind(&b); err != nil {
		return uid, b, handleError(c, apierror.BadRequest("invalid request body"))
	}
	for _, s := range []*string{&b.TargetVersion, &b.TargetCommitSHA, &b.TargetScope, &b.ExpectedVersion, &b.ExpectedCommitSHA, &b.ConsentCommitSHA} {
		*s = strings.TrimSpace(*s)
	}
	return uid, b, nil
}

// PlanMoveToCatalog handles POST /extensions/:ext_id/move-to-catalog/plan
// {target_version?}: what the move would change. Writes nothing.
func (h *ExtensionRepoInstallHandler) PlanMoveToCatalog(c echo.Context) error {
	_, b, err := h.bindMove(c)
	if err != nil || c.Response().Committed {
		return err
	}
	plan, err := h.mover.PlanMoveToCatalog(c.Request().Context(), c.Param("ext_id"), b.TargetVersion, b.TargetScope)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, plan)
}

// MoveToCatalog handles POST /extensions/:ext_id/move-to-catalog with the
// expected current state and the chosen catalog release. One way.
func (h *ExtensionRepoInstallHandler) MoveToCatalog(c echo.Context) error {
	uid, b, err := h.bindMove(c)
	if err != nil || c.Response().Committed {
		return err
	}
	res, err := h.mover.MoveToCatalog(c.Request().Context(), uid, c.Param("ext_id"), catalog.MoveRequest{
		ExpectedVersion: b.ExpectedVersion, ExpectedCommitSHA: b.ExpectedCommitSHA,
		TargetVersion: b.TargetVersion, TargetCommitSHA: b.TargetCommitSHA, TargetScope: b.TargetScope, ConsentCommitSHA: b.ConsentCommitSHA,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

type repinBody struct {
	RepoURL                 string `json:"repo_url"`
	Ref                     string `json:"ref"`
	AuthToken               string `json:"auth_token"`
	RemoveAuthToken         bool   `json:"remove_auth_token"`
	ExpectedVersion         string `json:"expected_version"`
	ExpectedCommitSHA       string `json:"expected_commit_sha"`
	ExpectedTargetCommitSHA string `json:"expected_target_commit_sha"`
	ConsentCommitSHA        string `json:"consent_commit_sha"`
}

func (h *ExtensionRepoInstallHandler) bindRepin(c echo.Context) (uuid.UUID, repoinstall.RepinRequest, error) {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return uid, repoinstall.RepinRequest{}, err
	}
	var b repinBody
	if err := c.Bind(&b); err != nil {
		return uid, repoinstall.RepinRequest{}, handleError(c, apierror.BadRequest("invalid request body"))
	}
	return uid, repoinstall.RepinRequest{
		RepoURL: strings.TrimSpace(b.RepoURL), Ref: strings.TrimSpace(b.Ref), AuthToken: strings.TrimSpace(b.AuthToken), RemoveAuthToken: b.RemoveAuthToken,
		ExpectedVersion: strings.TrimSpace(b.ExpectedVersion), ExpectedCommitSHA: strings.TrimSpace(b.ExpectedCommitSHA),
		ExpectedTargetCommitSHA: strings.TrimSpace(b.ExpectedTargetCommitSHA), ConsentCommitSHA: strings.TrimSpace(b.ConsentCommitSHA),
	}, nil
}

// PlanRepin handles POST /extensions/:ext_id/repin/plan {repo_url?, ref,
// auth_token? | remove_auth_token?}. Writes nothing; the answer never carries a token.
func (h *ExtensionRepoInstallHandler) PlanRepin(c echo.Context) error {
	_, req, err := h.bindRepin(c)
	if err != nil || c.Response().Committed {
		return err
	}
	plan, err := h.svc.PlanRepin(c.Request().Context(), c.Param("ext_id"), req)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, plan)
}

// Repin handles POST /extensions/:ext_id/repin: the plan's body plus the
// expected current version and commit.
func (h *ExtensionRepoInstallHandler) Repin(c echo.Context) error {
	uid, req, err := h.bindRepin(c)
	if err != nil || c.Response().Committed {
		return err
	}
	res, err := h.svc.Repin(c.Request().Context(), uid, c.Param("ext_id"), req)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}
