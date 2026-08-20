package middleware

import (
	"crypto/subtle"
	"log"
	"os"

	"github.com/labstack/echo/v4"
)

// EnvSpawnToken is the environment variable holding the shared secret that
// gates the spawn-time secret-materialization endpoint. It is deliberately
// NOT an agent API key: any agent identity that could authenticate as
// "an agent" would be able to decrypt every secret in its scope, which is
// exactly the leak this whole feature exists to close. Only the two
// spawners (mesh-dispatcher.py, fiddler.py) — trusted infra processes, not
// agent sessions — hold this value, the same way only they and the API
// process hold MESH_INTEGRATION_ENCRYPTION_KEY (pkg/encryption).
const EnvSpawnToken = "MESH_SPAWN_TOKEN"

// EnvSpawnTokenRequired, when truthy, makes a missing or empty EnvSpawnToken
// a hard startup failure for the materialization route instead of a silent
// "endpoint always 401s". Prod sets it; local dev/tests leave it unset so
// the stack runs without spawn-materialization configured at all.
const EnvSpawnTokenRequired = "MESH_SPAWN_TOKEN_REQUIRED"

// SpawnTokenHeader carries the shared secret on a materialize request.
const SpawnTokenHeader = "X-Spawn-Token"

// SpawnAuth gates the internal spawn-materialization route behind a static
// shared secret, checked in constant time, read fresh from the environment
// on every request rather than cached at startup — the same reason
// pkg/encryption resolves its key lazily: a value that can be rotated by an
// operator without a redeploy must be read live, not memoized once.
//
// Unconfigured means fail CLOSED, not fail open: an empty or unset
// EnvSpawnToken refuses every request with 401, even one that also sends an
// empty header. A misconfigured deployment must not silently accept "no
// token required" — see EnvSpawnTokenRequired for making that misconfiguration
// loud at startup instead of only at the first request.
func SpawnAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			want := os.Getenv(EnvSpawnToken)
			if want == "" {
				return unauthorizedJSON(c, "spawn materialization is not configured")
			}
			got := c.Request().Header.Get(SpawnTokenHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return unauthorizedJSON(c, "invalid spawn token")
			}
			return next(c)
		}
	}
}

// CheckSpawnTokenConfigured logs (and, if EnvSpawnTokenRequired is set,
// fatally exits) when EnvSpawnToken is unset at startup — the same
// fail-loud-at-boot pattern as pkg/encryption.CheckKeyConfigured, so a
// deploy that forgot to set the secret is caught in the startup log rather
// than discovered the first time a lane fails to spawn with its secrets.
func CheckSpawnTokenConfigured() {
	if os.Getenv(EnvSpawnToken) != "" {
		log.Printf("spawn-auth: %s configured — materialization endpoint active", EnvSpawnToken)
		return
	}
	if os.Getenv(EnvSpawnTokenRequired) != "" {
		log.Fatalf("spawn-auth: %s is required but not set", EnvSpawnToken)
	}
	log.Printf("spawn-auth: %s not set — materialization endpoint will refuse every request", EnvSpawnToken)
}
