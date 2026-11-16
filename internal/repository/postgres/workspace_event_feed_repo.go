package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Actor types of a feed event. The bus row itself rarely says who acted: the
// task transition writer leaves agent_id NULL and records the actor in
// payload.actor_type / payload.actor_id (on prod 13.11.2026: 1 row of 10769
// had agent_id). The feed derives the actor from both, the same way for the
// filter, the returned field and the joined name.
const (
	EventActorAgent  = "agent"
	EventActorUser   = "user"
	EventActorSystem = "system"
)

const uuidPattern = `'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'`

// eventActorTypeExpr: agent_id wins, then payload.actor_type agent|user, anything else is system.
const eventActorTypeExpr = `(CASE
	WHEN e.agent_id IS NOT NULL THEN 'agent'
	WHEN e.payload->>'actor_type' IN ('agent', 'user') THEN e.payload->>'actor_type'
	ELSE 'system' END)`

// eventActorIDExpr is payload.actor_id as a uuid when it is one, NULL otherwise
// (a malformed id must not fail the whole page with a cast error).
const eventActorIDExpr = `(CASE WHEN e.payload->>'actor_id' ~ ` + uuidPattern + ` THEN (e.payload->>'actor_id')::uuid END)`

// feedEventColumns are the bus columns a feed row reads, named (not e.*) so an
// additive migration cannot break the strict scan.
const feedEventColumns = `e.id, e.workspace_id, e.project_id, e.task_id, e.agent_id,
	e.event_type, e.subject, e.payload, e.tags, e.ttl,
	e.created_at, e.expires_at, e.memory_hint`

// WorkspaceEventCursor is the keyset position of the last row of a page.
type WorkspaceEventCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// WorkspaceEventFeedFilter selects events of one workspace. The project scope
// is mandatory: either AllProjects (a workspace owner or admin) or the
// project_members rows of MemberUserID / MemberAgentID. No scope = refused.
type WorkspaceEventFeedFilter struct {
	WorkspaceID   uuid.UUID
	AllProjects   bool
	MemberUserID  *uuid.UUID
	MemberAgentID *uuid.UUID

	EventTypes []domain.EventType
	ProjectID  *uuid.UUID
	ActorType  string // agent | user | system; empty = any
	AgentID    *uuid.UUID
	TaskID     *uuid.UUID
	Tags       []string // any of
	From       *time.Time
	To         *time.Time

	Cursor *WorkspaceEventCursor
	Limit  int
}

// WorkspaceFeedEvent is one feed row: the enriched bus message plus the derived actor.
type WorkspaceFeedEvent struct {
	domain.EnrichedEventBusMessage
	ActorType   string     `json:"actor_type"`
	ActorID     *uuid.UUID `json:"actor_id,omitempty"`
	ProjectSlug *string    `json:"project_slug,omitempty"`
}

// WorkspaceEventFeedRepo reads the workspace-wide event feed (GET /workspaces/:ws/events).
type WorkspaceEventFeedRepo struct {
	db *sqlx.DB
}

func NewWorkspaceEventFeedRepo(db *sqlx.DB) *WorkspaceEventFeedRepo {
	return &WorkspaceEventFeedRepo{db: db}
}

// ErrEventFeedNoScope is returned when the filter names no project scope.
var ErrEventFeedNoScope = errors.New("event feed: no project scope")

type workspaceFeedRow struct {
	enrichedEventRow
	ActorType   string     `db:"actor_type"`
	ActorID     *uuid.UUID `db:"actor_id"`
	ProjectSlug *string    `db:"project_slug"`
	UserName    *string    `db:"user_name"`
}

// List returns up to filter.Limit events, newest first, and whether more exist.
func (r *WorkspaceEventFeedRepo) List(ctx context.Context, f WorkspaceEventFeedFilter) ([]WorkspaceFeedEvent, bool, error) {
	if f.WorkspaceID == uuid.Nil {
		return nil, false, ErrEventFeedNoScope
	}
	args := []interface{}{f.WorkspaceID} // $1
	next := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	// Project scope. Deleted projects drop out (their events are not addressable).
	var scope string
	switch {
	case f.AllProjects:
		scope = `SELECT p2.id FROM projects p2 WHERE p2.workspace_id = $1 AND p2.deleted_at IS NULL`
	case f.MemberUserID != nil:
		// The workspace owner sees every project even without a role: the
		// membership middleware admits an owner whose workspace_members row is
		// missing (a best-effort insert at creation) without setting the role.
		u := next(*f.MemberUserID)
		scope = `SELECT p2.id FROM projects p2
			WHERE p2.workspace_id = $1 AND p2.deleted_at IS NULL AND (
				EXISTS (SELECT 1 FROM workspaces w WHERE w.id = $1 AND w.owner_id = ` + u + `)
				OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p2.id AND pm.user_id = ` + u + `))`
	case f.MemberAgentID != nil:
		scope = `SELECT pm.project_id FROM project_members pm JOIN projects p2 ON p2.id = pm.project_id
			WHERE p2.workspace_id = $1 AND p2.deleted_at IS NULL AND pm.agent_id = ` + next(*f.MemberAgentID)
	default:
		return nil, false, ErrEventFeedNoScope
	}

	conds := []string{"e.workspace_id = $1", "e.project_id IN (" + scope + ")"}
	if len(f.EventTypes) > 0 {
		types := make([]string, len(f.EventTypes))
		for i, t := range f.EventTypes {
			types[i] = string(t)
		}
		conds = append(conds, "e.event_type::text = ANY("+next(pq.Array(types))+")")
	}
	if f.ProjectID != nil {
		conds = append(conds, "e.project_id = "+next(*f.ProjectID))
	}
	if f.ActorType != "" {
		conds = append(conds, eventActorTypeExpr+" = "+next(f.ActorType))
	}
	if f.AgentID != nil {
		p := next(*f.AgentID)
		conds = append(conds, fmt.Sprintf(
			"(e.agent_id = %s OR (e.payload->>'actor_type' = 'agent' AND e.payload->>'actor_id' = %s::text))", p, p))
	}
	if f.TaskID != nil {
		conds = append(conds, "e.task_id = "+next(*f.TaskID))
	}
	if len(f.Tags) > 0 {
		conds = append(conds, "e.tags && "+next(pq.Array(f.Tags)))
	}
	if f.From != nil {
		conds = append(conds, "e.created_at >= "+next(*f.From))
	}
	if f.To != nil {
		conds = append(conds, "e.created_at <= "+next(*f.To))
	}
	if f.Cursor != nil {
		conds = append(conds, fmt.Sprintf("(e.created_at, e.id) < (%s, %s)", next(f.Cursor.CreatedAt), next(f.Cursor.ID)))
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	q := `
		SELECT
			` + feedEventColumns + `,
			t.title AS task_title,
			p.name  AS project_name,
			p.slug  AS project_slug,
			a.name  AS actor_name,
			u.display_name AS user_name,
			` + eventActorTypeExpr + ` AS actor_type,
			COALESCE(e.agent_id, ` + eventActorIDExpr + `) AS actor_id
		FROM (
			-- The page is cut first (idx_events_workspace_created serves the
			-- order), names are joined for its rows only: on prod 13.11 joining
			-- before the LIMIT read and joined all 6240 workspace rows (115 ms).
			SELECT ` + feedEventColumns + ` FROM event_bus_messages e
			WHERE ` + joinAnd(conds) + `
			ORDER BY e.created_at DESC, e.id DESC
			LIMIT ` + next(limit+1) + `
		) e
		JOIN projects p ON p.id = e.project_id
		LEFT JOIN tasks t ON t.id = e.task_id
		LEFT JOIN agents a ON a.id = COALESCE(e.agent_id,
			CASE WHEN e.payload->>'actor_type' = 'agent' THEN ` + eventActorIDExpr + ` END)
		LEFT JOIN users u ON e.agent_id IS NULL AND e.payload->>'actor_type' = 'user' AND u.id = ` + eventActorIDExpr + `
		ORDER BY e.created_at DESC, e.id DESC`

	var rows []workspaceFeedRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]WorkspaceFeedEvent, len(rows))
	for i := range rows {
		ev := WorkspaceFeedEvent{
			EnrichedEventBusMessage: rows[i].toEnrichedDomain(),
			ActorType:               rows[i].ActorType,
			ActorID:                 rows[i].ActorID,
			ProjectSlug:             rows[i].ProjectSlug,
		}
		if ev.ActorType == EventActorUser && rows[i].UserName != nil {
			ev.ActorName = rows[i].UserName
		}
		if ev.ActorType == EventActorSystem {
			ev.ActorID = nil
		}
		out[i] = ev
	}
	return out, hasMore, nil
}
