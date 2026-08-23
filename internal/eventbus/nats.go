package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	// StreamName is the JetStream stream for mesh events.
	StreamName = "MESH_EVENTS"

	// SubjectPrefix is the prefix for all event subjects.
	SubjectPrefix = "events"

	// SubjectWildcard matches all event subjects.
	SubjectWildcard = "events.>"

	// PGWriterConsumer is the durable consumer name for the PG writer.
	PGWriterConsumer = "pg-writer"

	// jsErrCodeInsufficientResources is the JetStream API error code a
	// nats-server returns when a stream's requested limits (MaxBytes in
	// particular) cannot be reserved against the account/server's actual
	// storage. nats.go v1.53.1 does not export this as a named constant
	// (checked: internal/eventbus has no other source for it), so it's
	// pinned here from a live measurement: nats-server 2.14.2, JetStream
	// configured with max_file_store=1MB, CreateOrUpdateStream with
	// MaxBytes=100MB returned code=500 err_code=10047
	// description="insufficient storage resources available".
	jsErrCodeInsufficientResources jetstream.ErrorCode = 10047

	// jszRequestTimeout bounds the one-off HTTP call to the NATS monitoring
	// endpoint used to discover the real storage limit after a failed stream
	// creation. This only runs on the (already slow) error path, and a hung
	// monitor endpoint must not hang server startup.
	jszRequestTimeout = 5 * time.Second
)

// streamCreator is the minimal slice of jetstream.JetStream that ensureStream
// needs. Defined locally instead of depending on the full jetstream.JetStream
// interface (which also carries KV/ObjectStore/consumer management methods
// ensureStream never touches) so that a test can inject a stub that fails in
// controlled ways without reimplementing the rest of the client.
// mock: external boundary — NATS JetStream server (§1o Mocking convention:
// this stubs a third-party network client, not our own DB/service code).
type streamCreator interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error)
}

// BuildSubject constructs a NATS subject from workspace slug, project slug, and event type.
// Format: events.{workspace_slug}.{project_slug}.{event_type}
func BuildSubject(workspaceSlug, projectSlug, eventType string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectPrefix, workspaceSlug, projectSlug, eventType)
}

// buildStreamConfig translates an EventBusConfig into the JetStream stream
// config ensureStream asks the server for.
func buildStreamConfig(cfg EventBusConfig) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:              StreamName,
		Subjects:          []string{SubjectWildcard},
		Storage:           jetstream.FileStorage,
		Retention:         jetstream.LimitsPolicy,
		MaxAge:            cfg.StreamMaxAge,
		MaxBytes:          cfg.StreamMaxBytes,
		MaxMsgSize:        cfg.MaxMsgSize,
		Discard:           jetstream.DiscardOld,
		Replicas:          cfg.NATSReplicas,
		Duplicates:        2 * time.Minute, // dedup window
		MaxMsgsPerSubject: -1,
		MaxMsgs:           -1,
	}
}

// ensureStream creates or updates the MESH_EVENTS JetStream stream.
//
// On a host whose real JetStream storage is smaller than cfg.StreamMaxBytes,
// the server rejects the request with a JetStream API error
// (err_code=10047, "insufficient storage resources available") rather than
// failing to boot. Previously that error propagated straight up and
// New() (and the whole event bus) refused to start.
//
// Instead, on that specific error, ensureStream queries the server's REAL
// configured limit and retries once with that value. The real limit is
// fetched from the NATS *monitoring* HTTP endpoint (GET {NATSMonitorURL}
// /jsz?config=1), NOT from jetstream.JetStream.AccountInfo() — verified live
// (nats-server 2.14.2, max_file_store=1MB, no explicit `accounts {}` block,
// which is how every nats-server in this repo's compose files runs):
// AccountInfo().Limits.MaxStore reported -1 ("unlimited") the whole time,
// while GET /jsz?config=1 correctly reported config.max_storage=1048576.
// AccountInfo() only carries a real number in a multi-tenant deployment with
// explicit per-account limits configured, which this project does not use.
//
// If the real limit still can't accommodate the request (or can't be
// determined at all — NATSMonitorURL unset, monitor unreachable, retry still
// fails), ensureStream returns a single actionable error naming both the
// requested and (when known) the available byte counts — never the raw
// opaque "insufficient storage resources" NATS error, and never a silent
// bus disablement. It never returns a partially-broken stream: same as
// before, only (non-nil, nil) or (nil, err).
func ensureStream(ctx context.Context, js streamCreator, cfg EventBusConfig) (jetstream.Stream, error) {
	streamCfg := buildStreamConfig(cfg)

	stream, err := js.CreateOrUpdateStream(ctx, streamCfg)
	if err == nil {
		log.Printf("[eventbus] Stream %s ensured (max_age=%s, max_bytes=%d, replicas=%d)",
			StreamName, cfg.StreamMaxAge, cfg.StreamMaxBytes, cfg.NATSReplicas)
		return stream, nil
	}

	if !isInsufficientStorageErr(err) {
		return nil, fmt.Errorf("failed to create/update stream %s: %w", StreamName, err)
	}

	log.Printf("[eventbus] Stream %s: requested max_bytes=%d rejected by NATS as insufficient storage resources; querying the host's real limit",
		StreamName, cfg.StreamMaxBytes)

	available, jszErr := discoverMaxStorage(ctx, cfg.NATSMonitorURL)
	if jszErr != nil {
		return nil, fmt.Errorf(
			"eventbus: stream %s requires max_bytes=%d but NATS reports insufficient storage resources, and the host's real limit could not be determined to retry with (%w)",
			StreamName, cfg.StreamMaxBytes, jszErr,
		)
	}

	// available<=0 covers both a genuine zero-quota host and a jsz response
	// that (for whatever reason) reports "unlimited" (-1) even though the
	// create call we just issued was rejected for storage — either way there
	// is no usable positive number to retry with.
	if available <= 0 || available >= cfg.StreamMaxBytes {
		return nil, fmt.Errorf(
			"eventbus: stream %s: requested %d bytes of storage, but only %d bytes are available on this NATS host — reduce NATS_STREAM_MAX_BYTES or increase the host's JetStream storage quota",
			StreamName, cfg.StreamMaxBytes, available,
		)
	}

	log.Printf("[eventbus] Stream %s: host reports max_bytes=%d available (requested %d) — retrying with the discovered limit",
		StreamName, available, cfg.StreamMaxBytes)

	retryCfg := streamCfg
	retryCfg.MaxBytes = available
	stream, err = js.CreateOrUpdateStream(ctx, retryCfg)
	if err != nil {
		return nil, fmt.Errorf(
			"eventbus: stream %s: requested %d bytes of storage, retried with the host's reported available %d bytes, and NATS still rejected it as insufficient storage — reduce NATS_STREAM_MAX_BYTES or increase the host's JetStream storage quota (%w)",
			StreamName, cfg.StreamMaxBytes, available, err,
		)
	}

	log.Printf("[eventbus] Stream %s ensured with reduced max_bytes=%d (requested %d, max_age=%s, replicas=%d)",
		StreamName, available, cfg.StreamMaxBytes, cfg.StreamMaxAge, cfg.NATSReplicas)

	return stream, nil
}

// isInsufficientStorageErr reports whether err is the JetStream API error
// for "not enough storage to honor this stream's limits" (err_code=10047).
func isInsufficientStorageErr(err error) bool {
	var jsErr jetstream.JetStreamError
	if errors.As(err, &jsErr) {
		if api := jsErr.APIError(); api != nil {
			return api.ErrorCode == jsErrCodeInsufficientResources
		}
	}
	// Fallback for a description that reaches us without surviving as a
	// typed jetstream.JetStreamError (e.g. wrapped by an older client or a
	// differently-versioned server) — match the literal text nats-server
	// sends, verified live against nats-server 2.14.2.
	return strings.Contains(strings.ToLower(err.Error()), "insufficient storage resources")
}

// jszConfigResponse is the subset of the NATS monitoring endpoint's
// GET /jsz?config=1 response body this package reads.
type jszConfigResponse struct {
	Config struct {
		MaxStorage int64 `json:"max_storage"`
	} `json:"config"`
}

// discoverMaxStorage queries the NATS server's monitoring HTTP endpoint for
// its REAL configured JetStream storage limit (config.max_storage from
// GET {monitorURL}/jsz?config=1) — see the ensureStream doc comment for why
// this, and not jetstream.JetStream.AccountInfo(), is the correct source.
func discoverMaxStorage(ctx context.Context, monitorURL string) (int64, error) {
	if strings.TrimSpace(monitorURL) == "" {
		return 0, errors.New("NATS_MONITOR_URL is not configured")
	}

	reqCtx, cancel := context.WithTimeout(ctx, jszRequestTimeout)
	defer cancel()

	url := strings.TrimRight(monitorURL, "/") + "/jsz?config=1"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("build jsz request for %s: %w", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("query jsz at %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("jsz at %s returned HTTP %d", url, resp.StatusCode)
	}

	var payload jszConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode jsz response from %s: %w", url, err)
	}

	return payload.Config.MaxStorage, nil
}
