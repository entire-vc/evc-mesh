package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/schema"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ExtensionHandler serves the instance-level extension catalog
// (docs/extensions/api.md, "Instance routes").
type ExtensionHandler struct {
	installed repository.InstalledExtensionRepository
	instance  extensions.InstanceStore // optional until instance settings land
}

// NewExtensionHandler creates a new ExtensionHandler. instance may be nil;
// every extension then reports {enabled:false, configured:false}.
func NewExtensionHandler(installed repository.InstalledExtensionRepository, instance extensions.InstanceStore) *ExtensionHandler {
	return &ExtensionHandler{installed: installed, instance: instance}
}

type instanceStatusResponse struct {
	Enabled    bool `json:"enabled"`
	Configured bool `json:"configured"`
}

type extensionSummaryResponse struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Version       string                 `json:"version"`
	Kind          string                 `json:"kind"`
	Runtime       string                 `json:"runtime"`
	Publisher     string                 `json:"publisher"`
	Description   string                 `json:"description"`
	Category      string                 `json:"category"`
	Levels        extensions.Levels      `json:"levels"`
	Source        string                 `json:"source"`
	TrustLevel    string                 `json:"trust_level"`
	Blocked       bool                   `json:"blocked"`
	BlockedReason string                 `json:"blocked_reason"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	Manifest      json.RawMessage        `json:"manifest"`
	Instance      instanceStatusResponse `json:"instance"`
}

// List handles GET /extensions.
func (h *ExtensionHandler) List(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := h.installed.List(ctx)
	if err != nil {
		return handleError(c, err)
	}
	out := make([]extensionSummaryResponse, 0, len(rows))
	for i := range rows {
		s, ok, serr := h.summary(c, &rows[i])
		if serr != nil {
			return handleError(c, serr)
		}
		if ok {
			out = append(out, s)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"extensions": out})
}

// summary builds one catalog entry. A failing instance store fails the whole
// listing: an unknown status must not look like "disabled, not configured".
func (h *ExtensionHandler) summary(c echo.Context, row *domain.InstalledExtension) (extensionSummaryResponse, bool, error) {
	m, err := extensions.ParseManifest(row.Manifest)
	if err != nil {
		log.Printf("[extensions] skip %q in catalog: %v", row.ExtensionID, err)
		return extensionSummaryResponse{}, false, nil
	}
	s := extensionSummaryResponse{
		ID: row.ExtensionID, Name: m.Name, Version: row.Version, Kind: m.Kind,
		Runtime: m.Runtime, Publisher: m.Publisher, Description: m.Description,
		Category: m.Category, Levels: m.Levels, Source: string(row.Source),
		TrustLevel: string(row.TrustLevel), Blocked: row.Blocked,
		BlockedReason: row.BlockedReason, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Manifest: m.Raw,
	}
	if h.instance != nil {
		is, ierr := h.instance.InstanceSetting(c.Request().Context(), row.ExtensionID)
		if ierr != nil {
			return extensionSummaryResponse{}, false, fmt.Errorf("instance setting %q: %w", row.ExtensionID, ierr)
		}
		if is != nil {
			s.Instance = instanceStatusResponse{Enabled: is.Enabled, Configured: true}
		}
	}
	return s, true, nil
}

// ManifestSchema handles GET /extensions/manifest-schema.
func (h *ExtensionHandler) ManifestSchema(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/schema+json", schema.ManifestV1())
}

// NotImplemented answers the install/update/rollback stubs with 501
// not_implemented; they get real handlers in later waves.
func (h *ExtensionHandler) NotImplemented(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]any{
		"code":       http.StatusNotImplemented,
		"error_code": "not_implemented",
		"message":    "This extension operation is not implemented yet",
	})
}
