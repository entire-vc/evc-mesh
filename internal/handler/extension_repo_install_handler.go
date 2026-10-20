package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionRepoInstallHandler serves install from a repository (BRAT mode),
// the Updates tab and rollback. Every route is for the instance administrator.
type ExtensionRepoInstallHandler struct {
	svc     *repoinstall.Service
	admins  InstanceAdminChecker
	catalog CatalogInstaller
}

// CatalogInstaller installs an extension from the signed Spark catalog.
type CatalogInstaller interface {
	Install(ctx context.Context, actor uuid.UUID, id string) (*domain.InstalledExtension, error)
	Updates(ctx context.Context) ([]repoinstall.UpdateInfo, error)
	Update(ctx context.Context, actor *uuid.UUID, id, consentSHA string) (*repoinstall.UpdateResult, error)
	UpdateAll(ctx context.Context, actor *uuid.UUID) ([]repoinstall.UpdateResult, error)
}

// isNotFound reports an apierror 404: the id belongs to the other install source.
func isNotFound(err error) bool {
	var ae *apierror.Error
	return errors.As(err, &ae) && ae.Code == http.StatusNotFound
}

// WithCatalogInstall enables {source:"catalog", id} on POST /extensions/install.
func (h *ExtensionRepoInstallHandler) WithCatalogInstall(i CatalogInstaller) *ExtensionRepoInstallHandler {
	h.catalog = i
	return h
}

func NewExtensionRepoInstallHandler(svc *repoinstall.Service, admins InstanceAdminChecker) *ExtensionRepoInstallHandler {
	return &ExtensionRepoInstallHandler{svc: svc, admins: admins}
}

type repoBody struct {
	Source    string `json:"source"`
	ID        string `json:"id"`
	RepoURL   string `json:"repo_url"`
	Ref       string `json:"ref"`
	AuthToken string `json:"auth_token"`
}

func (b *repoBody) clean() {
	b.RepoURL, b.Ref, b.AuthToken = strings.TrimSpace(b.RepoURL), strings.TrimSpace(b.Ref), strings.TrimSpace(b.AuthToken)
}

func (h *ExtensionRepoInstallHandler) bindAny(c echo.Context) (repoBody, error) {
	var b repoBody
	if err := c.Bind(&b); err != nil {
		return b, apierror.BadRequest("invalid request body")
	}
	b.clean()
	b.ID = strings.TrimSpace(b.ID)
	return b, nil
}

func (h *ExtensionRepoInstallHandler) bind(c echo.Context) (repoBody, error) {
	b, err := h.bindAny(c)
	if err != nil {
		return b, err
	}
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
	b, err := h.bindAny(c)
	if err != nil {
		return handleError(c, err)
	}
	switch b.Source {
	case "catalog":
		return h.installFromCatalog(c, uid, b)
	case "repo":
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(`source must be "repo" or "catalog"`))
	}
	if b.RepoURL == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("repo_url is required"))
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
	if h.catalog != nil {
		more, cerr := h.catalog.Updates(c.Request().Context())
		if cerr != nil {
			return handleError(c, cerr)
		}
		list = append(list, more...)
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
	consent := strings.TrimSpace(b.ConsentCommitSHA)
	res, err := h.svc.Update(c.Request().Context(), &uid, c.Param("ext_id"), consent)
	if err != nil && h.catalog != nil && isNotFound(err) {
		res, err = h.catalog.Update(c.Request().Context(), &uid, c.Param("ext_id"), consent)
	}
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
	if h.catalog != nil {
		more, cerr := h.catalog.UpdateAll(c.Request().Context(), &uid)
		if cerr != nil {
			return handleError(c, cerr)
		}
		res = append(res, more...)
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

func (h *ExtensionRepoInstallHandler) installFromCatalog(c echo.Context, uid uuid.UUID, b repoBody) error {
	if h.catalog == nil {
		return c.JSON(http.StatusNotImplemented, apierror.BadRequest("catalog install is not enabled on this instance"))
	}
	if b.ID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("id is required"))
	}
	row, err := h.catalog.Install(c.Request().Context(), uid, b.ID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"extension_id": row.ExtensionID, "version": row.Version, "source": row.Source, "trust_level": row.TrustLevel,
		"commit_sha": row.CommitSHA, "artifact_sha256": row.ArtifactSHA256,
	})
}
