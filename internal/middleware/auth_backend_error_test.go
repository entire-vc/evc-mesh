package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A failed key lookup (DB down, pool exhausted, canceled context) is not a
// verdict on the key: every agent-key middleware must answer it with a
// retryable 503, and keep 401 for a real denial (card an internal ticket).
func TestAgentKeyMiddlewares_BackendErrorIs503_DenialIs401(t *testing.T) {
	middlewares := map[string]func(*mockAgentService) echo.MiddlewareFunc{
		"AgentKeyAuth": func(s *mockAgentService) echo.MiddlewareFunc { return AgentKeyAuth(s, nil) },
		"DualAuth":     func(s *mockAgentService) echo.MiddlewareFunc { return DualAuth(newTestAuthService(), s, nil) },
		"OptionalAuth": func(s *mockAgentService) echo.MiddlewareFunc { return OptionalAuth(newTestAuthService(), s, nil) },
	}
	backendErrors := map[string]error{
		"conn refused":     errors.New("conn refused"),
		"wrapped canceled": fmt.Errorf("get workspace: %w", context.Canceled),
	}

	for name, build := range middlewares {
		for errName, backendErr := range backendErrors {
			t.Run(name+"/"+errName, func(t *testing.T) {
				rec, called := runAgentKeyMiddleware(t, build, backendErr)
				assert.False(t, called, "handler must not run when the key could not be verified")
				require.Equal(t, http.StatusServiceUnavailable, rec.Code)
				assert.Equal(t, "2", rec.Header().Get("Retry-After"))

				var body apierror.Error
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, http.StatusServiceUnavailable, body.Code)
				assert.Equal(t, "authentication backend unavailable", body.Message)
			})
		}

		t.Run(name+"/denial", func(t *testing.T) {
			rec, called := runAgentKeyMiddleware(t, build, apierror.Unauthorized("invalid API key"))
			assert.Empty(t, rec.Header().Get("Retry-After"))
			if name == "OptionalAuth" {
				// A rejected key on an optional route passes through anonymously, as before.
				assert.True(t, called)
				assert.Equal(t, http.StatusOK, rec.Code)
				return
			}
			assert.False(t, called)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			var body apierror.Error
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, http.StatusUnauthorized, body.Code)
			assert.Equal(t, "Invalid agent API key", body.Message)
		})
	}
}

func runAgentKeyMiddleware(t *testing.T, build func(*mockAgentService) echo.MiddlewareFunc, authErr error) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	svc := &mockAgentService{
		AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
			return nil, authErr
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	req.Header.Set("X-Agent-Key", "agk_my-workspace_abcdefghijklmnopqrstuvwxyz123456")
	rec := httptest.NewRecorder()
	c := newEchoContext(req, rec)

	var called bool
	err := build(svc)(func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusOK)
	})(c)
	require.NoError(t, err)
	return rec, called
}

// The backend error text may quote the key or its stored prefix (a driver
// echoing query arguments); neither may reach the log, nor anything else
// parsed from the key such as the workspace slug.
func TestAgentKeyMiddlewares_BackendErrorLogOmitsKey(t *testing.T) {
	const key = "agk_my-workspace_abcdefghijklmnopqrstuvwxyz123456"
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	leaky := fmt.Errorf("query prefix=abcdefgh key=%q: conn refused", key)
	rec, _ := runAgentKeyMiddleware(t, func(s *mockAgentService) echo.MiddlewareFunc { return AgentKeyAuth(s, nil) }, leaky)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	logged := logBuf.String()
	assert.Contains(t, logged, "conn refused")
	assert.NotContains(t, logged, key)
	assert.NotContains(t, logged, "my-workspace")
	assert.NotContains(t, logged, "abcdefgh")
	assert.NotContains(t, logged, "ijklmnop")

	// An error that does not quote the key leaves nothing of it in the log
	// either: the slug is not logged as a field of its own.
	logBuf.Reset()
	rec, _ = runAgentKeyMiddleware(t, func(s *mockAgentService) echo.MiddlewareFunc { return AgentKeyAuth(s, nil) }, errors.New("conn refused"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, logBuf.String(), "conn refused")
	assert.NotContains(t, logBuf.String(), "my-workspace")
}

// The request id stands in for the slug: it ties the log line to the access log.
func TestAuthBackendUnavailableJSON_LogsRequestID(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	rec := httptest.NewRecorder()
	c := newEchoContext(httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody), rec)
	c.Response().Header().Set(echo.HeaderXRequestID, "req-123")

	require.NoError(t, authBackendUnavailableJSON(c, "AgentKeyAuth", "my-workspace", "agk_my-workspace_abcdefghXYZ", errors.New("conn refused")))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, logBuf.String(), "request_id=req-123")
	assert.NotContains(t, logBuf.String(), "my-workspace")
}

func TestRedactAgentKey(t *testing.T) {
	const slug = "my-workspace"
	cases := map[string]struct{ msg, key, want string }{
		"full key":            {"bad key agk_my-workspace_abcdefghXYZ", "agk_my-workspace_abcdefghXYZ", "bad key [redacted]"},
		"full key quoted":     {`bad key "agk_my-workspace_abcdefghXYZ"`, "agk_my-workspace_abcdefghXYZ", `bad key "[redacted]"`},
		"secret only":         {"secret abcdefghXYZ", "agk_my-workspace_abcdefghXYZ", "secret [redacted]"},
		"prefix only":         {"prefix=abcdefgh", "agk_my-workspace_abcdefghXYZ", "prefix=[redacted]"},
		"short secret":        {"key ab missing", "agk_my-workspace_ab", "key [redacted] missing"},
		"empty secret":        {"conn refused", "agk_my-workspace_", "conn refused"},
		"empty secret quoted": {"bad key agk_my-workspace_", "agk_my-workspace_", "bad key [redacted]"},
		"slug only":           {"workspace my-workspace missing", "agk_my-workspace_abcdefghXYZ", "workspace [redacted] missing"},
		"no key in errors":    {"conn refused", "agk_my-workspace_abcdefghXYZ", "conn refused"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, redactAgentKey(tc.msg, slug, tc.key))
		})
	}
}

func TestRedactAgentKey_QuotedMaterial(t *testing.T) {
	cases := map[string]struct{ slug, secret string }{
		"tab in slug":          {"team\twest", "abcdefghXYZ"},
		"newline in slug":      {"team\nwest", "abcdefghXYZ"},
		"quote in slug":        {"team\"west", "abcdefghXYZ"},
		"backslash in slug":    {`team\west`, "abcdefghXYZ"},
		"unicode slug":         {"équipe", "abcdefghXYZ"},
		"tab in secret":        {"my-workspace", "a\tbcdefgXYZ"},
		"newline in secret":    {"my-workspace", "a\nbcdefgXYZ"},
		"quote in secret":      {"my-workspace", "a\"bcdefgXYZ"},
		"backslash in secret":  {"my-workspace", `a\bcdefgXYZ`},
		"escaped empty secret": {"team\twest", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			key := "agk_" + tc.slug + "_" + tc.secret
			slug, err := parseWorkspaceSlugFromKey(key)
			require.NoError(t, err)
			require.Equal(t, tc.slug, slug)
			values := map[string]string{"key": key, "slug": slug}
			if tc.secret != "" {
				values["secret"] = tc.secret
				values["prefix"] = tc.secret[:redactKeyPrefixLen]
			}
			for kind, value := range values {
				for _, format := range []string{"%q", "%+q"} {
					t.Run(kind+format, func(t *testing.T) {
						msg := "query value=" + fmt.Sprintf(format, value) + ": conn refused"
						assert.Equal(t, `query value="[redacted]": conn refused`, redactAgentKey(msg, slug, key))
					})
				}
			}
		})
	}
}
