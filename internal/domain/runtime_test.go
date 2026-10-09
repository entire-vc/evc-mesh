package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func runtimeExample(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../docs/api/agent-runtime.example.json")
	require.NoError(t, err)
	return data
}

func TestRuntimeCatalogPublishedContract(t *testing.T) {
	catalog, err := ParseRuntimeCatalog(runtimeExample(t))
	require.NoError(t, err)
	require.Len(t, catalog.Pools, 3)
	require.False(t, catalog.Bindings["worker-b"].Policy.QA.QuotaFallback)
}

func TestRuntimeCatalogRejectsUnsafeRequests(t *testing.T) {
	for _, scenario := range []string{"secret", "missing", "null", "unknown", "pool_alias", "physical_duplicate", "bad_reference", "cross_provider", "unpermitted_route"} {
		t.Run(scenario, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, json.Unmarshal(runtimeExample(t), &doc))
			switch scenario {
			case "secret":
				doc["api_key"] = "synthetic-value"
			case "missing":
				delete(doc, "schema_version")
			case "null":
				doc["controllers"] = nil
			case "unknown":
				doc["schema_version"] = 3
			case "pool_alias":
				doc["pools"].(map[string]any)["long-window"].(map[string]any)["aliases"] = []string{"shared-window"}
			case "physical_duplicate":
				doc["pools"].(map[string]any)["long-window"] = doc["pools"].(map[string]any)["shared-window"]
			case "bad_reference":
				doc["profiles"].(map[string]any)["preferred"].(map[string]any)["controller_ref"] = "missing"
			case "cross_provider":
				doc["pools"].(map[string]any)["long-window"].(map[string]any)["provider"] = "provider-b"
			case "unpermitted_route":
				doc["bindings"].(map[string]any)["worker-b"].(map[string]any)["policy"].(map[string]any)["primary_profiles"] = []string{"missing"}
			}
			data, err := json.Marshal(doc)
			require.NoError(t, err)
			_, err = ParseRuntimeCatalog(data)
			require.ErrorIs(t, err, ErrRuntimeInput)
		})
	}
	for _, input := range []string{`{"enabled":true,"enabled":false}`, `{} {}`, `{"secret":"synthetic-value"}`, `{"enabled":null}`} {
		var request struct {
			Enabled bool `json:"enabled"`
		}
		err := DecodeRuntimeJSON([]byte(input), &request)
		require.ErrorIs(t, err, ErrRuntimeInput)
		require.NotContains(t, err.Error(), "synthetic-value")
	}
}

func TestRuntimeDesiredForNarrowsToOneController(t *testing.T) {
	c, err := ParseRuntimeCatalog(runtimeExample(t))
	require.NoError(t, err)
	other := c.Controllers["runner-a"]
	other.Host = "other-host"
	c.Controllers["runner-b"] = other
	c.Pools["pool-b"] = RuntimePool{Provider: "provider-b", ResourceRef: "pool-b", Aliases: []string{}, MaxConcurrency: 1}
	c.Accounts["acct-b"] = RuntimeAccount{Provider: "provider-b", CredentialRef: "cred:only-b", QuotaPoolsByMode: map[string][]string{"subscription": {"pool-b"}}}
	p := c.Profiles["preferred"]
	p.ControllerRef, p.AccountRef = "runner-b", "acct-b"
	c.Profiles["only-b"] = p
	b := c.Bindings["worker-b"]
	b.PermittedProfiles = append(b.PermittedProfiles, "only-b")
	b.Policy.PrimaryProfiles = append(b.Policy.PrimaryProfiles, "only-b")
	b.Policy.QuotaEdges["reserve"] = []string{"only-b"}
	b.Policy.QuotaEdges["only-b"] = []string{"preferred"}
	b.Policy.PreferredAccounts["provider-b"] = "acct-b"
	b.Policy.APIReserveProviders = []string{"provider-a", "provider-b"}
	c.Bindings["worker-b"] = b
	acct := c.Accounts["preferred"]
	acct.QuotaPoolsByMode["api"] = []string{"reserve-window"}
	c.Accounts["preferred"] = acct
	c.Bindings["only-b-binding"] = RuntimeBinding{PermittedProfiles: []string{"only-b"}, Policy: RuntimePolicy{PrimaryProfiles: []string{"only-b"}}}

	_, ok := c.DesiredFor("missing")
	require.False(t, ok)

	a, ok := c.DesiredFor("runner-a")
	require.True(t, ok)
	require.ElementsMatch(t, []string{"preferred", "reserve"}, keysOf(a.Profiles))
	require.ElementsMatch(t, []string{"preferred", "reserve"}, keysOf(a.Accounts))
	require.ElementsMatch(t, []string{"shared-window", "long-window", "reserve-window"}, keysOf(a.Pools))
	require.Equal(t, []string{"worker-b"}, keysOf(a.Bindings))
	pol := a.Bindings["worker-b"]
	require.Equal(t, []string{"preferred", "reserve"}, pol.PermittedProfiles)
	require.Equal(t, []string{"preferred"}, pol.Policy.PrimaryProfiles)
	require.Equal(t, map[string][]string{"preferred": {"reserve"}, "reserve": {}}, pol.Policy.QuotaEdges)
	require.Equal(t, map[string]string{"provider-a": "preferred"}, pol.Policy.PreferredAccounts)
	require.Equal(t, []string{"provider-a"}, pol.Policy.APIReserveProviders)
	require.NotContains(t, a.Accounts["preferred"].QuotaPoolsByMode, "api", "a mode no profile of this controller runs in")
	require.Equal(t, []string{"provider-b"}, c.mustDesired(t, "runner-b").Bindings["worker-b"].Policy.APIReserveProviders)

	bView, ok := c.DesiredFor("runner-b")
	require.True(t, ok)
	require.Equal(t, []string{"only-b"}, keysOf(bView.Profiles))
	require.ElementsMatch(t, []string{"worker-b", "only-b-binding"}, keysOf(bView.Bindings))
	require.Equal(t, map[string]string{"provider-b": "acct-b"}, bView.Bindings["worker-b"].Policy.PreferredAccounts)
	require.Equal(t, "cred:only-b", bView.Accounts["acct-b"].CredentialRef)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (c *RuntimeCatalog) mustDesired(t *testing.T, ref string) RuntimeDesiredCatalog {
	t.Helper()
	d, ok := c.DesiredFor(ref)
	require.True(t, ok)
	return d
}
