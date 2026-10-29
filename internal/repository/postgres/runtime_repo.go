package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
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
	IfRevision int64 `json:"if_revision"`
	// Enabled is accepted for compatibility and ignored: the Runner is switched
	// on and off only by the extension enable/disable endpoints.
	Enabled *bool           `json:"enabled"`
	Config  json.RawMessage `json:"config"`
}

// RuntimeRestore is the body of POST /runtime/restore: save the stored catalog
// of Revision as a new revision on top of IfRevision.
type RuntimeRestore struct {
	IfRevision int64 `json:"if_revision"`
	Revision   int64 `json:"revision"`
}

// RuntimePolicySave is the strict body of PUT .../bindings/:id/policy.
// PermittedProfiles is optional: nil leaves the binding's list unchanged.
type RuntimePolicySave struct {
	IfRevision        int64                `json:"if_revision"`
	PermittedProfiles []string             `json:"permitted_profiles"`
	Policy            domain.RuntimePolicy `json:"policy"`
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

type RuntimeRepo struct {
	db     *sqlx.DB
	allow  extensions.AllowList
	strict bool
}

func NewRuntimeRepo(db *sqlx.DB) *RuntimeRepo { return &RuntimeRepo{db: db} }

// WithAllowList makes a save (or restore) into a Runner that is on refuse while
// the instance allow-list does not allow it; saving it switched off stays allowed.
func (r *RuntimeRepo) WithAllowList(a extensions.AllowList) *RuntimeRepo {
	r.allow = a
	return r
}

// WithStrictWorkspaceIsolation turns on strict workspace isolation: a save
// refuses a binding of another workspace, and Desired and Report leave out
// bindings of another workspace or of a workspace whose Runner is off or not
// connected. Off, every path behaves as before.
func (r *RuntimeRepo) WithStrictWorkspaceIsolation(on bool) *RuntimeRepo {
	r.strict = on
	return r
}

// runtimeExclusions lists the bindings of refs that strict isolation leaves
// out, sorted by ref. A binding of another workspace is foreign; one of ws
// follows the ws Runner: no connection or not allowed on the instance means
// the program is not connected, a connection switched off means runner_off.
func (r *RuntimeRepo) runtimeExclusions(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, bindings map[string]domain.RuntimeBinding) ([]domain.RuntimeExclusion, error) {
	if !r.strict || len(bindings) == 0 {
		return nil, nil
	}
	own := ""
	connection, err := runtimeConnection(ctx, q, ws, false)
	if err != nil {
		return nil, err
	}
	switch {
	case connection == nil:
		own = domain.RuntimeExcludedProgramNotConnected
	case !connection.IsActive:
		own = domain.RuntimeExcludedRunnerOff
	case r.allow != nil:
		allowed, allowErr := r.allow.Allowed(ctx, runner.ID)
		if allowErr != nil {
			return nil, allowErr
		}
		if !allowed {
			own = domain.RuntimeExcludedProgramNotConnected
		}
	}
	var out []domain.RuntimeExclusion
	for _, ref := range slices.Sorted(maps.Keys(bindings)) {
		switch {
		case bindings[ref].Binding.WorkspaceID != ws:
			out = append(out, domain.RuntimeExclusion{Ref: ref, Reason: domain.RuntimeExcludedForeignWorkspace})
		case own != "":
			out = append(out, domain.RuntimeExclusion{Ref: ref, Reason: own})
		}
	}
	return out, nil
}

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

// runtimeEditor is whom a catalog read or write is done for: the signed-in
// owner/admin, or the operator of an agent key that holds a live
// runtime_manager grant (then agent is that key, recorded as the author).
type runtimeEditor struct{ user, agent uuid.UUID }

func (e runtimeEditor) authorAgent() *uuid.UUID {
	if e.agent == uuid.Nil {
		return nil
	}
	return &e.agent
}

// runtimeCatalogEditor gates the workspace catalog (inventory, revisions, save,
// restore). A person needs owner/admin. An agent key gets exactly its operator's
// right: a live runtime_manager grant, the operator owning or administering ws,
// and the agent connected to ws (home workspace or a live workspace grant).
func runtimeCatalogEditor(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, actor RuntimeActor) (runtimeEditor, error) {
	if actor.Connector || actor.AgentID == uuid.Nil {
		return runtimeEditor{user: actor.UserID}, runtimeAdmin(ctx, q, ws, actor)
	}
	var row struct {
		Operator  *uuid.UUID `db:"operator"`
		Admin     bool       `db:"admin"`
		Connected bool       `db:"connected"`
	}
	err := sqlx.GetContext(ctx, q, &row, `SELECT m.operator_user_id AS operator,
		EXISTS (SELECT 1 FROM workspaces w WHERE w.id=$2 AND w.deleted_at IS NULL
			AND (w.owner_id=m.operator_user_id OR EXISTS (SELECT 1 FROM workspace_members wm
				WHERE wm.workspace_id=w.id AND wm.user_id=m.operator_user_id AND wm.role IN ('owner','admin')))) AS admin,
		(a.workspace_id=$2 OR EXISTS (SELECT 1 FROM agent_workspace_grants g
			WHERE g.agent_id=a.id AND g.workspace_id=$2 AND g.revoked_at IS NULL)) AS connected
		FROM agents a LEFT JOIN user_runtime_managers m
			ON m.agent_id=a.id AND m.operator_user_id=a.operator_user_id AND m.revoked_at IS NULL
		WHERE a.id=$1 AND a.deleted_at IS NULL`, actor.AgentID, ws)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.Operator == nil) {
		return runtimeEditor{}, errNotManager
	}
	if err != nil {
		return runtimeEditor{}, err
	}
	if !row.Admin {
		return runtimeEditor{}, apierror.Forbidden("operator is not an admin of this workspace")
	}
	if !row.Connected {
		return runtimeEditor{}, apierror.Forbidden("agent is not connected to this workspace")
	}
	return runtimeEditor{user: *row.Operator, agent: actor.AgentID}, nil
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

// lockRunnerConnection takes a transaction-scoped advisory lock that every
// creator of a workspace's Runner connection row holds, whichever of the two
// provider names it writes under.
func lockRunnerConnection(ctx context.Context, q sqlx.ExecerContext, ws uuid.UUID) error {
	_, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('runner-connection:' || $1::text, 0))`, ws.String())
	return err
}

func runtimeConnection(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, lock bool) (*domain.IntegrationConfig, error) {
	query := `SELECT ` + integrationConfigSelectCols + ` FROM integration_configs WHERE workspace_id=$1 AND provider IN ('runner','agent_runtime') ORDER BY (provider='runner') DESC LIMIT 1`
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
	if input.IfRevision < 0 {
		return nil, apierror.BadRequest("if_revision is required")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	editor, err := runtimeCatalogEditor(ctx, tx, ws, actor)
	if err != nil {
		return nil, err
	}
	// The unique index is per provider name, so it cannot keep a save under the
	// legacy name and an extension toggle under the new one from both creating a
	// row: the shared advisory lock serializes every creator of the Runner
	// connection, then the existing row lock serializes each compare-and-swap.
	err = lockRunnerConnection(ctx, tx, ws)
	if err != nil {
		return nil, err
	}
	// A connection created here starts on, as one created by the extension
	// enable does; afterwards only that switch changes it.
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_configs(id,workspace_id,provider,config,is_active) SELECT $1::uuid,$2::uuid,'agent_runtime','{}'::jsonb,true WHERE NOT EXISTS (SELECT 1 FROM integration_configs WHERE workspace_id=$2::uuid AND provider IN ('runner','agent_runtime')) ON CONFLICT(workspace_id,provider) DO NOTHING`, uuid.New(), ws)
	if err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, tx, ws, true)
	if err != nil {
		return nil, err
	}
	// A Runner that is on (a connection created above starts on) takes a new
	// catalog only while the instance allow-list allows it; one switched off
	// can still be edited.
	if connection.IsActive {
		err = extensions.RequireAllowed(ctx, r.allow, runner.ID)
		if err != nil {
			return nil, err
		}
	}
	revision, oldDigest, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return nil, err
	}
	if revision != input.IfRevision {
		return nil, apierror.Conflict("runtime desired revision changed")
	}
	// Inventory redacts credential references, so an editor sends back an
	// account with an empty credential_ref: it keeps the stored reference.
	config := input.Config
	var previous []byte
	if revision > 0 {
		err = tx.GetContext(ctx, &previous, `SELECT config FROM runtime_catalog_revisions WHERE integration_id=$1 AND revision=$2`, connection.ID, revision)
		if err != nil {
			return nil, err
		}
		config = inheritCredentialRefs(config, previous)
	}
	catalog, err := domain.ParseRuntimeCatalog(config)
	if err != nil {
		// Field by field, so the editor can put each reason next to its field.
		var fields domain.RuntimeFieldErrors
		if errors.As(err, &fields) {
			out := make(map[string]string, len(fields))
			for path, code := range fields {
				out["config."+path] = code
			}
			e := apierror.ValidationError(out)
			e.Message = "invalid runtime catalog"
			return nil, e
		}
		return nil, apierror.BadRequest("invalid runtime catalog")
	}
	canonical, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonical)
	digest := hex.EncodeToString(hash[:])
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
	if r.strict {
		// A binding of ws that carries a grant of another workspace is refused
		// as foreign too; an unknown grant is left to the grant check below.
		foreign := map[string]string{}
		for ref, b := range catalog.Bindings {
			foreignGrant := false
			if b.Binding.WorkspaceID == ws {
				var grantWorkspace uuid.UUID
				err = sqlx.GetContext(ctx, tx, &grantWorkspace, `SELECT workspace_id FROM agent_workspace_grants WHERE id=$1`, b.Binding.GrantID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				foreignGrant = err == nil && grantWorkspace != ws
			}
			if b.Binding.WorkspaceID != ws || foreignGrant {
				foreign["config.bindings."+ref+".binding.workspace_id"] = domain.RuntimeExcludedForeignWorkspace
			}
		}
		if len(foreign) > 0 {
			e := apierror.ValidationError(foreign)
			e.Code = http.StatusUnprocessableEntity
			e.Message = "runtime bindings must belong to this workspace"
			return nil, e
		}
	}
	fingerprints := make(map[string]string, len(catalog.Bindings))
	for ref, b := range catalog.Bindings {
		if fingerprints[ref], err = runtimeGrant(ctx, tx, b.Binding); err != nil {
			return nil, err
		}
	}
	revision++
	// enabled is carried from the connection: a catalog edit never switches
	// the Runner, so a stale editor cannot undo an operator's disable.
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_catalog_revisions(integration_id,revision,digest,config,enabled,created_by,author_agent_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, connection.ID, revision, digest, canonical, connection.IsActive, editor.user, editor.authorAgent())
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE integration_configs SET config=$2,updated_at=now() WHERE id=$1`, connection.ID, canonical)
	if err != nil {
		return nil, err
	}
	err = carryRuntimeAdmissions(ctx, tx, connection.ID, previous, catalog, fingerprints, oldDigest, digest)
	if err != nil {
		return nil, err
	}
	// An admission outlives its binding only as a disabled row: a binding taken
	// out of the catalog, or given to another agent, grant or workspace, no
	// longer leads the admitted agent, and a later revision that restores it
	// needs a new admission, as any changed binding does.
	refs := make([]string, 0, len(catalog.Bindings))
	agents := make([]string, 0, len(catalog.Bindings))
	workspaces := make([]string, 0, len(catalog.Bindings))
	grants := make([]string, 0, len(catalog.Bindings))
	for ref, b := range catalog.Bindings {
		refs = append(refs, ref)
		agents = append(agents, b.Binding.AgentID.String())
		workspaces = append(workspaces, b.Binding.WorkspaceID.String())
		grants = append(grants, b.Binding.GrantID.String())
	}
	_, err = tx.ExecContext(ctx, `UPDATE runtime_binding_admissions adm SET enabled=false,updated_by=$6,updated_at=now()
		WHERE adm.integration_id=$1 AND adm.enabled AND NOT EXISTS (
			SELECT 1 FROM unnest($2::text[],$3::uuid[],$4::uuid[],$5::uuid[]) AS b(ref,agent_id,workspace_id,grant_id)
			WHERE b.ref=adm.binding_ref AND b.agent_id=adm.agent_id AND b.workspace_id=adm.workspace_id AND b.grant_id=adm.grant_id)`,
		connection.ID, pq.Array(refs), pq.Array(agents), pq.Array(workspaces), pq.Array(grants), editor.user)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return &domain.RuntimeSnapshot{Mode: "managed", Revision: revision, Digest: digest, Enabled: connection.IsActive, DrainRequested: !connection.IsActive, Catalog: catalog.Redacted(), Controllers: []domain.RuntimeControllerState{}}, nil
}

// carryRuntimeAdmissions moves each receiver admission from the previous
// catalog digest to the new one when the save left what that receiver approved
// unchanged: its binding, the definitions of its permitted profiles, their
// accounts, pools and controller host. Without it every catalog save read every
// admission as disabled until the receiver granted it again, although most saves
// touch other bindings. A binding whose projection changed keeps the old digest
// and needs renewed approval, as before. The grant and its key fingerprint are
// matched as in SetBindingPolicy, so a rotated key never inherits an admission.
// The admission revision is the receiver's token and stays as is.
func carryRuntimeAdmissions(ctx context.Context, tx *sqlx.Tx, connectionID uuid.UUID, previous []byte, catalog *domain.RuntimeCatalog, fingerprints map[string]string, oldDigest, digest string) error {
	if len(previous) == 0 || oldDigest == digest {
		return nil
	}
	// Decoded without validation: a revision stored under older rules must not
	// fail a save, it only stops carrying admissions.
	var before domain.RuntimeCatalog
	if json.Unmarshal(previous, &before) != nil {
		return nil
	}
	for ref, binding := range catalog.Bindings {
		old, ok := runtimeBindingProjectionDigest(&before, ref)
		if !ok {
			continue
		}
		now, _ := runtimeBindingProjectionDigest(catalog, ref)
		if old != now {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runtime_binding_admissions SET catalog_digest=$3 WHERE integration_id=$1 AND binding_ref=$2 AND catalog_digest=$4 AND workspace_id=$5 AND agent_id=$6 AND grant_id=$7 AND grant_fingerprint=$8`, connectionID, ref, digest, oldDigest, binding.Binding.WorkspaceID, binding.Binding.AgentID, binding.Binding.GrantID, fingerprints[ref]); err != nil {
			return err
		}
	}
	return nil
}

// runtimeBindingProjectionDigest hashes the part of a catalog one receiver
// approved; ok is false when the catalog has no such binding. Map keys marshal
// sorted, so equal content gives equal digests.
func runtimeBindingProjectionDigest(catalog *domain.RuntimeCatalog, ref string) (string, bool) {
	binding, ok := catalog.Bindings[ref]
	if !ok {
		return "", false
	}
	profiles := map[string]any{}
	accounts := map[string]any{}
	pools := map[string]any{}
	hosts := map[string]string{}
	for _, name := range binding.PermittedProfiles {
		profile, ok := catalog.Profiles[name]
		profiles[name] = map[string]any{"defined": ok, "profile": profile}
		if !ok {
			continue
		}
		hosts[profile.ControllerRef] = catalog.Controllers[profile.ControllerRef].Host
		account, ok := catalog.Accounts[profile.AccountRef]
		accounts[profile.AccountRef] = map[string]any{"defined": ok, "account": account}
		for _, refs := range account.QuotaPoolsByMode {
			for _, poolRef := range refs {
				name, ok := catalog.ResolvePool(poolRef)
				pools[poolRef] = map[string]any{"defined": ok, "name": name, "pool": catalog.Pools[name]}
			}
		}
	}
	data, err := json.Marshal(map[string]any{"binding": binding, "profiles": profiles, "accounts": accounts, "pools": pools, "hosts": hosts})
	if err != nil {
		return "", false
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), true
}

// Restore saves the catalog of an earlier revision as a new revision. The
// config, credential references included, comes from the stored row, never
// from the client, and goes through the same validation and CAS as Save.
func (r *RuntimeRepo) Restore(ctx context.Context, ws uuid.UUID, actor RuntimeActor, input RuntimeRestore) (*domain.RuntimeSnapshot, error) {
	if input.IfRevision < 0 || input.Revision < 1 {
		return nil, apierror.BadRequest("if_revision and revision are required")
	}
	if _, err := runtimeCatalogEditor(ctx, r.db, ws, actor); err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime revision")
	}
	var config []byte
	err = r.db.GetContext(ctx, &config, `SELECT config FROM runtime_catalog_revisions WHERE integration_id=$1 AND revision=$2`, connection.ID, input.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Runtime revision")
	}
	if err != nil {
		return nil, err
	}
	return r.Save(ctx, ws, actor, RuntimeSave{IfRevision: input.IfRevision, Config: config})
}

type runtimeAuthorKey struct{}

// WithRuntimeAuthor records the signed-in user behind a Runner on/off switch:
// the switch appends a catalog revision, and a revision needs an author.
func WithRuntimeAuthor(ctx context.Context, user uuid.UUID) context.Context {
	return context.WithValue(ctx, runtimeAuthorKey{}, user)
}

func runtimeAuthor(ctx context.Context) uuid.UUID {
	user, _ := ctx.Value(runtimeAuthorKey{}).(uuid.UUID)
	return user
}

// setRunnerActive is the one Runner on/off switch. Controllers drain on the
// latest revision's enabled, not on is_active, so a flip that changes it is
// appended as a revision with the same catalog, authored by the ctx user.
func setRunnerActive(ctx context.Context, db *sqlx.DB, ws, id uuid.UUID, active bool) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	err = lockRunnerConnection(ctx, tx, ws)
	if err != nil {
		return err
	}
	var locked uuid.UUID
	err = tx.GetContext(ctx, &locked, `SELECT id FROM integration_configs WHERE id=$1 FOR UPDATE`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound("Integration")
	}
	if err != nil {
		return err
	}
	var latest struct {
		Revision int64  `db:"revision"`
		Digest   string `db:"digest"`
		Config   []byte `db:"config"`
		Enabled  bool   `db:"enabled"`
	}
	err = tx.GetContext(ctx, &latest, `SELECT revision,digest,config,enabled FROM runtime_catalog_revisions WHERE integration_id=$1 ORDER BY revision DESC LIMIT 1`, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing saved yet: there is no catalog for a controller to drain.
	case err != nil:
		return err
	case latest.Enabled != active:
		author := runtimeAuthor(ctx)
		if author == uuid.Nil {
			return apierror.Forbidden("switching the Runner requires a signed-in user")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_catalog_revisions(integration_id,revision,digest,config,enabled,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, latest.Revision+1, latest.Digest, latest.Config, active, author); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE integration_configs SET is_active=$2,updated_at=now() WHERE id=$1`, id, active); err != nil {
		return err
	}
	return tx.Commit()
}

// inheritCredentialRefs fills an account's missing or empty credential_ref from
// the same account in the previous revision. A new account, or input that is
// not a JSON object, passes through unchanged and the schema decides.
func inheritCredentialRefs(input, previous []byte) []byte {
	var next, prev map[string]json.RawMessage
	if json.Unmarshal(input, &next) != nil || json.Unmarshal(previous, &prev) != nil {
		return input
	}
	var nextAccounts, prevAccounts map[string]map[string]json.RawMessage
	if json.Unmarshal(next["accounts"], &nextAccounts) != nil || json.Unmarshal(prev["accounts"], &prevAccounts) != nil {
		return input
	}
	changed := false
	for ref, account := range nextAccounts {
		var current string
		if raw, ok := account["credential_ref"]; ok && (json.Unmarshal(raw, &current) != nil || current != "") {
			continue
		}
		stored, ok := prevAccounts[ref]["credential_ref"]
		if !ok || account == nil {
			continue
		}
		account["credential_ref"] = stored
		changed = true
	}
	if !changed {
		return input
	}
	accounts, err := json.Marshal(nextAccounts)
	if err != nil {
		return input
	}
	next["accounts"] = accounts
	out, err := json.Marshal(next)
	if err != nil {
		return input
	}
	return out
}

// RuntimeRevisionEntry is one saved catalog revision, newest first in Revisions.
type RuntimeRevisionEntry struct {
	Revision   int64     `db:"revision" json:"revision"`
	Digest     string    `db:"digest" json:"digest"`
	Enabled    bool      `db:"enabled" json:"enabled"`
	AuthorID   uuid.UUID `db:"author_id" json:"author_id"`
	AuthorName string    `db:"author_name" json:"author_name"`
	// AuthorType is "agent" when an agent key saved the revision for its
	// operator (AuthorID); AuthorAgentID and AuthorName then name the agent.
	AuthorType    string     `db:"author_type" json:"author_type"`
	AuthorAgentID *uuid.UUID `db:"author_agent_id" json:"author_agent_id,omitempty"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
}

// RuntimeRevisions is the body of GET /runtime/revisions. Managed tells a
// connected workspace with no saved revision apart from one without a connection.
type RuntimeRevisions struct {
	Managed   bool                   `json:"managed"`
	Revisions []RuntimeRevisionEntry `json:"revisions"`
}

// Revisions lists the workspace catalog revisions with their authors; same
// owner/admin gate as Inventory. No connection yet means managed=false.
func (r *RuntimeRepo) Revisions(ctx context.Context, ws uuid.UUID, actor RuntimeActor, limit int) (*RuntimeRevisions, error) {
	if _, err := runtimeCatalogEditor(ctx, r.db, ws, actor); err != nil {
		return nil, err
	}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil {
		return nil, err
	}
	out := &RuntimeRevisions{Revisions: []RuntimeRevisionEntry{}}
	if connection == nil {
		return out, nil
	}
	out.Managed = true
	err = r.db.SelectContext(ctx, &out.Revisions, `SELECT r.revision,r.digest,r.enabled,r.created_by AS author_id,
		COALESCE(ag.name,u.display_name,'') AS author_name,
		CASE WHEN r.author_agent_id IS NULL THEN 'user' ELSE 'agent' END AS author_type,r.author_agent_id,r.created_at
		FROM runtime_catalog_revisions r LEFT JOIN users u ON u.id=r.created_by LEFT JOIN agents ag ON ag.id=r.author_agent_id
		WHERE r.integration_id=$1 ORDER BY r.revision DESC LIMIT $2`, connection.ID, limit)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *RuntimeRepo) Inventory(ctx context.Context, ws uuid.UUID, actor RuntimeActor) (*domain.RuntimeSnapshot, error) {
	if _, err := runtimeCatalogEditor(ctx, r.db, ws, actor); err != nil {
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
		if _, ok := catalog.Controllers[row.Ref]; !ok {
			continue
		}
		var report domain.RuntimeReport
		err = json.Unmarshal(row.Data, &report)
		if err != nil {
			return nil, err
		}
		var grantRows int
		err = r.db.GetContext(ctx, &grantRows, `SELECT count(*) FROM agent_workspace_grants WHERE id=$1`, row.GrantID)
		if err != nil {
			return nil, err
		}
		if grantRows == 0 {
			continue
		}
		liveness, err := runtimeReportLiveness(ctx, r.db, connection, catalog, row.Ref, revision, digest, row.GrantID, row.Fingerprint, row.Data, row.At)
		if err != nil {
			return nil, err
		}
		current := liveness == domain.RuntimeControllerCurrent
		states = append(states, domain.RuntimeControllerState{ControllerRef: row.Ref, Report: report, ReceivedAt: row.At, Current: current})
	}
	return states, nil
}

// Desired returns one controller's slice of the current desired catalog. The
// caller must hold the exact configured reporter grant of that controller in
// the owner workspace, the same bar as Report: another agent's key gets 403,
// an unknown ref 404. Read-only; nothing is recorded.
func (r *RuntimeRepo) Desired(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor) (*domain.RuntimeDesired, error) {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return nil, apierror.Forbidden("controller desired state requires its exact owner-workspace agent key")
	}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime controller")
	}
	// Catalog, revision, digest and enabled all come from one immutable
	// revision row: reading integration_configs and the revision separately
	// could pair an old catalog with a newer digest if Save commits between.
	var latest struct {
		Revision int64  `db:"revision"`
		Digest   string `db:"digest"`
		Config   []byte `db:"config"`
		Enabled  bool   `db:"enabled"`
	}
	err = r.db.GetContext(ctx, &latest, `SELECT revision,digest,config,enabled FROM runtime_catalog_revisions WHERE integration_id=$1 ORDER BY revision DESC LIMIT 1`, connection.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Runtime controller")
	}
	if err != nil {
		return nil, err
	}
	catalog, err := domain.ParseRuntimeCatalog(latest.Config)
	if err != nil {
		return nil, err
	}
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return nil, apierror.NotFound("Runtime controller")
	}
	if _, err = runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: controller.ReporterGrantID}); err != nil {
		return nil, err
	}
	slice, _ := catalog.DesiredFor(ref)
	excluded, err := r.runtimeExclusions(ctx, r.db, ws, slice.Bindings)
	if err != nil {
		return nil, err
	}
	for _, e := range excluded {
		delete(slice.Bindings, e.Ref)
	}
	return &domain.RuntimeDesired{Revision: latest.Revision, Digest: latest.Digest, Enabled: latest.Enabled, DrainRequested: !latest.Enabled, ControllerRef: ref, Controller: controller, Catalog: slice, Excluded: excluded}, nil
}

func (r *RuntimeRepo) Report(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor, report domain.RuntimeReport) error {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return apierror.Forbidden("controller report requires its exact owner-workspace agent key")
	}
	if report.SchemaVersion != 2 || report.Revision < 1 || (report.Status != "applied" && report.Status != "rejected") || len(report.Capabilities) > 256 || len(report.Pools) > 256 || (report.Version != "" && !domain.ValidRuntimeVersion(report.Version)) {
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
	// Observations of a profile that only excluded bindings run are not
	// applied; the exclusions themselves are kept for the Runner screen.
	slice, _ := catalog.DesiredFor(ref)
	report.Excluded, err = r.runtimeExclusions(ctx, tx, ws, slice.Bindings)
	if err != nil {
		return err
	}
	if len(report.Excluded) > 0 {
		excluded := map[string]bool{}
		for _, e := range report.Excluded {
			excluded[e.Ref] = true
		}
		for profileRef := range report.Profiles {
			runs, kept := false, false
			for bindingRef, b := range slice.Bindings {
				if slices.Contains(b.PermittedProfiles, profileRef) {
					runs = true
					kept = kept || !excluded[bindingRef]
				}
			}
			if runs && !kept {
				delete(report.Profiles, profileRef)
			}
		}
	}
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
	return runtimeBindingView(owner, ref, connection.IsActive, catalog, binding, admission, revision, digest), nil
}

// runtimeBindingView is shared by the receiver read and the owner's policy save,
// so both answer in the same shape.
func runtimeBindingView(owner uuid.UUID, ref string, active bool, catalog *domain.RuntimeCatalog, binding domain.RuntimeBinding, admission RuntimeAdmission, revision int64, digest string) *RuntimeBindingView {
	profiles := map[string]domain.RuntimeProfile{}
	for _, p := range admission.PermittedProfiles {
		profiles[p] = catalog.Profiles[p]
	}
	// A receiver sees its own binding and permitted profile descriptions, never
	// the owner's catalog, credentials, unrelated tasks or other agents.
	return &RuntimeBindingView{ResourceOwnerWorkspaceID: owner, BindingRef: ref, Revision: revision, Digest: digest, Enabled: active && admission.Enabled, DrainRequested: !active, Binding: binding, Admission: admission, Profiles: profiles, PolicyProjection: runtimePolicyProjection(owner, catalog, binding, admission.PermittedProfiles, revision)}
}

// SetBindingPolicy rewrites the routing policy (and optionally the permitted
// profiles) of one binding inside the stored catalog. The server holds the
// unredacted catalog, so credential references and every other entry stay
// untouched and the client never has to echo a redacted catalog back. The save
// is a normal compare-and-swap on the catalog revision and creates a new
// immutable revision, exactly like Save.
func (r *RuntimeRepo) SetBindingPolicy(ctx context.Context, owner, receiver uuid.UUID, ref string, actor RuntimeActor, input RuntimePolicySave) (*RuntimeBindingView, error) {
	if input.IfRevision < 0 || len(input.PermittedProfiles) > 256 {
		return nil, apierror.BadRequest("invalid binding policy")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	err = runtimeAdmin(ctx, tx, owner, actor)
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
	revision, oldDigest, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return nil, err
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	binding, ok := catalog.Bindings[ref]
	if !ok || binding.Binding.WorkspaceID != receiver {
		return nil, apierror.NotFound("Runtime binding")
	}
	if revision != input.IfRevision {
		return nil, &apierror.Error{Code: http.StatusConflict, Message: "runtime desired revision changed", Details: "stale_revision"}
	}
	binding.Policy = input.Policy
	if input.PermittedProfiles != nil {
		binding.PermittedProfiles = input.PermittedProfiles
	}
	catalog.Bindings[ref] = binding
	canonical, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	// Same validation as a full save: schema, profiles within permitted, known
	// accounts, explicit API-reserve opt-in.
	catalog, err = domain.ParseRuntimeCatalog(canonical)
	if err != nil {
		return nil, apierror.BadRequest("invalid binding policy")
	}
	binding = catalog.Bindings[ref]
	hash := sha256.Sum256(canonical)
	digest := hex.EncodeToString(hash[:])
	revision++
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_catalog_revisions(integration_id,revision,digest,config,enabled,created_by) VALUES($1,$2,$3,$4,$5,$6)`, connection.ID, revision, digest, canonical, connection.IsActive, actor.UserID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE integration_configs SET config=$2,updated_at=now() WHERE id=$1`, connection.ID, canonical); err != nil {
		return nil, err
	}
	fingerprint, err := runtimeGrant(ctx, tx, binding.Binding)
	if err != nil {
		return nil, err
	}
	// The receiver's admission is bound to the catalog digest, which this edit
	// just changed. A policy edit only reorders and re-prefers profiles the
	// receiver already admitted (catalog validation rejects anything outside the
	// permitted list; the admitted set is intersected with it on read), so an
	// admission that matched the previous digest and the current grant is carried
	// to the new digest instead of silently disabling the agent. Its revision is
	// the receiver's token and stays as is.
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_binding_admissions SET catalog_digest=$3 WHERE integration_id=$1 AND binding_ref=$2 AND catalog_digest=$4 AND workspace_id=$5 AND agent_id=$6 AND grant_id=$7 AND grant_fingerprint=$8`, connection.ID, ref, digest, oldDigest, binding.Binding.WorkspaceID, binding.Binding.AgentID, binding.Binding.GrantID, fingerprint); err != nil {
		return nil, err
	}
	admission, err := runtimeAdmission(ctx, tx, connection.ID, ref, binding, fingerprint, digest)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return runtimeBindingView(owner, ref, connection.IsActive, catalog, binding, admission, revision, digest), nil
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
