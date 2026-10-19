package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// Every entry of the extension route -> capability allow-list must name a route
// that is really registered on the protected group. A stale or mistyped entry
// would grant a capability that opens nothing and make the manifest vocabulary
// lie about what an extension can do.
func TestExtensionRouteCapabilities_AreRegisteredRoutes(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	re := regexp.MustCompile(`\bapi\.(GET|POST|PUT|PATCH|DELETE)\("([^"]+)"`)
	registered := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		registered[m[1]+" /api/v1"+m[2]] = true
	}
	require.NotEmpty(t, registered)

	var missing []string
	for key := range mw.ExtensionRouteCapabilities() {
		if !registered[key] {
			missing = append(missing, key)
		}
	}
	assert.Empty(t, missing, "capability map names routes that are not registered: %s", strings.Join(missing, ", "))
}

// GET /extensions/catalog answered 404 on prod once; the UI treats 404 as "not
// deployed" and silently drops the catalog source. The route must stay registered.
func TestExtensionCatalogRoute_IsRegistered(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	assert.Regexp(t, `api\.GET\("/extensions/catalog",\s*extensionHandler\.Catalog\)`, string(src))
}
