package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
)

// The stub is only useful if the API's own Keep client accepts it: this
// drives it with secretstore.HTTPKeepClient, the code the API runs.
func newStub(t *testing.T, secret string) (*httptest.Server, *http.Client) {
	t.Helper()
	pemPath := filepath.Join(t.TempDir(), "stub.pem")
	cert, err := selfSigned(pemPath)
	require.NoError(t, err)
	s := &store{secret: []byte(secret), values: map[string]string{}, version: map[string]int{}, nonces: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /orgs/{org_id}/mesh/secrets/{ref}", s.put)
	mux.HandleFunc("POST /orgs/{org_id}/mesh/secrets/{ref}/resolve", s.resolve)
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return srv, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}

func TestKeepStub_PutThenResolveThroughTheAPIClient(t *testing.T) {
	srv, hc := newStub(t, "events-secret")
	c := &secretstore.HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("events-secret"), HTTP: hc}
	ref := secretstore.Ref{WorkspaceID: uuid.New(), ExtensionID: "mesh-secret", Field: "workspace.workspace.TOKEN"}

	id, err := c.PutValue(context.Background(), ref, "first")
	require.NoError(t, err)
	got, err := c.GetValue(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "first", got)

	_, err = c.PutValue(context.Background(), ref, "second")
	require.NoError(t, err)
	got, err = c.GetValue(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "second", got)

	_, err = c.GetValue(context.Background(), "never-written")
	assert.ErrorIs(t, err, secretstore.ErrKeepNotFound)
}

func TestKeepStub_RefusesAWrongSignature(t *testing.T) {
	srv, hc := newStub(t, "events-secret")
	c := &secretstore.HTTPKeepClient{BaseURL: srv.URL, OrgID: uuid.New(), WorkspaceID: uuid.New(), EventsSecret: []byte("other-secret"), HTTP: hc}
	_, err := c.PutValue(context.Background(), secretstore.Ref{ExtensionID: "mesh-secret", Field: "x"}, "v")
	assert.ErrorIs(t, err, secretstore.ErrKeepUnauthorized)
}
