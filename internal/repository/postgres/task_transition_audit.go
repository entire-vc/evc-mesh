package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func writeTransitionAudit(ctx context.Context, tx *sqlx.Tx, task *domain.Task, oldVersion, newVersion int64, audit *domain.TransitionAudit) error {
	if len(audit.Entries) == 0 {
		return fmt.Errorf("transition audit requires an action")
	}
	var workspaceID uuid.UUID
	if err := tx.GetContext(ctx, &workspaceID, `SELECT workspace_id FROM projects WHERE id=$1`, task.ProjectID); err != nil {
		return err
	}
	for _, entry := range audit.Entries {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("task:%s:%d:%s", task.ID, newVersion, entry.Action)))
		changes := make(map[string]any, len(entry.Changes)+12)
		for k, v := range entry.Changes {
			changes[k] = v
		}
		changes["event_id"], changes["old_version"], changes["new_version"] = id, oldVersion, newVersion
		if _, ok := changes["source"]; !ok {
			changes["source"] = audit.Source
		}
		changes["reason"] = audit.Reason
		if reason, ok := entry.Changes["reason"]; ok {
			changes["reason"] = reason
		}
		changes["session_id"], changes["correlation_id"], changes["trigger_task_id"] = audit.SessionID, audit.CorrelationID, audit.TriggerTaskID
		changes["lease_generation"], changes["previous_holder"] = audit.LeaseGeneration, audit.PreviousHolder
		data, err := json.Marshal(changes)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO activity_log(id,workspace_id,entity_type,entity_id,action,actor_id,actor_type,changes,created_at,
   event_id,source,reason,session_id,correlation_id,old_version,new_version,lease_generation,previous_holder,trigger_task_id)
   VALUES($1,$2,'task',$3,$4,$5,$6,$7,$8,$1,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			id, workspaceID, task.ID, entry.Action, audit.ActorID, audit.ActorType, data, task.UpdatedAt, audit.Source, changes["reason"], audit.SessionID, audit.CorrelationID, oldVersion, newVersion, audit.LeaseGeneration, audit.PreviousHolder, audit.TriggerTaskID)
		if err != nil {
			return fmt.Errorf("transition activity: %w", err)
		}
		changes["task_id"], changes["action"], changes["actor_id"], changes["actor_type"] = task.ID, entry.Action, audit.ActorID, audit.ActorType
		payload, err := json.Marshal(changes)
		if err != nil {
			return err
		}
		eventType := domain.EventTypeCustom
		if entry.Action == "task.moved" {
			eventType = domain.EventTypeStatusChange
		}
		expires := task.UpdatedAt.Add(24 * time.Hour)
		msg := domain.EventBusMessage{ID: id, WorkspaceID: workspaceID, ProjectID: task.ProjectID, TaskID: &task.ID, EventType: eventType, Subject: entry.Action, Payload: payload, Tags: pq.StringArray{"auto", "task"}, TTL: "86400 seconds", CreatedAt: task.UpdatedAt, ExpiresAt: &expires}
		encoded, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_event_outbox(id,task_id,task_version,action,message,created_at) VALUES($1,$2,$3,$4,$5,$6)`, id, task.ID, newVersion, entry.Action, encoded, task.UpdatedAt)
		if err != nil {
			return fmt.Errorf("transition outbox: %w", err)
		}
		// The feed is durable immediately, even while the broker is unavailable.
		// Its primary key is the sink dedup key; TTL applies only to the feed.
		_, err = tx.ExecContext(ctx, `INSERT INTO event_bus_messages(id,workspace_id,project_id,task_id,event_type,subject,payload,tags,ttl,created_at,expires_at)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,interval '24 hours',$9,$10)`, id, workspaceID, task.ProjectID, task.ID, eventType, entry.Action, payload, msg.Tags, task.UpdatedAt, expires)
		if err != nil {
			return fmt.Errorf("transition event feed: %w", err)
		}
	}
	return nil
}
