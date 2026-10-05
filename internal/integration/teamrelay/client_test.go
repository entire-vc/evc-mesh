package teamrelay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// shortenConfirmBackoff drops the probe retry backoff to ~zero for this test so
// negative probes don't sleep real backoff time.
func shortenConfirmBackoff(t *testing.T) {
	t.Helper()
	old := confirmProbeBackoff
	confirmProbeBackoff = time.Millisecond
	t.Cleanup(func() { confirmProbeBackoff = old })
}

func TestTransport_SyncUploadEndpointAndResponse(t *testing.T) {
	var capturedPath string
	var capturedMethod string
	var capturedKey string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path + "?" + r.URL.RawQuery
		capturedKey = r.Header.Get("X-Agent-Key")
		capturedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(syncUploadResponse{
			SyncURL: "wss://relay.example.com/share-id",
			WebURL:  "https://web.example.com/share/path/artifact.md",
			Path:    "path/artifact.md",
		})
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	content := []byte("# hello world")
	publicURL, err := transport(context.Background(), "share-id", "path/artifact.md", content, "text/markdown", "tr_agent_test")

	require.NoError(t, err)
	assert.Equal(t, "https://web.example.com/share/path/artifact.md", publicURL)
	assert.Equal(t, http.MethodPost, capturedMethod)
	assert.True(t, strings.HasPrefix(capturedPath, "/v1/web/shares/share-id/sync-upload?"), "endpoint must be /sync-upload, got %s", capturedPath)
	assert.Contains(t, capturedPath, "path=path%2Fartifact.md")
	assert.NotContains(t, capturedPath, "source=mesh-artifact", "old source param must not be present")
	assert.Equal(t, "tr_agent_test", capturedKey)
	assert.Equal(t, content, capturedBody)
}

func TestTransport_MissingRelayURL(t *testing.T) {
	t.Setenv("MESH_TEAMRELAY_RELAY_URL", "")
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	url, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/markdown", "key")
	assert.NoError(t, err)
	assert.Empty(t, url)
}

func TestTransport_EmptyPathResponse_ReturnsError(t *testing.T) {
	// Simulates TR returning {ok: false} or any response that doesn't populate the
	// syncUploadResponse fields — path will be "" which signals upload failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": false, "error": "share not found"}`))
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	publicURL, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/plain", "tr_agent_test")
	assert.Error(t, err, "expected error when relay returns ok=false body at HTTP 200")
	assert.Empty(t, publicURL)
	assert.Contains(t, err.Error(), "empty path")
}

func TestTransport_UnparsableResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json {{{`))
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	publicURL, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/plain", "tr_agent_test")
	assert.Error(t, err)
	assert.Empty(t, publicURL)
}

func TestTransport_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", srv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	publicURL, err := transport(context.Background(), "share-id", "file.md", []byte("data"), "text/plain", "bad-key")
	assert.NoError(t, err)
	assert.Empty(t, publicURL)
}

// ---------------------------------------------------------------------------
// confirmPublishedWebURL — the anonymous-reachability gate that decides whether
// a relay-returned web URL may be persisted as tr_public_url.
// ---------------------------------------------------------------------------

func TestConfirmPublishedWebURL(t *testing.T) {
	// Negative probes retry with backoff in production; tests don't need the sleep.
	shortenConfirmBackoff(t)

	t.Run("2xx confirms publication", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# doc"))
		}))
		defer srv.Close()

		assert.True(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"))
	})

	t.Run("401 auth wall (private share) does not confirm", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "This share requires authentication.", http.StatusUnauthorized)
		}))
		defer srv.Close()

		assert.False(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"),
			"a URL that 401s to anonymous callers must never be advertised as public")
	})

	t.Run("404 not-yet-synced does not confirm", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		assert.False(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/missing.md"))
	})

	t.Run("5xx does not confirm", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		assert.False(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"))
	})

	t.Run("302 to a login page does not confirm", func(t *testing.T) {
		// Redirect-style auth wall: the anonymous GET bounces to a login page
		// that itself answers 200. With Go's default redirect following the probe
		// would land on that 200 and wrongly confirm — the gate's contract is
		// what an anonymous browser sees on click, and a bounce to a login form
		// is not the published artifact.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("<html>sign in</html>"))
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		}))
		defer srv.Close()

		assert.False(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"),
			"a 302 to a 200 login page must not count as a published URL")
	})

	t.Run("network failure does not confirm", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		url := srv.URL + "/share/doc.md"
		srv.Close() // shut down so the request cannot connect

		assert.False(t, confirmPublishedWebURL(context.Background(), url),
			"fail closed: an unreachable confirm target means publication is not confirmed")
	})

	t.Run("probe carries no credentials", func(t *testing.T) {
		var gotAgentKey, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAgentKey = r.Header.Get("X-Agent-Key")
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		// The probe must look exactly like a browser click: any credential leaking
		// into it would let a private share "confirm" through authenticated eyes.
		require.True(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"))
		assert.Empty(t, gotAgentKey, "confirm probe must not send X-Agent-Key")
		assert.Empty(t, gotAuth, "confirm probe must not send Authorization")
	})

	t.Run("momentary 404 then 200 confirms after retry", func(t *testing.T) {
		// Web-side propagation can lag the sync-upload 2xx: the first probe
		// catches a not-yet-visible file. The bounded retry must recover it —
		// otherwise a genuinely published artifact never gets its URL.
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if hits.Add(1) == 1 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		assert.True(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"),
			"a momentary 404 right after an accepted upload must not permanently unconfirm the URL")
	})

	t.Run("persistent failure stays unconfirmed after exactly the attempt budget", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			http.Error(w, "This share requires authentication.", http.StatusUnauthorized)
		}))
		defer srv.Close()

		assert.False(t, confirmPublishedWebURL(context.Background(), srv.URL+"/share/doc.md"))
		assert.EqualValues(t, confirmProbeAttempts, hits.Load(), "the probe must try exactly its bounded budget, no more")
	})
}

// ---------------------------------------------------------------------------
// Publish — task → integration → transport → confirm probe, wired end to end.
// Repos are faked by embedding the interface: only the methods Publish calls
// are overridden; anything else panics loudly instead of returning zero values.
// ---------------------------------------------------------------------------

type fakeTaskRepo struct {
	repository.TaskRepository
	task *domain.Task
}

func (f *fakeTaskRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
	return f.task, nil
}

type fakePIRepo struct {
	repository.ProjectIntegrationRepository
	pi *domain.ProjectIntegration
}

func (f *fakePIRepo) Get(_ context.Context, _ uuid.UUID, _ string) (*domain.ProjectIntegration, error) {
	return f.pi, nil
}

type fakeProjectRepo struct {
	repository.ProjectRepository
	proj *domain.Project
}

func (f *fakeProjectRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Project, error) {
	return f.proj, nil
}

// newPublishTestClient wires a client whose task resolves to an enabled
// team_relay integration on share 1111…. The relay endpoints themselves are
// pointed at per-test httptest servers via MESH_TEAMRELAY_* env vars.
func newPublishTestClient() *client {
	projectID := uuid.New()
	return NewClient(
		&fakePIRepo{pi: &domain.ProjectIntegration{
			ProjectID: projectID,
			Type:      "team_relay",
			Enabled:   true,
			Settings:  json.RawMessage(`{"share_id":"11111111-1111-1111-1111-111111111111"}`),
			AgentKey:  "tr_agent_key",
		}},
		&fakeTaskRepo{task: &domain.Task{ID: uuid.New(), ProjectID: projectID}},
		&fakeProjectRepo{proj: &domain.Project{ID: projectID, Slug: "proj"}},
	).(*client)
}

func TestPublish_ConfirmedWebURLIsReturned(t *testing.T) {
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# doc"))
	}))
	defer webSrv.Close()
	webURL := webSrv.URL + "/share/doc.md"

	cpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncUploadResponse{
			SyncURL: "wss://relay.example.com/share-id",
			WebURL:  webURL,
			Path:    "proj/task/doc.md",
		})
	}))
	defer cpSrv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", cpSrv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	url, key, err := newPublishTestClient().Publish(context.Background(), uuid.New(), "doc.md", []byte("# doc"), "text/markdown")

	require.NoError(t, err)
	assert.Equal(t, webURL, url, "an anonymously served web URL must be returned as confirmed")
	assert.Equal(t, "tr_agent_key", key)
}

func TestPublish_PrivateShareYieldsNoURL(t *testing.T) {
	// The relay built a web URL, but the share answers 401 to anonymous
	// browsers — the exact case that used to leak an unopenable tr_public_url.
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "This share requires authentication.", http.StatusUnauthorized)
	}))
	defer webSrv.Close()

	cpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncUploadResponse{
			WebURL: webSrv.URL + "/share/doc.md",
			Path:   "proj/task/doc.md",
		})
	}))
	defer cpSrv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", cpSrv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	url, _, err := newPublishTestClient().Publish(context.Background(), uuid.New(), "doc.md", []byte("# doc"), "text/markdown")

	require.NoError(t, err, "upload itself succeeded; only publication is unconfirmed")
	assert.Empty(t, url, "a URL that 401s anonymously must not be returned as published")
}

func TestPublish_TransportErrorPropagates(t *testing.T) {
	cpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "relay broken", http.StatusInternalServerError)
	}))
	defer cpSrv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", cpSrv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	url, _, err := newPublishTestClient().Publish(context.Background(), uuid.New(), "doc.md", []byte("# doc"), "text/markdown")

	require.Error(t, err, "a failed sync-upload must surface as a Publish error so no metadata is written")
	assert.Empty(t, url)
}

func TestPublish_NoWebURLYieldsNone(t *testing.T) {
	// Share resolved by UUID, web publishing off on the relay side: sync-upload
	// succeeds but the control plane returns no web_url — nothing to advertise.
	cpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncUploadResponse{
			SyncURL: "wss://relay.example.com/share-id",
			Path:    "proj/task/doc.md",
		})
	}))
	defer cpSrv.Close()

	t.Setenv("MESH_TEAMRELAY_RELAY_URL", cpSrv.URL)
	t.Setenv("MESH_TEAMRELAY_TRANSPORT_ENABLED", "true")

	url, key, err := newPublishTestClient().Publish(context.Background(), uuid.New(), "doc.md", []byte("# doc"), "text/markdown")

	require.NoError(t, err)
	assert.Empty(t, url, "no web_url from the relay means nothing is publicly openable")
	assert.Equal(t, "tr_agent_key", key)
}
