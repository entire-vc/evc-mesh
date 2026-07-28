//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCrossTenant_SparkInstallRequiresMembership covers the one route deliberately
// exempt from the group-level guard. POST /spark/agents/:agent_id/install takes its
// target workspace from the request body, so no middleware can see which workspace
// to check — and it had no check of its own, which let any authenticated stranger
// register an agent into someone else's workspace and receive that agent's API key
// in the 201 response.
//
// The membership check now runs before the catalog is contacted, which is also
// what makes this test runnable without a Spark server: with membership, the
// request reaches the catalog and fails there (503); without it, it never gets
// that far (403).
//
// Requires MESH_SPARK_ENABLED=true on the server under test.
func TestCrossTenant_SparkInstallRequiresMembership(t *testing.T) {
	if os.Getenv("TEST_SPARK_ENABLED") != "true" {
		t.Skip("set TEST_SPARK_ENABLED=true and run the API with MESH_SPARK_ENABLED=true")
	}

	victim := newVictimFixture(t, "xts-victim")

	intruder := NewTestEnv(t)
	defer intruder.Cleanup(t)
	intruder.Register(t, uniqueEmail("xts-intruder"), "TestPass123", "Intruder")

	resp := intruder.Post(t, "/api/v1/spark/agents/some-catalog-id/install", map[string]any{
		"workspace_id": victim.wsID,
	})
	body := string(intruder.ReadBody(t, resp))
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a stranger installed an agent into another tenant's workspace (status %d, body %s)", resp.StatusCode, body)
	assert.NotContains(t, body, "agk_", "the response handed out an agent API key")

	var count int
	require.NoError(t, victim.env.DB.QueryRow(
		"SELECT COUNT(*) FROM agents WHERE workspace_id = $1 AND name <> 'victim-agent'", victim.wsID,
	).Scan(&count))
	assert.Zero(t, count, "a stranger's agent was created in another tenant's workspace")

	// The workspace's own owner gets past the membership bar and on to the
	// catalog, which is unreachable in tests — 503, not 403.
	ownerResp := victim.env.Post(t, "/api/v1/spark/agents/some-catalog-id/install", map[string]any{
		"workspace_id": victim.wsID,
	})
	ownerBody := string(victim.env.ReadBody(t, ownerResp))
	assert.NotEqual(t, http.StatusForbidden, ownerResp.StatusCode,
		"the workspace owner was refused their own install (body %s)", ownerBody)
	assert.Contains(t, fmt.Sprint(ownerResp.StatusCode), "50",
		"expected the owner to reach the (unavailable) Spark catalog, got %d: %s", ownerResp.StatusCode, ownerBody)
}
