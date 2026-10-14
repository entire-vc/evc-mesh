package eventbus

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtensionSubject(t *testing.T) {
	conn := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	got := ExtensionSubject("acme", conn, "task.updated")
	assert.Equal(t, "events.acme._ext.11111111-2222-3333-4444-555555555555.task.updated", got)
	// A project subject is events.{ws}.{project}.{type}; a project slug cannot
	// start with "_", so the two can never be the same subject.
	assert.NotEqual(t, BuildSubject("acme", "_ext", "task.updated"), got)
}

// mock: external boundary — NATS JetStream publish.
type stubJetStream struct {
	jetstream.JetStream
	subject string
	err     error
}

func (s *stubJetStream) Publish(_ context.Context, subject string, _ []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	s.subject = subject
	if s.err != nil {
		return nil, s.err
	}
	return &jetstream.PubAck{}, nil
}

func TestPublishExtensionEvent(t *testing.T) {
	js := &stubJetStream{}
	eb := &EventBus{js: js}
	require.NoError(t, eb.PublishExtensionEvent(context.Background(), "events.acme._ext.c.task.updated", []byte(`{}`), "m1"))
	assert.Equal(t, "events.acme._ext.c.task.updated", js.subject)

	js.err = errors.New("no stream")
	err := eb.PublishExtensionEvent(context.Background(), "events.acme._ext.c.task.updated", []byte(`{}`), "m2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events.acme._ext.c.task.updated")
}
