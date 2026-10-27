package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
)

// CatalogSource is the verified Spark catalog the instance holds.
type CatalogSource interface {
	Current() (*catalog.Catalog, error)
	Status() (fetchedAt time.Time, lastErr error)
	Detail(ctx context.Context, slug string) (*catalog.Card, error)
}

// WithCatalog joins the signed Spark catalog into GET /extensions/catalog.
func (h *ExtensionHandler) WithCatalog(src CatalogSource) *ExtensionHandler {
	h.catalog = src
	return h
}

// catalogStatusResponse tells the UI whether the Spark half of the list is
// there, so a missing catalog reads as "Spark unavailable", not "no extensions".
type catalogStatusResponse struct {
	Available bool       `json:"available"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// WithBuiltins tells the catalog which ids ship with this Mesh binary, so a
// Spark record with runtime "builtin" maps onto the code already here.
func (h *ExtensionHandler) WithBuiltins(known func(id string) bool) *ExtensionHandler {
	h.builtin = known
	return h
}

// Catalog handles GET /extensions/catalog. The signed Spark catalog is the
// list; every extension has one id in every source, so a row is never glued
// from two ids. Installed rows come first and carry the page of their catalog
// record. A catalog record not installed yet follows: source "catalog" for one
// that downloads, source "builtin" for one whose code ships with Mesh (a
// built-in theme an admin removed). A builtin record this binary does not
// carry is left out: there is nothing to install. Without Spark the last
// verified copy is used; with none at all, only the installed rows answer.
func (h *ExtensionHandler) Catalog(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := h.installed.List(ctx)
	if err != nil {
		return handleError(c, err)
	}
	st := catalogStatusResponse{}
	var cat *catalog.Catalog
	if h.catalog != nil {
		at, lastErr := h.catalog.Status()
		if !at.IsZero() {
			st.FetchedAt = &at
		}
		if lastErr != nil {
			st.Error = lastErr.Error()
		}
		if cur, cerr := h.catalog.Current(); cerr == nil {
			cat, st.Available = cur, true
		}
	}
	out := make([]extensionSummaryResponse, 0, len(rows))
	have := make(map[string]bool, len(rows))
	for i := range rows {
		s, ok, serr := h.summary(c, &rows[i])
		if serr != nil {
			return handleError(c, serr)
		}
		if !ok {
			continue
		}
		if e, hit := cat.Entry(s.ID); hit {
			s.PageURL = e.PageURL
			// A repo install that predates the catalog record holds the id
			// the catalog now owns: say so instead of showing it as that record.
			if rows[i].Source == domain.ExtensionSourceRepo {
				s.IDConflict = string(domain.ExtensionSourceCatalog)
			}
		}
		out = append(out, s)
		have[s.ID] = true
	}
	if cat != nil {
		for i := range cat.Extensions {
			e := &cat.Extensions[i]
			if have[e.ID] {
				continue
			}
			src := domain.ExtensionSourceCatalog
			if e.Runtime == "builtin" {
				if h.builtin == nil || !h.builtin(e.ID) {
					continue
				}
				src = domain.ExtensionSourceBuiltin
			}
			s := extensionSummaryResponse{
				ID: e.ID, Name: e.Name, Version: e.Latest.Version, Kind: e.Kind, Runtime: e.Runtime,
				Publisher: e.Publisher, Category: e.Category, Source: string(src),
				TrustLevel: e.Trust, PageURL: e.PageURL, Allowed: domain.DefaultExtensionAllowed(e.Trust),
			}
			if b, hit := cat.Blocks(e.ID, e.Latest.Version); hit {
				s.Blocked, s.BlockedReason = true, b.Reason
			}
			if h.allow != nil {
				ok, aerr := h.allow.Allowed(ctx, e.ID)
				if aerr != nil {
					return handleError(c, aerr)
				}
				s.Allowed = ok
			}
			out = append(out, s)
		}
	}
	return extensionJSON(c, http.StatusOK, map[string]any{"extensions": out, "catalog": st})
}

// CatalogCard handles GET /extensions/:ext_id/catalog-card: the README, release
// history and checks Spark shows for the extension. 404 when the catalog does
// not list it, 502 when Spark cannot be reached (the page then shows what the
// signed catalog already gave it).
func (h *ExtensionHandler) CatalogCard(c echo.Context) error {
	if h.catalog == nil {
		return echo.NewHTTPError(http.StatusNotFound, "catalog is not configured")
	}
	id := c.Param("ext_id")
	cat, err := h.catalog.Current()
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "catalog is not available yet")
	}
	if _, ok := cat.Entry(id); !ok {
		return echo.NewHTTPError(http.StatusNotFound, "extension is not in the catalog")
	}
	card, err := h.catalog.Detail(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "spark card is unavailable")
	}
	return extensionJSON(c, http.StatusOK, card)
}
