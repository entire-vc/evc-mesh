package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
)

// catalogRows serves rows as installed_extensions does; only List is used by
// the external runtime.
type catalogRows struct{ rows []domain.InstalledExtension }

func (c *catalogRows) UpsertBuiltin(context.Context, *domain.InstalledExtension) error { return nil }
func (c *catalogRows) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return nil, nil
}
func (c *catalogRows) List(context.Context) ([]domain.InstalledExtension, error) {
	return c.rows, nil
}

func catalogManifest(id, instanceProps, required string) string {
	return fmt.Sprintf(`{"schema":1,"id":%q,"name":%q,"version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":true,"workspace":true,"project":false},
 "settings":{"instance":{"type":"object","properties":{%s},"required":[%s]}},
 "capabilities":{"api":[],"events":[],"ui":[]},
 "external":{"base_url_setting":"svc_url","health_path":"/healthz"}}`, id, id, instanceProps, required)
}

// An extension installed from the catalog is loaded by the external runtime
// into the registry; its instance settings must be saved against the schema the
// manifest declares, not refused as "no instance-level settings".
func TestInstanceSettings_CatalogInstalledExtensionsAcceptDeclaredSchema(t *testing.T) {
	const url = "https://svc.example.test"
	rows := &catalogRows{}
	add := func(id, manifest string) {
		rows.rows = append(rows.rows, domain.InstalledExtension{ExtensionID: id, Manifest: json.RawMessage(manifest), Source: domain.ExtensionSourceCatalog})
	}
	add("cat-a", catalogManifest("cat-a", `"svc_url":{"type":"string"},"level":{"type":"string","enum":["store","org"]}`, `"svc_url"`))
	add("cat-b", catalogManifest("cat-b", `"svc_url":{"type":"string"}`, `"svc_url"`))
	// Instance level by ref, with a nested ref to a sibling definition.
	add("cat-ref", `{"schema":1,"id":"cat-ref","name":"r","version":"0.1.0","kind":"plugin","runtime":"external","mesh_api":">=1","publisher":"example","category":"development","levels":{"instance":true,"workspace":true},"settings":{"instance":{"$ref":"#/schemas/inst"}},"schemas":{"inst":{"type":"object","properties":{"svc_url":{"$ref":"#/schemas/url"}},"required":["svc_url"]},"url":{"type":"string","minLength":8}},"capabilities":{"events":[]},"external":{"base_url_setting":"svc_url","health_path":"/healthz"}}`)
	// No instance schema: the negative control.
	add("cat-none", `{"schema":1,"id":"cat-none","name":"n","version":"0.1.0","kind":"plugin","runtime":"external","mesh_api":">=1","publisher":"example","category":"development","levels":{"workspace":true},"capabilities":{"events":[]},"external":{"base_url_setting":"svc_url","health_path":"/healthz"}}`)

	reg := extensions.NewRegistry()
	rt := &external.Runtime{Reg: reg, Installed: rows}
	require.NoError(t, rt.Sync(context.Background()))

	repo := &fakeInstRepo{}
	h := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: true}, reg, nil)

	for _, id := range []string{"cat-a", "cat-b"} {
		body := fmt.Sprintf(`{"enabled":true,"settings":{"svc_url":%q}}`, url)
		rec, _ := callInstanceSettings(t, h, http.MethodPut, id, body, false)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", id, rec.Body.String())

		rec, _ = callInstanceSettings(t, h, http.MethodGet, id, "", false)
		require.Equal(t, http.StatusOK, rec.Code)
		var got instanceSettingsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.True(t, got.Enabled, id)
		require.Equal(t, url, got.Settings["svc_url"], id)
	}

	rec0, _ := callInstanceSettings(t, h, http.MethodPut, "cat-ref", fmt.Sprintf(`{"enabled":true,"settings":{"svc_url":%q}}`, url), false)
	require.Equal(t, http.StatusOK, rec0.Code, "nested #/schemas ref resolves: %s", rec0.Body.String())
	rec0, _ = callInstanceSettings(t, h, http.MethodPut, "cat-ref", `{"enabled":true,"settings":{"svc_url":"x"}}`, false)
	require.Equal(t, http.StatusUnprocessableEntity, rec0.Code, "the nested definition is enforced")

	// The declared schema still validates: a missing required key, a wrong type,
	// a value outside the enum are refused and nothing is written.
	before := *repo.row
	for _, bad := range []string{
		`{"enabled":true,"settings":{"level":"store"}}`,
		`{"enabled":true,"settings":{"svc_url":5}}`,
		`{"enabled":true,"settings":{}}`,
		`{"enabled":true}`,
		`{"enabled":true,"settings":{"svc_url":"https://x.example.test","level":"nope"}}`,
	} {
		rec, _ := callInstanceSettings(t, h, http.MethodPut, "cat-a", bad, false)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, bad)
		require.Equal(t, before.Settings, repo.row.Settings, "a refused PUT must not write")
	}

	// No instance schema: still refused.
	rec, _ := callInstanceSettings(t, h, http.MethodPut, "cat-none", fmt.Sprintf(`{"enabled":true,"settings":{"svc_url":%q}}`, url), false)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "no instance-level settings")
	rec, _ = callInstanceSettings(t, h, http.MethodPut, "cat-none", `{"enabled":true}`, false)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an empty PUT does not bypass the no-schema refusal")

	// RBAC is unchanged: a non-admin is refused before any validation.
	hn := NewExtensionInstanceSettingsHandler(repo, fakeAdmins{ok: false}, reg, nil)
	rec, _ = callInstanceSettings(t, hn, http.MethodPut, "cat-a", fmt.Sprintf(`{"enabled":true,"settings":{"svc_url":%q}}`, url), false)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
