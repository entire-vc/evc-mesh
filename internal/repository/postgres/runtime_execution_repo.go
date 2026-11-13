package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Execution observation telemetry. Everything here is evidence: no function in
// this file admits a binding, takes a lease, reserves a resource or alters the
// catalog, and a failure here never reaches a task, checkout or routing.

const (
	executionReportRetention  = 7 * 24 * time.Hour
	executionMaxAttemptsRead  = 100
	executionDefaultPageLimit = 50
	executionMaxPageLimit     = 200
)

// executionReporter authenticates a controller's exact owner-workspace reporter
// key and returns the locked connection, its catalog and the grant fingerprint.
func (r *RuntimeRepo) executionReporter(ctx context.Context, tx *sqlx.Tx, ws uuid.UUID, ref string, actor RuntimeActor) (*domain.IntegrationConfig, *domain.RuntimeCatalog, domain.RuntimeController, string, error) {
	var zero domain.RuntimeController
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return nil, nil, zero, "", apierror.Forbidden("execution telemetry requires the controller's exact owner-workspace agent key")
	}
	connection, err := runtimeConnection(ctx, tx, ws, true)
	if err != nil {
		return nil, nil, zero, "", err
	}
	if connection == nil {
		return nil, nil, zero, "", apierror.NotFound("Runtime controller")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, nil, zero, "", err
	}
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return nil, nil, zero, "", apierror.NotFound("Runtime controller")
	}
	fingerprint, err := runtimeGrant(ctx, tx, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: controller.ReporterGrantID})
	if err != nil {
		return nil, nil, zero, "", err
	}
	return connection, catalog, controller, fingerprint, nil
}

// RegisterExecutionInstance records one controller boot and returns its
// server-issued generation. It is telemetry fencing only: not a controller
// claim and not a launch or checkout lease. Repeating the same instance_id
// returns the original row and changes nothing.
func (r *RuntimeRepo) RegisterExecutionInstance(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor, req domain.ExecutionInstanceRequest) (*domain.ExecutionInstance, error) {
	if req.SchemaVersion != domain.ExecutionSchemaVersion || !domain.ValidExecutionToken(req.InstanceID) {
		return nil, apierror.BadRequest("invalid execution instance")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	connection, _, controller, fingerprint, err := r.executionReporter(ctx, tx, ws, ref, actor)
	if err != nil {
		return nil, err
	}
	out := domain.ExecutionInstance{SchemaVersion: domain.ExecutionSchemaVersion, InstanceID: req.InstanceID}
	var existing struct {
		Generation int64     `db:"generation"`
		ReceivedAt time.Time `db:"received_at"`
	}
	err = tx.GetContext(ctx, &existing, `SELECT generation,received_at FROM runtime_execution_instances WHERE integration_id=$1 AND controller_ref=$2 AND instance_id=$3`, connection.ID, ref, req.InstanceID)
	switch {
	case err == nil:
		out.Generation, out.ReceivedAt = existing.Generation, existing.ReceivedAt
		return &out, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	// The connection row is locked FOR UPDATE, so concurrent boots serialize
	// here and generations stay strictly monotone.
	err = tx.GetContext(ctx, &out.Generation, `INSERT INTO runtime_execution_instances(integration_id,controller_ref,instance_id,generation,reporter_grant_id,grant_fingerprint)
		SELECT $1,$2,$3,COALESCE(MAX(generation),0)+1,$4,$5 FROM runtime_execution_instances WHERE integration_id=$1 AND controller_ref=$2
		RETURNING generation`, connection.ID, ref, req.InstanceID, controller.ReporterGrantID, fingerprint)
	if err != nil {
		return nil, err
	}
	if err := tx.GetContext(ctx, &out.ReceivedAt, `SELECT received_at FROM runtime_execution_instances WHERE integration_id=$1 AND controller_ref=$2 AND instance_id=$3`, connection.ID, ref, req.InstanceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExecutionReportAck is the answer to a report; like every runtime answer it
// never authorizes a launch.
type ExecutionReportAck struct {
	Recorded         bool `json:"recorded"`
	Duplicate        bool `json:"duplicate"`
	Attempts         int  `json:"attempts"`
	LaunchAuthorized bool `json:"launch_authorized"`
}

type executionStored struct {
	TurnID     *string   `db:"turn_id"`
	State      string    `db:"state"`
	Generation int64     `db:"generation"`
	ObservedAt time.Time `db:"observed_at"`
}

// ReportExecutions stores one batch atomically. Order of checks: reporter key,
// instance/generation fencing, sequence/replay, then each row's binding,
// project and membership. A batch with any foreign or invalid row stores
// nothing.
func (r *RuntimeRepo) ReportExecutions(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor, report domain.ExecutionReport) (*ExecutionReportAck, error) {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return nil, apierror.Forbidden("execution telemetry requires the controller's exact owner-workspace agent key")
	}
	now := time.Now()
	if err := report.Validate(now, ref); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	connection, catalog, controller, fingerprint, err := r.executionReporter(ctx, tx, ws, ref, actor)
	if err != nil {
		return nil, err
	}
	var registered int64
	err = tx.GetContext(ctx, &registered, `SELECT generation FROM runtime_execution_instances WHERE integration_id=$1 AND controller_ref=$2 AND instance_id=$3`, connection.ID, ref, report.InstanceID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && registered != report.Generation) {
		return nil, apierror.Conflict("execution instance is not registered with this generation")
	}
	if err != nil {
		return nil, err
	}
	var newest int64
	err = tx.GetContext(ctx, &newest, `SELECT COALESCE(MAX(generation),0) FROM runtime_execution_instances WHERE integration_id=$1 AND controller_ref=$2`, connection.ID, ref)
	if err != nil {
		return nil, err
	}
	if newest > report.Generation {
		return nil, apierror.Conflict("execution instance was superseded by a newer boot")
	}
	hash := report.BodyHash()
	var storedHash string
	err = tx.GetContext(ctx, &storedHash, `SELECT body_hash FROM runtime_execution_reports WHERE integration_id=$1 AND controller_ref=$2 AND instance_id=$3 AND sequence=$4`, connection.ID, ref, report.InstanceID, report.Sequence)
	switch {
	case err == nil && storedHash == hash:
		return &ExecutionReportAck{Recorded: true, Duplicate: true, Attempts: len(report.Attempts)}, nil
	case err == nil:
		return nil, apierror.Conflict("execution report sequence was already used with a different body")
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var last int64
	err = tx.GetContext(ctx, &last, `SELECT COALESCE(MAX(sequence),0) FROM runtime_execution_reports WHERE integration_id=$1 AND controller_ref=$2 AND instance_id=$3`, connection.ID, ref, report.InstanceID)
	if err != nil {
		return nil, err
	}
	if report.Sequence <= last {
		return nil, apierror.Conflict("execution report sequence must increase")
	}

	checked := map[string]bool{}
	for _, a := range report.Attempts {
		err = r.checkExecutionAttempt(ctx, tx, connection, catalog, ref, a, checked)
		if err != nil {
			return nil, err
		}
		var prev executionStored
		err = tx.GetContext(ctx, &prev, `SELECT turn_id,state,generation,observed_at FROM runtime_execution_attempts WHERE integration_id=$1 AND controller_ref=$2 AND project_id=$3 AND agent_id=$4 AND attempt_id=$5`, connection.ID, ref, a.ProjectID, a.AgentID, a.AttemptID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, err
		default:
			if domain.ExecutionTerminal(prev.State) && !domain.ExecutionTerminal(a.State) {
				return nil, apierror.Conflict("a finished attempt cannot run again; a resumed session is a new attempt")
			}
			// A new turn starts with an unknown model/effort: it may only
			// name one that was observed after the previous observation.
			if !sameTurn(prev.TurnID, a.TurnID) && (a.Observed.Model != nil || a.Observed.Effort != nil) && !a.Observed.ObservedAt.Truncate(time.Microsecond).After(prev.ObservedAt.Truncate(time.Microsecond)) {
				return nil, apierror.BadRequest("a new turn needs its own observation of model and effort")
			}
		}
		prov, merr := json.Marshal(a.Provenance)
		if merr != nil {
			return nil, merr
		}
		if a.Provenance == nil {
			prov = nil
		}
		rr, o := a.ResolvedRoute, a.Observed
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_execution_attempts(integration_id,controller_ref,project_id,agent_id,attempt_id,workspace_id,binding_ref,task_id,instance_id,generation,sequence,turn_id,state,
				route_source,route_revision,route_digest,route_profile_ref,route_harness,route_model,route_effort,
				observed_harness,observed_model,observed_effort,observed_source,observed_at,provenance,reporter_grant_id,grant_fingerprint,heartbeat_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)
			ON CONFLICT(integration_id,controller_ref,project_id,agent_id,attempt_id) DO UPDATE SET
				binding_ref=EXCLUDED.binding_ref,task_id=EXCLUDED.task_id,instance_id=EXCLUDED.instance_id,generation=EXCLUDED.generation,sequence=EXCLUDED.sequence,turn_id=EXCLUDED.turn_id,state=EXCLUDED.state,
				route_source=EXCLUDED.route_source,route_revision=EXCLUDED.route_revision,route_digest=EXCLUDED.route_digest,route_profile_ref=EXCLUDED.route_profile_ref,route_harness=EXCLUDED.route_harness,route_model=EXCLUDED.route_model,route_effort=EXCLUDED.route_effort,
				observed_harness=EXCLUDED.observed_harness,observed_model=EXCLUDED.observed_model,observed_effort=EXCLUDED.observed_effort,observed_source=EXCLUDED.observed_source,observed_at=EXCLUDED.observed_at,provenance=EXCLUDED.provenance,
				reporter_grant_id=EXCLUDED.reporter_grant_id,grant_fingerprint=EXCLUDED.grant_fingerprint,heartbeat_at=EXCLUDED.heartbeat_at,received_at=now()
			WHERE runtime_execution_attempts.generation < EXCLUDED.generation
			   OR (runtime_execution_attempts.generation = EXCLUDED.generation AND runtime_execution_attempts.sequence < EXCLUDED.sequence)`,
			connection.ID, ref, a.ProjectID, a.AgentID, a.AttemptID, a.WorkspaceID, a.BindingRef, a.TaskID, a.InstanceID, a.Generation, report.Sequence, a.TurnID, a.State,
			rr.Source, rr.Revision, rr.Digest, rr.ProfileRef, rr.Harness, rr.Model, rr.Effort,
			o.Harness, o.Model, o.Effort, o.Source, o.ObservedAt, prov, controller.ReporterGrantID, fingerprint, report.ReportedAt)
		if err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_execution_reports(integration_id,controller_ref,instance_id,sequence,body_hash,reported_at) VALUES($1,$2,$3,$4,$5,$6)`, connection.ID, ref, report.InstanceID, report.Sequence, hash, report.ReportedAt); err != nil {
		return nil, err
	}
	// Bounded storage: envelopes and finished attempts past retention go; the
	// newest envelope of every instance stays so replay detection keeps working.
	cutoff := now.Add(-executionReportRetention)
	if _, err = tx.ExecContext(ctx, `DELETE FROM runtime_execution_reports r WHERE r.integration_id=$1 AND r.controller_ref=$2 AND r.received_at < $3
		AND r.sequence < (SELECT MAX(sequence) FROM runtime_execution_reports m WHERE m.integration_id=r.integration_id AND m.controller_ref=r.controller_ref AND m.instance_id=r.instance_id)`, connection.ID, ref, cutoff); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM runtime_execution_attempts WHERE integration_id=$1 AND controller_ref=$2 AND state IN ('stopped','completed','failed') AND received_at < $3`, connection.ID, ref, cutoff); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ExecutionReportAck{Recorded: true, Attempts: len(report.Attempts)}, nil
}

func taskKey(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func sameTurn(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// checkExecutionAttempt verifies one row: admitted binding → receiver
// workspace/agent, project membership and Runner project enablement. The
// answer for a foreign identity is the same generic 403 whatever part failed,
// so a reporter learns nothing about another workspace.
func (r *RuntimeRepo) checkExecutionAttempt(ctx context.Context, tx *sqlx.Tx, connection *domain.IntegrationConfig, catalog *domain.RuntimeCatalog, ref string, a domain.ExecutionAttemptReport, checked map[string]bool) error {
	denied := apierror.Forbidden("execution attempt is outside this controller's admitted scope")
	cacheKey := strings.Join([]string{a.BindingRef, a.WorkspaceID.String(), a.ProjectID.String(), a.AgentID.String(), taskKey(a.TaskID), strconv.FormatBool(domain.ExecutionTerminal(a.State))}, "|")
	if checked[cacheKey] {
		return nil
	}
	binding, ok := catalog.Bindings[a.BindingRef]
	if !ok || binding.Binding.WorkspaceID != a.WorkspaceID || binding.Binding.AgentID != a.AgentID {
		return denied
	}
	serves := false
	for _, p := range binding.PermittedProfiles {
		serves = serves || catalog.Profiles[p].ControllerRef == ref
	}
	if !serves {
		return denied
	}
	fingerprint, err := runtimeGrant(ctx, tx, binding.Binding)
	if err != nil {
		var forbidden *apierror.Error
		if errors.As(err, &forbidden) {
			return denied
		}
		return err
	}
	_, digest, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return err
	}
	admission, err := runtimeAdmission(ctx, tx, connection.ID, a.BindingRef, binding, fingerprint, digest)
	if err != nil {
		return err
	}
	if len(admission.PermittedProfiles) == 0 {
		return denied
	}
	// A finished attempt may still be closed after the project turned the
	// Runner off; a live one needs the project enabled.
	var allowed bool
	err = tx.GetContext(ctx, &allowed, `SELECT EXISTS (
		SELECT 1 FROM projects p JOIN project_members m ON m.project_id=p.id AND m.agent_id=$3
		WHERE p.id=$1 AND p.workspace_id=$2 AND p.deleted_at IS NULL
		AND ($4 OR EXISTS (SELECT 1 FROM project_integrations pi WHERE pi.project_id=p.id AND pi.extension_id=$5 AND pi.enabled)))`,
		a.ProjectID, a.WorkspaceID, a.AgentID, domain.ExecutionTerminal(a.State), domain.RunnerExtensionID)
	if err != nil {
		return err
	}
	if !allowed {
		return denied
	}
	if a.TaskID != nil {
		var inProject bool
		if err := tx.GetContext(ctx, &inProject, `SELECT EXISTS (SELECT 1 FROM tasks WHERE id=$1 AND project_id=$2)`, *a.TaskID, a.ProjectID); err != nil {
			return err
		}
		if !inProject {
			return denied
		}
	}
	checked[cacheKey] = true
	return nil
}

// ---- read projection ----

// ExecutionFilter narrows the batch read; zero values mean "all allowed".
type ExecutionFilter struct {
	ProjectID *uuid.UUID
	AgentID   *uuid.UUID
	Limit     int
	Cursor    string
}

type executionRoute struct {
	BindingRef    string  `json:"binding_ref"`
	ControllerRef string  `json:"controller_ref"`
	ProfileRef    string  `json:"profile_ref"`
	Harness       string  `json:"harness"`
	Model         string  `json:"model"`
	ModelSource   string  `json:"model_source"`
	Effort        *string `json:"effort"`
	Order         int     `json:"order"`
}

// Where executionRoute.Model comes from: the binding's policy.profile_models
// override, or the profile's own model.
const (
	ModelSourceBinding = "binding"
	ModelSourceProfile = "profile"
)

type ExecutionConfigured struct {
	Source   string           `json:"source"`
	Revision int64            `json:"revision"`
	Digest   string           `json:"digest"`
	Routes   []executionRoute `json:"routes"`
}

type ExecutionProfile struct {
	Source  string  `json:"source"`
	Harness string  `json:"harness"`
	Model   *string `json:"model"`
}

type ExecutionObservedView struct {
	Harness    *string                     `json:"harness"`
	Model      *string                     `json:"model"`
	Effort     *string                     `json:"effort"`
	Source     string                      `json:"source"`
	ObservedAt time.Time                   `json:"observed_at"`
	Provenance *domain.ExecutionProvenance `json:"provenance,omitempty"`
}

type ExecutionAttemptView struct {
	AttemptID     string                        `json:"attempt_id"`
	ControllerRef string                        `json:"controller_ref"`
	BindingRef    string                        `json:"binding_ref"`
	TaskID        *uuid.UUID                    `json:"task_id"`
	InstanceID    string                        `json:"instance_id"`
	Generation    int64                         `json:"generation"`
	Sequence      int64                         `json:"sequence"`
	TurnID        *string                       `json:"turn_id"`
	State         string                        `json:"state"`
	ResolvedRoute domain.ExecutionResolvedRoute `json:"resolved_route"`
	Observed      ExecutionObservedView         `json:"observed"`
	ReceivedAt    time.Time                     `json:"received_at"`
	HeartbeatAt   time.Time                     `json:"heartbeat_at"`
	Stale         bool                          `json:"stale"`
	Freshness     string                        `json:"freshness"`
	PolicyMatches bool                          `json:"policy_matches"`
}

type ExecutionView struct {
	WorkspaceID      uuid.UUID              `json:"workspace_id"`
	ProjectID        uuid.UUID              `json:"project_id"`
	AgentID          uuid.UUID              `json:"agent_id"`
	Managed          bool                   `json:"managed"`
	Enabled          bool                   `json:"enabled"`
	ManagementSource string                 `json:"management_source"`
	Reason           *string                `json:"reason"`
	Configured       *ExecutionConfigured   `json:"configured"`
	Attempts         []ExecutionAttemptView `json:"attempts"`
	// AttemptsTruncated is set when more attempts exist than the read returns.
	AttemptsTruncated bool             `json:"attempts_truncated,omitempty"`
	Profile           ExecutionProfile `json:"profile"`
	Freshness         string           `json:"freshness"`
}

type ExecutionList struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	MaxAgeSeconds int             `json:"max_age_seconds"`
	Items         []ExecutionView `json:"items"`
	NextCursor    *string         `json:"next_cursor"`
}

type executionPair struct {
	Project uuid.UUID `db:"project_id"`
	Agent   uuid.UUID `db:"agent_id"`
}

func encodeExecutionCursor(p executionPair) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.Project.String() + "|" + p.Agent.String()))
}

func decodeExecutionCursor(c string) (executionPair, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	parts := strings.Split(string(raw), "|")
	if err != nil || len(parts) != 2 {
		return executionPair{}, apierror.BadRequest("invalid cursor")
	}
	project, e1 := uuid.Parse(parts[0])
	agent, e2 := uuid.Parse(parts[1])
	if e1 != nil || e2 != nil {
		return executionPair{}, apierror.BadRequest("invalid cursor")
	}
	return executionPair{project, agent}, nil
}

// executionReader resolves who may read the workspace projection. A workspace
// owner/admin reads every project; anyone else reads the projects they are a
// member of. A connector or an agent of another workspace reads nothing.
func executionReader(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, actor RuntimeActor) (admin, isAgent bool, member uuid.UUID, err error) {
	if actor.Connector {
		return false, false, uuid.Nil, apierror.Forbidden("agent executions are not available to connectors")
	}
	if actor.AgentID != uuid.Nil {
		var connected bool
		err = sqlx.GetContext(ctx, q, &connected, `SELECT EXISTS (SELECT 1 FROM agents a WHERE a.id=$1 AND a.deleted_at IS NULL AND (a.workspace_id=$2 OR EXISTS (
			SELECT 1 FROM agent_workspace_grants g WHERE g.agent_id=a.id AND g.workspace_id=$2 AND g.revoked_at IS NULL)))`, actor.AgentID, ws)
		if err != nil {
			return false, false, uuid.Nil, err
		}
		if !connected || (actor.AuthWorkspaceID != uuid.Nil && actor.AuthWorkspaceID != ws) {
			return false, false, uuid.Nil, apierror.Forbidden("agent is not connected to this workspace")
		}
		return false, true, actor.AgentID, nil
	}
	if actor.UserID == uuid.Nil {
		return false, false, uuid.Nil, apierror.Forbidden("authentication required")
	}
	var role struct {
		Member bool `db:"member"`
		Admin  bool `db:"admin"`
	}
	err = sqlx.GetContext(ctx, q, &role, `SELECT
		(w.owner_id=$2 OR EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=w.id AND m.user_id=$2)) AS member,
		(w.owner_id=$2 OR EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=w.id AND m.user_id=$2 AND m.role IN ('owner','admin'))) AS admin
		FROM workspaces w WHERE w.id=$1 AND w.deleted_at IS NULL`, ws, actor.UserID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !role.Member) {
		return false, false, uuid.Nil, apierror.Forbidden("not a member of this workspace")
	}
	if err != nil {
		return false, false, uuid.Nil, err
	}
	return role.Admin, false, actor.UserID, nil
}

type executionRuntime struct {
	connection *domain.IntegrationConfig
	catalog    *domain.RuntimeCatalog
	revision   int64
	digest     string
}

type executionAttemptRow struct {
	ControllerRef   string     `db:"controller_ref"`
	WorkspaceID     uuid.UUID  `db:"workspace_id"`
	FirstSeenAt     time.Time  `db:"first_seen_at"`
	ProjectID       uuid.UUID  `db:"project_id"`
	AgentID         uuid.UUID  `db:"agent_id"`
	AttemptID       string     `db:"attempt_id"`
	BindingRef      string     `db:"binding_ref"`
	TaskID          *uuid.UUID `db:"task_id"`
	InstanceID      string     `db:"instance_id"`
	Generation      int64      `db:"generation"`
	Sequence        int64      `db:"sequence"`
	TurnID          *string    `db:"turn_id"`
	State           string     `db:"state"`
	RouteSource     string     `db:"route_source"`
	RouteRevision   int64      `db:"route_revision"`
	RouteDigest     *string    `db:"route_digest"`
	RouteProfileRef *string    `db:"route_profile_ref"`
	RouteHarness    *string    `db:"route_harness"`
	RouteModel      *string    `db:"route_model"`
	RouteEffort     *string    `db:"route_effort"`
	ObservedHarness *string    `db:"observed_harness"`
	ObservedModel   *string    `db:"observed_model"`
	ObservedEffort  *string    `db:"observed_effort"`
	ObservedSource  string     `db:"observed_source"`
	ObservedAt      time.Time  `db:"observed_at"`
	Provenance      []byte     `db:"provenance"`
	GrantID         uuid.UUID  `db:"reporter_grant_id"`
	Fingerprint     string     `db:"grant_fingerprint"`
	HeartbeatAt     time.Time  `db:"heartbeat_at"`
	ReceivedAt      time.Time  `db:"received_at"`
	IntegrationID   uuid.UUID  `db:"integration_id"`
	RN              int        `db:"rn"`
}

// AgentExecutions is the scoped batch projection: configured policy per
// project and agent next to the observed attempts, never collapsed into one
// model per agent. It reads only; it changes nothing.
func (r *RuntimeRepo) AgentExecutions(ctx context.Context, ws uuid.UUID, actor RuntimeActor, f ExecutionFilter) (*ExecutionList, error) {
	now := time.Now()
	limit := f.Limit
	if limit == 0 {
		limit = executionDefaultPageLimit
	}
	if limit < 1 || limit > executionMaxPageLimit {
		return nil, apierror.BadRequest("limit must be within 1..200")
	}
	var after executionPair
	if f.Cursor != "" {
		var err error
		if after, err = decodeExecutionCursor(f.Cursor); err != nil {
			return nil, err
		}
	}
	admin, isAgent, member, err := executionReader(ctx, r.db, ws, actor)
	if err != nil {
		return nil, err
	}
	if f.ProjectID != nil {
		var inWS bool
		err = r.db.GetContext(ctx, &inWS, `SELECT EXISTS (SELECT 1 FROM projects WHERE id=$1 AND workspace_id=$2 AND deleted_at IS NULL)`, *f.ProjectID, ws)
		if err != nil {
			return nil, err
		}
		if !inWS {
			return nil, apierror.NotFound("Project")
		}
	}

	runtimes, err := r.executionRuntimes(ctx, ws)
	if err != nil {
		return nil, err
	}
	// Agents the Runner may manage for this workspace, from every catalog that
	// binds one of its agents.
	managedAgents := map[uuid.UUID]bool{}
	for _, rt := range runtimes {
		for _, b := range rt.catalog.Bindings {
			if b.Binding.WorkspaceID == ws {
				managedAgents[b.Binding.AgentID] = true
			}
		}
	}
	agentIDs := make([]uuid.UUID, 0, len(managedAgents))
	for id := range managedAgents {
		agentIDs = append(agentIDs, id)
	}

	visibility := `($7::bool OR EXISTS (SELECT 1 FROM project_members v WHERE v.project_id=p.id AND (($9::bool AND v.agent_id=$8::uuid) OR (NOT $9::bool AND v.user_id=$8::uuid))))`
	var pairs []executionPair
	err = r.db.SelectContext(ctx, &pairs, `SELECT project_id, agent_id FROM (
			SELECT p.id AS project_id, m.agent_id FROM projects p JOIN project_members m ON m.project_id=p.id AND m.agent_id = ANY($2::uuid[])
			WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND `+visibility+`
			UNION
			SELECT p.id, e.agent_id FROM projects p JOIN runtime_execution_attempts e ON e.project_id=p.id AND e.workspace_id=$1
			WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND `+visibility+`
		) pairs
		WHERE ($3::uuid IS NULL OR project_id=$3) AND ($4::uuid IS NULL OR agent_id=$4)
		AND ($7::bool OR NOT $9::bool OR agent_id=$8::uuid)
		AND (project_id::text, agent_id::text) > ($5::text, $6::text)
		ORDER BY project_id, agent_id LIMIT $10`,
		ws, pq.Array(agentIDs), f.ProjectID, f.AgentID, nullUUIDText(after.Project), nullUUIDText(after.Agent), admin, member, isAgent, limit+1)
	if err != nil {
		return nil, err
	}
	out := &ExecutionList{SchemaVersion: domain.ExecutionSchemaVersion, GeneratedAt: now.UTC(), MaxAgeSeconds: domain.ExecutionFreshnessSeconds, Items: []ExecutionView{}}
	if len(pairs) > limit {
		pairs = pairs[:limit]
		cursor := encodeExecutionCursor(pairs[len(pairs)-1])
		out.NextCursor = &cursor
	}
	if len(pairs) == 0 {
		return out, nil
	}

	projects := make([]uuid.UUID, 0, len(pairs))
	agents := make([]uuid.UUID, 0, len(pairs))
	for _, p := range pairs {
		projects = append(projects, p.Project)
		agents = append(agents, p.Agent)
	}
	enabledProjects := map[uuid.UUID]bool{}
	var enabledList []uuid.UUID
	err = r.db.SelectContext(ctx, &enabledList, `SELECT project_id FROM project_integrations WHERE extension_id=$1 AND enabled AND project_id = ANY($2::uuid[])`, domain.RunnerExtensionID, pq.Array(projects))
	if err != nil {
		return nil, err
	}
	for _, id := range enabledList {
		enabledProjects[id] = true
	}
	type profileRow struct {
		ID    uuid.UUID `db:"id"`
		Type  string    `db:"agent_type"`
		Model *string   `db:"model"`
	}
	var profileRows []profileRow
	err = r.db.SelectContext(ctx, &profileRows, `SELECT id, agent_type::text AS agent_type, model FROM agents WHERE id = ANY($1::uuid[])`, pq.Array(agents))
	if err != nil {
		return nil, err
	}
	profiles := map[uuid.UUID]ExecutionProfile{}
	for _, p := range profileRows {
		profiles[p.ID] = ExecutionProfile{Source: "agent_profile", Harness: p.Type, Model: p.Model}
	}
	var rows []executionAttemptRow
	err = r.db.SelectContext(ctx, &rows, `SELECT integration_id, controller_ref, workspace_id, first_seen_at, project_id, agent_id, attempt_id, binding_ref, task_id, instance_id, generation, sequence, turn_id, state, route_source, route_revision, route_digest, route_profile_ref, route_harness, route_model, route_effort, observed_harness, observed_model, observed_effort, observed_source, observed_at, provenance, reporter_grant_id, grant_fingerprint, heartbeat_at, received_at FROM (
			SELECT e.integration_id, e.controller_ref, e.workspace_id, e.first_seen_at, e.project_id, e.agent_id, e.attempt_id, e.binding_ref, e.task_id, e.instance_id, e.generation, e.sequence, e.turn_id, e.state, e.route_source, e.route_revision, e.route_digest, e.route_profile_ref, e.route_harness, e.route_model, e.route_effort, e.observed_harness, e.observed_model, e.observed_effort, e.observed_source, e.observed_at, e.provenance, e.reporter_grant_id, e.grant_fingerprint, e.heartbeat_at, e.received_at, row_number() OVER (PARTITION BY e.project_id, e.agent_id ORDER BY e.received_at DESC, e.attempt_id) AS rn
			FROM runtime_execution_attempts e
			JOIN unnest($2::uuid[], $3::uuid[]) AS want(project_id, agent_id) ON want.project_id=e.project_id AND want.agent_id=e.agent_id
			WHERE e.workspace_id=$1
		) x WHERE rn <= $4`, ws, pq.Array(projects), pq.Array(agents), executionMaxAttemptsRead+1)
	if err != nil {
		return nil, err
	}
	live, err := r.executionControllerLiveness(ctx, runtimes)
	if err != nil {
		return nil, err
	}

	byPair := map[executionPair][]executionAttemptRow{}
	for _, row := range rows {
		k := executionPair{row.ProjectID, row.AgentID}
		byPair[k] = append(byPair[k], row)
	}
	for _, p := range pairs {
		view := ExecutionView{WorkspaceID: ws, ProjectID: p.Project, AgentID: p.Agent, ManagementSource: "runtime_policy", Attempts: []ExecutionAttemptView{}, Profile: profiles[p.Agent], Freshness: domain.FreshnessUnknown}
		if view.Profile.Source == "" {
			view.Profile.Source = "agent_profile"
		}
		var chosen *executionRuntime
		var routes []executionRoute
		for _, rt := range runtimes {
			for _, ref := range sortedBindingRefs(rt.catalog) {
				b := rt.catalog.Bindings[ref]
				if b.Binding.WorkspaceID != ws || b.Binding.AgentID != p.Agent {
					continue
				}
				admitted, aerr := r.executionAdmitted(ctx, rt, ref, b)
				if aerr != nil {
					return nil, aerr
				}
				if chosen == nil {
					chosen = rt
				}
				if chosen == rt {
					routes = append(routes, configuredRoutes(rt.catalog, ref, b, admitted)...)
				}
				view.Enabled = view.Enabled || (rt.connection.IsActive && admitted.Enabled)
			}
		}
		view.Managed = chosen != nil && enabledProjects[p.Project]
		view.Enabled = view.Managed && view.Enabled
		view.Reason = executionReason(chosen != nil, view.Managed, view.Enabled)
		if view.Managed {
			for i := range routes {
				routes[i].Order = i
			}
			view.Configured = &ExecutionConfigured{Source: "runtime_policy", Revision: chosen.revision, Digest: chosen.digest, Routes: routes}
		}
		attempts := byPair[p]
		if len(attempts) > executionMaxAttemptsRead {
			// One row past the cap proves the list is cut; the oldest goes.
			sort.Slice(attempts, func(i, j int) bool { return attempts[i].ReceivedAt.After(attempts[j].ReceivedAt) })
			attempts = attempts[:executionMaxAttemptsRead]
			view.AttemptsTruncated = true
		}
		sort.Slice(attempts, func(i, j int) bool {
			if attempts[i].ControllerRef != attempts[j].ControllerRef {
				return attempts[i].ControllerRef < attempts[j].ControllerRef
			}
			return attempts[i].AttemptID < attempts[j].AttemptID
		})
		for _, row := range attempts {
			view.Attempts = append(view.Attempts, executionAttemptView(now, row, view, live, runtimes))
		}
		view.Freshness = aggregateFreshness(view)
		out.Items = append(out.Items, view)
	}
	return out, nil
}

func nullUUIDText(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func execStrp(s string) *string { return &s }

// executionReason says why a project view is not a managed, enabled route.
// integration_disabled: a Runner binding exists but the project's agent-runner
// integration is off, so the UI can name the cause and point at the switch.
func executionReason(hasBinding, managed, enabled bool) *string {
	switch {
	case hasBinding && !managed:
		return execStrp("integration_disabled")
	case !managed:
		return execStrp("unmanaged")
	case !enabled:
		return execStrp("disabled")
	}
	return nil
}

func sortedBindingRefs(c *domain.RuntimeCatalog) []string {
	refs := make([]string, 0, len(c.Bindings))
	for ref := range c.Bindings {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// executionRuntimes lists the Runner connections whose catalog may bind this
// workspace's agents: its own and those admitting one of its agents.
func (r *RuntimeRepo) executionRuntimes(ctx context.Context, ws uuid.UUID) ([]*executionRuntime, error) {
	var ids []uuid.UUID
	err := r.db.SelectContext(ctx, &ids, `SELECT DISTINCT id FROM (
			SELECT integration_id AS id FROM runtime_binding_admissions WHERE workspace_id=$1
			UNION SELECT id FROM integration_configs WHERE workspace_id=$1 AND provider IN ('runner','agent_runtime')
		) c ORDER BY id`, ws)
	if err != nil {
		return nil, err
	}
	var out []*executionRuntime
	for _, id := range ids {
		var row integrationConfigRow
		err = r.db.GetContext(ctx, &row, `SELECT `+integrationConfigSelectCols+` FROM integration_configs WHERE id=$1 AND provider IN ('runner','agent_runtime')`, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		connection := row.toDomain()
		catalog, perr := domain.ParseRuntimeCatalog(connection.Config)
		if perr != nil {
			continue // an unreadable catalog manages nothing
		}
		revision, digest, rerr := runtimeRevision(ctx, r.db, connection.ID)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, &executionRuntime{connection: &connection, catalog: catalog, revision: revision, digest: digest})
	}
	return out, nil
}

func (r *RuntimeRepo) executionAdmitted(ctx context.Context, rt *executionRuntime, ref string, b domain.RuntimeBinding) (RuntimeAdmission, error) {
	fingerprint, err := runtimeGrant(ctx, r.db, b.Binding)
	if err != nil {
		var denied *apierror.Error
		if errors.As(err, &denied) {
			return RuntimeAdmission{PermittedProfiles: []string{}}, nil
		}
		return RuntimeAdmission{}, err
	}
	return runtimeAdmission(ctx, r.db, rt.connection.ID, ref, b, fingerprint, rt.digest)
}

// configuredRoutes is the policy order of one binding: primary profiles first,
// then their quota-edge reserves, limited to what is admitted. harness/model
// name the setting, not an execution; effort is null where the catalog has none.
// model is the binding's policy.profile_models override when it names the
// profile (model_source "binding"), else the profile's model as written, a
// placeholder included (model_source "profile").
func configuredRoutes(c *domain.RuntimeCatalog, ref string, b domain.RuntimeBinding, admitted RuntimeAdmission) []executionRoute {
	var order []string
	add := func(profile string) {
		if slices.Contains(admitted.PermittedProfiles, profile) && !slices.Contains(order, profile) {
			order = append(order, profile)
		}
	}
	for _, p := range b.Policy.PrimaryProfiles {
		add(p)
	}
	for _, p := range b.Policy.PrimaryProfiles {
		for _, reserve := range b.Policy.QuotaEdges[p] {
			add(reserve)
		}
	}
	routes := make([]executionRoute, 0, len(order))
	for _, profile := range order {
		p := c.Profiles[profile]
		model, source := p.Model, ModelSourceProfile
		if override, ok := b.Policy.ProfileModels[profile]; ok {
			model, source = override, ModelSourceBinding
		}
		routes = append(routes, executionRoute{BindingRef: ref, ControllerRef: p.ControllerRef, ProfileRef: profile, Harness: p.Harness, Model: model, ModelSource: source})
	}
	return routes
}

type executionLiveKey struct {
	integration uuid.UUID
	controller  string
}

type executionLive struct {
	generation  int64
	live        bool
	grantID     uuid.UUID
	fingerprint string
}

// executionControllerLiveness: the newest booted generation per controller and
// whether the controller can still be believed — its reporter grant is the
// exact live one, it is enabled in the catalog and has not paused itself.
func (r *RuntimeRepo) executionControllerLiveness(ctx context.Context, runtimes []*executionRuntime) (map[executionLiveKey]executionLive, error) {
	out := map[executionLiveKey]executionLive{}
	for _, rt := range runtimes {
		var gens []struct {
			Controller string `db:"controller_ref"`
			Generation int64  `db:"generation"`
		}
		if err := r.db.SelectContext(ctx, &gens, `SELECT controller_ref, MAX(generation) AS generation FROM runtime_execution_instances WHERE integration_id=$1 GROUP BY controller_ref`, rt.connection.ID); err != nil {
			return nil, err
		}
		for _, g := range gens {
			controller, ok := rt.catalog.Controllers[g.Controller]
			live := ok && controller.Enabled
			var grantID uuid.UUID
			var fingerprint string
			if live {
				var agent uuid.UUID
				err := r.db.GetContext(ctx, &agent, `SELECT agent_id FROM agent_workspace_grants WHERE id=$1`, controller.ReporterGrantID)
				if err == nil {
					fp, gerr := runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: agent, WorkspaceID: rt.connection.WorkspaceID, GrantID: controller.ReporterGrantID})
					live = gerr == nil
					grantID, fingerprint = controller.ReporterGrantID, fp
				} else {
					live = false
				}
			}
			if live {
				var paused bool
				var data []byte
				err := r.db.GetContext(ctx, &data, `SELECT report FROM runtime_controller_reports WHERE integration_id=$1 AND controller_ref=$2`, rt.connection.ID, g.Controller)
				if err == nil {
					var rep domain.RuntimeReport
					if json.Unmarshal(data, &rep) == nil {
						paused = rep.EmergencyPaused
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				live = !paused
			}
			out[executionLiveKey{rt.connection.ID, g.Controller}] = executionLive{generation: g.Generation, live: live, grantID: grantID, fingerprint: fingerprint}
		}
	}
	return out, nil
}

func executionAttemptView(now time.Time, row executionAttemptRow, item ExecutionView, live map[executionLiveKey]executionLive, runtimes []*executionRuntime) ExecutionAttemptView {
	l := live[executionLiveKey{row.IntegrationID, row.ControllerRef}]
	freshness := domain.ExecutionFreshness(now, domain.ExecutionFreshnessInput{
		State:             row.State,
		ModelKnown:        row.ObservedModel != nil,
		ObservedAt:        row.ObservedAt,
		HeartbeatAt:       row.HeartbeatAt,
		ReportedAt:        row.HeartbeatAt,
		ReceivedAt:        row.ReceivedAt,
		GenerationCurrent: l.generation == row.Generation,
		// Telemetry written under a grant that has since been rotated is not
		// believed: only the grant the catalog names now can vouch for it.
		ControllerLive: l.live && row.GrantID == l.grantID && row.Fingerprint == l.fingerprint,
	})
	// A disabled or unmanaged pair keeps its attempts as history, not running.
	if !item.Enabled {
		freshness = domain.FreshnessHistory
	}
	var provenance *domain.ExecutionProvenance
	if len(row.Provenance) > 0 {
		var p domain.ExecutionProvenance
		if json.Unmarshal(row.Provenance, &p) == nil {
			provenance = &p
		}
	}
	matches := false
	for _, rt := range runtimes {
		if rt.connection.ID == row.IntegrationID {
			matches = row.RouteRevision == rt.revision && (row.RouteDigest == nil || *row.RouteDigest == rt.digest)
		}
	}
	return ExecutionAttemptView{
		AttemptID: row.AttemptID, ControllerRef: row.ControllerRef, BindingRef: row.BindingRef, TaskID: row.TaskID,
		InstanceID: row.InstanceID, Generation: row.Generation, Sequence: row.Sequence, TurnID: row.TurnID, State: row.State,
		ResolvedRoute: domain.ExecutionResolvedRoute{Source: row.RouteSource, Revision: row.RouteRevision, Digest: row.RouteDigest, ProfileRef: row.RouteProfileRef, Harness: row.RouteHarness, Model: row.RouteModel, Effort: row.RouteEffort},
		Observed:      ExecutionObservedView{Harness: row.ObservedHarness, Model: row.ObservedModel, Effort: row.ObservedEffort, Source: row.ObservedSource, ObservedAt: row.ObservedAt, Provenance: provenance},
		ReceivedAt:    row.ReceivedAt, HeartbeatAt: row.HeartbeatAt,
		Stale: freshness == domain.FreshnessStale || freshness == domain.FreshnessOffline, Freshness: freshness, PolicyMatches: matches,
	}
}

// aggregateFreshness summarizes an item without picking one attempt's truth:
// current beats stale beats offline; an item with only history or nothing is
// unknown.
func aggregateFreshness(v ExecutionView) string {
	best := domain.FreshnessUnknown
	rank := map[string]int{domain.FreshnessUnknown: 0, domain.FreshnessOffline: 1, domain.FreshnessStale: 2, domain.FreshnessCurrent: 3}
	for _, a := range v.Attempts {
		if a.Freshness == domain.FreshnessHistory {
			continue
		}
		if rank[a.Freshness] > rank[best] {
			best = a.Freshness
		}
	}
	return best
}
