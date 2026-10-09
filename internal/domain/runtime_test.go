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
