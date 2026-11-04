package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// FileSigner is the optional host-local issuer signer: one Ed25519 key read once
// from a dedicated file. It is never derived from a request body, the publisher,
// the channel HMAC or VAPID material. IssueReceipt still verifies every signature
// under the ENROLLED issuer key, so a file whose public key was not enrolled
// produces no receipt.
type FileSigner struct{ key ed25519.PrivateKey }

// LoadFileSigner reads a base64 (canonical, std) 32-byte Ed25519 seed. The file
// must be a regular, non-symlink file without group/other permission bits.
// Every failure is an error that never includes key material.
func LoadFileSigner(path string) (*FileSigner, error) {
	raw, err := readHostFile(path, true)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("native review signer key file must hold canonical base64 of exactly 32 bytes")
	}
	return &FileSigner{key: ed25519.NewKeyFromSeed(seed)}, nil
}

func (s *FileSigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	if s == nil || len(s.key) != ed25519.PrivateKeySize {
		return nil, errors.New("signer not loaded")
	}
	return ed25519.Sign(s.key, message), nil
}

// PublicKey is the base64 public key to be enrolled as the issuer pin.
func (s *FileSigner) PublicKey() string {
	return base64.StdEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

func readHostFile(path string, private bool) ([]byte, error) {
	if path == "" {
		return nil, errors.New("empty path")
	}
	// O_NOFOLLOW makes the open itself refuse a symlink; every check below runs
	// on the opened descriptor, so there is no validate-then-open window.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file (no symlink)", path)
	}
	if private && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must not be accessible by group/other (chmod 600)", path)
	}
	if fi.Size() == 0 || fi.Size() > 1<<20 {
		return nil, fmt.Errorf("%s must be non-empty and at most 1 MiB", path)
	}
	return io.ReadAll(io.LimitReader(f, 1<<20+1))
}

// ReadIssuerPublicKeyFile reads the host-local issuer public key (the
// trusted provenance of the pin): canonical base64 of 32 bytes.
func ReadIssuerPublicKeyFile(path string) (string, error) {
	raw, err := readHostFile(path, false)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(raw))
	if _, err := domain.ParseIssuerKey(v); err != nil {
		return "", err
	}
	return v, nil
}

// EnrollmentManifest is the closed operator input. issuer_key is deliberately
// absent: the pin comes from the operator's host-local public key file, never
// from the manifest (and so never from a publisher). scope_domain is fixed.
type EnrollmentManifest struct {
	ID                     uuid.UUID `json:"id"`
	WorkspaceID            uuid.UUID `json:"workspace_id"`
	ProjectID              uuid.UUID `json:"project_id"`
	IssuerActorID          uuid.UUID `json:"issuer_actor_id"`
	PublisherActorID       uuid.UUID `json:"publisher_actor_id"`
	TransportSourceID      uuid.UUID `json:"transport_source_id"`
	ConsumerActorID        uuid.UUID `json:"consumer_actor_id"`
	AuthorityIssuerActorID uuid.UUID `json:"authority_issuer_actor_id"`
	ResultAuthorActorID    uuid.UUID `json:"result_author_actor_id"`
	OwnerActorID           uuid.UUID `json:"owner_actor_id"`
	AuthorityID            uuid.UUID `json:"authority_id"`
	ReceiverTask           uuid.UUID `json:"receiver_task"`
	ReceiverAttempt        uuid.UUID `json:"receiver_attempt"`
	ReceiverSession        uuid.UUID `json:"receiver_session"`
	ReceiverGeneration     int64     `json:"receiver_generation"`
	NativeRegistration     string    `json:"native_registration"`
	NativeStage            string    `json:"native_stage"`
	NativePackageSHA       string    `json:"native_package_sha256"`
	ReviewTaskID           uuid.UUID `json:"review_task_id"`
	SourceTaskID           uuid.UUID `json:"source_task_id"`
	ReviewerFamily         string    `json:"reviewer_family"`
	AuthorFamily           string    `json:"author_family"`
	ScopeNamespace         uuid.UUID `json:"scope_namespace"`
	ScopeTest              bool      `json:"scope_test"`
	CriteriaSHA            string    `json:"criteria_sha256"`
	ContextSHA             string    `json:"context_sha256"`
	SourceRepo             string    `json:"source_repo"`
	SourceSHA              string    `json:"source_sha"`
	PackageSHA             string    `json:"package_sha256"`
	ManifestSHA            string    `json:"manifest_sha256"`
	ConfigSHA              string    `json:"config_sha256"`
	ProductTaskID          uuid.UUID `json:"product_task_id"`
	ProductObservationID   string    `json:"product_observation_id"`
	ProductSessionID       string    `json:"product_session_id"`
	AuthorityBytesB64      string    `json:"authority_bytes_b64"`
	RootDispositionB64     string    `json:"root_disposition_bytes_b64"`
	AuthoritySHA           string    `json:"authority_sha256"`
	RootDispositionSHA     string    `json:"root_disposition_sha256"`
}

// ParseEnrollmentManifest is strict: one JSON object, no unknown fields, no
// trailing data, every UUID non-nil, bytes canonical base64 matching the digests.
func ParseEnrollmentManifest(data []byte) (*EnrollmentManifest, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, refuseN("manifest_invalid", err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m EnrollmentManifest
	if err := dec.Decode(&m); err != nil {
		return nil, refuseN("manifest_invalid", "manifest is not a closed JSON object: "+err.Error())
	}
	if dec.More() {
		return nil, refuseN("manifest_invalid", "trailing data after the manifest")
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, refuseN("manifest_invalid", "trailing data after the manifest")
	}
	for name, id := range map[string]uuid.UUID{"id": m.ID, "workspace_id": m.WorkspaceID, "project_id": m.ProjectID,
		"issuer_actor_id": m.IssuerActorID, "publisher_actor_id": m.PublisherActorID, "transport_source_id": m.TransportSourceID,
		"consumer_actor_id": m.ConsumerActorID, "authority_issuer_actor_id": m.AuthorityIssuerActorID,
		"result_author_actor_id": m.ResultAuthorActorID, "owner_actor_id": m.OwnerActorID, "authority_id": m.AuthorityID,
		"review_task_id": m.ReviewTaskID, "source_task_id": m.SourceTaskID, "scope_namespace": m.ScopeNamespace,
		"product_task_id": m.ProductTaskID} {
		if id == uuid.Nil {
			return nil, refuseN("manifest_invalid", name+" is required")
		}
	}
	return &m, nil
}

// Enrollment builds the row, binding the issuer pin from the operator-supplied
// provenance and verifying both original byte strings against their digests.
func (m *EnrollmentManifest) Enrollment(issuerKey string) (*NativeEnrollment, error) {
	ab, ok1 := decodeStdB64(m.AuthorityBytesB64)
	rb, ok2 := decodeStdB64(m.RootDispositionB64)
	if !ok1 || !ok2 || len(ab) == 0 || len(rb) == 0 {
		return nil, refuseN("manifest_invalid", "original authority/root-disposition bytes must be non-empty canonical base64")
	}
	if domain.NativeDigest(ab) != m.AuthoritySHA || domain.NativeDigest(rb) != m.RootDispositionSHA {
		return nil, refuseN("manifest_invalid", "original bytes do not match their declared digests")
	}
	return &NativeEnrollment{
		ID: m.ID, WorkspaceID: m.WorkspaceID, ProjectID: m.ProjectID, IssuerActorID: m.IssuerActorID, PublisherActorID: m.PublisherActorID,
		TransportSourceID: m.TransportSourceID, ConsumerActorID: m.ConsumerActorID, AuthorityIssuerActorID: m.AuthorityIssuerActorID,
		ResultAuthorActorID: m.ResultAuthorActorID, OwnerActorID: m.OwnerActorID, AuthorityID: m.AuthorityID, IssuerKey: issuerKey,
		ReceiverTask: m.ReceiverTask, ReceiverAttempt: m.ReceiverAttempt, ReceiverSession: m.ReceiverSession, ReceiverGeneration: m.ReceiverGeneration,
		NativeRegistration: m.NativeRegistration, NativeStage: m.NativeStage, NativePackageSHA: m.NativePackageSHA,
		ReviewTaskID: m.ReviewTaskID, SourceTaskID: m.SourceTaskID, ReviewerFamily: m.ReviewerFamily, AuthorFamily: m.AuthorFamily,
		ScopeDomain: domain.NativeReviewScopeDomain, ScopeNamespace: m.ScopeNamespace, ScopeTest: m.ScopeTest,
		CriteriaSHA: m.CriteriaSHA, ContextSHA: m.ContextSHA, SourceRepo: m.SourceRepo, SourceSHA: m.SourceSHA, PackageSHA: m.PackageSHA,
		ManifestSHA: m.ManifestSHA, ConfigSHA: m.ConfigSHA, ProductTaskID: m.ProductTaskID, ProductObservationID: m.ProductObservationID,
		ProductSessionID: m.ProductSessionID, AuthorityBytes: ab, RootDispositionBytes: rb,
		AuthoritySHA: m.AuthoritySHA, RootDispositionSHA: m.RootDispositionSHA,
	}, nil
}

func decodeStdB64(v string) ([]byte, bool) {
	raw, err := base64.StdEncoding.Strict().DecodeString(v)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != v {
		return nil, false
	}
	return raw, true
}

// EnrollAsOperator is the operator-only enrollment entry point (no HTTP route
// calls it). The operator must be a current member of the enrollment's
// workspace and none of the enrolled identities, so neither a publisher nor an
// issuer can enroll itself. Idempotent: repeating the identical enrollment
// returns the stored row; the same id with any different content is refused.
func (r *NativeReviewRepo) EnrollAsOperator(ctx context.Context, operator uuid.UUID, e *NativeEnrollment) (created bool, _ *NativeEnrollment, _ error) {
	if operator == uuid.Nil {
		return false, nil, refuseN("not_authorized", "operator identity is required")
	}
	for _, id := range []uuid.UUID{e.IssuerActorID, e.PublisherActorID, e.ConsumerActorID, e.AuthorityIssuerActorID, e.ResultAuthorActorID, e.OwnerActorID, e.TransportSourceID} {
		if id == operator {
			return false, nil, refuseN("not_authorized", "the operator cannot be an enrolled identity")
		}
	}
	var ok bool
	if err := r.db.GetContext(ctx, &ok, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1 AND workspace_id=$2)`, operator, e.WorkspaceID); err != nil {
		return false, nil, err
	}
	if !ok {
		return false, nil, refuseN("not_authorized", "operator is not a member of the enrollment workspace")
	}
	if prev, found, err := r.storedEnrollment(ctx, e); err != nil {
		return false, nil, err
	} else if found {
		return false, prev, nil
	}
	out, err := r.CreateEnrollment(ctx, e, operator)
	if err != nil {
		// A concurrent identical replay may have won the insert: re-read and
		// compare; identical content is the idempotent answer, anything else
		// keeps the original refusal.
		if prev, found, rerr := r.storedEnrollment(ctx, e); rerr == nil && found {
			return false, prev, nil
		}
		return false, nil, err
	}
	return true, out, nil
}

// storedEnrollment returns the stored row for e.ID when it exists and carries
// identical content; same id with different content is enrollment_conflict.
func (r *NativeReviewRepo) storedEnrollment(ctx context.Context, e *NativeEnrollment) (*NativeEnrollment, bool, error) {
	var prev NativeEnrollment
	err := r.db.GetContext(ctx, &prev, `SELECT `+nativeEnrollmentCols+` FROM native_review_enrollments WHERE id=$1`, e.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	a, b := prev, *e
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	a.CreatedBy, b.CreatedBy = uuid.Nil, uuid.Nil
	if !reflect.DeepEqual(a, b) {
		return nil, false, refuseN("enrollment_conflict", "an enrollment with this id exists with different content")
	}
	return &prev, true, nil
}

// rejectDuplicateKeys walks the JSON token stream and refuses any object that
// repeats a key (encoding/json would silently keep the last value).
func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, kerr := dec.Token()
				if kerr != nil {
					return kerr
				}
				k, _ := kt.(string)
				if seen[k] {
					return fmt.Errorf("duplicate manifest key %q", k)
				}
				seen[k] = true
				if werr := walk(); werr != nil {
					return werr
				}
			}
		case '[':
			for dec.More() {
				if werr := walk(); werr != nil {
					return werr
				}
			}
		}
		_, err = dec.Token()
		return err
	}
	if werr := walk(); werr != nil {
		if strings.HasPrefix(werr.Error(), "duplicate") {
			return werr
		}
		return fmt.Errorf("manifest is not valid JSON: %v", werr)
	}
	return nil
}
