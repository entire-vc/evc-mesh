package domain

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Lead-accept context: contract CH→Mesh lead-accept-context/v1
// (docs/lead-accept-context.md).
//
// A reader principal (mlr_) reads one narrow projection — the merge gate's
// lead-accept authority for one exact request tuple plus the named Mesh
// evidence records — through GET /api/v1/lead-accept-context. A publisher
// principal (mlp_) pushes registry observations of the live merge gate source
// through POST /api/v1/lead-accept-registry/attestations. Neither reaches any
// other route.

const (
	LeadAcceptKindReader    = "reader"
	LeadAcceptKindPublisher = "publisher"

	// LeadAcceptReaderTokenPrefix / LeadAcceptPublisherTokenPrefix start a
	// token: prefix + 43 characters of unpadded base64url (256 bits).
	LeadAcceptReaderTokenPrefix    = "mlr_"
	LeadAcceptPublisherTokenPrefix = "mlp_"

	// LeadAcceptProject is the one GitLab project v1 serves.
	LeadAcceptProject = "entire-vc/evc-spark"

	LeadAcceptContextSchema     = "lead-accept-context/v1"
	LeadAcceptAttestationSchema = "lead-accept-registry-attestation/v1"

	// LeadAcceptMaxAge is how long an accepted ok observation keeps the GET
	// open, counted from its observed_at.
	LeadAcceptMaxAge = 150 * time.Second
	// LeadAcceptMaxSkew: an observation from further in the future than this
	// (against the server's receive time) is refused.
	LeadAcceptMaxSkew = 5 * time.Second
	// LeadAcceptMaxDelay: an ok observation received later than this after its
	// observed_at is refused.
	LeadAcceptMaxDelay = 30 * time.Second
	// LeadAcceptRetention: observations older than this (by received_at) are
	// pruned, never the current one. The contract asks for at least 7 days.
	LeadAcceptRetention = 30 * 24 * time.Hour
	// LeadAcceptMaxArtifactBytes caps an artifact evidence: larger is 409.
	LeadAcceptMaxArtifactBytes = 1 << 20
	// LeadAcceptMaxBody caps an attestation body.
	LeadAcceptMaxBody = 16 << 10
	// LeadAcceptMaxEvidence is how many evidence refs one request may name.
	LeadAcceptMaxEvidence = 4

	// LeadAcceptDefaultGrace is how long the previous token still works after
	// a rotation when --grace is not given.
	LeadAcceptDefaultGrace = time.Hour

	// Error codes (the error_code of the Mesh error body), docs LA6 / LA7.
	LeadAcceptCodeScopeForbidden      = "lead_accept_scope_forbidden"
	LeadAcceptCodeEvidenceScope       = "evidence_scope_forbidden"
	LeadAcceptCodeRegistryUnavailable = "registry_unavailable"
	LeadAcceptCodeRegistrySourceError = "registry_source_error"
	LeadAcceptCodeRegistryStale       = "registry_stale"
	LeadAcceptCodeRegistryOutOfSync   = "registry_out_of_sync"
	LeadAcceptCodeNotApplicable       = "lead_accept_not_applicable"
	LeadAcceptCodeLeadNotAuthorized   = "lead_not_authorized"
	LeadAcceptCodeEvidenceRevoked     = "evidence_revoked"
	LeadAcceptCodeEvidenceChanged     = "evidence_changed"
	LeadAcceptCodeEvidenceUnsupported = "evidence_unsupported"
	LeadAcceptCodeEvidenceUnavailable = "evidence_unavailable"
	LeadAcceptCodeBindingConflict     = "approval_binding_conflict"
	LeadAcceptCodeClockSkew           = "attestation_clock_skew"
	LeadAcceptCodeDelayed             = "attestation_delayed"
	LeadAcceptCodeReplayed            = "attestation_replayed"
	LeadAcceptCodeNotNewer            = "attestation_not_newer"
)

// Server-side scope of every reader the operator command issues (docs LA3):
// the workspace and the ContentHub project the evidence must live in. The
// caller never chooses them.
var (
	LeadAcceptWorkspaceID       = uuid.MustParse("df814cd2-ca4b-47d6-9522-820e4eb47dc3")
	LeadAcceptEvidenceProjectID = uuid.MustParse("feceae7f-5037-4fdd-bf76-d149ede06281")
)

// LeadAcceptErrorSteps are the failed steps an ok=false observation may name.
var LeadAcceptErrorSteps = []string{"fetch", "read", "blob", "policy", "visibility"}

// LeadAcceptPrincipal is the server-side record of one lead-accept credential.
type LeadAcceptPrincipal struct {
	ID                 uuid.UUID       `db:"id" json:"id"`
	Name               string          `db:"name" json:"name"`
	Kind               string          `db:"kind" json:"kind"`
	WorkspaceID        *uuid.UUID      `db:"workspace_id" json:"workspace_id,omitempty"`
	EvidenceProjectID  *uuid.UUID      `db:"evidence_project_id" json:"evidence_project_id,omitempty"`
	Binding            json.RawMessage `db:"binding" json:"binding,omitempty"`
	BindingSHA256      *string         `db:"binding_sha256" json:"binding_sha256,omitempty"`
	TokenPrefix        *string         `db:"token_prefix" json:"token_prefix,omitempty"`
	PreviousValidUntil *time.Time      `db:"previous_valid_until" json:"previous_valid_until,omitempty"`
	CreatedAt          time.Time       `db:"created_at" json:"created_at"`
	IssuedAt           *time.Time      `db:"issued_at" json:"issued_at,omitempty"`
	RevokedAt          *time.Time      `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokeReason       *string         `db:"revoke_reason" json:"revoke_reason,omitempty"`
}

// --- request (LA4) ---------------------------------------------------------

// LeadAcceptRequest is the exact request tuple of a GET, and the binding of a
// reader. Field order is irrelevant: CanonicalJSON sorts keys.
type LeadAcceptRequest struct {
	ApprovalID       string   `json:"approval_id"`
	ApprovalVersion  int64    `json:"approval_version"`
	Evidence         []string `json:"evidence"`
	HeadSHA          string   `json:"head_sha"`
	LeadAcceptNoteID int64    `json:"lead_accept_note_id"`
	LeadGitlabID     int64    `json:"lead_gitlab_id"`
	MRIID            int64    `json:"mr_iid"`
	PayloadSHA256    string   `json:"payload_sha256"`
	Project          string   `json:"project"`
	ReviewNoteID     int64    `json:"review_note_id"`
}

// LeadAcceptEvidenceRef is one parsed evidence ref.
type LeadAcceptEvidenceRef struct {
	Ref      string
	Kind     string // "comment" or "artifact"
	TaskID   uuid.UUID
	RecordID uuid.UUID
}

var leadAcceptParams = []string{
	"project", "mr_iid", "head_sha", "lead_gitlab_id", "review_note_id",
	"lead_accept_note_id", "approval_id", "approval_version", "payload_sha256", "evidence",
}

const lowerUUIDPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var (
	lowerUUIDRe      = regexp.MustCompile(`^` + lowerUUIDPattern + `$`)
	evidenceRefRe    = regexp.MustCompile(`^(comment|artifact):(` + lowerUUIDPattern + `)/(` + lowerUUIDPattern + `)$`)
	gitlabProjectRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}(/[a-z0-9][a-z0-9._-]{0,99}){1,4}$`)
	leadUsernameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	leadAcceptNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
)

// LeadAcceptNameValid reports whether name is a valid principal name.
func LeadAcceptNameValid(name string) bool { return leadAcceptNameRe.MatchString(name) }

// ParseLeadAcceptQuery reads a GET query string. Every parameter is required;
// an unknown or repeated one (evidence aside) is refused, and nothing is
// normalised: a value either is in its one canonical form or it is a 400.
func ParseLeadAcceptQuery(rawQuery string) (parsed *LeadAcceptRequest, refusals map[string]string) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, map[string]string{"query": "the query string could not be parsed"}
	}
	bad := map[string]string{}
	allowed := map[string]bool{}
	for _, p := range leadAcceptParams {
		allowed[p] = true
	}
	for k := range values {
		if !allowed[k] {
			bad[k] = "unknown parameter"
		}
	}
	single := map[string]string{}
	for _, p := range leadAcceptParams {
		vs, ok := values[p]
		switch {
		case !ok || len(vs) == 0:
			bad[p] = "required"
		case p == "evidence":
		case len(vs) != 1:
			bad[p] = "must be given once"
		default:
			single[p] = vs[0]
		}
	}
	r := &LeadAcceptRequest{}
	if _, isBad := bad["evidence"]; !isBad {
		r.Evidence = append([]string{}, values["evidence"]...)
	}
	validateLeadAcceptRequest(r, single, bad)
	if len(bad) > 0 {
		return nil, bad
	}
	return r, nil
}

// ParseLeadAcceptRequestJSON reads a request object (the --request-file of
// `lead-accept issue --kind reader`): one JSON object with exactly the ten
// fields, integers as JSON numbers, every value in its canonical form.
func ParseLeadAcceptRequestJSON(body []byte) (parsed *LeadAcceptRequest, refusals map[string]string) {
	fields, perr := decodeStrictObject(body)
	if perr != nil {
		if perr.Fields != nil {
			return nil, perr.Fields
		}
		return nil, map[string]string{"request": "must be one JSON object"}
	}
	bad := map[string]string{}
	allowed := map[string]bool{}
	for _, p := range leadAcceptParams {
		allowed[p] = true
	}
	for k := range fields {
		if !allowed[k] {
			bad[k] = "unknown field"
		}
	}
	single := map[string]string{}
	ints := map[string]bool{"mr_iid": true, "lead_gitlab_id": true, "review_note_id": true,
		"lead_accept_note_id": true, "approval_version": true}
	r := &LeadAcceptRequest{}
	for _, p := range leadAcceptParams {
		raw, ok := fields[p]
		if !ok {
			bad[p] = "required"
			continue
		}
		switch {
		case p == "evidence":
			if err := json.Unmarshal(raw, &r.Evidence); err != nil || r.Evidence == nil {
				bad[p] = "must be an array of strings"
			}
		case ints[p]:
			// Kept as its literal text: posIntRe below refuses 1.0, 1e3, -1, "1".
			single[p] = string(raw)
		default:
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				bad[p] = "must be a string"
				continue
			}
			single[p] = s
		}
	}
	validateLeadAcceptRequest(r, single, bad)
	if len(bad) > 0 {
		return nil, bad
	}
	return r, nil
}

// validateLeadAcceptRequest checks the single-valued fields in single and the
// evidence already in r, filling r and recording refusals in bad.
func validateLeadAcceptRequest(r *LeadAcceptRequest, single, bad map[string]string) {
	str := func(name string, re *regexp.Regexp, msg string, dst *string) {
		v, ok := single[name]
		if !ok {
			return
		}
		if !re.MatchString(v) {
			bad[name] = msg
			return
		}
		*dst = v
	}
	num := func(name string, dst *int64) {
		v, ok := single[name]
		if !ok {
			return
		}
		if !posIntRe.MatchString(v) {
			bad[name] = "must be an integer >= 1 without sign or leading zero"
			return
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			bad[name] = "must be an integer >= 1"
			return
		}
		*dst = n
	}
	str("project", gitlabProjectRe, "must be a lowercase GitLab project path", &r.Project)
	num("mr_iid", &r.MRIID)
	str("head_sha", hex40Re, "must be a full 40-character lowercase hex commit SHA", &r.HeadSHA)
	num("lead_gitlab_id", &r.LeadGitlabID)
	num("review_note_id", &r.ReviewNoteID)
	num("lead_accept_note_id", &r.LeadAcceptNoteID)
	str("approval_id", lowerUUIDRe, "must be a UUID in canonical lowercase form", &r.ApprovalID)
	num("approval_version", &r.ApprovalVersion)
	str("payload_sha256", hex64Re, "must be 64 lowercase hex characters", &r.PayloadSHA256)
	if r.ApprovalID == uuid.Nil.String() {
		bad["approval_id"] = "must not be the nil UUID"
	}
	if _, b1 := bad["review_note_id"]; !b1 && r.ReviewNoteID > 0 {
		if _, b2 := bad["lead_accept_note_id"]; !b2 && r.LeadAcceptNoteID > 0 && r.LeadAcceptNoteID <= r.ReviewNoteID {
			bad["lead_accept_note_id"] = "must be greater than review_note_id"
		}
	}
	if _, isBad := bad["evidence"]; !isBad {
		if msg := checkEvidence(r.Evidence); msg != "" {
			bad["evidence"] = msg
		} else {
			sort.Strings(r.Evidence)
		}
	}
}

func checkEvidence(refs []string) string {
	if len(refs) < 1 || len(refs) > LeadAcceptMaxEvidence {
		return fmt.Sprintf("must be given 1 to %d times", LeadAcceptMaxEvidence)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		m := evidenceRefRe.FindStringSubmatch(ref)
		if len(m) != 4 || m[2] == uuid.Nil.String() || m[3] == uuid.Nil.String() {
			return "each must be comment:<task uuid>/<comment uuid> or artifact:<task uuid>/<artifact uuid>, full lowercase UUIDs"
		}
		if seen[ref] {
			return "must not repeat a ref"
		}
		seen[ref] = true
	}
	return ""
}

// EvidenceRefs parses the (already validated) evidence of r, in its order.
func (r *LeadAcceptRequest) EvidenceRefs() []LeadAcceptEvidenceRef {
	out := make([]LeadAcceptEvidenceRef, 0, len(r.Evidence))
	for _, ref := range r.Evidence {
		m := evidenceRefRe.FindStringSubmatch(ref)
		if m == nil {
			continue
		}
		out = append(out, LeadAcceptEvidenceRef{Ref: ref, Kind: m[1],
			TaskID: uuid.MustParse(m[2]), RecordID: uuid.MustParse(m[3])})
	}
	return out
}

// CanonicalJSON is the request as hashed: keys sorted, no whitespace,
// integers in decimal, evidence sorted.
func (r *LeadAcceptRequest) CanonicalJSON() []byte {
	c := *r
	c.Evidence = append([]string{}, r.Evidence...)
	sort.Strings(c.Evidence)
	b, err := CanonicalJSON(c)
	if err != nil { // cannot happen for strings and integers
		panic(err)
	}
	return b
}

// SHA256 is the request_sha256 of the contract (and the binding_sha256 of a
// reader bound to it).
func (r *LeadAcceptRequest) SHA256() string { return SHA256Hex(r.CanonicalJSON()) }

// SHA256Hex is the lowercase hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- attestation (LA7) -----------------------------------------------------

// LeadAcceptLead is one lead of the project in an observation.
type LeadAcceptLead struct {
	Username string `json:"username"`
	GitlabID int64  `json:"gitlab_id"`
	Lane     string `json:"lane"`
}

// LeadAcceptProjectPolicy is the gate's lead-accept policy for one project.
type LeadAcceptProjectPolicy struct {
	LeadAcceptAllowed bool             `json:"lead_accept_allowed"`
	PolicyReason      string           `json:"policy_reason"`
	Leads             []LeadAcceptLead `json:"leads"`
}

// LeadAcceptAttestation is a validated attestation body. For ok=false only
// Schema, ObservationID, ObservedAt, OK, ErrorStep and Error are set.
type LeadAcceptAttestation struct {
	Schema                     string                             `json:"schema"`
	ObservationID              string                             `json:"observation_id"`
	ObservedAt                 string                             `json:"observed_at"`
	OK                         bool                               `json:"ok"`
	ErrorStep                  string                             `json:"error_step,omitempty"`
	Error                      string                             `json:"error,omitempty"`
	GitCommit                  string                             `json:"git_commit,omitempty"`
	OriginMainCommit           string                             `json:"origin_main_commit,omitempty"`
	RegistryLastCommit         string                             `json:"registry_last_commit,omitempty"`
	RegistryBlobSHA1           string                             `json:"registry_blob_sha1,omitempty"`
	RegistryOriginBlobSHA1     string                             `json:"origin_registry_blob_sha1,omitempty"`
	RegistrySHA256             string                             `json:"registry_sha256,omitempty"`
	MirrorPolicyBlobSHA1       string                             `json:"mirror_policy_blob_sha1,omitempty"`
	MirrorPolicyOriginBlobSHA1 string                             `json:"origin_mirror_policy_blob_sha1,omitempty"`
	MirrorPolicySHA256         string                             `json:"mirror_policy_sha256,omitempty"`
	GateCodeSHA256             string                             `json:"gate_code_sha256,omitempty"`
	WorktreeClean              *bool                              `json:"worktree_clean,omitempty"`
	ClaimedInSync              *bool                              `json:"in_sync,omitempty"`
	Projects                   map[string]LeadAcceptProjectPolicy `json:"projects,omitempty"`

	// Parsed forms, not serialised.
	ObservedAtTime time.Time `json:"-"`
	ObservationUID uuid.UUID `json:"-"`
}

var (
	attestationCommon = []string{"schema", "observation_id", "observed_at", "ok"}
	attestationFail   = []string{"error_step", "error"}
	attestationOKHex  = map[string]*regexp.Regexp{
		"git_commit":                     hex40Re,
		"origin_main_commit":             hex40Re,
		"registry_last_commit":           hex40Re,
		"registry_blob_sha1":             hex40Re,
		"origin_registry_blob_sha1":      hex40Re,
		"registry_sha256":                hex64Re,
		"mirror_policy_blob_sha1":        hex40Re,
		"origin_mirror_policy_blob_sha1": hex40Re,
		"mirror_policy_sha256":           hex64Re,
		"gate_code_sha256":               hex64Re,
	}
)

// InSync is the contract's in_sync: the gate's working tree is clean for both
// files and each file's blob equals its blob in origin/main. It is computed
// here from the observed blobs, never taken from the publisher.
func (a *LeadAcceptAttestation) InSync() bool {
	return a.OK && a.WorktreeClean != nil && *a.WorktreeClean &&
		a.RegistryBlobSHA1 == a.RegistryOriginBlobSHA1 &&
		a.MirrorPolicyBlobSHA1 == a.MirrorPolicyOriginBlobSHA1
}

// ParseLeadAcceptAttestation decodes and validates an attestation body. Any
// refusal is a 400 with per-field reasons.
func ParseLeadAcceptAttestation(body []byte) (parsed *LeadAcceptAttestation, refusals map[string]string) {
	fields, perr := decodeStrictObject(body)
	if perr != nil {
		if perr.Fields != nil {
			return nil, perr.Fields
		}
		return nil, map[string]string{"body": "must be one JSON object"}
	}
	bad := map[string]string{}
	a := &LeadAcceptAttestation{}
	str := func(name string, dst *string) bool {
		raw, ok := fields[name]
		if !ok {
			bad[name] = "required"
			return false
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			bad[name] = "must be a string"
			return false
		}
		return true
	}
	if raw, ok := fields["ok"]; !ok {
		bad["ok"] = "required"
	} else if err := json.Unmarshal(raw, &a.OK); err != nil {
		bad["ok"] = "must be a boolean"
	}
	if str("schema", &a.Schema) && a.Schema != LeadAcceptAttestationSchema {
		bad["schema"] = "must be " + LeadAcceptAttestationSchema
	}
	if str("observation_id", &a.ObservationID) {
		if !lowerUUIDRe.MatchString(a.ObservationID) || a.ObservationID == uuid.Nil.String() {
			bad["observation_id"] = "must be a non-nil UUID in canonical lowercase form"
		} else {
			a.ObservationUID = uuid.MustParse(a.ObservationID)
		}
	}
	if str("observed_at", &a.ObservedAt) {
		t, err := time.Parse(time.RFC3339Nano, a.ObservedAt)
		if err != nil || !strings.HasSuffix(a.ObservedAt, "Z") {
			bad["observed_at"] = "must be an RFC3339 UTC timestamp ending in Z"
		} else {
			a.ObservedAtTime = t.UTC()
		}
	}
	if _, isBad := bad["ok"]; isBad {
		return nil, bad
	}
	allowed := map[string]bool{}
	for _, f := range attestationCommon {
		allowed[f] = true
	}
	if !a.OK {
		for _, f := range attestationFail {
			allowed[f] = true
		}
		for k := range fields {
			if !allowed[k] {
				bad[k] = "not allowed in an ok=false observation"
			}
		}
		if str("error_step", &a.ErrorStep) {
			okStep := false
			for _, s := range LeadAcceptErrorSteps {
				okStep = okStep || a.ErrorStep == s
			}
			if !okStep {
				bad["error_step"] = "must be one of " + strings.Join(LeadAcceptErrorSteps, ", ")
			}
		}
		if str("error", &a.Error) && (a.Error == "" || len(a.Error) > 300 || strings.ContainsAny(a.Error, "\r\n\t")) {
			bad["error"] = "must be a short single-line text (1-300 bytes)"
		}
		if len(bad) > 0 {
			return nil, bad
		}
		return a, nil
	}

	for f := range attestationOKHex {
		allowed[f] = true
	}
	allowed["worktree_clean"] = true
	allowed["in_sync"] = true
	allowed["projects"] = true
	for k := range fields {
		if !allowed[k] {
			bad[k] = "unknown field"
		}
	}
	dst := map[string]*string{
		"git_commit": &a.GitCommit, "origin_main_commit": &a.OriginMainCommit,
		"registry_last_commit": &a.RegistryLastCommit, "registry_blob_sha1": &a.RegistryBlobSHA1,
		"origin_registry_blob_sha1": &a.RegistryOriginBlobSHA1, "registry_sha256": &a.RegistrySHA256,
		"mirror_policy_blob_sha1": &a.MirrorPolicyBlobSHA1, "origin_mirror_policy_blob_sha1": &a.MirrorPolicyOriginBlobSHA1,
		"mirror_policy_sha256": &a.MirrorPolicySHA256, "gate_code_sha256": &a.GateCodeSHA256,
	}
	for name, re := range attestationOKHex {
		if str(name, dst[name]) && !re.MatchString(*dst[name]) {
			bad[name] = "must be lowercase hex of the right length"
		}
	}
	if raw, ok := fields["worktree_clean"]; !ok {
		bad["worktree_clean"] = "required"
	} else {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			bad["worktree_clean"] = "must be a boolean"
		} else {
			a.WorktreeClean = &b
		}
	}
	// in_sync is the publisher's own reading; the server recomputes it and
	// refuses a body that contradicts itself.
	if raw, ok := fields["in_sync"]; !ok {
		bad["in_sync"] = "required"
	} else {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			bad["in_sync"] = "must be a boolean"
		} else {
			a.ClaimedInSync = &b
		}
	}
	if len(bad) == 0 && *a.ClaimedInSync != a.InSync() {
		bad["in_sync"] = "contradicts worktree_clean and the blob sha1 fields"
	}
	if raw, ok := fields["projects"]; !ok {
		bad["projects"] = "required"
	} else if pol, msg := parseLeadAcceptProjects(raw); msg != "" {
		bad["projects"] = msg
	} else {
		a.Projects = map[string]LeadAcceptProjectPolicy{LeadAcceptProject: *pol}
	}
	if len(bad) > 0 {
		return nil, bad
	}
	return a, nil
}

func parseLeadAcceptProjects(raw json.RawMessage) (policy *LeadAcceptProjectPolicy, reason string) {
	projects, perr := decodeStrictObject(raw)
	if perr != nil {
		return nil, "must be an object with exactly the key " + LeadAcceptProject
	}
	if len(projects) != 1 {
		return nil, "must hold exactly one project, " + LeadAcceptProject
	}
	praw, ok := projects[LeadAcceptProject]
	if !ok {
		return nil, "only " + LeadAcceptProject + " is accepted"
	}
	pf, perr := decodeStrictObject(praw)
	if perr != nil {
		return nil, "project policy must be an object"
	}
	if len(pf) != 3 {
		return nil, "project policy must have exactly lead_accept_allowed, policy_reason, leads"
	}
	pol := &LeadAcceptProjectPolicy{}
	if err := json.Unmarshal(pf["lead_accept_allowed"], &pol.LeadAcceptAllowed); err != nil || pf["lead_accept_allowed"] == nil {
		return nil, "lead_accept_allowed must be a boolean"
	}
	if err := json.Unmarshal(pf["policy_reason"], &pol.PolicyReason); err != nil || pf["policy_reason"] == nil || len(pol.PolicyReason) > 500 {
		return nil, "policy_reason must be a string of at most 500 bytes"
	}
	var leads []json.RawMessage
	if err := json.Unmarshal(pf["leads"], &leads); err != nil || pf["leads"] == nil || leads == nil || len(leads) > 100 {
		return nil, "leads must be an array of at most 100 leads"
	}
	seen := map[int64]bool{}
	pol.Leads = make([]LeadAcceptLead, 0, len(leads))
	for i, lraw := range leads {
		lf, perr := decodeStrictObject(lraw)
		if perr != nil || len(lf) != 3 || lf["username"] == nil || lf["gitlab_id"] == nil || lf["lane"] == nil {
			return nil, fmt.Sprintf("leads[%d] must be {username, gitlab_id, lane}", i)
		}
		var l LeadAcceptLead
		if err := json.Unmarshal(lf["username"], &l.Username); err != nil || !leadUsernameRe.MatchString(l.Username) {
			return nil, fmt.Sprintf("leads[%d].username is not a GitLab username", i)
		}
		if !posIntRe.MatchString(string(lf["gitlab_id"])) {
			return nil, fmt.Sprintf("leads[%d].gitlab_id must be an integer >= 1", i)
		}
		l.GitlabID, _ = strconv.ParseInt(string(lf["gitlab_id"]), 10, 64)
		if err := json.Unmarshal(lf["lane"], &l.Lane); err != nil || l.Lane == "" || len(l.Lane) > 64 {
			return nil, fmt.Sprintf("leads[%d].lane must be a string of 1-64 bytes", i)
		}
		if seen[l.GitlabID] {
			return nil, fmt.Sprintf("leads[%d].gitlab_id repeats", i)
		}
		seen[l.GitlabID] = true
		pol.Leads = append(pol.Leads, l)
	}
	return pol, ""
}

// --- tokens ------------------------------------------------------------------

// LeadAcceptTokenPrefix returns the token prefix of a kind.
func LeadAcceptTokenPrefix(kind string) string {
	if kind == LeadAcceptKindPublisher {
		return LeadAcceptPublisherTokenPrefix
	}
	return LeadAcceptReaderTokenPrefix
}

// NewLeadAcceptToken returns a fresh token of kind: prefix + 43 base64url
// characters (32 random bytes).
func NewLeadAcceptToken(kind string) (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return LeadAcceptTokenPrefix(kind) + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// LeadAcceptTokenKind returns the kind a token's prefix names ("" when it has
// neither lead-accept prefix).
func LeadAcceptTokenKind(token string) string {
	switch {
	case strings.HasPrefix(token, LeadAcceptReaderTokenPrefix):
		return LeadAcceptKindReader
	case strings.HasPrefix(token, LeadAcceptPublisherTokenPrefix):
		return LeadAcceptKindPublisher
	}
	return ""
}

// LeadAcceptTokenWellFormed reports whether token has the shape of a
// lead-accept token, so a malformed one is refused without a lookup.
func LeadAcceptTokenWellFormed(token string) bool {
	kind := LeadAcceptTokenKind(token)
	if kind == "" || len(token) != 4+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(token[4:])
	return err == nil
}

// ErrLeadAcceptUnauthorized is the single refusal of any token failure.
var ErrLeadAcceptUnauthorized = errors.New("lead accept: unauthorized")

// --- response (LA5) ----------------------------------------------------------

// LeadAcceptAuthor names who wrote an evidence record.
type LeadAcceptAuthor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LeadAcceptMentions says which exact request values occur in the record.
type LeadAcceptMentions struct {
	ApprovalID    bool `json:"approval_id"`
	PayloadSHA256 bool `json:"payload_sha256"`
	HeadSHA       bool `json:"head_sha"`
}

// MentionsIn reports which of the request's exact values occur in b.
func (r *LeadAcceptRequest) MentionsIn(b []byte) LeadAcceptMentions {
	return LeadAcceptMentions{
		ApprovalID:    bytes.Contains(b, []byte(r.ApprovalID)),
		PayloadSHA256: bytes.Contains(b, []byte(r.PayloadSHA256)),
		HeadSHA:       bytes.Contains(b, []byte(r.HeadSHA)),
	}
}

// LeadAcceptEvidenceOut is one evidence record of the answer. Comment-only
// and artifact-only fields are pointers, so the other kind omits them.
type LeadAcceptEvidenceOut struct {
	Ref        string             `json:"ref"`
	Kind       string             `json:"kind"`
	TaskID     string             `json:"task_id"`
	ProjectID  string             `json:"project_id"`
	Name       *string            `json:"name,omitempty"`
	SizeBytes  *int64             `json:"size_bytes,omitempty"`
	Author     LeadAcceptAuthor   `json:"author"`
	SystemCode *string            `json:"-"`
	IsInternal *bool              `json:"is_internal,omitempty"`
	CreatedAt  string             `json:"created_at"`
	UpdatedAt  *string            `json:"updated_at,omitempty"`
	Edited     *bool              `json:"edited,omitempty"`
	SHA256     string             `json:"sha256"`
	Mentions   LeadAcceptMentions `json:"mentions"`
	Status     string             `json:"status"`
}

// MarshalJSON writes system_code (explicit null included) only for comments.
func (e LeadAcceptEvidenceOut) MarshalJSON() ([]byte, error) {
	type plain LeadAcceptEvidenceOut
	b, err := json.Marshal(plain(e))
	if err != nil || e.Kind != "comment" {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	sc, _ := json.Marshal(e.SystemCode)
	m["system_code"] = sc
	return json.Marshal(m)
}

// LeadAcceptLeadOut is the lead record of the answer.
type LeadAcceptLeadOut struct {
	Username string `json:"username"`
	GitlabID int64  `json:"gitlab_id"`
	Lane     string `json:"lane"`
	Status   string `json:"status"`
}

// LeadAcceptScopeOut is the project policy of the answer.
type LeadAcceptScopeOut struct {
	Project           string `json:"project"`
	LeadAcceptAllowed bool   `json:"lead_accept_allowed"`
	PolicyReason      string `json:"policy_reason"`
}

// LeadAcceptRegistryOut is the observation the answer rests on.
type LeadAcceptRegistryOut struct {
	ObservationID        string `json:"observation_id"`
	OK                   bool   `json:"ok"`
	GitCommit            string `json:"git_commit"`
	OriginMainCommit     string `json:"origin_main_commit"`
	RegistryLastCommit   string `json:"registry_last_commit"`
	RegistryBlobSHA1     string `json:"registry_blob_sha1"`
	RegistrySHA256       string `json:"registry_sha256"`
	MirrorPolicyBlobSHA1 string `json:"mirror_policy_blob_sha1"`
	MirrorPolicySHA256   string `json:"mirror_policy_sha256"`
	GateCodeSHA256       string `json:"gate_code_sha256"`
	InSync               bool   `json:"in_sync"`
	ObservedAt           string `json:"observed_at"`
	ReceivedAt           string `json:"received_at"`
	MaxAgeSeconds        int    `json:"max_age_seconds"`
	FreshUntil           string `json:"fresh_until"`
}

// LeadAcceptApprovalBinding is the gate-intake receipt check of the answer.
type LeadAcceptApprovalBinding struct {
	ApprovalID        string `json:"approval_id"`
	ApprovalVersion   int64  `json:"approval_version"`
	PayloadSHA256     string `json:"payload_sha256"`
	GateIntakeReceipt string `json:"gate_intake_receipt"`
}

// LeadAcceptContext is the 200 body of GET /api/v1/lead-accept-context.
type LeadAcceptContext struct {
	Schema          string                    `json:"schema"`
	CheckedAt       string                    `json:"checked_at"`
	Request         *LeadAcceptRequest        `json:"request"`
	RequestSHA256   string                    `json:"request_sha256"`
	Lead            LeadAcceptLeadOut         `json:"lead"`
	Scope           LeadAcceptScopeOut        `json:"scope"`
	Registry        LeadAcceptRegistryOut     `json:"registry"`
	Evidence        []LeadAcceptEvidenceOut   `json:"evidence"`
	ApprovalBinding LeadAcceptApprovalBinding `json:"approval_binding"`
	ContextSHA256   string                    `json:"context_sha256"`
}

// ComputeContextSHA256 is the contract's context_sha256: the canonical JSON of
// {request, lead, scope, registry:{git_commit, registry_sha256,
// mirror_policy_sha256, in_sync}, evidence:[{ref, author, created_at,
// updated_at|null, sha256, mentions}], approval_binding}.
func (c *LeadAcceptContext) ComputeContextSHA256() (string, error) {
	ev := make([]map[string]any, 0, len(c.Evidence))
	for _, e := range c.Evidence {
		var updated any
		if e.UpdatedAt != nil {
			updated = *e.UpdatedAt
		}
		ev = append(ev, map[string]any{
			"ref": e.Ref, "author": e.Author, "created_at": e.CreatedAt,
			"updated_at": updated, "sha256": e.SHA256, "mentions": e.Mentions,
		})
	}
	b, err := CanonicalJSON(map[string]any{
		"request": c.Request,
		"lead":    c.Lead,
		"scope":   c.Scope,
		"registry": map[string]any{
			"git_commit": c.Registry.GitCommit, "registry_sha256": c.Registry.RegistrySHA256,
			"mirror_policy_sha256": c.Registry.MirrorPolicySHA256, "in_sync": c.Registry.InSync,
		},
		"evidence":         ev,
		"approval_binding": c.ApprovalBinding,
	})
	if err != nil {
		return "", err
	}
	return SHA256Hex(b), nil
}

// LeadAcceptTime formats an instant as the answer writes it: RFC3339 UTC with
// up to microseconds (the database precision).
func LeadAcceptTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

// --- gate-intake receipt (approval_binding) ---------------------------------

var gateIntakeDescLineRe = regexp.MustCompile("(?m)^- ([a-z_0-9]+): `?([^`\\n]*)`?$")

// GateIntakeRequestFromDescription recovers the 11 fields of a gate-intake
// request from the description gate intake wrote on its card
// (GateIntakeRequest.Description). The receipt stores only request_sha256, so
// the recovered fields are trusted only when their canonical hash equals the
// receipt's: an edited description, a missing line or an unparsable value
// returns false, and the caller treats the receipt as unverifiable.
func GateIntakeRequestFromDescription(desc, requestSHA256 string) (*GateIntakeRequest, bool) {
	vals := map[string]string{}
	for _, m := range gateIntakeDescLineRe.FindAllStringSubmatch(desc, -1) {
		if _, dup := vals[m[1]]; dup {
			return nil, false
		}
		vals[m[1]] = m[2]
	}
	r := &GateIntakeRequest{}
	num := func(name string, dst *int64) bool {
		v, ok := vals[name]
		if !ok || !posIntRe.MatchString(v) {
			return false
		}
		n, err := strconv.ParseInt(v, 10, 64)
		*dst = n
		return err == nil
	}
	for _, f := range gateIntakeFields {
		if _, ok := vals[f]; !ok {
			return nil, false
		}
	}
	id, err := uuid.Parse(vals["approval_id"])
	if err != nil {
		return nil, false
	}
	r.ApprovalID = id
	r.Schema = vals["schema"]
	r.PayloadSHA256 = vals["payload_sha256"]
	r.ManifestSHA256 = vals["manifest_sha256"]
	r.HeadSHA = vals["head_sha"]
	r.MRURL = vals["mr_url"]
	r.ArticleURL = vals["article_url"]
	if !num("task_id", &r.TaskID) || !num("draft_version", &r.DraftVersion) ||
		!num("spark_project_id", &r.SparkProjectID) || !num("mr_iid", &r.MRIID) {
		return nil, false
	}
	if r.RequestSHA256() != requestSHA256 {
		return nil, false
	}
	return r, true
}
