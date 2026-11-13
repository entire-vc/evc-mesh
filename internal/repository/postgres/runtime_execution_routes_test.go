package postgres

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func routesExampleCatalog(t *testing.T) *domain.RuntimeCatalog {
	t.Helper()
	data, err := os.ReadFile("../../../docs/api/agent-runtime.example.json")
	require.NoError(t, err)
	c, err := domain.ParseRuntimeCatalog(data)
	require.NoError(t, err)
	return c
}

// A binding's policy.profile_models names the model its route shows; a profile
// without an override keeps the catalog's model, placeholder included.
func TestConfiguredRoutesTakeTheBindingModel(t *testing.T) {
	c := routesExampleCatalog(t)
	b := c.Bindings["worker-b"]
	b.Policy.ProfileModels = map[string]string{"preferred": "claude-opus-5-5"}
	admitted := RuntimeAdmission{PermittedProfiles: []string{"preferred", "reserve"}}

	routes := configuredRoutes(c, "worker-b", b, admitted)
	require.Len(t, routes, 2)
	require.Equal(t, "preferred", routes[0].ProfileRef)
	require.Equal(t, "claude-opus-5-5", routes[0].Model)
	require.Equal(t, ModelSourceBinding, routes[0].ModelSource)
	require.Equal(t, "reserve", routes[1].ProfileRef)
	require.Equal(t, c.Profiles["reserve"].Model, routes[1].Model)
	require.Equal(t, ModelSourceProfile, routes[1].ModelSource)

	// The override is per binding: the catalog's profile itself is unchanged.
	require.Equal(t, "model-a", c.Profiles["preferred"].Model)
}

// A catalog without profile_models produces the routes it produced before the
// field existed, byte for byte, plus model_source "profile" on every route.
func TestConfiguredRoutesWithoutOverrideAreUnchanged(t *testing.T) {
	// Captured from configuredRoutes on main before profile_models existed.
	const before = `[{"binding_ref":"worker-b","controller_ref":"runner-a","profile_ref":"preferred","harness":"prepared-harness","model":"model-a","effort":null,"order":0},{"binding_ref":"worker-b","controller_ref":"runner-a","profile_ref":"reserve","harness":"prepared-harness","model":"model-a","effort":null,"order":0}]`
	c := routesExampleCatalog(t)
	b := c.Bindings["worker-b"]
	require.Nil(t, b.Policy.ProfileModels)
	routes := configuredRoutes(c, "worker-b", b, RuntimeAdmission{PermittedProfiles: []string{"preferred", "reserve"}})
	out, err := json.Marshal(routes)
	require.NoError(t, err)

	got := string(out)
	require.Equal(t, 2, strings.Count(got, `"model_source":"profile",`))
	require.Equal(t, before, strings.ReplaceAll(got, `"model_source":"profile",`, ""))
}
