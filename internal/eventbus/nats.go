package eventbus

import (
	"context"
	"fmt"
	"log"
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
)

// BuildSubject constructs a NATS subject from workspace slug, project slug, and event type.
// Format: events.{workspace_slug}.{project_slug}.{event_type}
func BuildSubject(workspaceSlug, projectSlug string, eventType string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectPrefix, workspaceSlug, projectSlug, eventType)
}

// ensureStream creates or updates the MESH_EVENTS JetStream stream.
func ensureStream(ctx context.Context, js jetstream.JetStream, cfg EventBusConfig) (jetstream.Stream, error) {
	streamCfg := jetstream.StreamConfig{
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

	stream, err := js.CreateOrUpdateStream(ctx, streamCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create/update stream %s: %w", StreamName, err)
	}

	log.Printf("[eventbus] Stream %s ensured (max_age=%s, max_bytes=%d, replicas=%d)",
		StreamName, cfg.StreamMaxAge, cfg.StreamMaxBytes, cfg.NATSReplicas)

	return stream, nil
}
