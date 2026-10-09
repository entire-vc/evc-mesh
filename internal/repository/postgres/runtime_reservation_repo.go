package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Execution admission (R3). One transaction reserves the identity cap, the
// task writer, the controller worker and every server-derived canonical pool,
// or nothing. Consume is the durable start CAS; only a proven release or the
// TTL of an UNCONSUMED reservation frees occupancy.

func runtimeReservationErr(code int, reason string) *apierror.Error {
	messages := map[int]string{
		http.StatusConflict:  "runtime reservation conflict",
		http.StatusGone:      "runtime reservation is no longer usable",
		http.StatusLocked:    "runtime execution admission is disabled",
		http.StatusForbidden: "runtime reservation is not permitted",
	}
	return &apierror.Error{Code: code, Message: messages[code], Details: reason}
}

const runtimeReservationCols = `id,fence,integration_id,binding_ref,agent_id,workspace_id,grant_id,grant_fingerprint,
 scope_digest,task_id,checkout_generation,checkout_request_id,controller_ref,worker_ref,profile_ref,catalog_revision,
 catalog_digest,admission_revision,profile_revision,pools,pool_set_digest,state,expires_at,receipt_id,consumed_at,
 release_id,released_at,release_proof_kind,created_at`

type runtimeReservationRow struct {
	ID                 uuid.UUID      `db:"id"`
	Fence              int64          `db:"fence"`
	IntegrationID      uuid.UUID      `db:"integration_id"`
	BindingRef         string         `db:"binding_ref"`
	AgentID            uuid.UUID      `db:"agent_id"`
	WorkspaceID        uuid.UUID      `db:"workspace_id"`
	GrantID            uuid.UUID      `db:"grant_id"`
	GrantFingerprint   string         `db:"grant_fingerprint"`
	ScopeDigest        string         `db:"scope_digest"`
	TaskID             uuid.UUID      `db:"task_id"`
	CheckoutGeneration int64          `db:"checkout_generation"`
	CheckoutRequestID  uuid.NullUUID  `db:"checkout_request_id"`
	ControllerRef      string         `db:"controller_ref"`
	WorkerRef          string         `db:"worker_ref"`
	ProfileRef         string         `db:"profile_ref"`
	CatalogRevision    int64          `db:"catalog_revision"`
	CatalogDigest      string         `db:"catalog_digest"`
	AdmissionRevision  int64          `db:"admission_revision"`
	ProfileRevision    string         `db:"profile_revision"`
	Pools              []byte         `db:"pools"`
	PoolSetDigest      string         `db:"pool_set_digest"`
	State              string         `db:"state"`
	ExpiresAt          time.Time      `db:"expires_at"`
	ReceiptID          uuid.NullUUID  `db:"receipt_id"`
	ConsumedAt         sql.NullTime   `db:"consumed_at"`
	ReleaseID          uuid.NullUUID  `db:"release_id"`
	ReleasedAt         sql.NullTime   `db:"released_at"`
	ReleaseProofKind   sql.NullString `db:"release_proof_kind"`
	CreatedAt          time.Time      `db:"created_at"`
}

func nullUUIDPtr(v uuid.NullUUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := v.UUID
	return &id
}

func sameRequest(a uuid.NullUUID, b *uuid.UUID) bool {
	if b == nil {
		return !a.Valid
	}
	return a.Valid && a.UUID == *b
}

// effectiveState: an unconsumed reservation past its TTL is expired even
// before a sweep stores it; a consumed one past its run lease is reconcile
// and remains occupied.
func (row *runtimeReservationRow) effectiveState(now time.Time) string {
	if row.State == domain.RuntimeReservationReserved && !row.ExpiresAt.After(now) {
		return domain.RuntimeReservationExpired
	}
	if row.State == domain.RuntimeReservationConsumed && !row.ExpiresAt.After(now) {
		return domain.RuntimeReservationReconcile
	}
	return row.State
}

func (row *runtimeReservationRow) toDomain(owner uuid.UUID) (*domain.RuntimeReservation, error) {
	pools := []string{}
	err := json.Unmarshal(row.Pools, &pools)
	if err != nil {
		return nil, err
	}
	out := &domain.RuntimeReservation{
		ReservationID: row.ID, Fence: row.Fence, State: row.effectiveState(time.Now()),
		ResourceOwnerWorkspaceID: owner, BindingRef: row.BindingRef, AgentID: row.AgentID, WorkspaceID: row.WorkspaceID,
		GrantID: row.GrantID, TaskID: row.TaskID, CheckoutGeneration: row.CheckoutGeneration,
		CheckoutRequestID: nullUUIDPtr(row.CheckoutRequestID), WorkerRef: row.WorkerRef, ControllerRef: row.ControllerRef,
		ProfileID: row.ProfileRef, CatalogRevision: row.CatalogRevision, CatalogDigest: row.CatalogDigest,
		AdmissionRevision: row.AdmissionRevision, ProfileRevision: row.ProfileRevision, Pools: pools,
		PoolSetDigest: row.PoolSetDigest, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt,
	}
	if row.ReceiptID.Valid {
		out.ConsumeReceipt = &domain.RuntimeConsumeReceipt{ReceiptID: row.ReceiptID.UUID, ConsumedAt: row.ConsumedAt.Time, RunLeaseExpiresAt: row.ExpiresAt}
	}
	if row.ReleaseID.Valid {
		out.ReleaseReceipt = &domain.RuntimeReleaseReceipt{ReleaseID: row.ReleaseID.UUID, ReleasedAt: row.ReleasedAt.Time, ProofKind: row.ReleaseProofKind.String}
	}
	return out, nil
}

// runtimeSharedConnection takes FOR SHARE on the owner connection: a concurrent
// catalog Save or receiving Admit (both FOR UPDATE) cannot change the revision
// that this transaction checked until it commits.
func runtimeSharedConnection(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID) (*domain.IntegrationConfig, error) {
	var row integrationConfigRow
	err := sqlx.GetContext(ctx, q, &row, `SELECT `+integrationConfigSelectCols+` FROM integration_configs WHERE workspace_id=$1 AND provider='agent_runtime' FOR SHARE`, ws)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Runtime binding")
	}
	if err != nil {
		return nil, err
	}
	value := row.toDomain()
	return &value, nil
}

func runtimeReservationWriter(actor RuntimeActor, receiver uuid.UUID) error {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != receiver {
		return apierror.Forbidden("runtime reservation requires the bound workspace agent key")
	}
	return nil
}

// runtimeSweepExpired stores expiry of unconsumed reservations and frees their
// claims. Consumed rows are never touched: their occupancy ends only on release.
func runtimeSweepExpired(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, `WITH e AS (
 UPDATE runtime_reservations SET state='expired' WHERE state='reserved' AND expires_at<=now() RETURNING id
) DELETE FROM runtime_reservation_pool_claims c USING e WHERE c.reservation_id=e.id`)
	return err
}

func runtimeExpireOne(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `UPDATE runtime_reservations SET state='expired' WHERE id=$1 AND state='reserved'`, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM runtime_reservation_pool_claims WHERE reservation_id=$1`, id)
	return err
}

func runtimeLockedReservation(ctx context.Context, tx *sqlx.Tx, id, integration, receiver uuid.UUID, ref string) (*runtimeReservationRow, error) {
	var row runtimeReservationRow
	err := tx.GetContext(ctx, &row, `SELECT `+runtimeReservationCols+` FROM runtime_reservations
 WHERE id=$1 AND integration_id=$2 AND workspace_id=$3 AND binding_ref=$4 FOR UPDATE`, id, integration, receiver, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Runtime reservation")
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// runtimeTaskWriter verifies that the caller still holds the live checkout the
// reservation is (or will be) bound to. FOR SHARE keeps it stable until commit.
func runtimeTaskWriter(ctx context.Context, tx *sqlx.Tx, task, receiver, agent uuid.UUID, generation int64, request *uuid.UUID) error {
	var row struct {
		Holder     uuid.NullUUID `db:"checked_out_by"`
		Generation int64         `db:"checkout_generation"`
		Request    uuid.NullUUID `db:"checkout_request_id"`
		Live       bool          `db:"live"`
	}
	err := tx.GetContext(ctx, &row, `SELECT t.checked_out_by,t.checkout_generation,t.checkout_request_id,
 (t.checkout_expires IS NULL OR t.checkout_expires>now()) AS live
 FROM tasks t JOIN projects p ON p.id=t.project_id
 WHERE t.id=$1 AND p.workspace_id=$2 AND t.deleted_at IS NULL FOR SHARE OF t`, task, receiver)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound("Task")
	}
	if err != nil {
		return err
	}
	if !row.Holder.Valid || row.Holder.UUID != agent || !row.Live || row.Generation != generation || !sameRequest(row.Request, request) {
		return runtimeReservationErr(http.StatusConflict, "writer_lease_mismatch")
	}
	return nil
}

func runtimeScopeDigest(integration uuid.UUID, ref string, in domain.RuntimeAcquireInput) (string, error) {
	in.IdempotencyKey = ""
	data, err := json.Marshal(struct {
		Integration uuid.UUID                  `json:"integration"`
		Binding     string                     `json:"binding"`
		Input       domain.RuntimeAcquireInput `json:"input"`
	}{integration, ref, in})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runtimeUniqueConflict(err error) error {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23505" {
		return err
	}
	switch pqErr.Constraint {
	case "runtime_reservations_active_task":
		return runtimeReservationErr(http.StatusConflict, "task_writer_active")
	case "runtime_reservations_active_worker":
		return runtimeReservationErr(http.StatusConflict, "worker_active")
	case "runtime_reservation_pool_claims_pool_id_slot_key":
		return runtimeReservationErr(http.StatusConflict, "pool_exhausted")
	}
	return runtimeReservationErr(http.StatusConflict, "idempotency_scope_mismatch")
}

// runtimeAdmissionTarget resolves the binding/admission/profile the caller may
// currently use and returns 423 for every disabled switch.
type runtimeAdmissionTarget struct {
	connection  *domain.IntegrationConfig
	catalog     *domain.RuntimeCatalog
	binding     domain.RuntimeBinding
	fingerprint string
	revision    int64
	digest      string
	admission   RuntimeAdmission
}

func runtimeLoadTarget(ctx context.Context, tx *sqlx.Tx, owner, receiver uuid.UUID, ref string, actor RuntimeActor) (*runtimeAdmissionTarget, error) {
	connection, err := runtimeSharedConnection(ctx, tx, owner)
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
	fingerprint, err := runtimeBoundCaller(ctx, tx, receiver, binding, actor)
	if err != nil {
		return nil, err
	}
	revision, digest, err := runtimeRevision(ctx, tx, connection.ID)
	if err != nil {
		return nil, err
	}
	admission, err := runtimeAdmission(ctx, tx, connection.ID, ref, binding, fingerprint, digest)
	if err != nil {
		return nil, err
	}
	return &runtimeAdmissionTarget{connection: connection, catalog: catalog, binding: binding, fingerprint: fingerprint, revision: revision, digest: digest, admission: admission}, nil
}

// checkProfile applies every enable switch and the admitted profile set.
func (t *runtimeAdmissionTarget) checkProfile(profile string) error {
	if !t.connection.IsActive || !t.binding.Enabled || !t.admission.Enabled {
		return runtimeReservationErr(http.StatusLocked, "runtime_disabled")
	}
	if !slices.Contains(t.admission.PermittedProfiles, profile) {
		return runtimeReservationErr(http.StatusForbidden, "profile_not_admitted")
	}
	p := t.catalog.Profiles[profile]
	if !p.Enabled || !t.catalog.Controllers[p.ControllerRef].Enabled {
		return runtimeReservationErr(http.StatusLocked, "runtime_disabled")
	}
	return nil
}

// AcquireReservation returns the reservation and whether this call created it.
func (r *RuntimeRepo) AcquireReservation(ctx context.Context, owner, receiver uuid.UUID, ref string, actor RuntimeActor, in domain.RuntimeAcquireInput) (*domain.RuntimeReservation, bool, error) {
	if !in.Valid() {
		return nil, false, apierror.BadRequest("invalid runtime reservation request")
	}
	err := runtimeReservationWriter(actor, receiver)
	if err != nil {
		return nil, false, err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	target, err := runtimeLoadTarget(ctx, tx, owner, receiver, ref, actor)
	if err != nil {
		return nil, false, err
	}
	agent := target.binding.Binding.AgentID
	// Serializes every acquire of one identity across all workspaces, bindings
	// and controllers, so the cap count below cannot be raced.
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('runtime-agent:'||$1::text, 0))`, agent)
	if err != nil {
		return nil, false, err
	}
	scope, err := runtimeScopeDigest(target.connection.ID, ref, in)
	if err != nil {
		return nil, false, err
	}
	var existing runtimeReservationRow
	err = tx.GetContext(ctx, &existing, `SELECT `+runtimeReservationCols+` FROM runtime_reservations WHERE agent_id=$1 AND idempotency_key=$2`, agent, in.IdempotencyKey)
	if err == nil {
		if existing.ScopeDigest != scope {
			return nil, false, runtimeReservationErr(http.StatusConflict, "idempotency_scope_mismatch")
		}
		value, convErr := existing.toDomain(owner)
		return value, false, convErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	err = target.checkProfile(in.ProfileID)
	if err != nil {
		return nil, false, err
	}
	pools, ok := target.catalog.RuntimeProfilePools(owner, in.ProfileID)
	if !ok {
		return nil, false, runtimeReservationErr(http.StatusForbidden, "profile_not_admitted")
	}
	poolDigest := domain.RuntimePoolSetDigest(pools)
	profileRevision := domain.RuntimeProfileRevision(target.revision)
	if in.ExpectedCatalogRevision != target.revision || in.ExpectedCatalogDigest != target.digest ||
		in.ExpectedAdmissionRevision != target.admission.Revision || in.ExpectedProfileRevision != profileRevision ||
		in.ExpectedPoolSetDigest != poolDigest {
		return nil, false, runtimeReservationErr(http.StatusConflict, "stale_revision")
	}
	err = runtimeTaskWriter(ctx, tx, in.TaskID, receiver, agent, in.CheckoutGeneration, in.CheckoutRequestID)
	if err != nil {
		return nil, false, err
	}
	err = runtimeSweepExpired(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	var limit int
	err = tx.GetContext(ctx, &limit, `SELECT max_concurrent_tasks FROM agents WHERE id=$1 AND deleted_at IS NULL`, agent)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		return nil, false, runtimeReservationErr(http.StatusLocked, "identity_cap_unset")
	}
	var active int
	err = tx.GetContext(ctx, &active, `SELECT count(*) FROM runtime_reservations WHERE agent_id=$1 AND state IN ('reserved','consumed')`, agent)
	if err != nil {
		return nil, false, err
	}
	if active >= limit {
		return nil, false, runtimeReservationErr(http.StatusConflict, "identity_cap_reached")
	}
	controller := target.catalog.Profiles[in.ProfileID].ControllerRef
	var busy struct {
		Task   bool `db:"task"`
		Worker bool `db:"worker"`
	}
	err = tx.GetContext(ctx, &busy, `SELECT
 EXISTS (SELECT 1 FROM runtime_reservations WHERE task_id=$1 AND state IN ('reserved','consumed')) AS task,
 EXISTS (SELECT 1 FROM runtime_reservations WHERE integration_id=$2 AND controller_ref=$3 AND worker_ref=$4 AND state IN ('reserved','consumed')) AS worker`,
		in.TaskID, target.connection.ID, controller, in.WorkerRef)
	if err != nil {
		return nil, false, err
	}
	if busy.Task {
		return nil, false, runtimeReservationErr(http.StatusConflict, "task_writer_active")
	}
	if busy.Worker {
		return nil, false, runtimeReservationErr(http.StatusConflict, "worker_active")
	}
	capacity := map[string]int{}
	for _, pool := range target.catalog.Pools {
		capacity[domain.RuntimeCanonicalPoolID(owner, pool)] = pool.MaxConcurrency
	}
	slots := make(map[string]int, len(pools))
	for _, pool := range pools { // sorted: a stable lock order across transactions
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('runtime-pool:'||$1, 0))`, pool)
		if err != nil {
			return nil, false, err
		}
		var used []int
		err = tx.SelectContext(ctx, &used, `SELECT slot FROM runtime_reservation_pool_claims WHERE pool_id=$1`, pool)
		if err != nil {
			return nil, false, err
		}
		if len(used) >= capacity[pool] {
			return nil, false, runtimeReservationErr(http.StatusConflict, "pool_exhausted")
		}
		slot := 1
		for slices.Contains(used, slot) {
			slot++
		}
		slots[pool] = slot
	}
	poolsJSON, err := json.Marshal(pools)
	if err != nil {
		return nil, false, err
	}
	var row runtimeReservationRow
	err = tx.GetContext(ctx, &row, `INSERT INTO runtime_reservations(id,fence,integration_id,binding_ref,agent_id,workspace_id,grant_id,
 grant_fingerprint,idempotency_key,scope_digest,task_id,checkout_generation,checkout_request_id,controller_ref,worker_ref,profile_ref,
 catalog_revision,catalog_digest,admission_revision,profile_revision,pools,pool_set_digest,state,expires_at)
 VALUES($1,nextval('runtime_reservation_fence_seq'),$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,'reserved',now()+make_interval(secs=>$22))
 RETURNING `+runtimeReservationCols,
		uuid.New(), target.connection.ID, ref, agent, receiver, target.binding.Binding.GrantID, target.fingerprint, in.IdempotencyKey, scope,
		in.TaskID, in.CheckoutGeneration, in.CheckoutRequestID, controller, in.WorkerRef, in.ProfileID, target.revision, target.digest,
		target.admission.Revision, profileRevision, poolsJSON, poolDigest, in.TTLSeconds)
	if err != nil {
		return nil, false, runtimeUniqueConflict(err)
	}
	for _, pool := range pools {
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_reservation_pool_claims(reservation_id,pool_id,slot) VALUES($1,$2,$3)`, row.ID, pool, slots[pool])
		if err != nil {
			return nil, false, runtimeUniqueConflict(err)
		}
	}
	err = tx.Commit()
	if err != nil {
		return nil, false, err
	}
	value, err := row.toDomain(owner)
	return value, true, err
}

// ConsumeReservation is the durable start CAS. A replay of a committed consume
// returns the same receipt; nothing else turns a reservation into a run.
func (r *RuntimeRepo) ConsumeReservation(ctx context.Context, owner, receiver uuid.UUID, ref string, id uuid.UUID, actor RuntimeActor, in domain.RuntimeConsumeInput) (*domain.RuntimeReservation, error) {
	if !in.Valid() {
		return nil, apierror.BadRequest("invalid runtime consume request")
	}
	err := runtimeReservationWriter(actor, receiver)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Lock order: connection (share) before the reservation row, like acquire.
	target, err := runtimeLoadTarget(ctx, tx, owner, receiver, ref, actor)
	if err != nil {
		return nil, err
	}
	row, err := runtimeLockedReservation(ctx, tx, id, target.connection.ID, receiver, ref)
	if err != nil {
		return nil, err
	}
	if row.AgentID != actor.AgentID {
		return nil, apierror.NotFound("Runtime reservation")
	}
	if row.Fence != in.Fence || row.CheckoutGeneration != in.CheckoutGeneration {
		return nil, runtimeReservationErr(http.StatusConflict, "fence_mismatch")
	}
	switch row.effectiveState(time.Now()) {
	case domain.RuntimeReservationConsumed, domain.RuntimeReservationReconcile:
		return row.toDomain(owner)
	case domain.RuntimeReservationReleased:
		return nil, runtimeReservationErr(http.StatusGone, "reservation_released")
	case domain.RuntimeReservationExpired:
		err = runtimeExpireOne(ctx, tx, row.ID)
		if err != nil {
			return nil, err
		}
		err = tx.Commit()
		if err != nil {
			return nil, err
		}
		return nil, runtimeReservationErr(http.StatusGone, "reservation_expired")
	}
	if target.binding.Binding.GrantID != row.GrantID || target.binding.Binding.AgentID != row.AgentID || target.fingerprint != row.GrantFingerprint {
		return nil, runtimeReservationErr(http.StatusForbidden, "grant_changed")
	}
	err = target.checkProfile(row.ProfileRef)
	if err != nil {
		return nil, err
	}
	pools, ok := target.catalog.RuntimeProfilePools(owner, row.ProfileRef)
	if !ok || target.revision != row.CatalogRevision || target.digest != row.CatalogDigest ||
		target.admission.Revision != row.AdmissionRevision || domain.RuntimeProfileRevision(target.revision) != row.ProfileRevision ||
		domain.RuntimePoolSetDigest(pools) != row.PoolSetDigest {
		return nil, runtimeReservationErr(http.StatusConflict, "stale_revision")
	}
	err = runtimeTaskWriter(ctx, tx, row.TaskID, receiver, row.AgentID, row.CheckoutGeneration, nullUUIDPtr(row.CheckoutRequestID))
	if err != nil {
		return nil, err
	}
	var consumed runtimeReservationRow
	err = tx.GetContext(ctx, &consumed, `UPDATE runtime_reservations SET state='consumed',receipt_id=$2,consumed_at=now(),
 expires_at=now()+make_interval(secs=>$3) WHERE id=$1 AND state='reserved' RETURNING `+runtimeReservationCols, row.ID, uuid.New(), in.RunLeaseSeconds)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return consumed.toDomain(owner)
}

// ReleaseReservation frees occupancy only for the exact original writer and a
// positive stop proof. It deliberately does not require a live grant: a
// revoked grant must not leak capacity forever.
func (r *RuntimeRepo) ReleaseReservation(ctx context.Context, owner, receiver uuid.UUID, ref string, id uuid.UUID, actor RuntimeActor, in domain.RuntimeReleaseInput) (*domain.RuntimeReservation, error) {
	if !in.Valid() {
		return nil, apierror.BadRequest("invalid runtime release request")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.reservationForReader(ctx, tx, owner, receiver, ref, id, actor)
	if err != nil {
		return nil, err
	}
	if row.Fence != in.Fence || row.CheckoutGeneration != in.CheckoutGeneration || !sameRequest(row.CheckoutRequestID, in.CheckoutRequestID) {
		return nil, runtimeReservationErr(http.StatusConflict, "writer_mismatch")
	}
	state := row.effectiveState(time.Now())
	switch state {
	case domain.RuntimeReservationReleased:
		return row.toDomain(owner)
	case domain.RuntimeReservationExpired:
		err = runtimeExpireOne(ctx, tx, row.ID)
		if err != nil {
			return nil, err
		}
		err = tx.Commit()
		if err != nil {
			return nil, err
		}
		row.State = domain.RuntimeReservationExpired
		return row.toDomain(owner)
	}
	if !in.ProofAccepted(row.State) {
		return nil, runtimeReservationErr(http.StatusConflict, "release_unproven")
	}
	var released runtimeReservationRow
	err = tx.GetContext(ctx, &released, `UPDATE runtime_reservations SET state='released',release_id=$2,released_at=now(),
 release_proof_kind=$3,release_evidence_ref=NULLIF($4,'') WHERE id=$1 AND state IN ('reserved','consumed') RETURNING `+runtimeReservationCols,
		row.ID, uuid.New(), in.Proof.Kind, in.Proof.EvidenceRef)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM runtime_reservation_pool_claims WHERE reservation_id=$1`, row.ID)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}
	return released.toDomain(owner)
}

// GetReservation lets the writer reconcile a lost consume/release response.
func (r *RuntimeRepo) GetReservation(ctx context.Context, owner, receiver uuid.UUID, ref string, id uuid.UUID, actor RuntimeActor) (*domain.RuntimeReservation, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := r.reservationForReader(ctx, tx, owner, receiver, ref, id, actor)
	if err != nil {
		return nil, err
	}
	return row.toDomain(owner)
}

// reservationForReader: the exact bound agent, or a receiving workspace
// owner/admin. Anyone else, including another agent, gets not found.
func (r *RuntimeRepo) reservationForReader(ctx context.Context, tx *sqlx.Tx, owner, receiver uuid.UUID, ref string, id uuid.UUID, actor RuntimeActor) (*runtimeReservationRow, error) {
	if actor.Connector {
		return nil, apierror.Forbidden("runtime reservation requires the bound workspace agent key")
	}
	connection, err := runtimeConnection(ctx, tx, owner, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime reservation")
	}
	row, err := runtimeLockedReservation(ctx, tx, id, connection.ID, receiver, ref)
	if err != nil {
		return nil, err
	}
	if actor.AgentID != uuid.Nil {
		if actor.AgentID != row.AgentID || actor.AuthWorkspaceID != receiver {
			return nil, apierror.NotFound("Runtime reservation")
		}
		return row, nil
	}
	err = runtimeAdmin(ctx, tx, receiver, actor)
	if err != nil {
		return nil, err
	}
	return row, nil
}
