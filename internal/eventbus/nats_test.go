package eventbus

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insufficientStorageErr builds the exact error CreateOrUpdateStream returns
// on a real nats-server when a stream's MaxBytes exceeds what the host can
// give it — verified live against nats-server 2.14.2 (max_file_store=1MB,
// MaxBytes=100MB request): code=500 err_code=10047
// description="insufficient storage resources available".
func insufficientStorageErr() error {
	return &jetstream.APIError{
		Code:        500,
		ErrorCode:   jsErrCodeInsufficientResources,
		Description: "insufficient storage resources available",
	}
}

// captureLog redirects the standard logger to a buffer for the duration of
// the test and restores the previous writer on cleanup.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// --- streamCreator stubs (mock: external boundary — NATS JetStream server) ---

// scriptedCreator returns errs[i] (or nil, meaning success) on the i-th call
// and records every cfg it was called with.
type scriptedCreator struct {
	errs  []error
	calls int
	cfgs  []jetstream.StreamConfig
}

func (s *scriptedCreator) CreateOrUpdateStream(_ context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	s.cfgs = append(s.cfgs, cfg)
	idx := s.calls
	s.calls++
	if idx < len(s.errs) && s.errs[idx] != nil {
		return nil, s.errs[idx]
	}
	return nil, nil
}

func testCfg(monitorURL string, maxBytes int64) EventBusConfig {
	cfg := EventBusConfig{
		NATSMonitorURL: monitorURL,
		NATSReplicas:   1,
		StreamMaxAge:   30 * 24 * time.Hour,
		StreamMaxBytes: maxBytes,
		MaxMsgSize:     256 * 1024,
	}
	return cfg
}

// jszServer starts an httptest server that answers GET /jsz?config=1 with
// the given max_storage figure, mirroring the real nats-server monitoring
// endpoint response shape (verified live).
func jszServer(t *testing.T, maxStorage int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/jsz", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config":{"max_storage":` + itoa(maxStorage) + `}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestEnsureStream_SucceedsFirstTry(t *testing.T) {
	buf := captureLog(t)
	creator := &scriptedCreator{errs: []error{nil}}

	stream, err := ensureStream(context.Background(), creator, testCfg("", 10*1024*1024*1024))

	require.NoError(t, err)
	assert.Nil(t, stream) // scriptedCreator never returns a real Stream
	assert.Equal(t, 1, creator.calls)
	assert.Contains(t, buf.String(), "[eventbus] Stream MESH_EVENTS ensured")
}

func TestEnsureStream_NonStorageErrorPassesThroughWithoutRetry(t *testing.T) {
	creator := &scriptedCreator{errs: []error{errors.New("connection refused")}}

	stream, err := ensureStream(context.Background(), creator, testCfg("", 10*1024*1024*1024))

	require.Error(t, err)
	assert.Nil(t, stream)
	assert.Equal(t, 1, creator.calls, "a non-storage error must not trigger a jsz-discovery retry")
	assert.Contains(t, err.Error(), "failed to create/update stream MESH_EVENTS")
	assert.Contains(t, err.Error(), "connection refused")
}

// AC1 positive scenario: the host's real storage (reported via jsz) is
// smaller than the configured request but large enough for a retry to
// succeed — the bus must still come up, and the required log line must
// appear.
func TestEnsureStream_InsufficientStorage_RetrySucceedsWithDiscoveredLimit(t *testing.T) {
	buf := captureLog(t)
	srv := jszServer(t, 1_000_000) // host really has ~1MB

	creator := &scriptedCreator{errs: []error{insufficientStorageErr(), nil}}
	requested := int64(10 * 1024 * 1024 * 1024) // 10 GB, way more than the host has

	stream, err := ensureStream(context.Background(), creator, testCfg(srv.URL, requested))

	require.NoError(t, err)
	assert.Nil(t, stream)
	require.Equal(t, 2, creator.calls, "must retry exactly once with the discovered limit")
	assert.Equal(t, int64(1_000_000), creator.cfgs[1].MaxBytes, "retry must use the host's real reported limit, not a guess")
	assert.Equal(t, requested, creator.cfgs[0].MaxBytes, "first attempt must still use the originally configured request")

	logs := buf.String()
	assert.Contains(t, logs, "[eventbus] Stream MESH_EVENTS ensured", "AC1: the ensured log line must appear even though the bus needed a retry")
	assert.Contains(t, logs, "rejected by NATS as insufficient storage resources")
	assert.Contains(t, logs, "reduced max_bytes=1000000")
}

// AC2 negative control: jsz's own reported limit is degenerate relative to
// the request — either "unlimited" (<=0, which should not happen right
// after a storage rejection, but must not be trusted blindly) or already
// >= what was just rejected (retrying with it would just reproduce the same
// failure). Either way there is nothing sane to retry with, so ensureStream
// must fail immediately with a clear message naming both numbers — not the
// raw NATS error — and must never call CreateOrUpdateStream a second time.
func TestEnsureStream_InsufficientStorage_DiscoveredLimitNotUsable(t *testing.T) {
	requested := int64(10 * 1024 * 1024 * 1024)

	tests := []struct {
		name      string
		available int64
	}{
		{"jsz reports zero", 0},
		{"jsz reports unlimited (-1)", -1},
		{"jsz reports the same size that was just rejected", requested},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := jszServer(t, tt.available)
			creator := &scriptedCreator{errs: []error{insufficientStorageErr()}}

			stream, err := ensureStream(context.Background(), creator, testCfg(srv.URL, requested))

			require.Error(t, err)
			assert.Nil(t, stream)
			assert.Equal(t, 1, creator.calls, "must not retry when the discovered limit can't possibly help")

			msg := err.Error()
			assert.Contains(t, msg, "requested 10737418240 bytes")
			assert.NotContains(t, msg, "insufficient storage resources available", "must not leak the raw opaque NATS description as the whole story")
		})
	}
}

// AC1/AC2 boundary: a discovered limit that IS smaller than the request
// (however small) is still worth one retry — ensureStream must not give up
// just because the number looks tiny.
func TestEnsureStream_InsufficientStorage_TinyDiscoveredLimitIsStillRetried(t *testing.T) {
	srv := jszServer(t, 500) // host really only has 500 bytes free
	creator := &scriptedCreator{errs: []error{insufficientStorageErr(), nil}}
	requested := int64(10 * 1024 * 1024 * 1024)

	stream, err := ensureStream(context.Background(), creator, testCfg(srv.URL, requested))

	require.NoError(t, err)
	assert.Nil(t, stream)
	require.Equal(t, 2, creator.calls)
	assert.Equal(t, int64(500), creator.cfgs[1].MaxBytes)
}

// AC2 negative control, second shape: jsz itself is unavailable (unset
// NATS_MONITOR_URL) — must still fail with an actionable message, never the
// raw NATS error and never a silent bus disablement (a non-nil error, not a
// degraded/partial return).
func TestEnsureStream_InsufficientStorage_JSZUnavailable(t *testing.T) {
	creator := &scriptedCreator{errs: []error{insufficientStorageErr()}}

	stream, err := ensureStream(context.Background(), creator, testCfg("", 10*1024*1024*1024))

	require.Error(t, err)
	assert.Nil(t, stream)
	assert.Equal(t, 1, creator.calls)
	assert.Contains(t, err.Error(), "real limit could not be determined")
	assert.Contains(t, err.Error(), "NATS_MONITOR_URL is not configured")
}

// If the retry itself still fails (the discovered limit turns out to be
// stale/optimistic), the final error must still name both the originally
// requested and the discovered-available figures rather than surfacing the
// second raw NATS error on its own.
func TestEnsureStream_InsufficientStorage_RetryStillFails(t *testing.T) {
	srv := jszServer(t, 2_000_000)
	creator := &scriptedCreator{errs: []error{insufficientStorageErr(), insufficientStorageErr()}}
	requested := int64(10 * 1024 * 1024 * 1024)

	_, err := ensureStream(context.Background(), creator, testCfg(srv.URL, requested))

	require.Error(t, err)
	assert.Equal(t, 2, creator.calls)
	assert.Contains(t, err.Error(), "requested 10737418240 bytes")
	assert.Contains(t, err.Error(), "available 2000000 bytes")
	assert.Contains(t, err.Error(), "still rejected it as insufficient storage")
}

func TestIsInsufficientStorageErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"typed API error, matching code", insufficientStorageErr(), true},
		{"typed API error, different code", &jetstream.APIError{Code: 400, ErrorCode: 10003, Description: "bad request"}, false},
		{"plain error, matching text, different case", errors.New("Insufficient Storage Resources available on disk"), true},
		{"plain error, unrelated", errors.New("connection refused"), false},
		{"wrapped typed API error", errWrap{insufficientStorageErr()}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isInsufficientStorageErr(tt.err))
		})
	}
}

// errWrap wraps an error the way fmt.Errorf("...: %w", err) would, without
// pulling in fmt for a one-line test helper.
type errWrap struct{ err error }

func (e errWrap) Error() string { return "wrapped: " + e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }

func TestDiscoverMaxStorage(t *testing.T) {
	t.Run("empty monitor URL", func(t *testing.T) {
		_, err := discoverMaxStorage(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NATS_MONITOR_URL is not configured")
	})

	t.Run("happy path", func(t *testing.T) {
		srv := jszServer(t, 1048576)
		got, err := discoverMaxStorage(context.Background(), srv.URL)
		require.NoError(t, err)
		assert.Equal(t, int64(1048576), got)
	})

	t.Run("trailing slash on monitor URL is tolerated", func(t *testing.T) {
		srv := jszServer(t, 42)
		got, err := discoverMaxStorage(context.Background(), srv.URL+"/")
		require.NoError(t, err)
		assert.Equal(t, int64(42), got)
	})

	t.Run("non-200 response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := discoverMaxStorage(context.Background(), srv.URL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 500")
	})

	t.Run("malformed JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()

		_, err := discoverMaxStorage(context.Background(), srv.URL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode jsz response")
	})

	t.Run("unreachable host", func(t *testing.T) {
		srv := httptest.NewServer(nil)
		monitorURL := srv.URL
		srv.Close() // closed before use: connection refused

		_, err := discoverMaxStorage(context.Background(), monitorURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "query jsz at")
	})
}

func TestBuildStreamConfig(t *testing.T) {
	cfg := EventBusConfig{
		NATSReplicas:   3,
		StreamMaxAge:   time.Hour,
		StreamMaxBytes: 123,
		MaxMsgSize:     456,
	}

	sc := buildStreamConfig(cfg)

	assert.Equal(t, StreamName, sc.Name)
	assert.Equal(t, []string{SubjectWildcard}, sc.Subjects)
	assert.Equal(t, jetstream.FileStorage, sc.Storage)
	assert.Equal(t, jetstream.LimitsPolicy, sc.Retention)
	assert.Equal(t, time.Hour, sc.MaxAge)
	assert.Equal(t, int64(123), sc.MaxBytes)
	assert.Equal(t, int32(456), sc.MaxMsgSize)
	assert.Equal(t, 3, sc.Replicas)
}
