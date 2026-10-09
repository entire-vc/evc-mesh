package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Reservation states. RuntimeReservationReconcile is reported, never stored: a
// consumed reservation past its run lease stays occupied until a proven release.
const (
	RuntimeReservationReserved  = "reserved"
	RuntimeReservationConsumed  = "consumed"
	RuntimeReservationReconcile = "reconcile"
	RuntimeReservationReleased  = "released"
	RuntimeReservationExpired   = "expired"
)

// Release proof kinds. not_started is valid only before consume succeeded.
const (
	RuntimeProofNoChild           = "no_child"
	RuntimeProofStoppedBirthProof = "stopped_process_birth_proof"
	RuntimeProofNotStarted        = "not_started"
)

type RuntimeAcquireInput struct {
	IdempotencyKey            string     `json:"idempotency_key"`
	ProfileID                 string     `json:"profile_id"`
	TaskID                    uuid.UUID  `json:"task_id"`
	CheckoutGeneration        int64      `json:"checkout_generation"`
	CheckoutRequestID         *uuid.UUID `json:"checkout_request_id"`
	WorkerRef                 string     `json:"worker_ref"`
	ExpectedCatalogRevision   int64      `json:"expected_catalog_revision"`
	ExpectedCatalogDigest     string     `json:"expected_catalog_digest"`
	ExpectedAdmissionRevision int64      `json:"expected_admission_revision"`
	ExpectedProfileRevision   string     `json:"expected_profile_revision"`
	ExpectedPoolSetDigest     string     `json:"expected_pool_set_digest"`
	TTLSeconds                int        `json:"ttl_seconds"`
}

// Valid checks shape only; every authority decision is made by the repository.
func (in RuntimeAcquireInput) Valid() bool {
	return ValidRuntimeRef(in.IdempotencyKey) && ValidRuntimeRef(in.ProfileID) && ValidRuntimeRef(in.WorkerRef) &&
		in.TaskID != uuid.Nil && in.CheckoutGeneration > 0 && (in.CheckoutRequestID == nil || *in.CheckoutRequestID != uuid.Nil) &&
		in.ExpectedCatalogRevision > 0 && in.ExpectedCatalogDigest != "" && in.ExpectedAdmissionRevision > 0 &&
		in.ExpectedProfileRevision != "" && in.ExpectedPoolSetDigest != "" && in.TTLSeconds >= 10 && in.TTLSeconds <= 900
}

type RuntimeConsumeInput struct {
	Fence              int64 `json:"fence"`
	CheckoutGeneration int64 `json:"checkout_generation"`
	RunLeaseSeconds    int   `json:"run_lease_seconds"`
}

func (in RuntimeConsumeInput) Valid() bool {
	return in.Fence > 0 && in.CheckoutGeneration > 0 && in.RunLeaseSeconds >= 60 && in.RunLeaseSeconds <= 86400
}

type RuntimeReleaseProof struct {
	Kind        string `json:"kind"`
	EvidenceRef string `json:"evidence_ref"`
}

type RuntimeReleaseInput struct {
	Fence              int64               `json:"fence"`
	CheckoutRequestID  *uuid.UUID          `json:"checkout_request_id"`
	CheckoutGeneration int64               `json:"checkout_generation"`
	Stopped            bool                `json:"stopped"`
	Proof              RuntimeReleaseProof `json:"proof"`
}

func (in RuntimeReleaseInput) Valid() bool {
	return in.Fence > 0 && in.CheckoutGeneration > 0 && (in.CheckoutRequestID == nil || *in.CheckoutRequestID != uuid.Nil) &&
		(in.Proof.EvidenceRef == "" || ValidRuntimeRef(in.Proof.EvidenceRef))
}

// ProofAccepted reports whether the release positively proves that nothing
// started or that the started process is gone. Anything else keeps occupancy.
func (in RuntimeReleaseInput) ProofAccepted(state string) bool {
	if !in.Stopped {
		return false
	}
	switch in.Proof.Kind {
	case RuntimeProofNoChild:
		return in.Proof.EvidenceRef == "" || ValidRuntimeRef(in.Proof.EvidenceRef)
	case RuntimeProofStoppedBirthProof:
		return ValidRuntimeRef(in.Proof.EvidenceRef)
	case RuntimeProofNotStarted:
		return state == RuntimeReservationReserved
	}
	return false
}

type RuntimeConsumeReceipt struct {
	ReceiptID         uuid.UUID `json:"receipt_id"`
	ConsumedAt        time.Time `json:"consumed_at"`
	RunLeaseExpiresAt time.Time `json:"run_lease_expires_at"`
}

type RuntimeReleaseReceipt struct {
	ReleaseID  uuid.UUID `json:"release_id"`
	ReleasedAt time.Time `json:"released_at"`
	ProofKind  string    `json:"proof_kind"`
}

// RuntimeReservation is the wire form. It deliberately has no field for the
// idempotency key, grant-key fingerprint or credential references.
type RuntimeReservation struct {
	ReservationID            uuid.UUID              `json:"reservation_id"`
	Fence                    int64                  `json:"fence"`
	State                    string                 `json:"state"`
	ResourceOwnerWorkspaceID uuid.UUID              `json:"resource_owner_workspace_id"`
	BindingRef               string                 `json:"binding_ref"`
	AgentID                  uuid.UUID              `json:"agent_id"`
	WorkspaceID              uuid.UUID              `json:"workspace_id"`
	GrantID                  uuid.UUID              `json:"grant_id"`
	TaskID                   uuid.UUID              `json:"task_id"`
	CheckoutGeneration       int64                  `json:"checkout_generation"`
	CheckoutRequestID        *uuid.UUID             `json:"checkout_request_id"`
	WorkerRef                string                 `json:"worker_ref"`
	ControllerRef            string                 `json:"controller_ref"`
	ProfileID                string                 `json:"profile_id"`
	CatalogRevision          int64                  `json:"catalog_revision"`
	CatalogDigest            string                 `json:"catalog_digest"`
	AdmissionRevision        int64                  `json:"admission_revision"`
	ProfileRevision          string                 `json:"profile_revision"`
	Pools                    []string               `json:"pools"`
	PoolSetDigest            string                 `json:"pool_set_digest"`
	ExpiresAt                time.Time              `json:"expires_at"`
	CreatedAt                time.Time              `json:"created_at"`
	ConsumeReceipt           *RuntimeConsumeReceipt `json:"consume_receipt"`
	ReleaseReceipt           *RuntimeReleaseReceipt `json:"release_receipt"`
}

// RuntimeCanonicalPoolID is the cross-controller identity of a quota pool.
func RuntimeCanonicalPoolID(owner uuid.UUID, pool RuntimePool) string {
	return owner.String() + ":" + pool.Provider + ":" + pool.ResourceRef
}

// RuntimeProfileRevision is the profile revision published in the policy
// projection: any catalog save creates a new one.
func RuntimeProfileRevision(catalogRevision int64) string {
	return "rev-" + strconv.FormatInt(catalogRevision, 10)
}

// RuntimeProfilePools derives every canonical pool a profile consumes. The
// client cannot choose or omit a pool; it can only state the digest it expects.
func (c *RuntimeCatalog) RuntimeProfilePools(owner uuid.UUID, profileRef string) ([]string, bool) {
	profile, ok := c.Profiles[profileRef]
	if !ok {
		return nil, false
	}
	account, ok := c.Accounts[profile.AccountRef]
	if !ok {
		return nil, false
	}
	refs := account.QuotaPoolsByMode[profile.ExecutionMode]
	if len(refs) == 0 {
		return nil, false
	}
	pools := make([]string, 0, len(refs))
	for _, ref := range refs {
		name, ok := c.ResolvePool(ref)
		if !ok {
			return nil, false
		}
		id := RuntimeCanonicalPoolID(owner, c.Pools[name])
		if !slices.Contains(pools, id) {
			pools = append(pools, id)
		}
	}
	slices.Sort(pools)
	return pools, true
}

// RuntimePoolSetDigest is lowercase hex SHA-256 of sorted canonical ids joined by "\n".
func RuntimePoolSetDigest(pools []string) string {
	sorted := slices.Clone(pools)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}
