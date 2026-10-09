package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// RuntimeActor is populated from authentication middleware, never request JSON.
type RuntimeActor struct {
	UserID          uuid.UUID
	AgentID         uuid.UUID
	AuthWorkspaceID uuid.UUID
	Connector       bool
}

type RuntimeSave struct {
	IfRevision int64           `json:"if_revision"`
	Enabled    *bool           `json:"enabled"`
	Config     json.RawMessage `json:"config"`
}

type RuntimeAdmissionInput struct {
	IfRevision        int64    `json:"if_revision"`
	Enabled           *bool    `json:"enabled"`
	PermittedProfiles []string `json:"permitted_profiles"`
}

type RuntimeAdmission struct {
	Revision          int64    `json:"revision"`
	Enabled           bool     `json:"enabled"`
	PermittedProfiles []string `json:"permitted_profiles"`
}

type RuntimeBindingView struct {
	ResourceOwnerWorkspaceID uuid.UUID                        `json:"resource_owner_workspace_id"`
	BindingRef               string                           `json:"binding_ref"`
	Revision                 int64                            `json:"revision"`
	Digest                   string                           `json:"digest"`
	Enabled                  bool                             `json:"enabled"`
	DrainRequested           bool                             `json:"drain_requested"`
	Binding                  domain.RuntimeBinding            `json:"binding"`
	Admission                RuntimeAdmission                 `json:"admission"`
	Profiles                 map[string]domain.RuntimeProfile `json:"profiles"`
	PolicyProjection         map[string]any                   `json:"policy_projection"`
}

type RuntimeRepo struct{ db *sqlx.DB }

func NewRuntimeRepo(db *sqlx.DB) *RuntimeRepo { return &RuntimeRepo{db: db} }

// admin must be checked in the transaction too: a route-level role check is not
// sufficient to authorize administration of somebody else's provider resource.
func runtimeAdmin(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, actor RuntimeActor) error {
	if actor.Connector || actor.UserID == uuid.Nil || actor.AgentID != uuid.Nil {
		return apierror.Forbidden("runtime administration requires a workspace owner or admin")
	}
	var allowed bool
	err := sqlx.GetContext(ctx, q, &allowed, `SELECT EXISTS (
		SELECT 1 FROM workspaces w WHERE w.id=$1 AND w.deleted_at IS NULL
		AND (w.owner_id=$2 OR EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=w.id AND m.user_id=$2 AND m.role IN ('owner','admin'))))`, ws, actor.UserID)
	if err != nil {
		return err
	}
	if !allowed {
		return apierror.Forbidden("runtime administration requires a workspace owner or admin")
	}
	return nil
}

func runtimeGrant(ctx context.Context, q sqlx.QueryerContext, identity domain.RuntimeIdentity) (string, error) {
	var hash string
	err := sqlx.GetContext(ctx, q, &hash, `SELECT g.api_key_hash FROM agent_workspace_grants g
		JOIN agents a ON a.id=g.agent_id JOIN workspaces w ON w.id=g.workspace_id
		WHERE g.id=$1 AND g.agent_id=$2 AND g.workspace_id=$3 AND g.revoked_at IS NULL
		AND a.deleted_at IS NULL AND w.deleted_at IS NULL`, identity.GrantID, identity.AgentID, identity.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apierror.Forbidden("runtime requires an active exact workspace grant")
	}
	if err != nil {
		return "", err
	}
	// This internal fingerprint is never serialized. Rotation/reinvitation of
	// a stable grant ID invalidates admission and old controller observations.
	digest := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(digest[:]), nil
}

func runtimeConnection(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, lock bool) (*domain.IntegrationConfig, error) {
	query := `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE workspace_id=$1 AND provider='agent_runtime'`
	if lock {
		query += ` FOR UPDATE`
	}
	var row integrationConfigRow
	if err := sqlx.GetContext(ctx, q, &row, query, ws); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	value := row.toDomain()
	return &value, nil
}

func runtimeRevision(ctx context.Context, q sqlx.QueryerContext, id uuid.UUID) (revision int64, digest string, err error) {
	var row struct {
		Revision int64  `db:"revision"`
		Digest   string `db:"digest"`
	}
	err = sqlx.GetContext(ctx, q, &row, `SELECT revision,digest FROM runtime_catalog_revisions WHERE integration_id=$1 ORDER BY revision DESC LIMIT 1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return row.Revision, row.Digest, err
}

func (r *RuntimeRepo) Save(ctx context.Context, ws uuid.UUID, actor RuntimeActor, input RuntimeSave) (*domain.RuntimeSnapshot, error) {
	if input.IfRevision < 0 || input.Enabled == nil {
		return nil, apierror.BadRequest("if_revision and enabled are required")
	}
	catalog, err := domain.ParseRuntimeCatalog(input.Config)
	if err != nil {
		return nil, apierror.BadRequest("invalid runtime catalog")
	}
	canonical, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonical)
	digest := hex.EncodeToString(hash[:])
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	err = runtimeAdmin(ctx, tx, ws, actor)
	if err != nil {
		return nil, err
	}
	// The unique connection index serializes concurrent first saves, before
	// the existing row lock serializes every subsequent compare-and-swap.
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_configs(id,workspace_id,provider,config,is_active) VALUES($1,$2,'agent_runtime','{}',false) ON CONFLICT(workspace_id,provider) DO NOTHING`, uuid.New(), ws)
	if err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, tx, ws, true)
	if err != nil {
		return nil, err
	}
	revision, _, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return nil, err
	}
	if revision != input.IfRevision {
		return nil, apierror.Conflict("runtime desired revision changed")
	}
	for _, c := range catalog.Controllers {
		var identity domain.RuntimeIdentity
		err = tx.GetContext(ctx, &identity.AgentID, `SELECT agent_id FROM agent_workspace_grants WHERE id=$1 AND workspace_id=$2`, c.ReporterGrantID, ws)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.Forbidden("reporter grant must belong to the resource owner workspace")
		}
		if err != nil {
			return nil, err
		}
		identity.GrantID, identity.WorkspaceID = c.ReporterGrantID, ws
		if _, err = runtimeGrant(ctx, tx, identity); err != nil {
			return nil, err
		}
	}
	for _, b := range catalog.Bindings {
		if _, err = runtimeGrant(ctx, tx, b.Binding); err != nil {
			return nil, err
		}
	}
	revision++
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_catalog_revisions(integration_id,revision,digest,config,enabled,created_by) VALUES($1,$2,$3,$4,$5,$6)`, connection.ID, revision, digest, canonical, *input.Enabled, actor.UserID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE integration_configs SET config=$2,is_active=$3,updated_at=now() WHERE id=$1`, connection.ID, canonical, *input.Enabled)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return &domain.RuntimeSnapshot{Mode: "managed", Revision: revision, Digest: digest, Enabled: *input.Enabled, DrainRequested: !*input.Enabled, Catalog: catalog.Redacted(), Controllers: []domain.RuntimeControllerState{}}, nil
}

func (r *RuntimeRepo) Inventory(ctx context.Context, ws uuid.UUID, actor RuntimeActor) (*domain.RuntimeSnapshot, error) {
	if err := runtimeAdmin(ctx, r.db, ws, actor); err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return &domain.RuntimeSnapshot{Mode: "direct", Controllers: []domain.RuntimeControllerState{}}, nil
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	revision, digest, err := runtimeRevision(ctx, r.db, connection.ID)
	if err != nil {
		return nil, err
	}
	states, err := r.controllerStates(ctx, connection, catalog, revision, digest)
	if err != nil {
		return nil, err
	}
	return &domain.RuntimeSnapshot{Mode: "managed", Revision: revision, Digest: digest, Enabled: connection.IsActive, DrainRequested: !connection.IsActive, Catalog: catalog.Redacted(), Controllers: states}, nil
}

func (r *RuntimeRepo) controllerStates(ctx context.Context, connection *domain.IntegrationConfig, catalog *domain.RuntimeCatalog, revision int64, digest string) ([]domain.RuntimeControllerState, error) {
	var rows []struct {
		Ref         string    `db:"controller_ref"`
		GrantID     uuid.UUID `db:"reporter_grant_id"`
		Fingerprint string    `db:"grant_fingerprint"`
		Data        []byte    `db:"report"`
		At          time.Time `db:"received_at"`
	}
	err := r.db.SelectContext(ctx, &rows, `SELECT controller_ref,reporter_grant_id,grant_fingerprint,report,received_at FROM runtime_controller_reports WHERE integration_id=$1 ORDER BY controller_ref`, connection.ID)
	if err != nil {
		return nil, err
	}
	states := make([]domain.RuntimeControllerState, 0, len(rows))
	for _, row := range rows {
		controller, ok := catalog.Controllers[row.Ref]
		if !ok {
			continue
		}
		var report domain.RuntimeReport
		err = json.Unmarshal(row.Data, &report)
		if err != nil {
			return nil, err
		}
		var agent uuid.UUID
		err = r.db.GetContext(ctx, &agent, `SELECT agent_id FROM agent_workspace_grants WHERE id=$1`, row.GrantID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fingerprint, grantErr := runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: agent, WorkspaceID: connection.WorkspaceID, GrantID: row.GrantID})
		if grantErr != nil {
			var denied *apierror.Error
			if !errors.As(grantErr, &denied) {
				return nil, grantErr
			}
		}
		current := grantErr == nil && controller.ReporterGrantID == row.GrantID && fingerprint == row.Fingerprint && report.Revision == revision && report.Digest == digest && report.Status == "applied" && !report.EmergencyPaused && controller.Enabled && time.Since(row.At) <= time.Duration(controller.HeartbeatMaxAgeSeconds)*time.Second
		states = append(states, domain.RuntimeControllerState{ControllerRef: row.Ref, Report: report, ReceivedAt: row.At, Current: current})
	}
	return states, nil
}

func (r *RuntimeRepo) Report(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor, report domain.RuntimeReport) error {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return apierror.Forbidden("controller report requires its exact owner-workspace agent key")
	}
	if report.SchemaVersion != 2 || report.Revision < 1 || (report.Status != "applied" && report.Status != "rejected") || len(report.Capabilities) > 256 || len(report.Pools) > 256 {
		return apierror.BadRequest("invalid controller report")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	connection, err := runtimeConnection(ctx, tx, ws, true)
	if err != nil {
		return err
	}
	if connection == nil {
		return apierror.NotFound("Runtime connection")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return err
	}
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return apierror.NotFound("Runtime controller")
	}
	fingerprint, err := runtimeGrant(ctx, tx, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: controller.ReporterGrantID})
	if err != nil {
		return err
	}
	revision, digest, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return err
	}
	if report.Revision != revision || report.Digest != digest {
		return apierror.Conflict("controller report must match current revision and digest")
	}
	for _, capability := range controller.Capabilities {
		if report.Status == "applied" && !slices.Contains(report.Capabilities, capability) {
			return apierror.BadRequest("applied report lacks required controller capabilities")
		}
	}
	allowedPools := map[string]bool{}
	for _, p := range catalog.Profiles {
		if p.ControllerRef == ref {
			for _, pool := range catalog.Accounts[p.AccountRef].QuotaPoolsByMode[p.ExecutionMode] {
				name, _ := catalog.ResolvePool(pool)
				allowedPools[name] = true
			}
		}
	}
	canonicalPools := map[string]domain.RuntimePoolObservation{}
	now := time.Now()
	for name, observation := range report.Pools {
		canonical, ok := catalog.ResolvePool(name)
		if !ok || !allowedPools[canonical] {
			return apierror.Forbidden("controller cannot report an unrelated pool")
		}
		if _, duplicate := canonicalPools[canonical]; duplicate {
			return apierror.BadRequest("duplicate canonical pool observation")
		}
		if !slices.Contains([]string{"available", "exhausted", "threshold", "unknown", "auth_error", "network_error"}, observation.State) || observation.ObservedAt.IsZero() || observation.ObservedAt.After(now) {
			return apierror.BadRequest("invalid pool observation")
		}
		if observation.State == "threshold" && (catalog.Pools[canonical].ThresholdRef == "" || observation.ThresholdRef != catalog.Pools[canonical].ThresholdRef || observation.EvidenceKind != "configured_threshold") {
			return apierror.BadRequest("threshold requires an explicitly configured matching adapter rule")
		}
		canonicalPools[canonical] = observation
	}
	if len(report.Profiles) > 256 {
		return apierror.BadRequest("too many profile observations")
	}
	for profileRef, model := range report.Profiles {
		profile, ok := catalog.Profiles[profileRef]
		if !ok || profile.ControllerRef != ref {
			return apierror.Forbidden("controller cannot report an unrelated profile")
		}
		if !domain.ValidRuntimeRef(model.Model) || !domain.ValidRuntimeRef(model.ModelDeveloper) || !domain.ValidRuntimeRef(model.ModelFamily) || model.ObservedAt.IsZero() || model.ObservedAt.After(now) {
			return apierror.BadRequest("invalid model observation")
		}
	}
	report.Pools = canonicalPools
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_controller_reports(integration_id,controller_ref,reporter_grant_id,grant_fingerprint,report) VALUES($1,$2,$3,$4,$5) ON CONFLICT(integration_id,controller_ref) DO UPDATE SET reporter_grant_id=EXCLUDED.reporter_grant_id,grant_fingerprint=EXCLUDED.grant_fingerprint,report=EXCLUDED.report,received_at=now()`, connection.ID, ref, controller.ReporterGrantID, fingerprint, data)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type runtimeAdmissionRow struct {
	WorkspaceID   uuid.UUID `db:"workspace_id"`
	AgentID       uuid.UUID `db:"agent_id"`
	GrantID       uuid.UUID `db:"grant_id"`
	Fingerprint   string    `db:"grant_fingerprint"`
	Revision      int64     `db:"revision"`
	CatalogDigest string    `db:"catalog_digest"`
	Enabled       bool      `db:"enabled"`
	Profiles      []byte    `db:"permitted_profiles"`
}

func runtimeAdmission(ctx context.Context, q sqlx.QueryerContext, connectionID uuid.UUID, ref string, binding domain.RuntimeBinding, fingerprint, digest string) (RuntimeAdmission, error) {
	result := RuntimeAdmission{PermittedProfiles: []string{}}
	var row runtimeAdmissionRow
	err := sqlx.GetContext(ctx, q, &row, `SELECT workspace_id,agent_id,grant_id,grant_fingerprint,revision,catalog_digest,enabled,permitted_profiles FROM runtime_binding_admissions WHERE integration_id=$1 AND binding_ref=$2`, connectionID, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Revision = row.Revision
	if row.WorkspaceID != binding.Binding.WorkspaceID || row.AgentID != binding.Binding.AgentID || row.GrantID != binding.Binding.GrantID || row.Fingerprint != fingerprint || row.CatalogDigest != digest {
		return result, nil
	}
	var admitted []string
	err = json.Unmarshal(row.Profiles, &admitted)
	if err != nil {
		return result, err
	}
	for _, p := range binding.PermittedProfiles {
		if slices.Contains(admitted, p) {
			result.PermittedProfiles = append(result.PermittedProfiles, p)
		}
	}
	result.Enabled = row.Enabled && binding.Enabled && len(result.PermittedProfiles) > 0
	return result, nil
}

func runtimeBoundCaller(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, binding domain.RuntimeBinding, actor RuntimeActor) (string, error) {
	if binding.Binding.WorkspaceID != ws || actor.Connector {
		return "", apierror.Forbidden("runtime binding is not accessible in this workspace")
	}
	if actor.AgentID != uuid.Nil {
		if actor.AgentID != binding.Binding.AgentID || actor.AuthWorkspaceID != ws {
			return "", apierror.Forbidden("runtime requires the bound workspace agent key")
		}
	} else if err := runtimeAdmin(ctx, q, ws, actor); err != nil {
		return "", err
	}
	return runtimeGrant(ctx, q, binding.Binding)
}

// Binding scopes the lookup to both the resource owner and receiver. Explicit
// owner identity avoids a global name search or accidental collision disclosure.
func (r *RuntimeRepo) Binding(ctx context.Context, owner, receiver uuid.UUID, ref string, actor RuntimeActor) (*RuntimeBindingView, error) {
	connection, err := runtimeConnection(ctx, r.db, owner, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime binding")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	binding, ok := catalog.Bindings[ref]
	if !ok {
		return nil, apierror.NotFound("Runtime binding")
	}
	fingerprint, err := runtimeBoundCaller(ctx, r.db, receiver, binding, actor)
	if err != nil {
		return nil, err
	}
	revision, digest, err := runtimeRevision(ctx, r.db, connection.ID)
	if err != nil {
		return nil, err
	}
	admission, err := runtimeAdmission(ctx, r.db, connection.ID, ref, binding, fingerprint, digest)
	if err != nil {
		return nil, err
	}
	profiles := map[string]domain.RuntimeProfile{}
	for _, p := range admission.PermittedProfiles {
		profiles[p] = catalog.Profiles[p]
	}
	// A receiver sees its own binding and permitted profile descriptions, never
	// the owner's catalog, credentials, unrelated tasks or other agents.
	return &RuntimeBindingView{ResourceOwnerWorkspaceID: owner, BindingRef: ref, Revision: revision, Digest: digest, Enabled: connection.IsActive && admission.Enabled, DrainRequested: !connection.IsActive, Binding: binding, Admission: admission, Profiles: profiles, PolicyProjection: runtimePolicyProjection(owner, catalog, binding, admission.PermittedProfiles, revision)}, nil
}

func runtimePolicyProjection(owner uuid.UUID, catalog *domain.RuntimeCatalog, binding domain.RuntimeBinding, admitted []string, revision int64) map[string]any {
	accounts := map[string]any{}
	profiles := []map[string]any{}
	permitted := []string{}
	for _, ref := range binding.PermittedProfiles {
		if !slices.Contains(admitted, ref) {
			continue
		}
		profile := catalog.Profiles[ref]
		account := catalog.Accounts[profile.AccountRef]
		poolsByMode := map[string][]string{}
		for mode, refs := range account.QuotaPoolsByMode {
			canonical := []string{}
			for _, poolRef := range refs {
				name, _ := catalog.ResolvePool(poolRef)
				pool := catalog.Pools[name]
				// Owner-qualified canonical IDs preserve shared resources when
				// several controllers/workspaces consume the same connection.
				id := owner.String() + ":" + pool.Provider + ":" + pool.ResourceRef
				if !slices.Contains(canonical, id) {
					canonical = append(canonical, id)
				}
			}
			poolsByMode[mode] = canonical
		}
		accounts[profile.AccountRef] = map[string]any{"provider": account.Provider, "quota_pools_by_mode": poolsByMode}
		profiles = append(profiles, map[string]any{"profile_id": ref, "revision": fmt.Sprintf("rev-%d", revision), "provider": account.Provider, "account_ref": profile.AccountRef, "execution_mode": profile.ExecutionMode, "harness": profile.Harness, "model": profile.Model, "host": catalog.Controllers[profile.ControllerRef].Host, "capabilities": profile.Capabilities})
		permitted = append(permitted, ref)
	}
	primary := []string{}
	edges := map[string][]string{}
	preferred := map[string]string{}
	for _, ref := range binding.Policy.PrimaryProfiles {
		if slices.Contains(permitted, ref) {
			primary = append(primary, ref)
		}
	}
	for from, targets := range binding.Policy.QuotaEdges {
		if !slices.Contains(permitted, from) {
			continue
		}
		edges[from] = []string{}
		for _, target := range targets {
			if slices.Contains(permitted, target) {
				edges[from] = append(edges[from], target)
			}
		}
	}
	for provider, ref := range binding.Policy.PreferredAccounts {
		if _, ok := accounts[ref]; ok {
			preferred[provider] = ref
		}
	}
	return map[string]any{"schema_version": 2, "binding": binding.Binding, "accounts": accounts, "profiles": profiles, "primary_profiles": primary, "quota_edges": edges, "preferred_accounts": preferred, "api_reserve_providers": binding.Policy.APIReserveProviders, "max_attempts": binding.Policy.MaxAttempts, "evidence_max_age_seconds": binding.Policy.EvidenceMaxAgeSeconds}
}

func (r *RuntimeRepo) Admit(ctx context.Context, owner, receiver uuid.UUID, ref string, actor RuntimeActor, input RuntimeAdmissionInput) (*RuntimeAdmission, error) {
	if input.IfRevision < 0 || input.Enabled == nil || input.PermittedProfiles == nil || len(input.PermittedProfiles) > 256 {
		return nil, apierror.BadRequest("invalid receiving admission")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	err = runtimeAdmin(ctx, tx, receiver, actor)
	if err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, tx, owner, true)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime binding")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	binding, ok := catalog.Bindings[ref]
	if !ok || binding.Binding.WorkspaceID != receiver {
		return nil, apierror.NotFound("Runtime binding")
	}
	fingerprint, err := runtimeGrant(ctx, tx, binding.Binding)
	if err != nil {
		return nil, err
	}
	_, digest, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return nil, err
	}
	current, err := runtimeAdmission(ctx, tx, connection.ID, ref, binding, fingerprint, digest)
	if err != nil {
		return nil, err
	}
	if current.Revision != input.IfRevision {
		return nil, apierror.Conflict("receiving admission revision changed")
	}
	seen := map[string]bool{}
	for _, p := range input.PermittedProfiles {
		if seen[p] || !slices.Contains(binding.PermittedProfiles, p) {
			return nil, apierror.BadRequest("admission contains an unpermitted or duplicate profile")
		}
		seen[p] = true
	}
	if *input.Enabled && len(input.PermittedProfiles) == 0 {
		return nil, apierror.BadRequest("enabled admission requires permitted profiles")
	}
	profiles, err := json.Marshal(input.PermittedProfiles)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_binding_admissions(integration_id,binding_ref,workspace_id,agent_id,grant_id,grant_fingerprint,revision,catalog_digest,enabled,permitted_profiles,updated_by)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	ON CONFLICT(integration_id,binding_ref) DO UPDATE SET workspace_id=EXCLUDED.workspace_id,agent_id=EXCLUDED.agent_id,grant_id=EXCLUDED.grant_id,grant_fingerprint=EXCLUDED.grant_fingerprint,revision=EXCLUDED.revision,catalog_digest=EXCLUDED.catalog_digest,enabled=EXCLUDED.enabled,permitted_profiles=EXCLUDED.permitted_profiles,updated_by=EXCLUDED.updated_by,updated_at=now()`, connection.ID, ref, receiver, binding.Binding.AgentID, binding.Binding.GrantID, fingerprint, current.Revision+1, digest, *input.Enabled, profiles, actor.UserID)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return &RuntimeAdmission{Revision: current.Revision + 1, Enabled: *input.Enabled, PermittedProfiles: input.PermittedProfiles}, nil
}

func (r *RuntimeRepo) Preview(ctx context.Context, owner, receiver uuid.UUID, ref string, actor RuntimeActor, input domain.RuntimePreviewInput) (*domain.RuntimePreview, error) {
	if len(input.RequiredCapabilities) > 256 {
		return nil, apierror.BadRequest("too many required capabilities")
	}
	for _, capability := range input.RequiredCapabilities {
		if !domain.ValidRuntimeRef(capability) {
			return nil, apierror.BadRequest("invalid required capability")
		}
	}
	view, err := r.Binding(ctx, owner, receiver, ref, actor)
	if err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, r.db, owner, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime binding")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	revision, digest, err := runtimeRevision(ctx, r.db, connection.ID)
	if err != nil {
		return nil, err
	}
	if revision != view.Revision || digest != view.Digest {
		return nil, apierror.Conflict("runtime desired revision changed")
	}
	states, err := r.controllerStates(ctx, connection, catalog, revision, digest)
	if err != nil {
		return nil, err
	}
	byController := map[string]domain.RuntimeControllerState{}
	globalPools := map[string]domain.RuntimePoolObservation{}
	for _, state := range states {
		byController[state.ControllerRef] = state
		if !state.Current {
			continue
		}
		for pool, fact := range state.Report.Pools {
			previous, exists := globalPools[pool]
			if !exists {
				globalPools[pool] = fact
				continue
			}
			// Never turn contradictory host observations into extra quota.
			// Conservative conflicts remain unknown until a fresh common fact.
			if previous.State != fact.State || previous.Verified != fact.Verified || previous.ThresholdRef != fact.ThresholdRef {
				fact.State = "unknown"
				fact.Verified = false
			}
			if previous.ObservedAt.Before(fact.ObservedAt) {
				fact.ObservedAt = previous.ObservedAt
			}
			globalPools[pool] = fact
		}
	}
	facts := map[string]domain.RuntimeCandidateEvidence{}
	now := time.Now()
	for ref, profile := range catalog.Profiles {
		state := byController[profile.ControllerRef]
		fact := domain.RuntimeCandidateEvidence{ControllerCurrent: state.Current, Pools: globalPools}
		model, ok := state.Report.Profiles[ref]
		if ok && model.ObservedAt.Before(now) && now.Sub(model.ObservedAt) <= time.Duration(view.Binding.Policy.EvidenceMaxAgeSeconds)*time.Second {
			fact.ActualModelDeveloper = model.ModelDeveloper
			fact.ActualModelFamily = model.ModelFamily
		}
		facts[ref] = fact
	}
	// Provenance comes only from a controller attestation stored under the
	// controller's current reporter grant; a preview body cannot supply it.
	provenance, err := r.runtimeProvenance(ctx, connection, catalog, input.ArtifactID, input.ArtifactRevision)
	if err != nil {
		return nil, err
	}
	result := domain.PreviewRuntime(catalog, view.Binding, revision, view.Enabled, view.Admission.PermittedProfiles, facts, provenance, input, now)
	return &result, nil
}

// Attest records which agents authored an exact artifact revision. It uses the
// same exact owner-workspace reporter grant as Report and merges authors, so a
// later attestation can add authors but never silently drop one.
func (r *RuntimeRepo) Attest(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor, report domain.RuntimeProvenanceReport) error {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return apierror.Forbidden("provenance attestation requires its exact owner-workspace agent key")
	}
	if report.ArtifactID == uuid.Nil || report.ArtifactRevision == "" || len(report.ArtifactRevision) > 128 || len(report.Authors) == 0 || len(report.Authors) > 64 {
		return apierror.BadRequest("invalid provenance attestation")
	}
	for _, author := range report.Authors {
		if author.AgentID == uuid.Nil || !domain.ValidRuntimeRef(author.ModelDeveloper) || !domain.ValidRuntimeRef(author.ModelFamily) {
			return apierror.BadRequest("invalid provenance attestation")
		}
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	connection, err := runtimeConnection(ctx, tx, ws, true)
	if err != nil {
		return err
	}
	if connection == nil {
		return apierror.NotFound("Runtime connection")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return err
	}
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return apierror.NotFound("Runtime controller")
	}
	fingerprint, err := runtimeGrant(ctx, tx, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: controller.ReporterGrantID})
	if err != nil {
		return err
	}
	var stored []byte
	err = tx.GetContext(ctx, &stored, `SELECT authors FROM runtime_artifact_provenance WHERE integration_id=$1 AND artifact_id=$2 AND artifact_revision=$3 FOR UPDATE`, connection.ID, report.ArtifactID, report.ArtifactRevision)
	authors := report.Authors
	if err == nil {
		var previous []domain.RuntimeAuthor
		unmarshalErr := json.Unmarshal(stored, &previous)
		if unmarshalErr != nil {
			return unmarshalErr
		}
		for _, p := range previous {
			if !slices.ContainsFunc(authors, func(a domain.RuntimeAuthor) bool { return a.AgentID == p.AgentID }) {
				authors = append(authors, p)
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(authors) > 64 {
		return apierror.BadRequest("too many provenance authors")
	}
	data, err := json.Marshal(authors)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_artifact_provenance(integration_id,artifact_id,artifact_revision,controller_ref,reporter_grant_id,grant_fingerprint,complete,authors)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(integration_id,artifact_id,artifact_revision) DO UPDATE SET controller_ref=EXCLUDED.controller_ref,reporter_grant_id=EXCLUDED.reporter_grant_id,grant_fingerprint=EXCLUDED.grant_fingerprint,complete=EXCLUDED.complete,authors=EXCLUDED.authors,recorded_at=now()`,
		connection.ID, report.ArtifactID, report.ArtifactRevision, ref, controller.ReporterGrantID, fingerprint, report.Complete, data)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// runtimeProvenance returns nil (unknown) unless the row was written by the
// controller's CURRENT reporter grant: a rotated or revoked grant voids it.
func (r *RuntimeRepo) runtimeProvenance(ctx context.Context, connection *domain.IntegrationConfig, catalog *domain.RuntimeCatalog, artifact uuid.UUID, revision string) (*domain.RuntimeProvenance, error) {
	if artifact == uuid.Nil || revision == "" {
		return nil, nil
	}
	var row struct {
		Controller  string          `db:"controller_ref"`
		Grant       uuid.UUID       `db:"reporter_grant_id"`
		Fingerprint string          `db:"grant_fingerprint"`
		Complete    bool            `db:"complete"`
		Authors     json.RawMessage `db:"authors"`
	}
	err := r.db.GetContext(ctx, &row, `SELECT controller_ref,reporter_grant_id,grant_fingerprint,complete,authors FROM runtime_artifact_provenance WHERE integration_id=$1 AND artifact_id=$2 AND artifact_revision=$3`, connection.ID, artifact, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	controller, ok := catalog.Controllers[row.Controller]
	if !ok || controller.ReporterGrantID != row.Grant {
		return nil, nil
	}
	var reporter uuid.UUID
	if err = r.db.GetContext(ctx, &reporter, `SELECT agent_id FROM agent_workspace_grants WHERE id=$1`, row.Grant); err != nil {
		return nil, nil //nolint:nilerr // a vanished grant means unknown provenance
	}
	current, err := runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: reporter, WorkspaceID: connection.WorkspaceID, GrantID: row.Grant})
	if err != nil || current != row.Fingerprint {
		return nil, nil //nolint:nilerr // an unusable attestation is unknown provenance, not a request error
	}
	var authors []domain.RuntimeAuthor
	err = json.Unmarshal(row.Authors, &authors)
	if err != nil {
		return nil, err
	}
	return &domain.RuntimeProvenance{ArtifactID: artifact, Revision: revision, Authors: authors, Complete: row.Complete}, nil
}
