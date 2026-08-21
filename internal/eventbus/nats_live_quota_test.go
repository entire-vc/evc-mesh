package eventbus

import (
	"bytes"
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// TestEnsureStream_LiveInsufficientStorage exercises ensureStream() against
// a REAL nats-server whose JetStream storage is deliberately quota-limited,
// instead of a scripted streamCreator — this is what the task's AC actually
// asks to be proven ("a real log line", not just a passing unit test).
//
// It is opt-in (skipped unless EVENTBUS_LIVE_QUOTA_TEST_NATS_URL is set)
// because it needs a purpose-built nats-server, not the unlimited-storage
// one `make ci`/docker-compose.yml starts for the rest of the suite. To run
// it locally:
//
//	cat > /tmp/nats-quota.conf <<'EOF'
//	port: 4222
//	http_port: 8222
//	jetstream { store_dir: "/data", max_file_store: 1MB }
//	EOF
//	docker run -d --name nats-quota-test -p 14222:4222 -p 18222:8222 \
//	  -v /tmp/nats-quota.conf:/nats.conf:ro nats:2-alpine -js -c /nats.conf
//	EVENTBUS_LIVE_QUOTA_TEST_NATS_URL=nats://localhost:14222 \
//	EVENTBUS_LIVE_QUOTA_TEST_MONITOR_URL=http://localhost:18222 \
//	  go test ./internal/eventbus/... -run TestEnsureStream_LiveInsufficientStorage -v
func TestEnsureStream_LiveInsufficientStorage(t *testing.T) {
	natsURL := os.Getenv("EVENTBUS_LIVE_QUOTA_TEST_NATS_URL")
	monitorURL := os.Getenv("EVENTBUS_LIVE_QUOTA_TEST_MONITOR_URL")
	if natsURL == "" || monitorURL == "" {
		t.Skip("EVENTBUS_LIVE_QUOTA_TEST_NATS_URL / EVENTBUS_LIVE_QUOTA_TEST_MONITOR_URL not set — skipping live quota test (see doc comment for how to run it)")
	}

	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	defer nc.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Clean slate: this nats-server is purpose-built for this test and has
	// a tiny quota, so a stream left over from a previous run would eat
	// into it.
	_ = js.DeleteStream(ctx, StreamName)
	_ = js.DeleteStream(ctx, "QUOTA_FILLER")

	t.Run("positive: bus comes up on a host with far less storage than requested", func(t *testing.T) {
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		cfg := testCfg(monitorURL, 10*1024*1024*1024) // ask for 10 GB on a 1MB host
		cfg.NATSReplicas = 1

		stream, err := ensureStream(ctx, js, cfg)
		require.NoError(t, err, "the event bus must still come up on a host with max_storage < 10 GiB")
		require.NotNil(t, stream)
		t.Cleanup(func() { _ = js.DeleteStream(ctx, StreamName) })

		logs := buf.String()
		t.Logf("captured log output:\n%s", logs)
		require.Contains(t, logs, "[eventbus] Stream MESH_EVENTS ensured", "AC1: required log line")
	})

	t.Run("negative: a genuinely unreachable size produces a clear error, not the raw NATS error", func(t *testing.T) {
		// Fill most of the 1MB quota with an unrelated stream first, so that
		// even the "real limit" jsz reports (the static max_file_store
		// config, not remaining free space) is NOT actually available to
		// MESH_EVENTS — the retry itself must fail too.
		_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:      "QUOTA_FILLER",
			Subjects:  []string{"quota-filler.>"},
			Storage:   jetstream.FileStorage,
			MaxBytes:  900 * 1024,
			Retention: jetstream.LimitsPolicy,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = js.DeleteStream(ctx, "QUOTA_FILLER") })

		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		cfg := testCfg(monitorURL, 10*1024*1024*1024)
		cfg.NATSReplicas = 1

		stream, err := ensureStream(ctx, js, cfg)
		t.Logf("captured log output:\n%s", buf.String())
		require.Error(t, err, "a genuinely unreachable size must fail, not silently disable the bus")
		require.Nil(t, stream)

		msg := err.Error()
		t.Logf("final error: %s", msg)
		require.Contains(t, msg, "10737418240", "must name the requested size")
		require.NotEqual(t, "insufficient storage resources available", msg, "must not be JUST the raw opaque NATS error")
	})
}
