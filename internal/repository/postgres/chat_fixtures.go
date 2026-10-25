package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// Isolated fixtures for the agent-chat CI run (E6 setup): four users
// A (member), B (member), C (admin), V (viewer), two agents (Claude, Codex) and
// one agent whose runner lease has lapsed (offline), all inside the scratch
// workspace e2e-ci-sandbox. SeedChatFixtures is idempotent; CleanupChatFixtures
// removes exactly what it created and nothing else.
//
// Passwords are never read from a flag or printed: the caller passes the VALUES
// it resolved from CI variables, and the report carries only the variable names.
// The agents get a hash of a random key nobody holds, so they cannot
// authenticate until a key is rotated through the normal agent-key flow.

// ChatFixturesWorkspaceSlug is the only workspace the fixtures may touch.
const ChatFixturesWorkspaceSlug = "e2e-ci-sandbox"

const (
	chatFixturesEmailDomain = "e2e-ci.invalid"
	chatFixturesPrefix      = "e2e-chat-"
	chatFixturesProjectSlug = "e2e-chat-fixtures"
	// Ownership marker: seed adopts, and cleanup deletes, only rows carrying it.
	chatFixturesMarker      = "E2E Chat"
	chatFixturesProjectName = chatFixturesMarker + " Fixtures"
)

// ErrChatFixturesRefused is returned for any workspace other than the sandbox.
// ErrChatFixturesForeignRow means a row with a reserved name exists but is not ours.
var ErrChatFixturesForeignRow = errors.New("chat fixtures refused: a row with a reserved name is not a fixture")

var ErrChatFixturesRefused = errors.New("chat fixtures refused: only the e2e-ci-sandbox workspace is allowed")

// ChatFixtureUser describes one fixture user; PasswordEnv is the CI variable
// NAME the password comes from.
type ChatFixtureUser struct {
	Key         string
	Role        string
	PasswordEnv string
}

// ChatFixtureUsers are the users A/B/C/V in a stable order.
var ChatFixtureUsers = []ChatFixtureUser{
	{Key: "a", Role: "member", PasswordEnv: "E2E_CHAT_PASSWORD_A"},
	{Key: "b", Role: "member", PasswordEnv: "E2E_CHAT_PASSWORD_B"},
	{Key: "c", Role: "admin", PasswordEnv: "E2E_CHAT_PASSWORD_C"},
	{Key: "v", Role: "viewer", PasswordEnv: "E2E_CHAT_PASSWORD_V"},
}

// ChatFixtureAgents are the agents, by slug suffix and type.
var ChatFixtureAgents = []struct{ Key, Name, Type string }{
	{"claude", "E2E Chat Claude", "claude_code"},
	{"codex", "E2E Chat Codex", "codex"},
	{"offline", "E2E Chat Offline", "claude_code"},
}

// ChatFixturesReport lists what a run did, ids and names only.
type ChatFixturesReport struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	Users       map[string]uuid.UUID // key a/b/c/v -> user id
	Agents      map[string]uuid.UUID // key claude/codex/offline -> agent id
	Created     int                  // rows inserted by this run
	Removed     int                  // rows deleted by this run
}

// ChatFixtureEmail is the login of a fixture user.
func ChatFixtureEmail(key string) string {
	return chatFixturesPrefix + key + "@" + chatFixturesEmailDomain
}

func chatFixturesWorkspace(ctx context.Context, tx *sqlx.Tx, slug string) (uuid.UUID, error) {
	if slug != ChatFixturesWorkspaceSlug {
		return uuid.Nil, fmt.Errorf("%w: %q", ErrChatFixturesRefused, slug)
	}
	var ws uuid.UUID
	err := tx.GetContext(ctx, &ws, `SELECT id FROM workspaces WHERE slug = $1`, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("workspace %q does not exist: create the sandbox first", slug)
	}
	return ws, err
}

// SeedChatFixtures creates or repairs the fixtures. passwords maps the user key
// (a/b/c/v) to the password value; a missing one is an error, never a default.
func SeedChatFixtures(ctx context.Context, db *sqlx.DB, workspaceSlug string, passwords map[string]string) (*ChatFixturesReport, error) {
	if workspaceSlug != ChatFixturesWorkspaceSlug {
		return nil, fmt.Errorf("%w: %q", ErrChatFixturesRefused, workspaceSlug)
	}
	for _, u := range ChatFixtureUsers {
		if passwords[u.Key] == "" {
			return nil, fmt.Errorf("password for user %s is empty: set CI variable %s", strings.ToUpper(u.Key), u.PasswordEnv)
		}
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	ws, err := chatFixturesWorkspace(ctx, tx, workspaceSlug)
	if err != nil {
		return nil, err
	}
	rep := &ChatFixturesReport{WorkspaceID: ws, Users: map[string]uuid.UUID{}, Agents: map[string]uuid.UUID{}}

	for _, u := range ChatFixtureUsers {
		id, created, uerr := seedChatFixtureUser(ctx, tx, u, passwords[u.Key])
		if uerr != nil {
			return nil, fmt.Errorf("user %s: %w", u.Key, uerr)
		}
		rep.Users[u.Key] = id
		if created {
			rep.Created++
		}
		res, merr := tx.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role)
			VALUES ($1,$2,$3,$4::workspace_role)
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role
			WHERE workspace_members.role::text <> EXCLUDED.role::text`, uuid.New(), ws, id, u.Role)
		if merr != nil {
			return nil, fmt.Errorf("member %s: %w", u.Key, merr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			rep.Created++
		}
	}

	var projectName string
	err = tx.QueryRowxContext(ctx, `SELECT id, name FROM projects WHERE workspace_id = $1 AND slug = $2`, ws, chatFixturesProjectSlug).Scan(&rep.ProjectID, &projectName)
	if err == nil && projectName != chatFixturesProjectName {
		return nil, fmt.Errorf("%w: project %q exists and is not a fixture", ErrChatFixturesForeignRow, chatFixturesProjectSlug)
	}
	if errors.Is(err, sql.ErrNoRows) {
		rep.ProjectID = uuid.New()
		if _, err = tx.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,$3,$4)`,
			rep.ProjectID, ws, chatFixturesProjectName, chatFixturesProjectSlug); err != nil {
			return nil, fmt.Errorf("project: %w", err)
		}
		rep.Created++
	} else if err != nil {
		return nil, err
	}

	// Project-scoped chat routes need a project membership for non-admin users,
	// so the users join the fixture project with the role matching their own.
	for _, u := range ChatFixtureUsers {
		res, merr := tx.ExecContext(ctx, `INSERT INTO project_members (id, project_id, user_id, workspace_id, role)
			VALUES ($1,$2,$3,$4,$5::project_role) ON CONFLICT DO NOTHING`, uuid.New(), rep.ProjectID, rep.Users[u.Key], ws, u.Role)
		if merr != nil {
			return nil, fmt.Errorf("project member user %s: %w", u.Key, merr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			rep.Created++
		}
	}

	for _, a := range ChatFixtureAgents {
		slug := chatFixturesPrefix + a.Key
		var id uuid.UUID
		var existingName string
		gerr := tx.QueryRowxContext(ctx, `SELECT id, name FROM agents WHERE workspace_id = $1 AND slug = $2`, ws, slug).Scan(&id, &existingName)
		if gerr == nil && existingName != a.Name {
			return nil, fmt.Errorf("%w: agent %q exists and is not a fixture", ErrChatFixturesForeignRow, slug)
		}
		if errors.Is(gerr, sql.ErrNoRows) {
			id = uuid.New()
			hash, herr := unusableAgentKeyHash()
			if herr != nil {
				return nil, herr
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO agents (id, workspace_id, name, slug, agent_type, api_key_hash, api_key_prefix)
				VALUES ($1,$2,$3,$4,$5::agent_type,$6,$7)`, id, ws, a.Name, slug, a.Type, hash, id.String()[:8]); err != nil {
				return nil, fmt.Errorf("agent %s: %w", a.Key, err)
			}
			rep.Created++
		} else if gerr != nil {
			return nil, gerr
		}
		rep.Agents[a.Key] = id
		res, perr := tx.ExecContext(ctx, `INSERT INTO project_members (id, project_id, agent_id, workspace_id, role)
			VALUES ($1,$2,$3,$4,'member') ON CONFLICT DO NOTHING`, uuid.New(), rep.ProjectID, id, ws)
		if perr != nil {
			return nil, fmt.Errorf("project member %s: %w", a.Key, perr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			rep.Created++
		}
	}
	// The chat only accepts agents served by a runner of the workspace (an
	// agent_controllers row, see AgentChatRepo.ServedAgents), so the offline agent
	// gets a row whose lease lapsed long ago: allowed in the settings, offline in
	// the picker. No runner announces this agent; a lapsed row would be taken over
	// by one that did, and setup then fails with "offline agent fixture is online".
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_controllers (agent_id, runner_id, runner_name, host, version, lease_expires_at)
		VALUES ($1, $2, $2, 'none', '', now() - interval '1 day') ON CONFLICT (agent_id) DO NOTHING`,
		rep.Agents["offline"], chatFixturesPrefix+"offline-runner")
	if err != nil {
		return nil, fmt.Errorf("offline agent controller: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		rep.Created++
	}
	return rep, tx.Commit()
}

// seedChatFixtureUser inserts the user, or re-hashes the password when the CI
// variable changed since the last run.
func seedChatFixtureUser(ctx context.Context, tx *sqlx.Tx, u ChatFixtureUser, password string) (id uuid.UUID, created bool, err error) {
	email := ChatFixtureEmail(u.Key)
	var hash, display string
	err = tx.QueryRowxContext(ctx, `SELECT id, password_hash, display_name FROM users WHERE email = $1`, email).Scan(&id, &hash, &display)
	switch {
	case err == nil:
		if !strings.HasPrefix(display, chatFixturesMarker+" ") {
			return uuid.Nil, false, fmt.Errorf("%w: user %q exists and is not a fixture", ErrChatFixturesForeignRow, email)
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil {
			return id, false, nil
		}
		newHash, herr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if herr != nil {
			return uuid.Nil, false, herr
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET password_hash = $2, is_active = true WHERE id = $1`, id, string(newHash))
		return id, false, err
	case errors.Is(err, sql.ErrNoRows):
		newHash, herr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if herr != nil {
			return uuid.Nil, false, herr
		}
		id = uuid.New()
		_, err = tx.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, display_name, username, is_active)
			VALUES ($1,$2,$3,$4,$5,true)`, id, email, string(newHash), chatFixturesMarker+" "+strings.ToUpper(u.Key), chatFixturesPrefix+u.Key)
		return id, true, err
	default:
		return uuid.Nil, false, err
	}
}

// unusableAgentKeyHash hashes 32 random bytes that are dropped at once.
func unusableAgentKeyHash() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(fmt.Sprintf("%x", b)), bcrypt.MinCost)
	return string(h), err
}

// CleanupChatFixtures deletes the fixture project (and with it every thread,
// turn and message created in it), the fixture agents and the fixture users, by
// their reserved names only. Idempotent: a second run reports zero.
func CleanupChatFixtures(ctx context.Context, db *sqlx.DB, workspaceSlug string) (*ChatFixturesReport, error) {
	if workspaceSlug != ChatFixturesWorkspaceSlug {
		return nil, fmt.Errorf("%w: %q", ErrChatFixturesRefused, workspaceSlug)
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	ws, err := chatFixturesWorkspace(ctx, tx, workspaceSlug)
	if err != nil {
		return nil, err
	}
	rep := &ChatFixturesReport{WorkspaceID: ws}
	del := func(q string, args ...any) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		rep.Removed += int(n)
		return nil
	}
	emails := make([]string, 0, len(ChatFixtureUsers))
	for _, u := range ChatFixtureUsers {
		emails = append(emails, ChatFixtureEmail(u.Key))
	}
	// Order: project first (cascades the chat tables), then the agents the threads
	// pointed at, then membership and the users themselves.
	steps := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM projects WHERE workspace_id = $1 AND slug = $2 AND name = $3`, []any{ws, chatFixturesProjectSlug, chatFixturesProjectName}},
		{`DELETE FROM agents WHERE workspace_id = $1 AND slug = ANY($2) AND name LIKE $3`, []any{ws, agentSlugs(), chatFixturesMarker + " %"}},
		{`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id IN (SELECT id FROM users WHERE email = ANY($2) AND display_name LIKE $3)`, []any{ws, pq.Array(emails), chatFixturesMarker + " %"}},
		{`DELETE FROM users WHERE email = ANY($1) AND display_name LIKE $2`, []any{pq.Array(emails), chatFixturesMarker + " %"}},
	}
	for _, s := range steps {
		if err := del(s.q, s.args...); err != nil {
			return nil, err
		}
	}
	return rep, tx.Commit()
}

func agentSlugs() any {
	out := make([]string, 0, len(ChatFixtureAgents))
	for _, a := range ChatFixtureAgents {
		out = append(out, chatFixturesPrefix+a.Key)
	}
	return pq.Array(out)
}
