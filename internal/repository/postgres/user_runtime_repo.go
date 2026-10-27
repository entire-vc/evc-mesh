package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	maxUserRuntimeConfigBytes = 256 << 10
	maxConcurrentTasksCap     = 1000
)

// UserRuntimeActor is who calls an operator-level runtime route. It comes from
// authentication middleware, never from request JSON.
type UserRuntimeActor struct {
	UserID    uuid.UUID
	AgentID   uuid.UUID
	Connector bool
}

// UserRuntimeConfig is the operator's "My Runner" document.
type UserRuntimeConfig struct {
	UserID    uuid.UUID       `json:"user_id" db:"user_id"`
	Revision  int64           `json:"revision" db:"revision"`
	Config    json.RawMessage `json:"config" db:"config"`
	UpdatedAt time.Time       `json:"updated_at" db:"updated_at"`
}

// UserRuntimeSave is the body of PUT .../runtime.
type UserRuntimeSave struct {
	IfRevision int64           `json:"if_revision"`
	Config     json.RawMessage `json:"config"`
}

type UserRuntimeHistory struct {
	ID         int64           `json:"id" db:"id"`
	Revision   int64           `json:"revision" db:"revision"`
	Kind       string          `json:"kind" db:"kind"`
	Detail     json.RawMessage `json:"detail" db:"detail"`
	AuthorType string          `json:"author_type" db:"author_type"`
	AuthorID   uuid.UUID       `json:"author_id" db:"author_id"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
}

type UserRuntimeManager struct {
	AgentID   uuid.UUID `json:"agent_id" db:"agent_id"`
	AgentName string    `json:"agent_name" db:"agent_name"`
	GrantedAt time.Time `json:"granted_at" db:"granted_at"`
}

// AgentRuntimeView is the answer of GET /agents/me/runtime.
type AgentRuntimeView struct {
	AgentID          uuid.UUID       `json:"agent_id"`
	OperatorUserID   *uuid.UUID      `json:"operator_user_id"`
	Revision         int64           `json:"revision"`
	Profile          json.RawMessage `json:"profile"`
	Resources        json.RawMessage `json:"resources"`
	Limits           AgentRuntimeLim `json:"limits"`
	RuntimeManager   bool            `json:"runtime_manager"`
	OperatorResolved bool            `json:"operator_resolved"`
}

type AgentRuntimeLim struct {
	MaxConcurrentTasks int `json:"max_concurrent_tasks"`
}

type UserRuntimeRepo struct{ db *sqlx.DB }

func NewUserRuntimeRepo(db *sqlx.DB) *UserRuntimeRepo { return &UserRuntimeRepo{db: db} }

func operatorOnly(actor UserRuntimeActor) error {
	if actor.Connector || actor.AgentID != uuid.Nil || actor.UserID == uuid.Nil {
		return apierror.Forbidden("operator runtime requires a signed-in user")
	}
	return nil
}

func normalizeConfig(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxUserRuntimeConfigBytes {
		return nil, apierror.BadRequest("config is required and must be under 256 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, apierror.BadRequest("config must be a JSON object")
	}
	for key, v := range doc {
		switch key {
		case "accounts", "pools", "profiles", "agents":
			if _, ok := v.(map[string]any); !ok {
				return nil, apierror.BadRequest("config." + key + " must be an object keyed by name")
			}
		default:
			return nil, apierror.BadRequest("unknown config section: " + key)
		}
	}
	if err := rejectSecrets(doc, ""); err != nil {
		return nil, err
	}
	if err := validateAccounts(doc["accounts"]); err != nil {
		return nil, err
	}
	out, _ := json.Marshal(doc)
	return out, nil
}

// rejectSecrets keeps secret values out: accounts are cred: references only.
func rejectSecrets(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(k)
			for _, bad := range []string{"secret", "password", "token", "api_key", "apikey", "private_key"} {
				if strings.Contains(lk, bad) {
					return apierror.BadRequest("config must not contain secrets (field " + path + k + "); use cred: references")
				}
			}
			if lk == "credential" || lk == "cred" {
				if s, ok := child.(string); !ok || !strings.HasPrefix(s, "cred:") {
					return apierror.BadRequest("credential must be a cred: reference")
				}
			}
			if err := rejectSecrets(child, path+k+"."); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := rejectSecrets(child, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *UserRuntimeRepo) get(ctx context.Context, q sqlx.QueryerContext, user uuid.UUID, lock bool) (UserRuntimeConfig, error) {
	query := `SELECT user_id, revision, config, updated_at FROM user_runtime_configs WHERE user_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var v UserRuntimeConfig
	err := sqlx.GetContext(ctx, q, &v, query, user)
	if errors.Is(err, sql.ErrNoRows) {
		return UserRuntimeConfig{UserID: user, Config: json.RawMessage(`{}`)}, nil
	}
	return v, err
}

// Get returns the operator's document (revision 0 and {} when never saved).
func (r *UserRuntimeRepo) Get(ctx context.Context, actor UserRuntimeActor) (UserRuntimeConfig, error) {
	if err := operatorOnly(actor); err != nil {
		return UserRuntimeConfig{}, err
	}
	return r.get(ctx, r.db, actor.UserID, false)
}

func (r *UserRuntimeRepo) save(ctx context.Context, operator uuid.UUID, authorType string, author uuid.UUID, in UserRuntimeSave) (UserRuntimeConfig, error) {
	cfg, err := normalizeConfig(in.Config)
	if err != nil {
		return UserRuntimeConfig{}, err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return UserRuntimeConfig{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The row is created on first save; the lock serialises writers per user.
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_runtime_configs (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, operator); err != nil {
		return UserRuntimeConfig{}, err
	}
	cur, err := r.get(ctx, tx, operator, true)
	if err != nil {
		return UserRuntimeConfig{}, err
	}
	if cur.Revision != in.IfRevision {
		return UserRuntimeConfig{}, apierror.Conflict("runtime revision changed; reload and retry")
	}
	var out UserRuntimeConfig
	if uerr := tx.GetContext(ctx, &out, `UPDATE user_runtime_configs SET revision=revision+1, config=$2, updated_at=now()
		WHERE user_id=$1 RETURNING user_id, revision, config, updated_at`, operator, string(cfg)); uerr != nil {
		return UserRuntimeConfig{}, uerr
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_runtime_history (user_id, revision, kind, detail, author_type, author_id)
		VALUES ($1,$2,'config',$3,$4,$5)`, operator, out.Revision, string(cfg), authorType, author); err != nil {
		return UserRuntimeConfig{}, err
	}
	return out, tx.Commit()
}

// Save replaces the operator's document with optimistic revision check.
func (r *UserRuntimeRepo) Save(ctx context.Context, actor UserRuntimeActor, in UserRuntimeSave) (UserRuntimeConfig, error) {
	if err := operatorOnly(actor); err != nil {
		return UserRuntimeConfig{}, err
	}
	return r.save(ctx, actor.UserID, "user", actor.UserID, in)
}

// isManager reports whether the agent holds a live runtime_manager assignment
// from its own operator, and returns that operator.
func (r *UserRuntimeRepo) isManager(ctx context.Context, q sqlx.QueryerContext, agent uuid.UUID) (operator uuid.UUID, ok bool, err error) {
	err = sqlx.GetContext(ctx, q, &operator, `SELECT m.operator_user_id FROM user_runtime_managers m
		JOIN agents a ON a.id=m.agent_id AND a.operator_user_id=m.operator_user_id
		WHERE m.agent_id=$1 AND m.revoked_at IS NULL AND a.deleted_at IS NULL`, agent)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return operator, err == nil, err
}

func managerOnly(actor UserRuntimeActor) error {
	if actor.Connector || actor.AgentID == uuid.Nil {
		return apierror.Forbidden("runtime_manager assignment requires an agent key")
	}
	return nil
}

var errNotManager = apierror.Forbidden("agent is not a runtime_manager of its operator")

// SaveAsManager lets an assigned agent edit its operator's document with its key.
func (r *UserRuntimeRepo) SaveAsManager(ctx context.Context, actor UserRuntimeActor, in UserRuntimeSave) (UserRuntimeConfig, error) {
	if err := managerOnly(actor); err != nil {
		return UserRuntimeConfig{}, err
	}
	operator, ok, err := r.isManager(ctx, r.db, actor.AgentID)
	if err != nil {
		return UserRuntimeConfig{}, err
	}
	if !ok {
		return UserRuntimeConfig{}, errNotManager
	}
	return r.save(ctx, operator, "agent", actor.AgentID, in)
}

// History returns the newest changes first, with authors.
func (r *UserRuntimeRepo) History(ctx context.Context, actor UserRuntimeActor, limit int) ([]UserRuntimeHistory, error) {
	if err := operatorOnly(actor); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := []UserRuntimeHistory{}
	err := r.db.SelectContext(ctx, &out, `SELECT id, revision, kind, detail, author_type, author_id, created_at
		FROM user_runtime_history WHERE user_id=$1 ORDER BY id DESC LIMIT $2`, actor.UserID, limit)
	return out, err
}

func (r *UserRuntimeRepo) setLimits(ctx context.Context, operator uuid.UUID, authorType string, author, agent uuid.UUID, limit int) error {
	if limit < 0 || limit > maxConcurrentTasksCap {
		return apierror.BadRequest("max_concurrent_tasks out of range")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET max_concurrent_tasks=$3, updated_at=now()
		WHERE id=$1 AND operator_user_id=$2 AND deleted_at IS NULL`, agent, operator, limit)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("Agent")
	}
	cur, err := r.get(ctx, tx, operator, false)
	if err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"agent_id": agent, "max_concurrent_tasks": limit})
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_runtime_history (user_id, revision, kind, detail, author_type, author_id)
		VALUES ($1,$2,'limits',$3,$4,$5)`, operator, cur.Revision, string(detail), authorType, author); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLimits: the operator sets max_concurrent_tasks of one of their agents.
func (r *UserRuntimeRepo) SetLimits(ctx context.Context, actor UserRuntimeActor, agent uuid.UUID, limit int) error {
	if err := operatorOnly(actor); err != nil {
		return err
	}
	return r.setLimits(ctx, actor.UserID, "user", actor.UserID, agent, limit)
}

// SetLimitsAsManager: an assigned agent does the same for agents of its operator.
func (r *UserRuntimeRepo) SetLimitsAsManager(ctx context.Context, actor UserRuntimeActor, agent uuid.UUID, limit int) error {
	if err := managerOnly(actor); err != nil {
		return err
	}
	operator, ok, err := r.isManager(ctx, r.db, actor.AgentID)
	if err != nil {
		return err
	}
	if !ok {
		return errNotManager
	}
	return r.setLimits(ctx, operator, "agent", actor.AgentID, agent, limit)
}

// Managers lists live assignments of the operator.
func (r *UserRuntimeRepo) Managers(ctx context.Context, actor UserRuntimeActor) ([]UserRuntimeManager, error) {
	if err := operatorOnly(actor); err != nil {
		return nil, err
	}
	out := []UserRuntimeManager{}
	err := r.db.SelectContext(ctx, &out, `SELECT m.agent_id, a.name AS agent_name, m.granted_at
		FROM user_runtime_managers m JOIN agents a ON a.id=m.agent_id
		WHERE m.operator_user_id=$1 AND m.revoked_at IS NULL ORDER BY m.granted_at LIMIT 100`, actor.UserID)
	return out, err
}

func (r *UserRuntimeRepo) managerChange(ctx context.Context, actor UserRuntimeActor, agent uuid.UUID, grant bool) error {
	if err := operatorOnly(actor); err != nil {
		return err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var own bool
	if oerr := tx.GetContext(ctx, &own, `SELECT EXISTS (SELECT 1 FROM agents WHERE id=$1 AND operator_user_id=$2 AND deleted_at IS NULL)`, agent, actor.UserID); oerr != nil {
		return oerr
	}
	if !own {
		return apierror.NotFound("Agent")
	}
	kind := "manager_revoked"
	if grant {
		kind = "manager_granted"
		_, err = tx.ExecContext(ctx, `INSERT INTO user_runtime_managers (operator_user_id, agent_id) VALUES ($1,$2)
			ON CONFLICT (operator_user_id, agent_id) DO UPDATE SET revoked_at=NULL, granted_at=now()`, actor.UserID, agent)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE user_runtime_managers SET revoked_at=now()
			WHERE operator_user_id=$1 AND agent_id=$2 AND revoked_at IS NULL`, actor.UserID, agent)
	}
	if err != nil {
		return err
	}
	cur, err := r.get(ctx, tx, actor.UserID, false)
	if err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"agent_id": agent})
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_runtime_history (user_id, revision, kind, detail, author_type, author_id)
		VALUES ($1,$2,$3,$4,'user',$1)`, actor.UserID, cur.Revision, kind, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *UserRuntimeRepo) GrantManager(ctx context.Context, actor UserRuntimeActor, agent uuid.UUID) error {
	return r.managerChange(ctx, actor, agent, true)
}

func (r *UserRuntimeRepo) RevokeManager(ctx context.Context, actor UserRuntimeActor, agent uuid.UUID) error {
	return r.managerChange(ctx, actor, agent, false)
}

// AgentRuntime answers GET /agents/me/runtime: the agent's profile, the operator
// resources it references, and its limits. An agent with no operator gets an
// empty, valid answer so the workspace-catalog path keeps working.
func (r *UserRuntimeRepo) AgentRuntime(ctx context.Context, actor UserRuntimeActor) (*AgentRuntimeView, error) {
	if actor.Connector || actor.AgentID == uuid.Nil {
		return nil, apierror.Forbidden("runtime requires an agent key")
	}
	var a struct {
		Operator *uuid.UUID `db:"operator_user_id"`
		Max      int        `db:"max_concurrent_tasks"`
	}
	if err := r.db.GetContext(ctx, &a, `SELECT operator_user_id, max_concurrent_tasks FROM agents WHERE id=$1 AND deleted_at IS NULL`, actor.AgentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Agent")
		}
		return nil, err
	}
	view := &AgentRuntimeView{AgentID: actor.AgentID, OperatorUserID: a.Operator, Limits: AgentRuntimeLim{MaxConcurrentTasks: a.Max},
		Profile: json.RawMessage(`null`), Resources: json.RawMessage(`{}`)}
	if a.Operator == nil {
		return view, nil
	}
	_, mgr, err := r.isManager(ctx, r.db, actor.AgentID)
	if err != nil {
		return nil, err
	}
	view.RuntimeManager = mgr
	cur, err := r.get(ctx, r.db, *a.Operator, false)
	if err != nil {
		return nil, err
	}
	view.Revision, view.OperatorResolved = cur.Revision, true
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(cur.Config, &doc)
	var agents map[string]json.RawMessage
	_ = json.Unmarshal(doc["agents"], &agents)
	if p, ok := agents[actor.AgentID.String()]; ok {
		view.Profile = p
	}
	view.Resources = referencedResources(doc, view.Profile)
	return view, nil
}

// accountFields is the closed set of account attributes; anything else (a generic
// "value" field, nested blobs) is refused so a plaintext secret has nowhere to hide.
var accountFields = map[string]bool{
	"credential": true, "cred": true, "provider": true, "kind": true, "label": true,
	"description": true, "enabled": true, "base_url": true, "plan": true,
}

// validateAccounts checks every account against accountFields: scalar values only,
// and the credential must be a cred: reference.
func validateAccounts(raw any) error {
	accounts, _ := raw.(map[string]any)
	for name, v := range accounts {
		fields, ok := v.(map[string]any)
		if !ok {
			return apierror.BadRequest("account " + name + " must be an object")
		}
		for k, fv := range fields {
			if !accountFields[strings.ToLower(k)] {
				return apierror.BadRequest("account " + name + ": unsupported field " + k)
			}
			switch fv.(type) {
			case string, bool, float64, nil:
			default:
				return apierror.BadRequest("account " + name + ": field " + k + " must be a scalar")
			}
		}
	}
	return nil
}

// stringsAt returns the string, or the strings of the list, stored under key.
func stringsAt(obj map[string]any, key string) []string {
	switch t := obj[key].(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, c := range t {
			if s, ok := c.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// referencedResources returns only what the agent's profile reaches by explicit
// reference fields: the profiles it names (profile, primary_profiles, quota_edges
// targets), the pools and accounts those profiles name (pool, pools, account,
// accounts), and the accounts of those pools. Other string values never select
// a resource.
func referencedResources(doc map[string]json.RawMessage, profile json.RawMessage) json.RawMessage {
	var prof struct {
		Profile         string   `json:"profile"`
		PrimaryProfiles []string `json:"primary_profiles"`
		QuotaEdges      []struct {
			To string `json:"to"`
		} `json:"quota_edges"`
	}
	_ = json.Unmarshal(profile, &prof)
	want := map[string]bool{prof.Profile: true}
	for _, p := range prof.PrimaryProfiles {
		want[p] = true
	}
	for _, e := range prof.QuotaEdges {
		want[e.To] = true
	}
	section := func(name string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(doc[name], &m)
		return m
	}
	profiles, pools, accounts := section("profiles"), section("pools"), section("accounts")
	pickedProfiles := map[string]json.RawMessage{}
	wantPools, wantAccounts := map[string]bool{}, map[string]bool{}
	for name, v := range profiles {
		if !want[name] {
			continue
		}
		pickedProfiles[name] = v
		var obj map[string]any
		_ = json.Unmarshal(v, &obj)
		for _, k := range []string{"pool", "pools"} {
			for _, s := range stringsAt(obj, k) {
				wantPools[s] = true
			}
		}
		for _, k := range []string{"account", "accounts"} {
			for _, s := range stringsAt(obj, k) {
				wantAccounts[s] = true
			}
		}
	}
	pickedPools := map[string]json.RawMessage{}
	for name, v := range pools {
		if !wantPools[name] {
			continue
		}
		pickedPools[name] = v
		var obj map[string]any
		_ = json.Unmarshal(v, &obj)
		for _, s := range stringsAt(obj, "accounts") {
			wantAccounts[s] = true
		}
	}
	pickedAccounts := map[string]json.RawMessage{}
	for name, v := range accounts {
		if wantAccounts[name] {
			pickedAccounts[name] = v
		}
	}
	out, _ := json.Marshal(map[string]any{"profiles": pickedProfiles, "pools": pickedPools, "accounts": pickedAccounts})
	return out
}

// RuntimeController is the live-or-lapsed lease of the runner that drives an agent.
type RuntimeController struct {
	RunnerID       string    `json:"runner_id"`
	Name           string    `json:"name"`
	Host           string    `json:"host"`
	Version        string    `json:"version"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Live           bool      `json:"live"`
}

// OperatorAgent is one row of GET /me/runtime/agents.
type OperatorAgent struct {
	AgentID            uuid.UUID          `json:"agent_id"`
	Name               string             `json:"name"`
	OperatorUserID     *uuid.UUID         `json:"operator_user_id"`
	MaxConcurrentTasks int                `json:"max_concurrent_tasks"`
	RuntimeManager     bool               `json:"runtime_manager"`
	Controller         *RuntimeController `json:"controller"`
}

type RunnerAgent struct {
	AgentID uuid.UUID `json:"agent_id"`
	Name    string    `json:"name"`
}

// OperatorRunner is one row of GET /me/runtime/runners: the operator's agents
// grouped by the runner that holds their lease. The controller table keeps only
// the lease expiry (renewed by heartbeats), so that is the freshness signal;
// live = the lease has not lapsed.
type OperatorRunner struct {
	RunnerID       string        `json:"runner_id"`
	Name           string        `json:"name"`
	Host           string        `json:"host"`
	Version        string        `json:"version"`
	LeaseExpiresAt time.Time     `json:"lease_expires_at"`
	Live           bool          `json:"live"`
	Agents         []RunnerAgent `json:"agents"`
}

type operatorAgentRow struct {
	AgentID        uuid.UUID  `db:"agent_id"`
	Name           string     `db:"name"`
	OperatorUserID *uuid.UUID `db:"operator_user_id"`
	MaxConcurrent  int        `db:"max_concurrent_tasks"`
	RuntimeManager bool       `db:"runtime_manager"`
	RunnerID       *string    `db:"runner_id"`
	RunnerName     *string    `db:"runner_name"`
	Host           *string    `db:"host"`
	Version        *string    `db:"version"`
	LeaseExpiresAt *time.Time `db:"lease_expires_at"`
	Live           *bool      `db:"live"`
}

// effectiveControllerSQL is a LEFT JOIN LATERAL fragment (alias ctl, outer alias
// a) naming the runner that drives agent a. An explicit agent_controllers row
// with a live lease wins. Otherwise the controller is derived from the agent's
// enabled admission plus the freshest report of that admission's integration:
// runner = controller_ref, host and version from the catalog entry, and the
// lease end = received_at + the catalog's heartbeat_max_age_seconds (120 s when
// the entry is missing). An agent without an enabled admission has no derived
// controller; a lapsed explicit row is the last resort.
const effectiveControllerSQL = `LEFT JOIN LATERAL (
		SELECT u.runner_id, u.runner_name, u.host, u.version, u.lease_expires_at FROM (
			SELECT c.runner_id::text AS runner_id, c.runner_name::text AS runner_name, c.host::text AS host,
				c.version::text AS version, c.lease_expires_at, 0 AS pri
			FROM agent_controllers c WHERE c.agent_id = a.id AND c.lease_expires_at > now()
			UNION ALL
			(SELECT rep.controller_ref, rep.controller_ref,
				COALESCE(cat.ctrl->>'host', ''), COALESCE(cat.ctrl->>'version', ''),
				rep.received_at + make_interval(secs => COALESCE(NULLIF((cat.ctrl->>'heartbeat_max_age_seconds')::int, 0), 120)), 1
			FROM runtime_binding_admissions adm
			JOIN runtime_controller_reports rep ON rep.integration_id = adm.integration_id
			LEFT JOIN LATERAL (SELECT config->'controllers'->rep.controller_ref AS ctrl FROM runtime_catalog_revisions
				WHERE integration_id = rep.integration_id ORDER BY revision DESC LIMIT 1) cat ON true
			WHERE adm.agent_id = a.id AND adm.enabled
			ORDER BY rep.received_at DESC LIMIT 1)
			UNION ALL
			SELECT c.runner_id::text, c.runner_name::text, c.host::text, c.version::text, c.lease_expires_at, 2
			FROM agent_controllers c WHERE c.agent_id = a.id AND c.lease_expires_at <= now()
		) u ORDER BY u.pri, u.lease_expires_at DESC LIMIT 1
	) ctl ON true`

// operatorAgentsSQL lists the agents of workspace $2 the user $1 operates, and,
// when $3 (the caller is the workspace's owner or admin), every agent of the
// workspace its runner drives: an enabled admission on the workspace's runner
// integration. Agents of the same operator in other workspaces stay out.
// No LIMIT: a workspace's agents are a bounded set, and a silent cut would hide
// agents and runners from the screen without any signal.
const operatorAgentsSQL = `SELECT a.id AS agent_id, a.name, a.operator_user_id, a.max_concurrent_tasks,
		EXISTS (SELECT 1 FROM user_runtime_managers m
			WHERE m.operator_user_id=a.operator_user_id AND m.agent_id=a.id AND m.revoked_at IS NULL) AS runtime_manager,
		ctl.runner_id, ctl.runner_name, ctl.host, ctl.version, ctl.lease_expires_at,
		(ctl.lease_expires_at > now()) AS live
	FROM agents a ` + effectiveControllerSQL + `
	WHERE a.workspace_id=$2 AND a.deleted_at IS NULL
		AND (a.operator_user_id=$1 OR ($3 AND EXISTS (SELECT 1 FROM runtime_binding_admissions adm
			JOIN integration_configs ic ON ic.id=adm.integration_id
			WHERE adm.agent_id=a.id AND adm.enabled
				AND ic.workspace_id=$2 AND ic.provider IN ('runner','agent_runtime'))))
	ORDER BY a.name, a.id`

// WorkspaceAdmin reports whether the user owns or administers the workspace;
// a caller outside it is refused. owner_id wins over a membership row, so an
// owner whose row says "member" keeps the owner's view.
func (r *UserRuntimeRepo) WorkspaceAdmin(ctx context.Context, ws, user uuid.UUID) (bool, error) {
	var role sql.NullString
	err := r.db.GetContext(ctx, &role, `SELECT CASE WHEN w.owner_id=$2 THEN 'owner'
			ELSE (SELECT wm.role::text FROM workspace_members wm WHERE wm.workspace_id=w.id AND wm.user_id=$2) END
		FROM workspaces w WHERE w.id=$1 AND w.deleted_at IS NULL`, ws, user)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !role.Valid) {
		return false, apierror.Forbidden("not a member of this workspace")
	}
	if err != nil {
		return false, err
	}
	return role.String == "owner" || role.String == "admin", nil
}

func (r *UserRuntimeRepo) operatorRows(ctx context.Context, actor UserRuntimeActor, ws uuid.UUID) ([]operatorAgentRow, error) {
	if err := operatorOnly(actor); err != nil {
		return nil, err
	}
	if ws == uuid.Nil {
		return nil, apierror.BadRequest("workspace_id is required")
	}
	admin, err := r.WorkspaceAdmin(ctx, ws, actor.UserID)
	if err != nil {
		return nil, err
	}
	var rows []operatorAgentRow
	err = r.db.SelectContext(ctx, &rows, operatorAgentsSQL, actor.UserID, ws, admin)
	return rows, err
}

// OperatorAgents lists the agents of the workspace the signed-in user operates;
// for its owner or admin, also the agents the workspace's runner drives.
func (r *UserRuntimeRepo) OperatorAgents(ctx context.Context, actor UserRuntimeActor, ws uuid.UUID) ([]OperatorAgent, error) {
	rows, err := r.operatorRows(ctx, actor, ws)
	if err != nil {
		return nil, err
	}
	out := make([]OperatorAgent, 0, len(rows))
	for _, row := range rows {
		a := OperatorAgent{AgentID: row.AgentID, Name: row.Name, OperatorUserID: row.OperatorUserID,
			MaxConcurrentTasks: row.MaxConcurrent, RuntimeManager: row.RuntimeManager}
		if row.RunnerID != nil && row.LeaseExpiresAt != nil {
			a.Controller = &RuntimeController{RunnerID: *row.RunnerID, Name: deref(row.RunnerName), Host: deref(row.Host),
				Version: deref(row.Version), LeaseExpiresAt: *row.LeaseExpiresAt, Live: row.Live != nil && *row.Live}
		}
		out = append(out, a)
	}
	return out, nil
}

// OperatorRunners groups the agents OperatorAgents lists by the runner that leases them.
func (r *UserRuntimeRepo) OperatorRunners(ctx context.Context, actor UserRuntimeActor, ws uuid.UUID) ([]OperatorRunner, error) {
	rows, err := r.operatorRows(ctx, actor, ws)
	if err != nil {
		return nil, err
	}
	out := []OperatorRunner{}
	idx := map[string]int{}
	for _, row := range rows {
		if row.RunnerID == nil || row.LeaseExpiresAt == nil {
			continue
		}
		i, ok := idx[*row.RunnerID]
		if !ok {
			i = len(out)
			idx[*row.RunnerID] = i
			out = append(out, OperatorRunner{RunnerID: *row.RunnerID, Name: deref(row.RunnerName), Host: deref(row.Host),
				Version: deref(row.Version), Agents: []RunnerAgent{}})
		}
		o := &out[i]
		o.Agents = append(o.Agents, RunnerAgent{AgentID: row.AgentID, Name: row.Name})
		if row.LeaseExpiresAt.After(o.LeaseExpiresAt) {
			o.LeaseExpiresAt = *row.LeaseExpiresAt
			o.Name, o.Host, o.Version = deref(row.RunnerName), deref(row.Host), deref(row.Version)
		}
		o.Live = o.Live || (row.Live != nil && *row.Live)
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
