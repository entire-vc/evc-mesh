package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/extensions/agentchat"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Error codes of the agent-chat user API (agent-chat-openapi).
const (
	ChatCodeThreadNotFound  = "thread_not_found"
	ChatCodeNotAllowed      = "chat_not_allowed"
	ChatCodeNotEnabled      = "chat_not_enabled"
	ChatCodeUserOnly        = "user_only"
	ChatCodeAgentNA         = "agent_not_available"
	ChatCodeTurnInProgress  = "turn_in_progress"
	ChatCodeEmptyMessage    = "empty_message"
	ChatCodeAttachmentNF    = "attachment_not_found"
	ChatCodeInvalidCursor   = "invalid_cursor"
	ChatCodeValidationError = "validation_failed"
)

// Limits of the thread and message API.
const (
	ChatTitleMax       = 120
	ChatAutoTitleRunes = 60
	ChatBodyMax        = 20000
	ChatPageDefault    = 30
	ChatPageMax        = 100
	// DefaultOfflineTurnTTL is how long a turn may wait in queued for a runner
	// (ADR decision: 24 h) before the sweeper expires it.
	DefaultOfflineTurnTTL = 24 * time.Hour
)

func chatErr(status int, code, message string) *apierror.Error {
	return &apierror.Error{Code: status, ErrorCode: code, Message: message}
}

// ChatThreadNotFound is the one answer for a stranger's thread, a deleted one
// and one that never existed: they must be identical on the wire.
func ChatThreadNotFound() *apierror.Error {
	return chatErr(http.StatusNotFound, ChatCodeThreadNotFound, "thread not found")
}

// ChatTurnRef is the TurnRef of the OpenAPI.
type ChatTurnRef struct {
	ID            uuid.UUID `json:"id"`
	State         string    `json:"state"`
	QueuePosition *int      `json:"queue_position,omitempty"`
	AgentOffline  bool      `json:"agent_offline,omitempty"`
}

// ChatThread is the Thread of the OpenAPI. user_id, workspace_id,
// execution_epoch and the runtime JSONB never leave the server.
type ChatThread struct {
	ID            uuid.UUID    `json:"id"`
	ProjectID     uuid.UUID    `json:"project_id"`
	AgentID       uuid.UUID    `json:"agent_id"`
	Title         string       `json:"title"`
	TitleIsCustom bool         `json:"title_is_custom"`
	LastMessageAt *time.Time   `json:"last_message_at"`
	CreatedAt     time.Time    `json:"created_at"`
	SessionState  string       `json:"session_state"`
	ActiveTurn    *ChatTurnRef `json:"active_turn"`
}

// ChatAttachmentView is the Attachment of the OpenAPI.
type ChatAttachmentView struct {
	ID          uuid.UUID `json:"id"`
	FileName    string    `json:"file_name"`
	Mime        string    `json:"mime"`
	Size        int64     `json:"size"`
	Width       *int      `json:"width"`
	Height      *int      `json:"height"`
	DownloadURL string    `json:"download_url,omitempty"`
}

// ChatMessage is the Message of the OpenAPI.
type ChatMessage struct {
	ID              uuid.UUID            `json:"id"`
	ThreadID        uuid.UUID            `json:"thread_id"`
	Role            string               `json:"role"`
	Body            string               `json:"body"`
	Status          string               `json:"status"`
	Truncated       bool                 `json:"truncated"`
	TurnID          *uuid.UUID           `json:"turn_id"`
	ErrorCode       *string              `json:"error_code"`
	ClientMessageID *string              `json:"client_message_id"`
	Attachments     []ChatAttachmentView `json:"attachments"`
	CreatedAt       time.Time            `json:"created_at"`
}

// ChatThreadPage and ChatMessagePage are the list envelopes.
type ChatThreadPage struct {
	Items      []ChatThread `json:"items"`
	HasMore    bool         `json:"has_more"`
	NextCursor *string      `json:"next_cursor"`
}

type ChatMessagePage struct {
	Items      []ChatMessage `json:"items"`
	HasMore    bool          `json:"has_more"`
	NextCursor *string       `json:"next_cursor"`
	ActiveTurn *ChatTurnRef  `json:"active_turn"`
}

// ChatAccepted is the MessageAccepted of the OpenAPI.
type ChatAccepted struct {
	Message ChatMessage `json:"message"`
	Turn    ChatTurnRef `json:"turn"`
}

// ChatStopResult is the StopResponse of the OpenAPI.
type ChatStopResult struct {
	Stopped bool         `json:"stopped"`
	Turn    *ChatTurnRef `json:"turn"`
}

// ChatGate is what a request needs to know before it touches a thread: the
// project's workspace, the caller's effective role and both settings levels.
type ChatGate struct {
	WorkspaceID uuid.UUID
	Role        string
	Workspace   agentchat.WorkspaceSettings
	Project     agentchat.ProjectSettings
}

// AgentChatRepo owns the ext_agent_chat_* tables. Every thread-scoped method
// takes the caller's user id and puts `user_id = $caller` into the query that
// resolves the thread: the privacy rule is enforced by SQL, not by a check
// that runs before it.
type AgentChatRepo struct{ db *sqlx.DB }

func NewAgentChatRepo(db *sqlx.DB) *AgentChatRepo { return &AgentChatRepo{db: db} }

const chatGateSQL = `SELECT p.workspace_id,
		ic.is_active AS ws_active, ic.config AS ws_config,
		pi.enabled AS pi_enabled, pi.settings AS pi_settings,
		pm.role::text AS pm_role, wm.role::text AS wm_role
	FROM projects p
	LEFT JOIN integration_configs ic ON ic.workspace_id = p.workspace_id AND ic.provider = 'agent-chat'
	LEFT JOIN project_integrations pi ON pi.project_id = p.id AND pi.extension_id = 'agent-chat'
	LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $2
	LEFT JOIN workspace_members wm ON wm.workspace_id = p.workspace_id AND wm.user_id = $2
	WHERE p.id = $1 AND p.deleted_at IS NULL`

type chatGateRow struct {
	WorkspaceID uuid.UUID      `db:"workspace_id"`
	WsActive    sql.NullBool   `db:"ws_active"`
	WsConfig    []byte         `db:"ws_config"`
	PIEnabled   sql.NullBool   `db:"pi_enabled"`
	PISettings  []byte         `db:"pi_settings"`
	PMRole      sql.NullString `db:"pm_role"`
	WMRole      sql.NullString `db:"wm_role"`
}

func chatSettingsMap(raw []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Gate resolves the chat access of a signed-in user to a project. Not a user
// (agent key, connector token), extension off in the workspace or the project,
// or no agent selected → 404 chat_not_enabled; a role outside allowed_roles → 403.
func (r *AgentChatRepo) Gate(ctx context.Context, actor UserRuntimeActor, project uuid.UUID) (*ChatGate, error) {
	if actor.Connector || actor.AgentID != uuid.Nil || actor.UserID == uuid.Nil {
		return nil, chatErr(http.StatusForbidden, ChatCodeUserOnly, "chat is for signed-in users")
	}
	var row chatGateRow
	if err := r.db.GetContext(ctx, &row, chatGateSQL, project, actor.UserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Project")
		}
		return nil, err
	}
	notEnabled := chatErr(http.StatusNotFound, ChatCodeNotEnabled, "chat is not enabled in this project")
	if !row.WsActive.Valid || !row.WsActive.Bool || !row.PIEnabled.Valid || !row.PIEnabled.Bool {
		return nil, notEnabled
	}
	wsRaw, err := chatSettingsMap(row.WsConfig)
	if err != nil {
		return nil, err
	}
	ws, err := agentchat.ParseWorkspaceSettings(wsRaw)
	if err != nil {
		return nil, err
	}
	prRaw, err := chatSettingsMap(row.PISettings)
	if err != nil {
		return nil, err
	}
	pr, err := agentchat.ParseProjectSettings(prRaw)
	if err != nil {
		return nil, err
	}
	if len(pr.Agents) == 0 {
		return nil, notEnabled
	}
	role := row.PMRole.String
	if !row.PMRole.Valid && row.WMRole.Valid && (row.WMRole.String == "owner" || row.WMRole.String == "admin") {
		// Workspace owners and admins reach every project without a membership row.
		role = agentchat.RoleAdmin
	}
	if role == "" || !pr.Allows(role) {
		return nil, chatErr(http.StatusForbidden, ChatCodeNotAllowed, "chat is not available for your role")
	}
	return &ChatGate{WorkspaceID: row.WorkspaceID, Role: role, Workspace: ws, Project: pr}, nil
}

// ChatAutoTitle is the first 60 characters of the first message on one line.
func ChatAutoTitle(body string) string {
	line := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(line) <= ChatAutoTitleRunes {
		return line
	}
	return string([]rune(line)[:ChatAutoTitleRunes])
}

type agentChatThreadRow struct {
	ID            uuid.UUID    `db:"id"`
	ProjectID     uuid.UUID    `db:"project_id"`
	AgentID       uuid.UUID    `db:"agent_id"`
	Title         string       `db:"title"`
	TitleIsCustom bool         `db:"title_is_custom"`
	LastMessageAt sql.NullTime `db:"last_message_at"`
	CreatedAt     time.Time    `db:"created_at"`
	HasSession    bool         `db:"has_session"`
	SortAt        time.Time    `db:"sort_at"`
}

const chatThreadCols = `t.id, t.project_id, t.agent_id, t.title, t.title_is_custom, t.last_message_at, t.created_at,
	COALESCE(t.runtime->>'session_id', '') <> '' AS has_session,
	COALESCE(t.last_message_at, t.created_at) AS sort_at`

// Every thread query has this predicate; there is deliberately no variant
// without user_id.
const chatOwnThread = `t.workspace_id = $1 AND t.project_id = $2 AND t.user_id = $3 AND t.deleted_at IS NULL`

const chatTurnRefSQL = `SELECT t.id, t.state, t.created_at,
		EXISTS (SELECT 1 FROM agent_controllers c WHERE c.agent_id = t.agent_id AND c.lease_expires_at > now()) AS live,
		EXISTS (SELECT 1 FROM ext_agent_chat_turns b
			WHERE b.agent_id = t.agent_id AND b.workspace_id = t.workspace_id AND b.state IN ('claimed', 'streaming')) AS busy,
		(SELECT count(*) FROM ext_agent_chat_turns q
			WHERE q.agent_id = t.agent_id AND q.workspace_id = t.workspace_id AND q.state = 'queued'
			AND (q.created_at, q.id) < (t.created_at, t.id)) AS ahead
	FROM ext_agent_chat_turns t
	WHERE t.workspace_id = $1 AND t.thread_id = $2 AND t.state IN ('queued', 'claimed', 'streaming')`

type chatTurnRow struct {
	ID        uuid.UUID `db:"id"`
	State     string    `db:"state"`
	CreatedAt time.Time `db:"created_at"`
	Live      bool      `db:"live"`
	Busy      bool      `db:"busy"`
	Ahead     int       `db:"ahead"`
}

// activeTurn returns the thread's unfinished turn (at most one: the partial
// unique index) with the queue facts the UI shows for a waiting turn.
func activeTurn(ctx context.Context, q sqlx.QueryerContext, ws, thread uuid.UUID) (*ChatTurnRef, error) {
	var row chatTurnRow
	if err := sqlx.GetContext(ctx, q, &row, chatTurnRefSQL, ws, thread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	ref := &ChatTurnRef{ID: row.ID, State: row.State}
	if row.State == "queued" {
		ref.AgentOffline = !row.Live
		if !row.Live || row.Busy {
			pos := row.Ahead + 1
			ref.QueuePosition = &pos
		}
	}
	return ref, nil
}

// chatSessionState is the header state of the chat. The runner owns the
// runtime JSONB; a thread with a recorded session and no turn is ready, one
// without is stopped (the next message starts it again).
func chatSessionState(hasSession bool, turn *ChatTurnRef) string {
	if turn != nil {
		switch turn.State {
		case "streaming":
			return "working"
		case "claimed":
			return "starting"
		}
	}
	if hasSession {
		return "ready"
	}
	return "stopped"
}

func (r *AgentChatRepo) toThread(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, row agentChatThreadRow) (ChatThread, error) {
	turn, err := activeTurn(ctx, q, ws, row.ID)
	if err != nil {
		return ChatThread{}, err
	}
	th := ChatThread{ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, Title: row.Title,
		TitleIsCustom: row.TitleIsCustom, CreatedAt: row.CreatedAt, ActiveTurn: turn,
		SessionState: chatSessionState(row.HasSession, turn)}
	if row.LastMessageAt.Valid {
		th.LastMessageAt = &row.LastMessageAt.Time
	}
	return th, nil
}

// CreateThread starts an empty thread with one of the project's chat agents.
func (r *AgentChatRepo) CreateThread(ctx context.Context, gate *ChatGate, project, user, agent uuid.UUID, title string) (*ChatThread, error) {
	if !gate.Project.HasAgent(agent) {
		return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeAgentNA, "the agent is not in the project's chat agent list")
	}
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > ChatTitleMax {
		return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeValidationError, "title is too long")
	}
	id := uuid.New()
	// A title given at creation is the user's own choice: auto-titling must not overwrite it.
	_, err := r.db.ExecContext(ctx, `INSERT INTO ext_agent_chat_threads (id, workspace_id, project_id, agent_id, user_id, title, title_is_custom)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, gate.WorkspaceID, project, agent, user, title, title != "")
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23503" {
			return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeAgentNA, "the agent is not available in this workspace")
		}
		return nil, err
	}
	return r.GetThread(ctx, gate.WorkspaceID, project, user, id)
}

// GetThread returns one of the caller's threads. Someone else's, a deleted one
// and a missing one are the same 404.
func (r *AgentChatRepo) GetThread(ctx context.Context, ws, project, user, thread uuid.UUID) (*ChatThread, error) {
	var row agentChatThreadRow
	err := r.db.GetContext(ctx, &row, `SELECT `+chatThreadCols+` FROM ext_agent_chat_threads t WHERE `+chatOwnThread+` AND t.id = $4`,
		ws, project, user, thread)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ChatThreadNotFound()
		}
		return nil, err
	}
	th, err := r.toThread(ctx, r.db, ws, row)
	if err != nil {
		return nil, err
	}
	return &th, nil
}

func encodeChatCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeChatCursor(s string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	u, err := uuid.Parse(id)
	return t, u, err
}

func chatLimit(limit int) int {
	if limit <= 0 {
		return ChatPageDefault
	}
	if limit > ChatPageMax {
		return ChatPageMax
	}
	return limit
}

// ListThreads returns the caller's threads, latest activity first. agent is
// an optional filter (uuid.Nil = all agents).
func (r *AgentChatRepo) ListThreads(ctx context.Context, ws, project, user, agent uuid.UUID, limit int, cursor string) (*ChatThreadPage, error) {
	limit = chatLimit(limit)
	q := `SELECT ` + chatThreadCols + ` FROM ext_agent_chat_threads t WHERE ` + chatOwnThread
	args := []any{ws, project, user}
	if agent != uuid.Nil {
		args = append(args, agent)
		q += ` AND t.agent_id = $` + strconv.Itoa(len(args))
	}
	if cursor != "" {
		at, id, err := decodeChatCursor(cursor)
		if err != nil {
			return nil, chatErr(http.StatusBadRequest, ChatCodeInvalidCursor, "invalid cursor")
		}
		args = append(args, at, id)
		q += ` AND (COALESCE(t.last_message_at, t.created_at), t.id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, limit+1)
	q += ` ORDER BY COALESCE(t.last_message_at, t.created_at) DESC, t.id DESC LIMIT $` + strconv.Itoa(len(args))
	var rows []agentChatThreadRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil { // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- only fixed fragments and $n placeholders are concatenated.
		return nil, err
	}
	page := &ChatThreadPage{Items: []ChatThread{}}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
		last := rows[len(rows)-1]
		c := encodeChatCursor(last.SortAt, last.ID)
		page.NextCursor = &c
	}
	for _, row := range rows {
		th, err := r.toThread(ctx, r.db, ws, row)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, th)
	}
	return page, nil
}

// RenameThread sets a user-chosen title; auto-titling stops for this thread.
func (r *AgentChatRepo) RenameThread(ctx context.Context, ws, project, user, thread uuid.UUID, title string) (*ChatThread, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > ChatTitleMax {
		return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeValidationError, "title must be 1-120 characters")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE ext_agent_chat_threads t SET title = $5, title_is_custom = true
		WHERE `+chatOwnThread+` AND t.id = $4`, ws, project, user, thread, title)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ChatThreadNotFound()
	}
	return r.GetThread(ctx, ws, project, user, thread)
}

// cancelActiveTurn ends a queued turn and flags a claimed or streaming one.
func cancelActiveTurn(ctx context.Context, tx *sqlx.Tx, ws, thread uuid.UUID) error {
	if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET state = 'cancelled', cancel_requested = true,
			cancel_requested_at = now(), finished_at = now(), stop_reason = 'user'
		WHERE workspace_id = $1 AND thread_id = $2 AND state = 'queued'`, ws, thread); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET cancel_requested = true, cancel_requested_at = COALESCE(cancel_requested_at, now())
		WHERE workspace_id = $1 AND thread_id = $2 AND state IN ('claimed', 'streaming')`, ws, thread)
	return err
}

// StopTurn is POST .../stop: idempotent, 200 with stopped=false when nothing runs.
func (r *AgentChatRepo) StopTurn(ctx context.Context, ws, project, user, thread uuid.UUID) (*ChatStopResult, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id uuid.UUID
	if err = tx.GetContext(ctx, &id, `SELECT t.id FROM ext_agent_chat_threads t WHERE `+chatOwnThread+` AND t.id = $4 FOR UPDATE`,
		ws, project, user, thread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ChatThreadNotFound()
		}
		return nil, err
	}
	before, err := activeTurn(ctx, tx, ws, thread)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return &ChatStopResult{Stopped: false}, tx.Commit()
	}
	if err := cancelActiveTurn(ctx, tx, ws, thread); err != nil {
		return nil, err
	}
	ref := *before
	if before.State == "queued" {
		ref.State, ref.QueuePosition, ref.AgentOffline = "cancelled", nil, false
		// The user message of a cancelled queued turn was never delivered.
		if _, err := tx.ExecContext(ctx, `UPDATE ext_agent_chat_messages SET status = 'cancelled'
			WHERE workspace_id = $1 AND turn_id = $2 AND role = 'user' AND status = 'queued'`, ws, before.ID); err != nil {
			return nil, err
		}
	}
	return &ChatStopResult{Stopped: true, Turn: &ref}, tx.Commit()
}

type chatMessageRow struct {
	ID              uuid.UUID      `db:"id"`
	ThreadID        uuid.UUID      `db:"thread_id"`
	Role            string         `db:"role"`
	Body            string         `db:"body"`
	Status          string         `db:"status"`
	Truncated       bool           `db:"truncated"`
	TurnID          *uuid.UUID     `db:"turn_id"`
	ErrorCode       sql.NullString `db:"error_code"`
	ClientMessageID sql.NullString `db:"client_message_id"`
	CreatedAt       time.Time      `db:"created_at"`
}

const chatMessageCols = `m.id, m.thread_id, m.role, m.body, m.status, m.truncated, m.turn_id, m.error_code, m.client_message_id, m.created_at`

func (row chatMessageRow) toMessage() ChatMessage {
	m := ChatMessage{ID: row.ID, ThreadID: row.ThreadID, Role: row.Role, Body: row.Body, Status: row.Status,
		Truncated: row.Truncated, TurnID: row.TurnID, Attachments: []ChatAttachmentView{}, CreatedAt: row.CreatedAt}
	if row.ErrorCode.Valid {
		m.ErrorCode = &row.ErrorCode.String
	}
	if row.ClientMessageID.Valid {
		m.ClientMessageID = &row.ClientMessageID.String
	}
	return m
}

// ListMessages is one history page, newest first. The thread is joined in the
// same query, with the owner predicate, so a message of someone else's thread
// cannot be read even if a caller skipped GetThread.
func (r *AgentChatRepo) ListMessages(ctx context.Context, ws, project, user, thread uuid.UUID, limit int, before string) (*ChatMessagePage, error) {
	if _, err := r.GetThread(ctx, ws, project, user, thread); err != nil {
		return nil, err
	}
	limit = chatLimit(limit)
	q := `SELECT ` + chatMessageCols + ` FROM ext_agent_chat_messages m
		JOIN ext_agent_chat_threads t ON t.id = m.thread_id AND t.workspace_id = m.workspace_id
		WHERE ` + chatOwnThread + ` AND t.id = $4`
	args := []any{ws, project, user, thread}
	if before != "" {
		at, id, err := decodeChatCursor(before)
		if err != nil {
			return nil, chatErr(http.StatusBadRequest, ChatCodeInvalidCursor, "invalid cursor")
		}
		args = append(args, at, id)
		q += ` AND (m.created_at, m.id) < ($5, $6)`
	}
	args = append(args, limit+1)
	q += ` ORDER BY m.created_at DESC, m.id DESC LIMIT $` + strconv.Itoa(len(args))
	var rows []chatMessageRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil { // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- only fixed fragments and $n placeholders are concatenated.
		return nil, err
	}
	page := &ChatMessagePage{Items: []ChatMessage{}}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
		c := encodeChatCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
		page.NextCursor = &c
	}
	for _, row := range rows {
		page.Items = append(page.Items, row.toMessage())
	}
	turn, err := activeTurn(ctx, r.db, ws, thread)
	if err != nil {
		return nil, err
	}
	page.ActiveTurn = turn
	return page, nil
}

// SendInput is the body of POST .../messages.
type SendInput struct {
	ClientMessageID string
	Body            string
	AttachmentIDs   []uuid.UUID
}

// SendMessage stores the user message and a queued turn in one transaction.
// A repeat of (thread, client_message_id) returns the original answer; a send
// while a turn is unfinished is 409 turn_in_progress. The turn waits in queued
// for a runner, however long that takes (until expires_at).
func (r *AgentChatRepo) SendMessage(ctx context.Context, gate *ChatGate, project, user, thread uuid.UUID, in SendInput) (*ChatAccepted, error) {
	ws := gate.WorkspaceID
	body := strings.TrimSpace(in.Body)
	if len(in.AttachmentIDs) > 0 {
		// Attachments (presign, upload, ownership) are a separate unit; until then
		// no id can name an uploaded object of the caller.
		return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeAttachmentNF, "attachment not found")
	}
	if body == "" {
		return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeEmptyMessage, "message is empty")
	}
	if utf8.RuneCountInString(body) > ChatBodyMax || utf8.RuneCountInString(in.ClientMessageID) < 8 || utf8.RuneCountInString(in.ClientMessageID) > 64 {
		return nil, chatErr(http.StatusUnprocessableEntity, ChatCodeValidationError, "invalid message")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// The row lock serialises concurrent sends on one thread.
	var th struct {
		AgentID       uuid.UUID `db:"agent_id"`
		Title         string    `db:"title"`
		TitleIsCustom bool      `db:"title_is_custom"`
	}
	if err = tx.GetContext(ctx, &th, `SELECT t.agent_id, t.title, t.title_is_custom FROM ext_agent_chat_threads t
		WHERE `+chatOwnThread+` AND t.id = $4 FOR UPDATE`, ws, project, user, thread); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ChatThreadNotFound()
		}
		return nil, err
	}

	var prior chatMessageRow
	err = tx.GetContext(ctx, &prior, `SELECT `+chatMessageCols+` FROM ext_agent_chat_messages m
		WHERE m.workspace_id = $1 AND m.thread_id = $2 AND m.client_message_id = $3`, ws, thread, in.ClientMessageID)
	if err == nil {
		return r.replay(ctx, tx, ws, prior)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	turn, err := activeTurn(ctx, tx, ws, thread)
	if err != nil {
		return nil, err
	}
	if turn != nil {
		return nil, chatErr(http.StatusConflict, ChatCodeTurnInProgress, "the agent is still answering the previous message")
	}

	ttl := DefaultOfflineTurnTTL
	if h := gate.Workspace.OfflineExpiryHours; h > 0 {
		ttl = time.Duration(h) * time.Hour
	}
	msgID, turnID := uuid.New(), uuid.New()
	// Messages and turns reference each other: the turn goes in first without its
	// user_message_id (nullable), the message points at it, then the turn is completed.
	if _, err = tx.ExecContext(ctx, `INSERT INTO ext_agent_chat_turns (id, workspace_id, thread_id, agent_id, state, expires_at)
		VALUES ($1, $2, $3, $4, 'queued', now() + $5 * interval '1 second')`, turnID, ws, thread, th.AgentID, int64(ttl.Seconds())); err != nil {
		return nil, err
	}
	var created chatMessageRow
	err = tx.GetContext(ctx, &created, `INSERT INTO ext_agent_chat_messages (id, workspace_id, thread_id, role, body, status, turn_id, client_message_id)
		VALUES ($1, $2, $3, 'user', $4, 'queued', $5, $6)
		RETURNING id, thread_id, role, body, status, truncated, turn_id, error_code, client_message_id, created_at`,
		msgID, ws, thread, body, turnID, in.ClientMessageID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ext_agent_chat_turns SET user_message_id = $3 WHERE id = $1 AND workspace_id = $2`, turnID, ws, msgID); err != nil {
		return nil, err
	}
	title := th.Title
	if !th.TitleIsCustom && title == "" {
		title = ChatAutoTitle(body)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ext_agent_chat_threads SET title = $3, last_message_at = $4 WHERE id = $1 AND workspace_id = $2`,
		thread, ws, title, created.CreatedAt); err != nil {
		return nil, err
	}
	ref, err := activeTurn(ctx, tx, ws, thread)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ChatAccepted{Message: created.toMessage(), Turn: *ref}, nil
}

// replay answers an idempotent repeat with the original message and its turn.
func (r *AgentChatRepo) replay(ctx context.Context, tx *sqlx.Tx, ws uuid.UUID, prior chatMessageRow) (*ChatAccepted, error) {
	if prior.TurnID == nil {
		return nil, chatErr(http.StatusConflict, ChatCodeTurnInProgress, "the original turn is gone")
	}
	var t chatTurnRow
	if err := tx.GetContext(ctx, &t, `SELECT t.id, t.state, t.created_at, false AS live, false AS busy, 0 AS ahead
		FROM ext_agent_chat_turns t WHERE t.id = $1 AND t.workspace_id = $2`, *prior.TurnID, ws); err != nil {
		return nil, err
	}
	ref := ChatTurnRef{ID: t.ID, State: t.State}
	if cur, err := activeTurn(ctx, tx, ws, prior.ThreadID); err != nil {
		return nil, err
	} else if cur != nil && cur.ID == t.ID {
		ref = *cur
	}
	return &ChatAccepted{Message: prior.toMessage(), Turn: ref}, tx.Commit()
}

// ServedAgents says which of the given agents a workspace runner serves and
// that are members of the project (the same rule as the picker, without the
// live-lease filter: an agent whose runner is momentarily offline can be chosen).
func (r *AgentChatRepo) ServedAgents(ctx context.Context, project uuid.UUID, agents []uuid.UUID) (map[uuid.UUID]bool, error) {
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.String()
	}
	var found []uuid.UUID
	if err := r.db.SelectContext(ctx, &found, `SELECT a.id
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id AND p.deleted_at IS NULL
		JOIN agents a ON a.id = pm.agent_id AND a.workspace_id = p.workspace_id AND a.deleted_at IS NULL
		JOIN agent_controllers c ON c.agent_id = a.id
		WHERE pm.project_id = $1 AND a.id = ANY($2::uuid[])`, project, pq.Array(ids)); err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(found))
	for _, id := range found {
		out[id] = true
	}
	return out, nil
}
