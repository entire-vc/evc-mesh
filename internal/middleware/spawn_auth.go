package middleware

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// EnvSpawnerAgentIDs lists, comma-separated, the agent ids allowed to call
// the materialization route with their own X-Agent-Key (see SpawnerAuth).
// Read on every request, not cached at startup, so an operator can add or
// drop a spawner without a redeploy. Unset or empty admits no agent.
const EnvSpawnerAgentIDs = "MESH_SPAWNER_AGENT_IDS"

// spawnerAgentIDs parses EnvSpawnerAgentIDs. An entry that is not a UUID (or
// is the nil UUID) is skipped rather than fatal: a typo in one entry must not
// take the endpoint down for the spawners that are listed correctly, and a
// skipped entry can only admit fewer agents, never more.
func spawnerAgentIDs() map[uuid.UUID]struct{} {
	ids := make(map[uuid.UUID]struct{})
	for _, part := range strings.Split(os.Getenv(EnvSpawnerAgentIDs), ",") {
		id, err := uuid.Parse(strings.TrimSpace(part))
		if err != nil || id == uuid.Nil {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}

// SpawnerAuth gates the materialization route by the spawner's own agent
// identity: an X-Agent-Key that authenticates through the ordinary agent-key
// path AND whose agent is listed in EnvSpawnerAgentIDs. Authenticating is not
// enough: any agent can do that, and any agent able to call this route could
// decrypt every secret in its scope — the leak this whole feature exists to
// close. So a valid agent outside the allowlist gets 403, and an empty
// allowlist admits nobody (fail closed). A deleted agent or a rotated key no
// longer authenticates and gets 401, so revoking a spawner is the ordinary
// key revocation, not a shared-secret rotation across every spawner host.
// A lookup that fails without judging the key (DB down, canceled context) is
// a retryable 503, as in AgentKeyAuth, so a spawner never reads it as a
// revoked key.
//
// A mot_ Bearer token is never looked at here: OAuth connector identities act
// for a person and have no business on spawn infra, so such a request is
// judged on its other headers alone (and, carrying none, gets 401).
//
// The key is the only way in. The shared spawn token this route used to
// accept is gone: a request without X-Agent-Key gets 401 whatever
// else it carries and however the environment is set.
func SpawnerAuth(agentService service.AgentService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			apiKey := c.Request().Header.Get("X-Agent-Key")
			if apiKey == "" {
				return unauthorizedJSON(c, "spawner agent key required")
			}
			workspaceSlug, err := parseWorkspaceSlugFromKey(apiKey)
			if err != nil {
				return unauthorizedJSON(c, "Invalid agent API key format")
			}
			agent, err := agentService.Authenticate(c.Request().Context(), workspaceSlug, apiKey)
			if err != nil && !isAgentKeyDenial(err) {
				return authBackendUnavailableJSON(c, "SpawnerAuth", workspaceSlug, apiKey, err)
			}
			if err != nil || agent == nil {
				return unauthorizedJSON(c, "Invalid agent API key")
			}
			if _, ok := spawnerAgentIDs()[agent.ID]; !ok {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("agent is not an authorized spawner"))
			}
			setAgentAuthContext(c, agent)
			return next(c)
		}
	}
}

// LogSpawnerAllowlist reports at startup whether the materialization route
// can admit anyone, so a deploy without EnvSpawnerAgentIDs shows up in the
// startup log rather than the first time a lane fails to spawn with its
// secrets. It never exits: an empty allowlist is a closed endpoint, not a
// broken API, and local dev/tests run without one.
func LogSpawnerAllowlist() {
	if n := len(spawnerAgentIDs()); n > 0 {
		log.Printf("spawn-auth: %s lists %d agent(s) — agent-key path active", EnvSpawnerAgentIDs, n)
		return
	}
	log.Printf("spawn-auth: %s not set — materialization endpoint will refuse every request", EnvSpawnerAgentIDs)
}
