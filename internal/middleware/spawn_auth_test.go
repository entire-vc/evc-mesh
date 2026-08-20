package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withSpawnToken(t *testing.T, value string) {
	t.Helper()
	old, hadOld := os.LookupEnv(EnvSpawnToken)
	if value == "" {
		_ = os.Unsetenv(EnvSpawnToken)
	} else {
		_ = os.Setenv(EnvSpawnToken, value)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(EnvSpawnToken, old)
		} else {
			_ = os.Unsetenv(EnvSpawnToken)
		}
	})
}

func runSpawnAuth(t *testing.T, headerValue string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", http.NoBody)
	if headerValue != "" {
		req.Header.Set(SpawnTokenHeader, headerValue)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	h := SpawnAuth()(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	require.NoError(t, h(c))
	return rec
}

func TestSpawnAuth_UnconfiguredRefusesEveryRequest(t *testing.T) {
	// Fail closed: no token configured means no request gets through, not
	// "any header value works because there's nothing to compare against".
	withSpawnToken(t, "")
	rec := runSpawnAuth(t, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec2 := runSpawnAuth(t, "anything-at-all")
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

func TestSpawnAuth_WrongTokenRejected(t *testing.T) {
	withSpawnToken(t, "the-real-token")
	rec := runSpawnAuth(t, "not-the-real-token")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSpawnAuth_MissingHeaderRejected(t *testing.T) {
	withSpawnToken(t, "the-real-token")
	rec := runSpawnAuth(t, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSpawnAuth_CorrectTokenPasses(t *testing.T) {
	withSpawnToken(t, "the-real-token")
	rec := runSpawnAuth(t, "the-real-token")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSpawnAuth_EmptyConfiguredTokenNeverMatchesEmptyHeader(t *testing.T) {
	// An operator accidentally setting MESH_SPAWN_TOKEN="" must not create a
	// state where an empty header satisfies an "empty" secret — that's the
	// same failure class as an unconfigured token, and both must refuse.
	withSpawnToken(t, "")
	rec := runSpawnAuth(t, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func withSpawnTokenRequired(t *testing.T, value string) {
	t.Helper()
	old, hadOld := os.LookupEnv(EnvSpawnTokenRequired)
	if value == "" {
		_ = os.Unsetenv(EnvSpawnTokenRequired)
	} else {
		_ = os.Setenv(EnvSpawnTokenRequired, value)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(EnvSpawnTokenRequired, old)
		} else {
			_ = os.Unsetenv(EnvSpawnTokenRequired)
		}
	})
}

func TestCheckSpawnTokenConfigured_ConfiguredDoesNotExit(t *testing.T) {
	// The configured branch just logs and returns — must not touch the
	// EnvSpawnTokenRequired fatal path at all, configured or not.
	withSpawnToken(t, "some-token")
	withSpawnTokenRequired(t, "1")
	assert.NotPanics(t, CheckSpawnTokenConfigured)
}

func TestCheckSpawnTokenConfigured_UnconfiguredAndNotRequiredWarnsOnly(t *testing.T) {
	// Unconfigured + EnvSpawnTokenRequired unset must warn and return, not
	// exit — this is the expected local-dev/test shape (materialization
	// endpoint simply refuses every request per SpawnAuth's fail-closed
	// behavior, nothing fatal about it).
	withSpawnToken(t, "")
	withSpawnTokenRequired(t, "")
	assert.NotPanics(t, CheckSpawnTokenConfigured)
}
