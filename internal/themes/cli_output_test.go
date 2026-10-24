package themes_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/themes"
)

// TestCLIOutput_InstallsAndIsRejectedWhenTampered takes what `mesh-theme build` wrote
// (packages/mesh-theme-sdk, T6.3; testdata/cli-dist is its output for fixtures/theme-good,
// kept current by scripts/check-cli.mjs) and runs it through the server's own checks.
func TestCLIOutput_InstallsAndIsRejectedWhenTampered(t *testing.T) {
	dist := filepath.Join("testdata", "cli-dist")
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dist, name))
		require.NoError(t, err)
		return b
	}
	raw := read("mesh-extension.json")
	files := map[string][]byte{"views.js": read("views.js"), "theme.css": read("theme.css")}
	m := parse(t, raw)

	// Green: the manifest the CLI wrote satisfies the contract and its hashes match its files.
	spec, err := themes.ParseSpec(m)
	require.NoError(t, err)
	require.NotEmpty(t, spec.ContractReport.Passed)
	svc, _, _, _ := newService()
	require.NoError(t, svc.PrepareInstall(context.Background(), m, filesOf(files)))

	// Red 1: the bundle on the repository is not the one the manifest hashes.
	svc, _, _, store := newService()
	err = svc.PrepareInstall(context.Background(), m, filesOf(map[string][]byte{"views.js": append(files["views.js"], ' '), "theme.css": files["theme.css"]}))
	ae := apiErr(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, ae.StatusCode())
	require.Equal(t, themes.CodeHashMismatch, ae.ErrorCode)
	require.Contains(t, ae.Validation, "theme.views.sha256")
	require.Empty(t, store.Objects)

	// Red 2: the report names another bundle than the manifest's views.
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	report := doc["theme"].(map[string]any)["contract_report"].(map[string]any)
	report["bundle_sha256"] = "0000000000000000000000000000000000000000000000000000000000000000"
	stale, err := json.Marshal(doc)
	require.NoError(t, err)
	_, err = themes.ParseSpec(parse(t, stale))
	require.Contains(t, apiErr(t, err).Validation, "theme.contract_report.bundle_sha256")

	// Red 3: a slot that did not pass is absent from the report, so the theme is refused.
	report["bundle_sha256"] = spec.Views.SHA256
	delete(report["passed"].(map[string]any), "board.card")
	failed, err := json.Marshal(doc)
	require.NoError(t, err)
	_, err = themes.ParseSpec(parse(t, failed))
	require.Contains(t, apiErr(t, err).Validation, "theme.contract_report.passed.board.card")
}
