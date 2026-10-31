package uictx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/examples/external-extension/refext"
)

// Contract test between the iframe context token Mesh issues and the reference
// extension's verifier (examples/external-extension), written from
// docs/extensions/ui-iframe.md only: the JWKS at its documented path, ES256,
// iss, aud, exp and the claims a screen reads.
// Run all of them with: go test ./internal/... -run RefExt

func TestContract_RefExt_ContextToken(t *testing.T) {
	useKey(t)
	var svc *Service
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != refext.JWKSPath {
			http.NotFound(w, r)
			return
		}
		doc, err := svc.JWKS(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/jwk-set+json")
		_, _ = w.Write(doc)
	}))
	t.Cleanup(srv.Close)
	svc = New(&memStore{}, srv.URL)
	ctx := context.Background()

	user, ws, proj, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tok, _, err := svc.Issue(ctx, Claims{
		UserID: user, Email: "a@example.com", WorkspaceID: ws, ProjectID: &proj, TaskID: &task,
		Roles: []string{"admin"}, Audience: "reference-ext",
	})
	require.NoError(t, err)

	v := &refext.ContextVerifier{MeshOrigin: srv.URL, ExtensionID: "reference-ext"}
	got, err := v.Verify(ctx, tok)
	require.NoError(t, err)
	assert.Equal(t, &refext.Context{
		UserID: user.String(), Email: "a@example.com", WorkspaceID: ws.String(),
		ProjectID: proj.String(), TaskID: task.String(), Roles: []string{"admin"},
	}, got)

	// Red controls: the same verifier refuses a token for another extension, one
	// from another issuer, and an expired one.
	other := &refext.ContextVerifier{MeshOrigin: srv.URL, ExtensionID: "someone-else"}
	_, err = other.Verify(ctx, tok)
	assert.Error(t, err, "aud is another extension")

	foreign := &refext.ContextVerifier{MeshOrigin: srv.URL + "/", ExtensionID: "reference-ext"}
	_, err = foreign.Verify(ctx, tok)
	assert.Error(t, err, "iss is not this verifier's Mesh")

	svc.now = func() time.Time { return time.Now().Add(-2 * TokenTTL) }
	old, _, err := svc.Issue(ctx, Claims{UserID: user, WorkspaceID: ws, Audience: "reference-ext"})
	require.NoError(t, err)
	_, err = v.Verify(ctx, old)
	assert.Error(t, err, "expired")
}
