package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionRepoInstallHandler serves install from a repository (BRAT mode),
// the Updates tab and rollback. Every route is for the instance administrator.
type ExtensionRepoInstallHandler struct {
	svc    *repoinstall.Service
	admins InstanceAdminChecker
}

func NewExtensionRepoInstallHandler(svc *repoinstall.Service, admins InstanceAdminChecker) *ExtensionRepoInstallHandler {
	return &ExtensionRepoInstallHandler{svc: svc, admins: admins}
}

type repoBody struct {
	Source    string `json:"source"`
	RepoURL   string `json:"repo_url"`
	Ref       string `json:"ref"`
	AuthToken string `json:"auth_token"`
}

func (b *repoBody) clean() {
	b.RepoURL, b.Ref, b.AuthToken = strings.TrimSpace(b.RepoURL), strings.TrimSpace(b.Ref), strings.TrimSpace(b.AuthToken)
}

func (h *ExtensionRepoInstallHandler) bind(c echo.Context) (repoBody, error) {
	var b repoBody
	if err := c.Bind(&b); err != nil {
		return b, apierror.BadRequest("invalid request body")
	}
	b.clean()
	if b.RepoURL == "" {
		return b, apierror.BadRequest("repo_url is required")
	}
	return b, nil
}

// Refs handles POST /extensions/repo/refs: the tags and branches to pick from.
func (h *ExtensionRepoInstallHandler) Refs(c echo.Context) error {
	if _, denied, err := authorizeInstanceAdmin(c, h.admins); denied {
		return err
	}
	b, err := h.bind(c)
	if err != nil {
		return handleError(c, err)
	}
	refs, err := h.svc.ListRefs(c.Request().Context(), b.RepoURL, b.AuthToken)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, refs)
}

// Preview handles POST /extensions/repo/preview: read the manifest, install nothing.
func (h *ExtensionRepoInstallHandler) Preview(c echo.Context) error {
	if _, denied, err := authorizeInstanceAdmin(c, h.admins); denied {
		return err
	}
	b, err := h.bind(c)
	if err != nil {
		return handleError(c, err)
	}
	p, err := h.svc.Preview(c.Request().Context(), b.RepoURL, b.Ref, b.AuthToken)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, p)
}

// Install handles POST /extensions/install {source:"repo", repo_url, ref, auth_token?}.
func (h *ExtensionRepoInstallHandler) Install(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	b, err := h.bind(c)
	if err != nil {
		return handleError(c, err)
	}
	if b.Source != "repo" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(`source must be "repo"; catalog installs arrive with the catalog`))
	}
	row, err := h.svc.Install(c.Request().Context(), uid, b.RepoURL, b.Ref, b.AuthToken)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"extension_id": row.ExtensionID, "version": row.Version, "source": row.Source, "trust_level": row.TrustLevel,
		"repo_url": row.RepoURL, "repo_ref": row.RepoRef, "commit_sha": row.CommitSHA,
	})
}

// Updates handles GET /extensions/updates.
func (h *ExtensionRepoInstallHandler) Updates(c echo.Context) error {
	if _, denied, err := authorizeInstanceAdmin(c, h.admins); denied {
		return err
	}
	list, err := h.svc.Updates(c.Request().Context())
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"updates": list})
}

type updateBody struct {
	// ConsentCommitSHA is the commit whose added permissions the admin accepted.
	ConsentCommitSHA string `json:"consent_commit_sha"`
}

// Update handles POST /extensions/:ext_id/update.
func (h *ExtensionRepoInstallHandler) Update(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	var b updateBody
	_ = c.Bind(&b)
	res, err := h.svc.Update(c.Request().Context(), &uid, c.Param("ext_id"), strings.TrimSpace(b.ConsentCommitSHA))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// UpdateAll handles POST /extensions/update-all.
func (h *ExtensionRepoInstallHandler) UpdateAll(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	res, err := h.svc.UpdateAll(c.Request().Context(), &uid)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"results": res})
}

// Rollback handles POST /extensions/:ext_id/rollback.
func (h *ExtensionRepoInstallHandler) Rollback(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	row, err := h.svc.Rollback(c.Request().Context(), &uid, c.Param("ext_id"))
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"extension_id": row.ExtensionID, "version": row.Version, "commit_sha": row.CommitSHA, "repo_ref": row.RepoRef})
}
