package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
)

type fakeCatalog struct {
	cat  *catalog.Catalog
	at   time.Time
	err  error
	card *catalog.Card
}

func (f fakeCatalog) Current() (*catalog.Catalog, error) {
	if f.cat == nil {
		return nil, catalog.ErrNoCatalog
	}
	return f.cat, nil
}
func (f fakeCatalog) Status() (time.Time, error) { return f.at, f.err }
func (f fakeCatalog) Detail(context.Context, string) (*catalog.Card, error) {
	if f.card == nil {
		return nil, errors.New("spark down")
	}
	return f.card, nil
}

type mergedBody struct {
	Extensions []map[string]any `json:"extensions"`
	Catalog    struct {
		Available bool   `json:"available"`
		Error     string `json:"error"`
	} `json:"catalog"`
}

func builtinRepo() *fakeInstalledRepo {
	return &fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "team-relay", Version: "1.2.0", Source: domain.ExtensionSourceBuiltin, Manifest: json.RawMessage(testManifest)},
	}}
}

func TestExtensionCatalog_MixedListBuiltinAndSpark(t *testing.T) {
	cat := &catalog.Catalog{
		Extensions: []catalog.Entry{
			{ID: "team-relay", Name: "Team Relay from Spark"}, // already installed: must not duplicate
			{ID: "mesh-ext-hello", Name: "Hello", Kind: "integration", Runtime: "external", Trust: "verified", Publisher: "acme", PageURL: "https://spark.entire.vc/mesh-extensions/mesh-ext-hello", Latest: catalog.Release{Version: "1.0.0"}},
			{ID: "shady", Name: "Shady", Trust: "community", Latest: catalog.Release{Version: "0.1.0"}},
		},
		Blocklist: []catalog.Block{{ID: "shady", Reason: "malware"}},
	}
	h := NewExtensionHandler(builtinRepo(), nil).WithCatalog(fakeCatalog{cat: cat, at: time.Now()})
	rec := doExt(t, h.Catalog, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)
	var body mergedBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Extensions, 3)
	assert.True(t, body.Catalog.Available)

	byID := map[string]map[string]any{}
	for _, e := range body.Extensions {
		byID[e["id"].(string)] = e
	}
	assert.Equal(t, "builtin", byID["team-relay"]["source"])
	assert.Equal(t, true, byID["team-relay"]["installed"])
	hello := byID["mesh-ext-hello"]
	assert.Equal(t, "catalog", hello["source"])
	assert.Equal(t, false, hello["installed"])
	assert.Equal(t, "verified", hello["trust_level"])
	assert.Equal(t, true, hello["allowed"], "verified is allowed by default")
	shady := byID["shady"]
	assert.Equal(t, false, shady["allowed"], "community needs an explicit allow")
	assert.Equal(t, true, shady["blocked"])
	assert.Equal(t, "malware", shady["blocked_reason"])
}

// Spark down and nothing cached: the built-in half still answers and the
// response says the catalog is unavailable, instead of looking empty.
func TestExtensionCatalog_SparkUnavailableKeepsBuiltin(t *testing.T) {
	h := NewExtensionHandler(builtinRepo(), nil).WithCatalog(fakeCatalog{err: errors.New("catalog answered 502")})
	rec := doExt(t, h.Catalog, http.MethodGet)
	require.Equal(t, http.StatusOK, rec.Code)
	var body mergedBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Extensions, 1)
	assert.False(t, body.Catalog.Available)
	assert.Contains(t, body.Catalog.Error, "502")
}

func TestExtensionCatalogCard(t *testing.T) {
	cat := &catalog.Catalog{Extensions: []catalog.Entry{{ID: "mesh-ext-hello", Name: "Hello"}}}
	card := &catalog.Card{ID: "mesh-ext-hello", Readme: "# Hello", Versions: []catalog.CardVersion{{Version: "1.0.0"}}}
	call := func(src fakeCatalog, id string) int {
		h := NewExtensionHandler(builtinRepo(), nil).WithCatalog(src)
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetParamNames("ext_id")
		c.SetParamValues(id)
		if err := h.CatalogCard(c); err != nil {
			var he *echo.HTTPError
			require.ErrorAs(t, err, &he)
			return he.Code
		}
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, call(fakeCatalog{cat: cat, card: card}, "mesh-ext-hello"))
	assert.Equal(t, http.StatusNotFound, call(fakeCatalog{cat: cat, card: card}, "unknown"))
	assert.Equal(t, http.StatusBadGateway, call(fakeCatalog{cat: cat}, "mesh-ext-hello"))
	assert.Equal(t, http.StatusServiceUnavailable, call(fakeCatalog{}, "mesh-ext-hello"))
}
