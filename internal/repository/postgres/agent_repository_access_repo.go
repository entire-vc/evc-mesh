package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AgentRepositoryAccessRepo owns agents.repository_access and its revision.
// It is the only writer of the two columns: the generic agent
// UPDATEs, heartbeat, profile and config import never name them.
type AgentRepositoryAccessRepo struct {
	db      *sqlx.DB
	runtime *UserRuntimeRepo
}

func NewAgentRepositoryAccessRepo(db *sqlx.DB) *AgentRepositoryAccessRepo {
	return &AgentRepositoryAccessRepo{db: db, runtime: NewUserRuntimeRepo(db)}
}

// AgentRepositoryAccessPut is the body of PUT /agents/{id}/repository-access.
type AgentRepositoryAccessPut struct {
	ExpectedRevision *int64                 `json:"expected_revision"`
	Repositories     []domain.RepositoryRef `json:"repositories"`
}

// AgentRepositoryAccessAudit is the metadata of the agent_activity_log event
// domain.AgentRepositoryAccessChangedEvent. Old*/New* repositories are null for
// "not configured" (legacy); OldRevision is 0 then.
type AgentRepositoryAccessAudit struct {
	ActorType       string                 `json:"actor_type"` // user | agent
	ActorID         uuid.UUID              `json:"actor_id"`
	OldRevision     int64                  `json:"old_revision"`
	NewRevision     int64                  `json:"new_revision"`
	OldRepositories []domain.RepositoryRef `json:"old_repositories"`
	NewRepositories []domain.RepositoryRef `json:"new_repositories"`
}

type repositoryAccessRow struct {
	WorkspaceID uuid.UUID     `db:"workspace_id"`
	Operator    *uuid.UUID    `db:"operator_user_id"`
	Access      []byte        `db:"repository_access"` // []byte, not json.RawMessage: NULL must scan
	Revision    sql.NullInt64 `db:"repository_access_revision"`
}

func (row repositoryAccessRow) toDomain() (*domain.AgentRepositoryAccess, error) {
	if !row.Revision.Valid {
		return nil, nil
	}
	out := &domain.AgentRepositoryAccess{Revision: row.Revision.Int64, Repositories: []domain.RepositoryRef{}}
	if err := json.Unmarshal(row.Access, &out.Repositories); err != nil {
		return nil, fmt.Errorf("decode repository_access: %w", err)
	}
	if out.Repositories == nil {
		out.Repositories = []domain.RepositoryRef{}
	}
	return out, nil
}

const repositoryAccessSelect = `SELECT workspace_id, operator_user_id, repository_access, repository_access_revision
	FROM agents WHERE id = $1 AND deleted_at IS NULL`

// Get returns the agent's configured access, or nil when it was never set.
func (r *AgentRepositoryAccessRepo) Get(ctx context.Context, agentID uuid.UUID) (*domain.AgentRepositoryAccess, error) {
	var row repositoryAccessRow
	if err := r.db.GetContext(ctx, &row, repositoryAccessSelect, agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Agent")
		}
		return nil, err
	}
	return row.toDomain()
}

// Put replaces the list when expected_revision equals the stored one (0 for
// "not configured") and returns the new state with revision+1. The row lock
// serialises concurrent writers: the second one sees the new revision and gets
// 409, so no write is lost. The audit event is written in the same transaction.
func (r *AgentRepositoryAccessRepo) Put(ctx context.Context, actor UserRuntimeActor, agentID uuid.UUID, in AgentRepositoryAccessPut) (*domain.AgentRepositoryAccess, error) {
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 0 {
		return nil, apierror.BadRequest("expected_revision is required and must be >= 0")
	}
	repos, err := domain.NormalizeRepositoryRefs(in.Repositories)
	if err != nil {
		return nil, apierror.BadRequest(err.Error())
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var row repositoryAccessRow
	if err = tx.GetContext(ctx, &row, repositoryAccessSelect+` FOR UPDATE`, agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Agent")
		}
		return nil, err
	}
	actorType, actorID, err := r.authorize(ctx, tx, actor, agentID, row)
	if err != nil {
		return nil, err
	}
	old, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	var current int64
	var oldRepos []domain.RepositoryRef
	if old != nil {
		current, oldRepos = old.Revision, old.Repositories
	}
	if *in.ExpectedRevision != current {
		return nil, apierror.Conflict(fmt.Sprintf("repository_access revision changed: expected %d, current %d; re-read and retry", *in.ExpectedRevision, current))
	}

	next := &domain.AgentRepositoryAccess{Revision: current + 1, Repositories: repos}
	listJSON, err := json.Marshal(repos)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agents
		SET repository_access = $2, repository_access_revision = $3, updated_at = now()
		WHERE id = $1`, agentID, listJSON, next.Revision); err != nil {
		return nil, err
	}

	audit, err := json.Marshal(AgentRepositoryAccessAudit{
		ActorType: actorType, ActorID: actorID,
		OldRevision: current, NewRevision: next.Revision,
		OldRepositories: oldRepos, NewRepositories: repos,
	})
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf("repository access changed by %s: revision %d -> %d, %d repositories", actorType, current, next.Revision, len(repos))
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_activity_log
		(id, agent_id, workspace_id, event_type, task_id, message, metadata, created_at)
		VALUES ($1, $2, $3, $4, NULL, $5, $6, $7)`,
		uuid.New(), agentID, row.WorkspaceID, domain.AgentRepositoryAccessChangedEvent, message, audit, time.Now()); err != nil {
		return nil, err
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	return next, nil
}

var (
	errRepositoryAccessConnector = apierror.Forbidden("repository access cannot be changed through an OAuth connector")
	errRepositoryAccessSelf      = apierror.Forbidden("an agent cannot change its own repository access; its operator or a workspace owner/admin does")
	errRepositoryAccessManager   = apierror.Forbidden("repository access of another agent needs a live runtime_manager grant from that agent's operator")
	errRepositoryAccessUser      = apierror.Forbidden("repository access is changed by the agent's operator or a workspace owner/admin")
)

// authorize decides who may write. A person: the agent's operator, or an owner
// or admin of the agent's workspace. An agent key: only with a live
// runtime_manager grant, only for ANOTHER agent of the same operator. An OAuth
// connector never; an agent key never for itself.
func (r *AgentRepositoryAccessRepo) authorize(ctx context.Context, tx *sqlx.Tx, actor UserRuntimeActor, target uuid.UUID, row repositoryAccessRow) (string, uuid.UUID, error) {
	if actor.Connector {
		return "", uuid.Nil, errRepositoryAccessConnector
	}
	if actor.AgentID != uuid.Nil {
		if actor.AgentID == target {
			return "", uuid.Nil, errRepositoryAccessSelf
		}
		operator, ok, err := r.runtime.isManager(ctx, tx, actor.AgentID)
		if err != nil {
			return "", uuid.Nil, err
		}
		if !ok || row.Operator == nil || *row.Operator != operator {
			return "", uuid.Nil, errRepositoryAccessManager
		}
		return "agent", actor.AgentID, nil
	}
	if actor.UserID == uuid.Nil {
		return "", uuid.Nil, errRepositoryAccessUser
	}
	if row.Operator != nil && *row.Operator == actor.UserID {
		return "user", actor.UserID, nil
	}
	var admin bool
	if err := tx.GetContext(ctx, &admin, `SELECT EXISTS (
		SELECT 1 FROM workspaces w WHERE w.id = $1 AND w.deleted_at IS NULL
		AND (w.owner_id = $2 OR EXISTS (SELECT 1 FROM workspace_members m
			WHERE m.workspace_id = w.id AND m.user_id = $2 AND m.role IN ('owner','admin'))))`,
		row.WorkspaceID, actor.UserID); err != nil {
		return "", uuid.Nil, err
	}
	if !admin {
		return "", uuid.Nil, errRepositoryAccessUser
	}
	return "user", actor.UserID, nil
}

// RepositoryAccessState is one side of a history entry.
type RepositoryAccessState struct {
	Revision     int64                  `json:"revision"`
	Repositories []domain.RepositoryRef `json:"repositories"`
}

// RepositoryAccessActor names who made a change. Name is the person's display
// name (username when unset) or the agent's name; empty when the row is gone.
type RepositoryAccessActor struct {
	Type string    `json:"type"` // user | agent
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// RepositoryAccessHistoryItem is one write. Old is null when the agent was not
// configured before it.
type RepositoryAccessHistoryItem struct {
	ID        uuid.UUID              `json:"id"`
	CreatedAt time.Time              `json:"created_at"`
	Actor     RepositoryAccessActor  `json:"actor"`
	Old       *RepositoryAccessState `json:"old"`
	New       RepositoryAccessState  `json:"new"`
}

// RepositoryAccessHistoryPage is GET /agents/{id}/repository-access/history.
type RepositoryAccessHistoryPage struct {
	Items      []RepositoryAccessHistoryItem `json:"items"`
	NextCursor *string                       `json:"next_cursor"`
}

const (
	RepositoryAccessHistoryDefaultLimit = 20
	RepositoryAccessHistoryMaxLimit     = 100
)

type repositoryAccessCursor struct {
	At time.Time
	ID uuid.UUID
}

func encodeRepositoryAccessCursor(c repositoryAccessCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

func decodeRepositoryAccessCursor(s string) (repositoryAccessCursor, error) {
	bad := apierror.BadRequest("invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return repositoryAccessCursor{}, bad
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return repositoryAccessCursor{}, bad
	}
	at, e1 := time.Parse(time.RFC3339Nano, parts[0])
	id, e2 := uuid.Parse(parts[1])
	if e1 != nil || e2 != nil {
		return repositoryAccessCursor{}, bad
	}
	return repositoryAccessCursor{At: at, ID: id}, nil
}

type repositoryAccessHistoryRow struct {
	ID        uuid.UUID `db:"id"`
	CreatedAt time.Time `db:"created_at"`
	Metadata  []byte    `db:"metadata"`
	ActorName string    `db:"actor_name"`
}

// History lists the agent's repository access writes, newest first, from the
// audit events Put writes. limit 0 means the default; cursor "" the first page.
// Who may read is decided by the route: the same as GET /agents/{id}.
func (r *AgentRepositoryAccessRepo) History(ctx context.Context, agentID uuid.UUID, limit int, cursor string) (*RepositoryAccessHistoryPage, error) {
	if limit == 0 {
		limit = RepositoryAccessHistoryDefaultLimit
	}
	if limit < 1 || limit > RepositoryAccessHistoryMaxLimit {
		return nil, apierror.BadRequest(fmt.Sprintf("limit must be between 1 and %d", RepositoryAccessHistoryMaxLimit))
	}
	var exists bool
	if err := r.db.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1 AND deleted_at IS NULL)`, agentID); err != nil {
		return nil, err
	}
	if !exists {
		return nil, apierror.NotFound("Agent")
	}

	args := []any{agentID, domain.AgentRepositoryAccessChangedEvent}
	after := ""
	if cursor != "" {
		cur, err := decodeRepositoryAccessCursor(cursor)
		if err != nil {
			return nil, err
		}
		args = append(args, cur.At, cur.ID)
		after = ` AND (l.created_at, l.id) < ($3, $4)`
	}
	args = append(args, limit+1)
	q := `SELECT l.id, l.created_at, l.metadata,
		COALESCE(CASE l.metadata->>'actor_type'
			WHEN 'user' THEN (SELECT COALESCE(NULLIF(u.display_name, ''), u.username) FROM users u WHERE u.id::text = l.metadata->>'actor_id')
			WHEN 'agent' THEN (SELECT a.name FROM agents a WHERE a.id::text = l.metadata->>'actor_id')
		END, '') AS actor_name
		FROM agent_activity_log l
		WHERE l.agent_id = $1 AND l.event_type = $2` + after + `
		ORDER BY l.created_at DESC, l.id DESC
		LIMIT $` + fmt.Sprint(len(args))
	var rows []repositoryAccessHistoryRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}

	page := &RepositoryAccessHistoryPage{Items: []RepositoryAccessHistoryItem{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := encodeRepositoryAccessCursor(repositoryAccessCursor{At: last.CreatedAt, ID: last.ID})
		page.NextCursor = &next
	}
	for _, row := range rows {
		var a AgentRepositoryAccessAudit
		if err := json.Unmarshal(row.Metadata, &a); err != nil {
			return nil, fmt.Errorf("decode repository access audit %s: %w", row.ID, err)
		}
		item := RepositoryAccessHistoryItem{
			ID: row.ID, CreatedAt: row.CreatedAt,
			Actor: RepositoryAccessActor{Type: a.ActorType, ID: a.ActorID, Name: row.ActorName},
			New:   RepositoryAccessState{Revision: a.NewRevision, Repositories: nonNilRefs(a.NewRepositories)},
		}
		if a.OldRevision > 0 {
			item.Old = &RepositoryAccessState{Revision: a.OldRevision, Repositories: nonNilRefs(a.OldRepositories)}
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func nonNilRefs(in []domain.RepositoryRef) []domain.RepositoryRef {
	if in == nil {
		return []domain.RepositoryRef{}
	}
	return in
}
