package hostlocalqa

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Target is the six-field identity of the receiving session.
type Target struct {
	AgentID     uuid.UUID `json:"agent_id"`
	HostID      string    `json:"host_id"`
	Hostname    string    `json:"hostname"`
	RunnerID    uuid.UUID `json:"runner_id"`
	SessionID   string    `json:"session_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
}

// Producer is the complete local terminal pin.
type Producer struct {
	ProducerPID       int64  `json:"producer_pid"`
	ProducerStarted   string `json:"producer_started"`
	TerminalSessionID string `json:"terminal_session_id"`
	Transport         string `json:"transport"`
	TTY               string `json:"tty"`
}

// Admitted reports whether the target names the one session the fixed policy
// allows (workspace, agent and hostname). Runner, host and session come from the
// authenticated receiver, not from the policy.
func (p *Policy) Admitted(t Target) bool {
	return t.WorkspaceID == p.WorkspaceID && t.AgentID == p.AgentID && t.Hostname == p.Hostname
}

func parseTarget(m map[string]any, key string) (Target, error) {
	o, err := object(m, key)
	if err != nil {
		return Target{}, err
	}
	err = exactKeys(o, key, "agent_id", "host_id", "hostname", "runner_id", "session_id", "workspace_id")
	if err != nil {
		return Target{}, err
	}
	var t Target
	if t.AgentID, err = uuidField(o, "agent_id"); err != nil {
		return t, err
	}
	if t.RunnerID, err = uuidField(o, "runner_id"); err != nil {
		return t, err
	}
	if t.WorkspaceID, err = uuidField(o, "workspace_id"); err != nil {
		return t, err
	}
	if t.HostID, err = bounded(o, "host_id", 128); err != nil {
		return t, err
	}
	if t.Hostname, err = bounded(o, "hostname", 255); err != nil {
		return t, err
	}
	t.SessionID, err = bounded(o, "session_id", 128)
	return t, err
}

func parseProducer(m map[string]any, key string) (Producer, error) {
	o, err := object(m, key)
	if err != nil {
		return Producer{}, err
	}
	err = exactKeys(o, key, "producer_pid", "producer_started", "terminal_session_id", "transport", "tty")
	if err != nil {
		return Producer{}, err
	}
	var p Producer
	if p.ProducerPID, err = positive(o, "producer_pid"); err != nil {
		return p, err
	}
	if p.ProducerStarted, err = bounded(o, "producer_started", 128); err != nil {
		return p, err
	}
	if p.TerminalSessionID, err = bounded(o, "terminal_session_id", 128); err != nil {
		return p, err
	}
	if terr := constStr(o, "transport", Transport); terr != nil {
		return p, terr
	}
	p.Transport = Transport
	if p.TTY, err = str(o, "tty"); err != nil {
		return p, err
	}
	if !ttyRE.MatchString(p.TTY) {
		return p, invalid("tty must be a /dev/ttys device")
	}
	return p, nil
}

// header validates the three fields every body starts with.
func (p *Policy) header(m map[string]any) error {
	if err := constInt(m, "schema_version", SchemaVersion); err != nil {
		return err
	}
	return constStr(m, "capability", Capability)
}

// ExpiryError reports why expires is not acceptable at now by the server clock:
// it must be in the future and at most MaxExpirySeconds ahead. Empty = fine.
func ExpiryError(expires int64, now time.Time) string {
	switch {
	case expires <= now.Unix():
		return "expired"
	case expires > now.Unix()+MaxExpirySeconds:
		return "expiry_too_far"
	}
	return ""
}

// Registration is the body of POST /runner/host-local-qa/v1/receivers.
type Registration struct {
	Target   Target
	Producer Producer
	Expires  int64
	Senders  []uuid.UUID
}

// ParseRegistration validates a register body against the fixed policy. A
// target that names another workspace, agent or hostname is syntactically valid
// here; the caller refuses it (Admitted) with the same answer as any other
// identity mismatch.
func (p *Policy) ParseRegistration(body []byte) (*Registration, error) {
	m, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	err = exactKeys(m, "body", "schema_version", "capability", "policy_sha256", "target", "producer", "expires", "allowed_sender_agent_ids")
	if err != nil {
		return nil, err
	}
	err = p.header(m)
	if err != nil {
		return nil, err
	}
	err = constStr(m, "policy_sha256", p.SHA256)
	if err != nil {
		return nil, err
	}
	var r Registration
	if r.Target, err = parseTarget(m, "target"); err != nil {
		return nil, err
	}
	if r.Producer, err = parseProducer(m, "producer"); err != nil {
		return nil, err
	}
	if r.Expires, err = positive(m, "expires"); err != nil {
		return nil, err
	}
	arr, ok := m["allowed_sender_agent_ids"].([]any)
	if !ok || len(arr) == 0 || len(arr) > len(p.AllowedSenderAgents) {
		return nil, invalid("allowed_sender_agent_ids must list 1..%d agents", len(p.AllowedSenderAgents))
	}
	seen := map[uuid.UUID]bool{}
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, invalid("allowed_sender_agent_ids must hold strings")
		}
		id, err := parseUUID("allowed_sender_agent_ids", s)
		if err != nil {
			return nil, err
		}
		if seen[id] || !p.SenderAllowed(id) {
			return nil, invalid("allowed_sender_agent_ids must be unique agents the policy allows")
		}
		seen[id] = true
		r.Senders = append(r.Senders, id)
	}
	return &r, nil
}

// Envelope is the sender envelope, minus the text, which is always the frozen
// brief. It is comparable: two envelopes are identical iff they are ==.
type Envelope struct {
	ArtifactID           int64     `json:"artifact_id"`
	Candidate            string    `json:"candidate"`
	Capability           string    `json:"capability"`
	ExpectedRevision     int64     `json:"expected_admission_revision"`
	Expires              int64     `json:"expires"`
	OperationManifestSHA string    `json:"operation_manifest_sha256"`
	PolicySHA256         string    `json:"policy_sha256"`
	Producer             Producer  `json:"producer"`
	QAPackageSHA         string    `json:"qa_package_sha256"`
	ReceiverID           uuid.UUID `json:"receiver_id"`
	RequestID            uuid.UUID `json:"request_id"`
	SchemaVersion        int       `json:"schema_version"`
	SourceTaskID         uuid.UUID `json:"source_task_id"`
	Target               Target    `json:"target"`
	TextSHA256           string    `json:"text_sha256"`
}

// ParseSend validates a sender body exactly against schema.json.
func (p *Policy) ParseSend(body []byte) (*Envelope, error) {
	m, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	err = exactKeys(m, "body", "artifact_id", "candidate", "capability", "expected_admission_revision", "expires",
		"operation_manifest_sha256", "policy_sha256", "producer", "qa_package_sha256", "receiver_id", "request_id",
		"schema_version", "source_task_id", "target", "text", "text_sha256")
	if err != nil {
		return nil, err
	}
	err = p.header(m)
	if err != nil {
		return nil, err
	}
	e := &Envelope{Capability: Capability, SchemaVersion: SchemaVersion, ArtifactID: p.ArtifactID,
		Candidate: p.Candidate, OperationManifestSHA: p.OperationManifestSHA, PolicySHA256: p.SHA256,
		QAPackageSHA: p.QAPackageSHA, SourceTaskID: p.SourceTaskID, TextSHA256: p.TextSHA256}
	err = constInt(m, "artifact_id", p.ArtifactID)
	if err != nil {
		return nil, err
	}
	for key, want := range map[string]string{"candidate": p.Candidate, "operation_manifest_sha256": p.OperationManifestSHA,
		"policy_sha256": p.SHA256, "qa_package_sha256": p.QAPackageSHA, "text_sha256": p.TextSHA256,
		"source_task_id": p.SourceTaskID.String(), "text": p.Text} {
		if cerr := constStr(m, key, want); cerr != nil {
			return nil, cerr
		}
	}
	if e.ExpectedRevision, err = positive(m, "expected_admission_revision"); err != nil {
		return nil, err
	}
	if e.Expires, err = positive(m, "expires"); err != nil {
		return nil, err
	}
	if e.ReceiverID, err = uuidField(m, "receiver_id"); err != nil {
		return nil, err
	}
	if e.RequestID, err = uuidField(m, "request_id"); err != nil {
		return nil, err
	}
	if e.Producer, err = parseProducer(m, "producer"); err != nil {
		return nil, err
	}
	if e.Target, err = parseTarget(m, "target"); err != nil {
		return nil, err
	}
	return e, nil
}

// Binding is the part of a delivery that ACK and check bodies repeat.
type Binding struct {
	ReceiverID       uuid.UUID
	AdmissionRev     int64
	DeliveryID       uuid.UUID
	RequestID        uuid.UUID
	PolicySHA256     string
	TextSHA256       string
	Target           Target
	Producer         Producer
	Expires          int64
	ObservedAt       time.Time
	State            string
	ResultCommentURL string
	ErrorCode        string
}

// Matches reports whether the binding names exactly this stored envelope.
func (b *Binding) Matches(delivery, rcv uuid.UUID, e Envelope) bool {
	return b.DeliveryID == delivery && b.ReceiverID == rcv && b.AdmissionRev == e.ExpectedRevision &&
		b.RequestID == e.RequestID && b.PolicySHA256 == e.PolicySHA256 && b.TextSHA256 == e.TextSHA256 &&
		b.Target == e.Target && b.Producer == e.Producer && b.Expires == e.Expires
}

func parseBinding(m map[string]any) (*Binding, error) {
	var (
		b   Binding
		err error
	)
	if b.ReceiverID, err = uuidField(m, "receiver_id"); err != nil {
		return nil, err
	}
	if b.AdmissionRev, err = positive(m, "admission_revision"); err != nil {
		return nil, err
	}
	if b.DeliveryID, err = uuidField(m, "delivery_id"); err != nil {
		return nil, err
	}
	if b.RequestID, err = uuidField(m, "request_id"); err != nil {
		return nil, err
	}
	if b.PolicySHA256, err = hashField(m, "policy_sha256"); err != nil {
		return nil, err
	}
	if b.TextSHA256, err = hashField(m, "text_sha256"); err != nil {
		return nil, err
	}
	if b.Target, err = parseTarget(m, "target"); err != nil {
		return nil, err
	}
	if b.Producer, err = parseProducer(m, "producer"); err != nil {
		return nil, err
	}
	if b.Expires, err = positive(m, "expires"); err != nil {
		return nil, err
	}
	return &b, nil
}

// ParseCheck validates the body of POST .../deliveries/:delivery_id/check. It
// carries no text and no token.
func (p *Policy) ParseCheck(body []byte) (*Binding, error) {
	m, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	if err := exactKeys(m, "body", "receiver_id", "admission_revision", "delivery_id", "request_id", "capability",
		"policy_sha256", "text_sha256", "target", "producer", "expires"); err != nil {
		return nil, err
	}
	if err := constStr(m, "capability", Capability); err != nil {
		return nil, err
	}
	return parseBinding(m)
}

// Delivery states.
const (
	StateQueued       = "queued"
	StateReceived     = "received"
	StateAcknowledged = "acknowledged"
	StateFailed       = "failed"
	StateUncertain    = "uncertain"
)

var errorCodeRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// ParseAck validates the body of POST .../deliveries/:delivery_id/ack. It
// repeats every binding id, hash and identity plus observed_at (RFC 3339, UTC).
// error_code is required for failed and uncertain and forbidden otherwise.
func (p *Policy) ParseAck(body []byte) (*Binding, error) {
	m, err := parseObject(body)
	if err != nil {
		return nil, err
	}
	required := []string{"schema_version", "capability", "receiver_id", "admission_revision", "delivery_id", "request_id",
		"policy_sha256", "text_sha256", "target", "producer", "expires", "state", "observed_at"}
	optional := map[string]bool{"result_comment_url": true, "error_code": true}
	for k := range m {
		if optional[k] {
			required = append(required, k)
		}
	}
	err = exactKeys(m, "body", required...)
	if err != nil {
		return nil, err
	}
	err = p.header(m)
	if err != nil {
		return nil, err
	}
	b, err := parseBinding(m)
	if err != nil {
		return nil, err
	}
	if b.State, err = str(m, "state"); err != nil {
		return nil, err
	}
	switch b.State {
	case StateReceived, StateAcknowledged, StateFailed, StateUncertain:
	default:
		return nil, invalid("state must be received, acknowledged, failed or uncertain")
	}
	at, err := str(m, "observed_at")
	if err != nil {
		return nil, err
	}
	if b.ObservedAt, err = time.Parse(time.RFC3339Nano, at); err != nil || !strings.HasSuffix(at, "Z") {
		return nil, invalid("observed_at must be an RFC 3339 UTC time")
	}
	if _, ok := m["result_comment_url"]; ok {
		u, uerr := str(m, "result_comment_url")
		if uerr != nil {
			return nil, uerr
		}
		pu, perr := url.Parse(u)
		if len(u) > 2048 || perr != nil || pu.Scheme != "https" || pu.Host == "" || pu.User != nil {
			return nil, invalid("result_comment_url must be an https URL")
		}
		b.ResultCommentURL = u
	}
	if _, ok := m["error_code"]; ok {
		if b.ErrorCode, err = str(m, "error_code"); err != nil || !errorCodeRE.MatchString(b.ErrorCode) {
			return nil, invalid("error_code must be lowercase letters, digits and underscores")
		}
	}
	if (b.State == StateFailed || b.State == StateUncertain) != (b.ErrorCode != "") {
		return nil, invalid("error_code is required for failed and uncertain, and only for them")
	}
	return b, nil
}
