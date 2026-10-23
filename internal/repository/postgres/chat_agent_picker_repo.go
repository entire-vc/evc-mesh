package postgres

import (
	"context"
	"hash/fnv"
	"strconv"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Presence values of a ChatAgent, as in the agent-chat OpenAPI.
const (
	ChatPresenceOnline  = "online"
	ChatPresenceOffline = "offline"
	ChatPresenceBusy    = "busy"
)

// ChatAgent is one row of GET /projects/{id}/chat/agents.
type ChatAgent struct {
	AgentID        uuid.UUID `json:"agent_id"`
	Name           string    `json:"name"`
	ColorToken     string    `json:"color_token"`
	Presence       string    `json:"presence"`
	ActiveSessions int       `json:"active_sessions"`
	QueueLength    int       `json:"queue_length"`
}

type ChatAgentPickerRepo struct{ db *sqlx.DB }

func NewChatAgentPickerRepo(db *sqlx.DB) *ChatAgentPickerRepo { return &ChatAgentPickerRepo{db: db} }

// chatPresence derives the picker status from runner data. A lapsed or missing
// lease is offline; a live runner is busy once the agent serves as many active
// chat sessions as its max_concurrent_tasks (0 = no limit, never busy).
func chatPresence(live bool, running, limit int) string {
	switch {
	case !live:
		return ChatPresenceOffline
	case limit > 0 && running >= limit:
		return ChatPresenceBusy
	default:
		return ChatPresenceOnline
	}
}

// chatColorToken maps an agent to one of the avatar-1..8 theme tokens, stable per id.
func chatColorToken(id uuid.UUID) string {
	h := fnv.New32a()
	_, _ = h.Write(id[:])
	return "avatar-" + strconv.Itoa(int(h.Sum32()%8)+1)
}

type chatAgentRow struct {
	AgentID uuid.UUID `db:"agent_id"`
	Name    string    `db:"name"`
	Limit   int       `db:"max_concurrent_tasks"`
	Live    bool      `db:"live"`
	Running int       `db:"running"`
	Queued  int       `db:"queued"`
}

// Agents served by a runner of the workspace (an agent_controllers row, live or
// lapsed) and members of the project. The inner join drops runner-less agents;
// the project_id predicate keeps other projects' agents out. The agent's own
// workspace must equal the project's, so a stale cross-workspace membership
// cannot surface.
//
// Load comes from chat sessions, not cards (agent-chat-openapi, ChatAgent): an
// active session is a turn in claimed|streaming, counted for the agent across
// projects because the runner limit is per agent; queue_length is the turns of
// this project still queued for the agent. Cards (checked_out_by) play no part. Guest agents (home workspace elsewhere, active
// agent_workspace_grants row) are excluded on purpose: the picker lists agents
// served by this workspace's runners, and a guest is served by its home
// workspace's runner.
const chatAgentsSQL = `SELECT a.id AS agent_id, a.name, a.max_concurrent_tasks,
		(c.lease_expires_at > now()) AS live,
		(SELECT count(*) FROM ext_agent_chat_turns tr
			WHERE tr.agent_id = a.id AND tr.workspace_id = a.workspace_id
			AND tr.state IN ('claimed', 'streaming')) AS running,
		(SELECT count(*) FROM ext_agent_chat_turns tr
			JOIN ext_agent_chat_threads th ON th.id = tr.thread_id AND th.workspace_id = tr.workspace_id
			WHERE tr.agent_id = a.id AND tr.workspace_id = a.workspace_id
			AND th.project_id = pm.project_id AND tr.state = 'queued') AS queued
	FROM project_members pm
	JOIN projects p ON p.id = pm.project_id AND p.deleted_at IS NULL
	JOIN agents a ON a.id = pm.agent_id AND a.workspace_id = p.workspace_id AND a.deleted_at IS NULL
	JOIN agent_controllers c ON c.agent_id = a.id
	WHERE pm.project_id = $1
	ORDER BY a.name, a.id`

// List returns the chat agents of a project. The caller must be a signed-in
// user; the project-membership check is the route's RequireProjectMember.
func (r *ChatAgentPickerRepo) List(ctx context.Context, actor UserRuntimeActor, project uuid.UUID) ([]ChatAgent, error) {
	if actor.Connector || actor.AgentID != uuid.Nil || actor.UserID == uuid.Nil {
		return nil, apierror.ForbiddenWithDetails("chat is for signed-in users", "user_only")
	}
	var rows []chatAgentRow
	if err := r.db.SelectContext(ctx, &rows, chatAgentsSQL, project); err != nil {
		return nil, err
	}
	return toChatAgents(rows), nil
}

func toChatAgents(rows []chatAgentRow) []ChatAgent {
	out := make([]ChatAgent, 0, len(rows))
	for _, row := range rows {
		out = append(out, ChatAgent{AgentID: row.AgentID, Name: row.Name, ColorToken: chatColorToken(row.AgentID),
			Presence: chatPresence(row.Live, row.Running, row.Limit), ActiveSessions: row.Running, QueueLength: row.Queued})
	}
	return out
}
