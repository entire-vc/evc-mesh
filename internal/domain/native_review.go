package domain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// native-review-admission/v1: authenticated immutable reviewer receipt and the
// atomic check-and-reserve of one synthetic operation. Every object is closed:
// unknown or missing fields refuse, there are no nullable security bindings.

const (
	NativeReviewSchema = "native-review-admission/v1"

	// nativeSignDomain prefixes the ASCII receipt_sha256 under the issuer signature.
	nativeSignDomain = "native-review-admission/v1\n"

	NativeReviewScopeDomain = "t12-successor-synthetic"

	NativeVerdictShip      = "SHIP"
	NativeVerdictDoNotShip = "DO-NOT-SHIP"

	// NativeReviewBudgetMS is the original witness window; it is never changed here.
	NativeReviewBudgetMS = 1000
	// NativeReviewMaxBody bounds the verdict bytes (the channel MAX_MESSAGE
	// bounds the whole packet); oversize is a refusal, never truncation.
	NativeReviewMaxBody = 256 << 10

	// Admission predicates of the original witness, integer nanoseconds.
	nrMaxWindowNS    = uint64(60_000_000_000)
	nrBudgetNS       = uint64(1_000_000_000)
	nrCutoffMarginNS = uint64(100_000_000)
	nrSkewNS         = uint64(5_000_000)
	nrInt63          = uint64(1) << 63
)

var (
	reSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reHex32     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reGitOID    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	reDecimal   = regexp.MustCompile(`^(0|[1-9]\d{0,18})$`)
	reFamily    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reStage     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	reRepo      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	reTime      = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{1,9})?Z$`)
	errNRClosed = errors.New("native review: trailing data")
)

// NativeRefusal is a typed fail-closed refusal with a stable code.
type NativeRefusal struct{ Code, Message string }

func (e *NativeRefusal) Error() string { return e.Code + ": " + e.Message }

// NativeRefuse builds a refusal.
func NativeRefuse(code, msg string) error { return &NativeRefusal{Code: code, Message: msg} }

type NativeScope struct {
	Domain      string    `json:"domain"`
	Namespace   uuid.UUID `json:"namespace"`
	Test        bool      `json:"test"`
	CriteriaSHA string    `json:"criteria_sha256"`
	ContextSHA  string    `json:"context_sha256"`
}

type NativePins struct {
	SourceRepo  string `json:"source_repo"`
	SourceSHA   string `json:"source_sha"`
	PackageSHA  string `json:"package_sha256"`
	ManifestSHA string `json:"manifest_sha256"`
	ConfigSHA   string `json:"config_sha256"`
}

type NativeProduct struct {
	OwningTaskID  uuid.UUID `json:"owning_task_id"`
	ObservationID string    `json:"observation_id"`
	SessionID     string    `json:"session_id"`
	AuthoritySHA  string    `json:"authority_sha256"`
	RootDispSHA   string    `json:"root_disposition_sha256"`
}

type NativeReceiver struct {
	Task       uuid.UUID `json:"task"`
	Attempt    uuid.UUID `json:"attempt"`
	Session    uuid.UUID `json:"session"`
	Generation int64     `json:"generation"`
}

// NativeBinding mirrors the existing closed Runner ledger binding.
type NativeBinding struct {
	Producer string `json:"producer"`
	Task     string `json:"task"`
	Stage    string `json:"stage"`
	Package  string `json:"package_sha256"`
	Test     bool   `json:"test"`
}

// DeriveNativeRegistration is the identity the Runner ledger gives a registration:
// SHA256 of the ASCII canonical JSON {"binding":...,"receiver":...} (sorted keys,
// compact). The signed native.registration must equal it, so the receipt's native
// link is the exact registration the receiver will look up. The event id is not
// part of it: it stays append-only after the receipt.
func DeriveNativeRegistration(b NativeBinding, r NativeReceiver) (string, error) {
	c, err := CanonicalJSON(struct {
		Binding  NativeBinding  `json:"binding"`
		Receiver NativeReceiver `json:"receiver"`
	}{b, r})
	return NativeDigest(c), err
}

type NativeLink struct {
	Registration string        `json:"registration"`
	Binding      NativeBinding `json:"binding"`
}

type NativeBody struct {
	Encoding string `json:"encoding"`
	Bytes    string `json:"bytes"`
	SHA256   string `json:"sha256"`
}

// NativeIssueRequest is what the reviewer posts under its own credential. It
// cannot name issuer, publisher, families, author, owner, workspace or project:
// those come from the authenticated actor and the trusted enrollment.
type NativeIssueRequest struct {
	Schema       string         `json:"schema"`
	EnrollmentID uuid.UUID      `json:"enrollment_id"`
	ReviewTaskID uuid.UUID      `json:"review_task_id"`
	SourceTaskID uuid.UUID      `json:"source_task_id"`
	Verdict      string         `json:"verdict"`
	Scope        NativeScope    `json:"scope"`
	Pins         NativePins     `json:"pins"`
	Product      NativeProduct  `json:"product"`
	Receiver     NativeReceiver `json:"receiver"`
	Native       NativeLink     `json:"native"`
	Body         NativeBody     `json:"body"`
}

// NativeReceipt is the immutable canonical receipt (unsigned fields).
type NativeReceipt struct {
	Schema          string         `json:"schema"`
	ReceiptID       uuid.UUID      `json:"receipt_id"`
	EnrollmentID    uuid.UUID      `json:"enrollment_id"`
	AuthorityID     uuid.UUID      `json:"authority_id"`
	Product         NativeProduct  `json:"product"`
	IssuerActorID   uuid.UUID      `json:"issuer_actor_id"`
	PublisherID     uuid.UUID      `json:"publisher_actor_id"`
	TransportSource uuid.UUID      `json:"transport_source_id"`
	ResultAuthorID  uuid.UUID      `json:"result_author_actor_id"`
	ReviewTaskID    uuid.UUID      `json:"review_task_id"`
	SourceTaskID    uuid.UUID      `json:"source_task_id"`
	WorkspaceID     uuid.UUID      `json:"workspace_id"`
	ProjectID       uuid.UUID      `json:"project_id"`
	OwnerActorID    uuid.UUID      `json:"owner_actor_id"`
	ReviewerFamily  string         `json:"reviewer_family"`
	AuthorFamily    string         `json:"author_family"`
	Verdict         string         `json:"verdict"`
	Scope           NativeScope    `json:"scope"`
	Pins            NativePins     `json:"pins"`
	Receiver        NativeReceiver `json:"receiver"`
	Native          NativeLink     `json:"native"`
	Body            NativeBody     `json:"body"`
}

// NativeReserveRequest carries no issuer, budget, expiry, clock or enrollment.
type NativeReserveRequest struct {
	Schema       string         `json:"schema"`
	AuthorityID  uuid.UUID      `json:"authority_id"`
	ReceiptID    uuid.UUID      `json:"receipt_id"`
	ReceiptSHA   string         `json:"receipt_sha256"`
	Scope        NativeScope    `json:"scope"`
	Pins         NativePins     `json:"pins"`
	Receiver     NativeReceiver `json:"receiver"`
	Registration string         `json:"native_registration"`
	EventID      string         `json:"native_event_id"`
	RequestID    uuid.UUID      `json:"request_id"`
}

// NativeAuthorityRequest creates the mutable guard for one receipt. Timing is
// the original not-before/expiry as canonical decimal strings.
type NativeAuthorityRequest struct {
	Schema          string    `json:"schema"`
	EnrollmentID    uuid.UUID `json:"enrollment_id"`
	AuthorityID     uuid.UUID `json:"authority_id"`
	ReceiptID       uuid.UUID `json:"receipt_id"`
	BudgetID        uuid.UUID `json:"budget_id"`
	BudgetMS        int       `json:"budget_ms"`
	MaxReservations int       `json:"max_reservations"`
	NotBeforeNS     string    `json:"not_before_ns"`
	ExpiresNS       string    `json:"expires_ns"`
	ClockBinding    string    `json:"clock_binding_sha256"`
}

// NativeReservation is the committed, authenticated readback.
type NativeReservation struct {
	ReservationID  uuid.UUID      `json:"reservation_id"`
	AuthorityID    uuid.UUID      `json:"authority_id"`
	ReceiptID      uuid.UUID      `json:"receipt_id"`
	ReceiptSHA     string         `json:"receipt_sha256"`
	RequestID      uuid.UUID      `json:"request_id"`
	Receiver       NativeReceiver `json:"receiver"`
	Registration   string         `json:"native_registration"`
	EventID        string         `json:"native_event_id"`
	CommittedAt    time.Time      `json:"committed_at"`
	MarkerID       uuid.UUID      `json:"marker_id"`
	Consumed       bool           `json:"consumed"`
	ExecuteAllowed bool           `json:"execute_allowed"`
}

// StrictDecodeNative decodes one closed JSON object: unknown fields and trailing
// data refuse. Missing fields stay zero and are refused by Validate.
func StrictDecodeNative(raw []byte, v any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return NativeRefuse("schema_invalid", err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return NativeRefuse("schema_invalid", err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return NativeRefuse("schema_invalid", errNRClosed.Error())
	}
	return nil
}

// rejectDuplicateKeys walks the token stream and refuses any object that repeats
// a key at any depth: encoding/json keeps the last value, other parsers keep the
// first, and an attacker must not choose which one a verifier sees.
func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	type frame struct {
		obj  bool
		keys map[string]struct{}
		want bool // next string token in an object is a key
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err // malformed input is reported by the real decode too
		}
		var top *frame
		if n := len(stack); n > 0 {
			top = stack[n-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, &frame{obj: true, keys: map[string]struct{}{}, want: true})
			case '[':
				stack = append(stack, &frame{})
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].obj {
					stack[n-1].want = true
				}
			}
		case string:
			if top != nil && top.obj && top.want {
				if _, dup := top.keys[t]; dup {
					return fmt.Errorf("duplicate object key %q", t)
				}
				top.keys[t] = struct{}{}
				top.want = false
				continue
			}
			if top != nil && top.obj {
				top.want = true
			}
		default:
			if top != nil && top.obj {
				top.want = true
			}
		}
	}
}

// Digest returns the lowercase sha256 hex of b.
func NativeDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// ReceiptSHA is SHA256 over the canonical unsigned receipt.
func (r *NativeReceipt) Canonical() (canonical []byte, sha string, err error) {
	canonical, err = CanonicalJSON(r)
	if err != nil {
		return nil, "", err
	}
	return canonical, NativeDigest(canonical), nil
}

func (s NativeScope) validate() error {
	switch {
	case s.Domain != NativeReviewScopeDomain:
		return NativeRefuse("scope_domain", "scope.domain must be "+NativeReviewScopeDomain)
	case s.Namespace == uuid.Nil:
		return NativeRefuse("scope_namespace", "scope.namespace required")
	case !s.Test:
		return NativeRefuse("scope_not_test", "scope.test must be true")
	case !reSHA256.MatchString(s.CriteriaSHA) || !reSHA256.MatchString(s.ContextSHA):
		return NativeRefuse("scope_digest", "criteria_sha256/context_sha256 must be sha256 hex")
	}
	return nil
}

func (p NativePins) validate() error {
	if !reRepo.MatchString(p.SourceRepo) || !reGitOID.MatchString(p.SourceSHA) || !reSHA256.MatchString(p.PackageSHA) ||
		!reSHA256.MatchString(p.ManifestSHA) || !reSHA256.MatchString(p.ConfigSHA) {
		return NativeRefuse("pins_invalid", "pins require source_repo, full source_sha and sha256 package/manifest/config")
	}
	return nil
}

func (p NativeProduct) validate() error {
	if p.OwningTaskID == uuid.Nil || !reHex32.MatchString(p.ObservationID) || !reHex32.MatchString(p.SessionID) ||
		!reSHA256.MatchString(p.AuthoritySHA) || !reSHA256.MatchString(p.RootDispSHA) {
		return NativeRefuse("product_invalid", "product binding incomplete")
	}
	return nil
}

func (r NativeReceiver) validate() error {
	if r.Task == uuid.Nil || r.Attempt == uuid.Nil || r.Session == uuid.Nil || r.Generation <= 0 {
		return NativeRefuse("receiver_invalid", "receiver task/attempt/session/generation required")
	}
	return nil
}

func (l NativeLink) validate() error {
	b := l.Binding
	if !reSHA256.MatchString(l.Registration) || !canonicalUUID(b.Producer) || !canonicalUUID(b.Task) || !reStage.MatchString(b.Stage) ||
		!reSHA256.MatchString(b.Package) || !b.Test {
		return NativeRefuse("native_invalid", "native registration/binding incomplete")
	}
	return nil
}

// DecodedBytes verifies encoding, bound and digest and returns the raw verdict.
func (b NativeBody) DecodedBytes() ([]byte, error) {
	if b.Encoding != "base64" || b.Bytes == "" || !reSHA256.MatchString(b.SHA256) {
		return nil, NativeRefuse("body_missing", "body requires base64 bytes and sha256")
	}
	if base64.StdEncoding.DecodedLen(len(b.Bytes)) > NativeReviewMaxBody+3 {
		return nil, NativeRefuse("body_too_large", "verdict bytes exceed the bound; truncation is not allowed")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(b.Bytes)
	if err != nil || len(raw) == 0 || len(raw) > NativeReviewMaxBody {
		return nil, NativeRefuse("body_invalid", "body bytes are not canonical base64 within the bound")
	}
	if NativeDigest(raw) != b.SHA256 {
		return nil, NativeRefuse("body_digest_mismatch", "body sha256 does not match the bytes")
	}
	return raw, nil
}

// Validate checks the closed shape of an issue request.
func (q *NativeIssueRequest) Validate() error {
	if q.Schema != NativeReviewSchema {
		return NativeRefuse("schema_invalid", "schema must be "+NativeReviewSchema)
	}
	if q.EnrollmentID == uuid.Nil || q.ReviewTaskID == uuid.Nil || q.SourceTaskID == uuid.Nil {
		return NativeRefuse("binding_missing", "enrollment_id/review_task_id/source_task_id required")
	}
	if q.Verdict != NativeVerdictShip && q.Verdict != NativeVerdictDoNotShip {
		return NativeRefuse("verdict_invalid", "verdict must be SHIP or DO-NOT-SHIP")
	}
	for _, err := range []error{q.Scope.validate(), q.Pins.validate(), q.Product.validate(), q.Receiver.validate(), q.Native.validate()} {
		if err != nil {
			return err
		}
	}
	_, err := q.Body.DecodedBytes()
	return err
}

// Validate checks the closed shape of a reserve request.
func (q *NativeReserveRequest) Validate() error {
	if q.Schema != NativeReviewSchema {
		return NativeRefuse("schema_invalid", "schema must be "+NativeReviewSchema)
	}
	if q.AuthorityID == uuid.Nil || q.ReceiptID == uuid.Nil || q.RequestID == uuid.Nil ||
		!reSHA256.MatchString(q.ReceiptSHA) || !reSHA256.MatchString(q.Registration) || q.EventID == "" {
		return NativeRefuse("binding_missing", "authority/receipt/request ids, receipt_sha256, native registration and event required")
	}
	for _, err := range []error{q.Scope.validate(), q.Pins.validate(), q.Receiver.validate()} {
		if err != nil {
			return err
		}
	}
	return nil
}

// Digest is the payload digest bound to request_id.
func (q *NativeReserveRequest) Digest() (string, error) {
	c, err := CanonicalJSON(q)
	if err != nil {
		return "", err
	}
	return NativeDigest(c), nil
}

// Validate checks the closed shape of an authority request.
func (q *NativeAuthorityRequest) Validate() error {
	if q.Schema != NativeReviewSchema {
		return NativeRefuse("schema_invalid", "schema must be "+NativeReviewSchema)
	}
	if q.EnrollmentID == uuid.Nil || q.AuthorityID == uuid.Nil || q.ReceiptID == uuid.Nil || q.BudgetID == uuid.Nil {
		return NativeRefuse("binding_missing", "enrollment/authority/receipt/budget ids required")
	}
	if q.BudgetMS != NativeReviewBudgetMS || q.MaxReservations != 1 {
		return NativeRefuse("budget_invalid", "budget_ms must be 1000 and max_reservations 1")
	}
	if !reSHA256.MatchString(q.ClockBinding) {
		return NativeRefuse("clock_binding_invalid", "clock_binding_sha256 required")
	}
	nb, err := ParseNS(q.NotBeforeNS)
	if err != nil {
		return err
	}
	ex, err := ParseNS(q.ExpiresNS)
	if err != nil {
		return err
	}
	if nb == 0 || nb >= ex || ex >= nrInt63 || ex-nb > nrMaxWindowNS {
		return NativeRefuse("window_invalid", "need 0 < not_before < expires < 2^63 and window <= 60s")
	}
	return nil
}

// ParseNS parses a canonical unsigned decimal nanosecond string: no sign,
// whitespace, exponent or leading zeros; integer arithmetic only.
func ParseNS(s string) (uint64, error) {
	if !reDecimal.MatchString(s) {
		return 0, NativeRefuse("ns_not_canonical", "nanoseconds must be a canonical unsigned decimal string")
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, NativeRefuse("ns_overflow", "nanoseconds overflow")
	}
	return v, nil
}

// ClockEvidence is what a trusted clock provider proves after the guard locks
// are held. Nothing in a request can supply it.
type ClockEvidence struct {
	BindingSHA string `json:"clock_binding_sha256"`
	OuterMono  string `json:"outer_mono_ns"`
	OuterWall  string `json:"outer_wall_ns"`
	SampleMono string `json:"sample_mono_ns"`
	SampleWall string `json:"sample_wall_ns"`
}

// CheckAdmission applies the original witness predicates in integer arithmetic.
// Any parse failure, skew or boundary violation refuses; there is no rounding.
func (e ClockEvidence) CheckAdmission(notBefore, expires, bindingWant string) error {
	if e.BindingSHA == "" || e.BindingSHA != bindingWant {
		return NativeRefuse("clock_binding_mismatch", "clock evidence is not bound to this authority")
	}
	nb, err := ParseNS(notBefore)
	if err != nil {
		return err
	}
	ex, err := ParseNS(expires)
	if err != nil {
		return err
	}
	var v [4]uint64
	for i, s := range []string{e.OuterMono, e.OuterWall, e.SampleMono, e.SampleWall} {
		if v[i], err = ParseNS(s); err != nil {
			return err
		}
	}
	om, ow, sm, sw := v[0], v[1], v[2], v[3]
	if nb > ow || ow >= ex {
		return NativeRefuse("window_closed", "outer wall is outside [not_before, expires)")
	}
	if ow > nrInt63 || om > nrInt63 || sm > nrInt63 || sw > nrInt63 {
		return NativeRefuse("ns_overflow", "clock value out of range")
	}
	end := om + nrBudgetNS
	if rem := om + (ex - ow); rem < end {
		end = rem
	}
	if end < nrCutoffMarginNS {
		return NativeRefuse("window_cutoff", "window shorter than the cutoff margin")
	}
	cutoff := end - nrCutoffMarginNS
	if sm < om {
		return NativeRefuse("clock_regressed", "sample precedes the outer monotonic start")
	}
	wallDelta, monoDelta := int64(sw)-int64(ow), int64(sm)-int64(om)
	skew := wallDelta - monoDelta
	if skew < 0 {
		skew = -skew
	}
	if uint64(skew) > nrSkewNS {
		return NativeRefuse("clock_skew", "wall/monotonic skew exceeds the original bound")
	}
	if sm >= cutoff || cutoff-sm < nrCutoffMarginNS {
		return NativeRefuse("window_cutoff", "sample is inside the cutoff margin; reserve refused")
	}
	return nil
}

func (r NativeReceiver) String() string {
	return fmt.Sprintf("%s/%s/%s/%d", r.Task, r.Attempt, r.Session, r.Generation)
}

func canonicalUUID(v string) bool {
	u, err := uuid.Parse(v)
	return err == nil && u.String() == v
}

// ValidFamily is the closed model-family token shared with the Runner channel.
func ValidFamily(v string) bool { return reFamily.MatchString(v) }

// ValidRepo is the closed repository id shared with the Runner channel.
func ValidRepo(v string) bool { return reRepo.MatchString(v) }

// ValidStage is the closed native stage token.
func ValidStage(v string) bool { return reStage.MatchString(v) }

// NativePacket is the flat signed packet the ordinary publisher forwards
// unchanged: every immutable receipt field, then receipt_sha256, created_at (one
// server UTC timestamp persisted at issue) and issuer_signature. The digest
// covers the receipt fields only.
type NativePacket struct {
	NativeReceipt
	ReceiptSHA      string `json:"receipt_sha256"`
	CreatedAt       string `json:"created_at"`
	IssuerSignature string `json:"issuer_signature"`
}

// Canonical is the exact canonical packet bytes (sorted keys, compact).
func (p *NativePacket) Canonical() ([]byte, error) { return CanonicalJSON(p) }

// NativeSigningMessage is what the issuer signs: domain + ASCII receipt digest.
func NativeSigningMessage(receiptSHA string) []byte {
	return []byte(nativeSignDomain + receiptSHA)
}

// decodeCanonicalB64 accepts only canonical standard base64 of exactly n bytes.
func decodeCanonicalB64(v string, n int) ([]byte, bool) {
	raw, err := base64.StdEncoding.Strict().DecodeString(v)
	if err != nil || len(raw) != n || base64.StdEncoding.EncodeToString(raw) != v {
		return nil, false
	}
	return raw, true
}

// ParseIssuerKey checks an enrolled issuer public key: canonical base64, 32 bytes.
func ParseIssuerKey(v string) (ed25519.PublicKey, error) {
	raw, ok := decodeCanonicalB64(v, ed25519.PublicKeySize)
	if !ok {
		return nil, NativeRefuse("issuer_key_invalid", "issuer_key must be canonical base64 of exactly 32 bytes")
	}
	return ed25519.PublicKey(raw), nil
}

// VerifyIssuerSignature checks the Ed25519 signature over the signing message
// against the enrolled key. A malformed signature or key refuses.
func VerifyIssuerSignature(issuerKey, receiptSHA, signature string) error {
	key, err := ParseIssuerKey(issuerKey)
	if err != nil {
		return err
	}
	sig, ok := decodeCanonicalB64(signature, ed25519.SignatureSize)
	if !ok {
		return NativeRefuse("issuer_signature_invalid", "issuer_signature must be canonical base64 of exactly 64 bytes")
	}
	if !reSHA256.MatchString(receiptSHA) || !ed25519.Verify(key, NativeSigningMessage(receiptSHA), sig) {
		return NativeRefuse("issuer_signature_invalid", "issuer signature does not verify against the enrolled issuer key")
	}
	return nil
}

// FormatNativeCreatedAt renders the persisted server timestamp: UTC, whole seconds.
func FormatNativeCreatedAt(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

// ValidCreatedAt reports whether v has the packet timestamp shape.
func ValidCreatedAt(v string) bool { return reTime.MatchString(v) }

// Validate is the closed structural check of an immutable receipt: every id is
// present, every closed token matches, the body bytes verify against their digest.
// A missing authority_id (or any other id) refuses.
func (r *NativeReceipt) Validate() error {
	if r.Schema != NativeReviewSchema {
		return NativeRefuse("schema_invalid", "schema must be "+NativeReviewSchema)
	}
	for name, id := range map[string]uuid.UUID{
		"receipt_id": r.ReceiptID, "enrollment_id": r.EnrollmentID, "authority_id": r.AuthorityID, "issuer_actor_id": r.IssuerActorID,
		"publisher_actor_id": r.PublisherID, "transport_source_id": r.TransportSource, "result_author_actor_id": r.ResultAuthorID,
		"review_task_id": r.ReviewTaskID, "source_task_id": r.SourceTaskID, "workspace_id": r.WorkspaceID, "project_id": r.ProjectID,
		"owner_actor_id": r.OwnerActorID,
	} {
		if id == uuid.Nil {
			return NativeRefuse("binding_missing", name+" required")
		}
	}
	if !ValidFamily(r.ReviewerFamily) || !ValidFamily(r.AuthorFamily) || r.ReviewerFamily == r.AuthorFamily {
		return NativeRefuse("family_invalid", "reviewer/author family must be closed tokens and differ")
	}
	if r.Verdict != NativeVerdictShip && r.Verdict != NativeVerdictDoNotShip {
		return NativeRefuse("verdict_invalid", "verdict must be SHIP or DO-NOT-SHIP")
	}
	for _, err := range []error{r.Scope.validate(), r.Pins.validate(), r.Product.validate(), r.Receiver.validate(), r.Native.validate()} {
		if err != nil {
			return err
		}
	}
	_, err := r.Body.DecodedBytes()
	return err
}

// Verify is the full packet check a consumer of the flat packet makes: closed
// structure, receipt_sha256 recomputed from the immutable fields, a well-formed
// server timestamp and the issuer signature under the ENROLLED key.
func (p *NativePacket) Verify(issuerKey string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, sum, err := p.NativeReceipt.Canonical()
	if err != nil {
		return err
	}
	if sum != p.ReceiptSHA {
		return NativeRefuse("receipt_digest_mismatch", "receipt_sha256 does not match the receipt fields")
	}
	if !ValidCreatedAt(p.CreatedAt) {
		return NativeRefuse("created_at_invalid", "created_at must be a server UTC timestamp")
	}
	return VerifyIssuerSignature(issuerKey, p.ReceiptSHA, p.IssuerSignature)
}
