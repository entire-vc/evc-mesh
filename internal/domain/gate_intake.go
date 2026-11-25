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
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Gate intake: contract CH→Mesh gate-create/v1 (docs/gate-intake.md).
//
// A gate-intake principal is a service credential bound, on the server, to one
// destination for the cards it opens. It authenticates only
// POST /api/v1/gate-intake; every other route refuses it.

const (
	// GateIntakeTokenPrefix starts every gate-intake token: "mgi_" + 43
	// characters of unpadded base64url (32 random bytes, 256 bits).
	GateIntakeTokenPrefix = "mgi_"
	// GateIntakeTokenDisplayLen is how much of a token is kept as its public
	// prefix (shown by `gate-intake show`, written to the audit).
	GateIntakeTokenDisplayLen = 12
	// GateIntakeMaxBody caps the request body.
	GateIntakeMaxBody = 16 << 10
	// GateIntakeDefaultGrace is how long the previous token still works after
	// a rotation when --grace is not given.
	GateIntakeDefaultGrace = time.Hour

	// GateIntakeSchemaContentHub is the request schema of the ContentHub
	// Spark gate wake; GateIntakeResponseSchema names the answer.
	GateIntakeSchemaContentHub = "contenthub-spark-gate-wake/v1"
	GateIntakeResponseSchema   = "mesh-gate-intake/v1"
	// GateIntakeSparkProjectID is the only spark_project_id v1 accepts.
	GateIntakeSparkProjectID = 12
	// GateIntakeArticlePrefix is what article_url must start with.
	GateIntakeArticlePrefix = "https://spark.entire.vc/blog/"
	// GateIntakeExternalRefPrefix names the card's external ref.
	GateIntakeExternalRefPrefix = "contenthub-approval:"

	// Error codes of the intake (the error_code of the Mesh error body).
	GateIntakeCodeTargetFixed         = "intake_target_fixed"
	GateIntakeCodeIdempotencyMismatch = "idempotency_key_mismatch"
	GateIntakeCodePayloadConflict     = "approval_payload_conflict"
)

// GateIntakeTargetFields are the destination-choosing fields a caller may not
// send: the destination belongs to the principal. Any of them is a 403.
var GateIntakeTargetFields = []string{
	"workspace_id", "project_id", "assignee_id", "assignee_type",
	"labels", "status", "status_id", "priority",
}

// gateIntakeFields are exactly the 11 fields of a request, in no special order.
var gateIntakeFields = []string{
	"schema", "approval_id", "task_id", "draft_version", "payload_sha256",
	"manifest_sha256", "spark_project_id", "mr_iid", "head_sha", "mr_url", "article_url",
}

// GateIntakePrincipal is the server-side binding of one gate-intake credential.
type GateIntakePrincipal struct {
	ID                 uuid.UUID  `db:"id" json:"id"`
	Name               string     `db:"name" json:"name"`
	WorkspaceID        uuid.UUID  `db:"workspace_id" json:"workspace_id"`
	ProjectID          uuid.UUID  `db:"project_id" json:"project_id"`
	AssigneeAgentID    uuid.UUID  `db:"assignee_agent_id" json:"assignee_agent_id"`
	Labels             []string   `db:"-" json:"labels"`
	Priority           Priority   `db:"priority" json:"priority"`
	Schema             string     `db:"schema" json:"schema"`
	TokenPrefix        *string    `db:"token_prefix" json:"token_prefix,omitempty"`
	PreviousValidUntil *time.Time `db:"previous_valid_until" json:"previous_valid_until,omitempty"`
	CreatedAt          time.Time  `db:"created_at" json:"created_at"`
	IssuedAt           *time.Time `db:"issued_at" json:"issued_at,omitempty"`
	RevokedAt          *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	RevokeReason       *string    `db:"revoke_reason" json:"revoke_reason,omitempty"`
}

// GateIntakeRequest is a validated request body (all 11 fields).
type GateIntakeRequest struct {
	Schema         string    `json:"schema"`
	ApprovalID     uuid.UUID `json:"approval_id"`
	TaskID         int64     `json:"task_id"`
	DraftVersion   int64     `json:"draft_version"`
	PayloadSHA256  string    `json:"payload_sha256"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	SparkProjectID int64     `json:"spark_project_id"`
	MRIID          int64     `json:"mr_iid"`
	HeadSHA        string    `json:"head_sha"`
	MRURL          string    `json:"mr_url"`
	ArticleURL     string    `json:"article_url"`
}

// GateIntakeResponse is the body of a 201 and of every replay 200.
type GateIntakeResponse struct {
	Schema        string    `json:"schema"`
	ApprovalID    uuid.UUID `json:"approval_id"`
	TaskID        uuid.UUID `json:"task_id"`
	ProjectID     uuid.UUID `json:"project_id"`
	RequestSHA256 string    `json:"request_sha256"`
	Replayed      bool      `json:"replayed"`
	CreatedAt     time.Time `json:"created_at"`
}

// GateIntakeReceipt is the stored idempotency record of one approval.
type GateIntakeReceipt struct {
	PrincipalID   uuid.UUID       `db:"principal_id"`
	ApprovalID    uuid.UUID       `db:"approval_id"`
	RequestSHA256 string          `db:"request_sha256"`
	TaskID        uuid.UUID       `db:"task_id"`
	Response      json.RawMessage `db:"response"`
	CreatedAt     time.Time       `db:"created_at"`
}

// GateIntakeParseError is a refusal of a request body. Status is the HTTP
// status (400, 403, 413, 415); Code the error_code; Fields the per-field
// reasons of a validation failure.
type GateIntakeParseError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *GateIntakeParseError) Error() string { return e.Code + ": " + e.Message }

var (
	hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex40Re = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// A positive decimal integer as JSON writes it: no sign, no leading zero,
	// no fraction, no exponent.
	posIntRe = regexp.MustCompile(`^[1-9]\d{0,17}$`)
)

// ParseGateIntakeRequest decodes and validates a request body. The checks run
// in the contract's order: not one JSON object → 415; a destination field →
// 403 intake_target_fixed; any other unknown field → 400; then the typed
// field validation → 400. The body size (413) is the caller's to cap.
func ParseGateIntakeRequest(body []byte) (*GateIntakeRequest, *GateIntakeParseError) {
	fields, perr := decodeStrictObject(body)
	if perr != nil {
		return nil, perr
	}
	for _, f := range GateIntakeTargetFields {
		if _, ok := fields[f]; ok {
			return nil, &GateIntakeParseError{Status: 403, Code: GateIntakeCodeTargetFixed,
				Message: "the destination of a gate card is fixed by the principal; " + f + " cannot be sent"}
		}
	}
	allowed := map[string]bool{}
	for _, f := range gateIntakeFields {
		allowed[f] = true
	}
	bad := map[string]string{}
	for k := range fields {
		if !allowed[k] {
			bad[k] = "unknown field"
		}
	}
	for _, f := range gateIntakeFields {
		if _, ok := fields[f]; !ok {
			bad[f] = "required"
		}
	}
	if len(bad) > 0 {
		return nil, validationFailed(bad)
	}

	r := &GateIntakeRequest{}
	str := func(name string, dst *string) {
		if err := json.Unmarshal(fields[name], dst); err != nil {
			bad[name] = "must be a string"
		}
	}
	num := func(name string, dst *int64) {
		raw := string(fields[name])
		if !posIntRe.MatchString(raw) {
			bad[name] = "must be an integer >= 1"
			return
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			bad[name] = "must be an integer >= 1"
			return
		}
		*dst = v
	}
	var approval string
	str("schema", &r.Schema)
	str("approval_id", &approval)
	num("task_id", &r.TaskID)
	num("draft_version", &r.DraftVersion)
	str("payload_sha256", &r.PayloadSHA256)
	str("manifest_sha256", &r.ManifestSHA256)
	num("spark_project_id", &r.SparkProjectID)
	num("mr_iid", &r.MRIID)
	str("head_sha", &r.HeadSHA)
	str("mr_url", &r.MRURL)
	str("article_url", &r.ArticleURL)

	if _, ok := bad["schema"]; !ok && r.Schema != GateIntakeSchemaContentHub {
		bad["schema"] = "must be " + GateIntakeSchemaContentHub
	}
	if _, ok := bad["approval_id"]; !ok {
		id, err := uuid.Parse(approval)
		if err != nil || id == uuid.Nil || id.String() != approval {
			bad["approval_id"] = "must be a non-nil UUID in canonical lowercase form"
		} else {
			r.ApprovalID = id
		}
	}
	if _, ok := bad["spark_project_id"]; !ok && r.SparkProjectID != GateIntakeSparkProjectID {
		bad["spark_project_id"] = "must be 12"
	}
	if _, ok := bad["payload_sha256"]; !ok && !hex64Re.MatchString(r.PayloadSHA256) {
		bad["payload_sha256"] = "must be 64 lowercase hex characters"
	}
	if _, ok := bad["manifest_sha256"]; !ok && !hex64Re.MatchString(r.ManifestSHA256) {
		bad["manifest_sha256"] = "must be 64 lowercase hex characters"
	}
	if bad["payload_sha256"] == "" && bad["manifest_sha256"] == "" && r.PayloadSHA256 == r.ManifestSHA256 {
		bad["manifest_sha256"] = "must differ from payload_sha256"
	}
	if _, ok := bad["head_sha"]; !ok && !hex40Re.MatchString(r.HeadSHA) {
		bad["head_sha"] = "must be a full 40-character lowercase hex commit SHA"
	}
	if _, ok := bad["mr_url"]; !ok {
		if msg := checkMRURL(r.MRURL, r.MRIID, bad["mr_iid"] == ""); msg != "" {
			bad["mr_url"] = msg
		}
	}
	if _, ok := bad["article_url"]; !ok {
		if msg := checkArticleURL(r.ArticleURL); msg != "" {
			bad["article_url"] = msg
		}
	}
	if len(bad) > 0 {
		return nil, validationFailed(bad)
	}
	return r, nil
}

func validationFailed(fields map[string]string) *GateIntakeParseError {
	return &GateIntakeParseError{Status: 400, Code: "validation_failed", Message: "Validation failed", Fields: fields}
}

// decodeStrictObject reads body as exactly one JSON object with unique keys
// and nothing after it. Anything else is a 415: the route takes one JSON object.
func decodeStrictObject(body []byte) (map[string]json.RawMessage, *GateIntakeParseError) {
	notJSON := &GateIntakeParseError{Status: 415, Code: "unsupported_media_type",
		Message: "the body must be one JSON object"}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, notJSON
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, notJSON
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, notJSON
		}
		key, ok := kt.(string)
		if !ok {
			return nil, notJSON
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, notJSON
		}
		if _, dup := out[key]; dup {
			return nil, validationFailed(map[string]string{key: "duplicate key"})
		}
		out[key] = v
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, notJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, notJSON
	}
	return out, nil
}

func checkMRURL(raw string, iid int64, iidOK bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "must be an https URL of a merge request"
	}
	if !iidOK {
		return ""
	}
	if !strings.HasSuffix(u.Path, "/-/merge_requests/"+strconv.FormatInt(iid, 10)) {
		return "path must end with /-/merge_requests/<mr_iid>"
	}
	return ""
}

func checkArticleURL(raw string) string {
	if !strings.HasPrefix(raw, GateIntakeArticlePrefix) {
		return "must start with " + GateIntakeArticlePrefix
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "must be an https URL"
	}
	// The slug is read from the parsed path, not from the raw string: a query
	// or a fragment after /blog/ must not stand in for it. One segment, an
	// optional trailing slash; an escaped slash decodes into a second segment.
	if !strings.HasPrefix(u.Path, "/blog/") {
		return "path must be /blog/<slug>"
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/blog/"), "/")
	if slug == "" || slug == "." || slug == ".." || strings.Contains(slug, "/") {
		return "path must be /blog/<slug> with a non-empty slug"
	}
	return ""
}

// CanonicalJSON is the fingerprint input: the 11 fields, keys sorted, no
// whitespace, integers in decimal, strings without HTML escaping. Receipts
// never expire, so this encoding is frozen: a change would turn every later
// replay of a stored approval into a 409. A golden vector pins it.
func (r *GateIntakeRequest) CanonicalJSON() []byte {
	vals := map[string]any{
		"schema":           r.Schema,
		"approval_id":      r.ApprovalID.String(),
		"task_id":          r.TaskID,
		"draft_version":    r.DraftVersion,
		"payload_sha256":   r.PayloadSHA256,
		"manifest_sha256":  r.ManifestSHA256,
		"spark_project_id": r.SparkProjectID,
		"mr_iid":           r.MRIID,
		"head_sha":         r.HeadSHA,
		"mr_url":           r.MRURL,
		"article_url":      r.ArticleURL,
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeGateIntakeString(&b, k)
		b.WriteByte(':')
		switch v := vals[k].(type) {
		case string:
			writeGateIntakeString(&b, v)
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		}
	}
	b.WriteByte('}')
	return b.Bytes()
}

func writeGateIntakeString(b *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
}

// RequestSHA256 is the hex SHA-256 of CanonicalJSON: the fingerprint that
// decides replay (same) or conflict (different) for one approval.
func (r *GateIntakeRequest) RequestSHA256() string {
	sum := sha256.Sum256(r.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

// Title is the card title of the gate.
func (r *GateIntakeRequest) Title() string {
	return fmt.Sprintf("Spark gate: MR !%d · approval %s · v%d", r.MRIID, r.ApprovalID.String()[:8], r.DraftVersion)
}

// Description lists all 11 fields of the request on the card.
func (r *GateIntakeRequest) Description(requestSHA string) string {
	var b strings.Builder
	b.WriteString("Gate card opened by ContentHub through gate intake (" + GateIntakeSchemaContentHub + ").\n\n")
	fmt.Fprintf(&b, "- schema: `%s`\n", r.Schema)
	fmt.Fprintf(&b, "- approval_id: `%s`\n", r.ApprovalID)
	fmt.Fprintf(&b, "- task_id: %d\n", r.TaskID)
	fmt.Fprintf(&b, "- draft_version: %d\n", r.DraftVersion)
	fmt.Fprintf(&b, "- payload_sha256: `%s`\n", r.PayloadSHA256)
	fmt.Fprintf(&b, "- manifest_sha256: `%s`\n", r.ManifestSHA256)
	fmt.Fprintf(&b, "- spark_project_id: %d\n", r.SparkProjectID)
	fmt.Fprintf(&b, "- mr_iid: %d\n", r.MRIID)
	fmt.Fprintf(&b, "- head_sha: `%s`\n", r.HeadSHA)
	fmt.Fprintf(&b, "- mr_url: %s\n", r.MRURL)
	fmt.Fprintf(&b, "- article_url: %s\n", r.ArticleURL)
	fmt.Fprintf(&b, "\nrequest_sha256: `%s`\n", requestSHA)
	return b.String()
}

// NewGateIntakeToken returns a fresh token: "mgi_" + 43 base64url characters.
func NewGateIntakeToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return GateIntakeTokenPrefix + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// HashGateIntakeToken is the stored form of a token: hex SHA-256.
func HashGateIntakeToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GateIntakeTokenWellFormed reports whether s has the shape of a token, so a
// malformed one is refused without a database lookup.
func GateIntakeTokenWellFormed(s string) bool {
	if !strings.HasPrefix(s, GateIntakeTokenPrefix) || len(s) != len(GateIntakeTokenPrefix)+43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s[len(GateIntakeTokenPrefix):])
	return err == nil
}

// GateIntakeTokenDisplay is the public prefix of a token.
func GateIntakeTokenDisplay(token string) string {
	if len(token) <= GateIntakeTokenDisplayLen {
		return token
	}
	return token[:GateIntakeTokenDisplayLen]
}

// ErrGateIntakeUnauthorized is the single refusal of any token failure.
var ErrGateIntakeUnauthorized = errors.New("gate intake: unauthorized")
