package oautherror

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConstructors_CodeAndStatus(t *testing.T) {
	cases := []struct {
		name   string
		e      *Error
		code   string
		status int
	}{
		{"InvalidRequest", InvalidRequest("d"), "invalid_request", http.StatusBadRequest},
		{"InvalidClient", InvalidClient("d"), "invalid_client", http.StatusUnauthorized},
		{"InvalidGrant", InvalidGrant("d"), "invalid_grant", http.StatusBadRequest},
		{"UnauthorizedClient", UnauthorizedClient("d"), "unauthorized_client", http.StatusBadRequest},
		{"UnsupportedGrantType", UnsupportedGrantType("d"), "unsupported_grant_type", http.StatusBadRequest},
		{"UnsupportedResponseType", UnsupportedResponseType("d"), "unsupported_response_type", http.StatusBadRequest},
		{"InvalidScope", InvalidScope("d"), "invalid_scope", http.StatusBadRequest},
		{"AccessDenied", AccessDenied("d"), "access_denied", http.StatusBadRequest},
		{"InvalidClientMetadata", InvalidClientMetadata("d"), "invalid_client_metadata", http.StatusBadRequest},
		{"ServerError", ServerError("d"), "server_error", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, tc.e.Code)
			assert.Equal(t, tc.status, tc.e.Status)
			assert.Equal(t, "d", tc.e.Description)
			assert.Equal(t, tc.code == "server_error", tc.e.IsServerError())
		})
	}
}

func TestError_ErrorString(t *testing.T) {
	assert.Equal(t, "invalid_grant: code expired", InvalidGrant("code expired").Error())
	assert.Equal(t, "invalid_grant", InvalidGrant("").Error(), "no trailing \": \" when there is no description")
	var err error = InvalidScope("x")
	assert.EqualError(t, err, "invalid_scope: x", "*Error satisfies the error interface")
}

func TestBody_RFC6749Shape(t *testing.T) {
	assert.Equal(t,
		map[string]string{"error": "invalid_grant", "error_description": "code expired"},
		InvalidGrant("code expired").Body())

	b := InvalidRequest("").Body()
	assert.Equal(t, map[string]string{"error": "invalid_request"}, b)
	_, has := b["error_description"]
	assert.False(t, has, "an empty description must be omitted, not sent as \"\"")
}

// A server_error's Description is a raw internal error (SQL text, dial
// errors) — Body must never carry it to the caller.
func TestBody_ServerErrorDescriptionIsRedacted(t *testing.T) {
	internal := `pq: relation "oauth_tokens" does not exist (dial tcp 10.0.0.5:5432)`
	e := ServerError(internal)
	b := e.Body()
	assert.Equal(t, "server_error", b["error"])
	assert.Equal(t, "internal server error", b["error_description"])
	for k, v := range b {
		assert.NotContains(t, v, "pq:", "field %s leaks the internal description", k)
		assert.NotContains(t, v, "10.0.0.5", "field %s leaks the internal description", k)
	}
	assert.Equal(t, internal, e.Description, "the internal description stays available for server-side logging")
	assert.Contains(t, e.Error(), internal)

	// Redaction applies even with no description at all.
	assert.Equal(t, "internal server error", ServerError("").Body()["error_description"])
	// And only to server_error: a client error keeps its own description.
	assert.Equal(t, "bad verifier", InvalidGrant("bad verifier").Body()["error_description"])
}
