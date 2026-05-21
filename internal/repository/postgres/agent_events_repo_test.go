//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func newTestAgentEvt(agentID uuid.UUID, eventType string, ttl time.Duration) *domain.AgentEvent {
	now := time.Now().UTC().Truncate(time.Microsecond)
	eid, err := uuid.NewV7()
	if err != nil {
		eid = uuid.New()
	}
	return &domain.AgentEvent{
		EventID:     eid,
		AgentID:     agentID,
		WorkspaceID: uuid.New(),
		EventType:   eventType,
		Payload:     json.RawMessage(`{"test":true}`),
		EmittedAt:   now,
		ExpiresAt:   now.Add(ttl),
	}
}

func TestAgentEventsRepo_CreateAndLookup(t *testing.T) {
	db := testDB(t)
	repo := NewAgentEventsRepo(db)
	ctx := context.Background()

	agentID := uuid.New()
	ev := newTestAgentEvt(agentID, "task.assigned", 24*time.Hour)
	require.NoError(t, repo.Create(ctx, ev))
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agent_events WHERE event_id = $1", ev.EventID)
	})

	got, err := repo.Lookup(ctx, ev.EventID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ev.EventID, got.EventID)
	assert.Equal(t, ev.AgentID, got.AgentID)
	assert.Equal(t, ev.EventType, got.EventType)
}

func TestAgentEventsRepo_LookupExpired_ReturnsNil(t *testing.T) {
	db := testDB(t)
	repo := NewAgentEventsRepo(db)
	ctx := context.Background()

	agentID := uuid.New()
	ev := newTestAgentEvt(agentID, "task.commented", -2*time.Second)
	require.NoError(t, repo.Create(ctx, ev))
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agent_events WHERE event_id = $1", ev.EventID)
	})

	got, err := repo.Lookup(ctx, ev.EventID)
	require.NoError(t, err)
	assert.Nil(t, got, "expired event must return nil")
}

func TestAgentEventsRepo_LookupUnknown_ReturnsNil(t *testing.T) {
	db := testDB(t)
	repo := NewAgentEventsRepo(db)
	got, err := repo.Lookup(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestAgentEventsRepo_ListAfter_OrderedASC(t *testing.T) {
	db := testDB(t)
	repo := NewAgentEventsRepo(db)
	ctx := context.Background()

	agentID := uuid.New()
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		ev := newTestAgentEvt(agentID, "task.assigned", 24*time.Hour)
		require.NoError(t, repo.Create(ctx, ev))
		ids = append(ids, ev.EventID)
		evc := ev.EventID
		t.Cleanup(func() {
			_, _ = db.ExecContext(ctx, "DELETE FROM agent_events WHERE event_id = $1", evc)
		})
		time.Sleep(2 * time.Millisecond)
	}

	events, err := repo.ListAfter(ctx, agentID, ids[0], 100)
	require.NoError(t, err)
	require.Len(t, events, 4)
	for i := 0; i < len(events)-1; i++ {
		assert.True(t, events[i].EventID.String() < events[i+1].EventID.String())
	}
}

func TestAgentEventsRepo_ListAfter_EmptyResult(t *testing.T) {
	db := testDB(t)
	repo := NewAgentEventsRepo(db)
	ctx := context.Background()

	agentID := uuid.New()
	ev := newTestAgentEvt(agentID, "task.created", 24*time.Hour)
	require.NoError(t, repo.Create(ctx, ev))
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agent_events WHERE event_id = $1", ev.EventID)
	})

	events, err := repo.ListAfter(ctx, agentID, ev.EventID, 100)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestAgentEventsRepo_DeleteExpired(t *testing.T) {
	db := testDB(t)
	repo := NewAgentEventsRepo(db)
	ctx := context.Background()

	agentID := uuid.New()
	expired := newTestAgentEvt(agentID, "task.assigned", -2*time.Second)
	live := newTestAgentEvt(agentID, "task.assigned", 24*time.Hour)
	require.NoError(t, repo.Create(ctx, expired))
	require.NoError(t, repo.Create(ctx, live))
	expID, livID := expired.EventID, live.EventID
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM agent_events WHERE event_id IN ($1,$2)", expID, livID)
	})

	deleted, err := repo.DeleteExpired(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1))

	got, err := repo.Lookup(ctx, live.EventID)
	require.NoError(t, err)
	assert.NotNil(t, got)

	gone, err := repo.Lookup(ctx, expired.EventID)
	require.NoError(t, err)
	assert.Nil(t, gone)
}
