// Package hostlocalqa holds the frozen, reviewed contract of the host-local QA
// assignment capability (host_local_qa_assignment_v1): the one fixed policy, the
// exact assignment text, and strict parsers for every wire body. Nothing here
// reads configuration from a request: policy.json and brief.txt are embedded
// bytes whose SHA-256 are pinned below, so changing either requires a reviewed
// source change and a new pin.
package hostlocalqa

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

const (
	// Capability is the only capability this package admits. It is disjoint from
	// the legacy readonly_metadata capability.
	Capability    = "host_local_qa_assignment_v1"
	SchemaVersion = 1

	// PolicySHA256 and BriefSHA256 pin the embedded bytes.
	PolicySHA256 = "8bfef84ce6a570ed99d8f24eac32d836a2fd78ab12b459b6c24917aa754652ad"
	BriefSHA256  = "762203baafebca0b17c46b4dda2e63b4fd1f1ae02fa74e089dd1e43bc3c47d8c"

	// MaxExpirySeconds bounds both a receiver admission and a delivery.
	MaxExpirySeconds = 8 * 60 * 60

	// Transport is the only producer transport.
	Transport = "iterm"
)

//go:embed policy.json
var policyBytes []byte

//go:embed brief.txt
var briefBytes []byte

// Policy is the fixed policy.json. Unknown fields are a load error.
type Policy struct {
	AgentID              uuid.UUID   `json:"agent_id"`
	AllowedSenderAgents  []uuid.UUID `json:"allowed_sender_agent_ids"`
	ArtifactID           int64       `json:"artifact_id"`
	Candidate            string      `json:"candidate"`
	Capability           string      `json:"capability"`
	DefaultEnabled       bool        `json:"default_enabled"`
	Hostname             string      `json:"hostname"`
	MaxExpirySeconds     int64       `json:"max_expiry_seconds"`
	OperationManifestSHA string      `json:"operation_manifest_sha256"`
	Profile              string      `json:"profile"`
	QAPackageSHA         string      `json:"qa_package_sha256"`
	SchemaVersion        int         `json:"schema_version"`
	SourceTaskID         uuid.UUID   `json:"source_task_id"`
	TextSHA256           string      `json:"text_sha256"`
	Vault                string      `json:"vault"`
	WorkspaceID          uuid.UUID   `json:"workspace_id"`

	// Text is the exact assignment text (brief.txt), including its final LF.
	Text string `json:"-"`
	// SHA256 is the hash of the exact policy.json bytes.
	SHA256 string `json:"-"`
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// LoadPolicy parses the embedded policy and verifies every pin.
func LoadPolicy() (*Policy, error) {
	if got := sum(policyBytes); got != PolicySHA256 {
		return nil, fmt.Errorf("hostlocalqa: policy.json sha256 %s does not match the reviewed pin", got)
	}
	if got := sum(briefBytes); got != BriefSHA256 {
		return nil, fmt.Errorf("hostlocalqa: brief.txt sha256 %s does not match the reviewed pin", got)
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(policyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("hostlocalqa: policy.json: %w", err)
	}
	p.Text, p.SHA256 = string(briefBytes), PolicySHA256
	switch {
	case p.Capability != Capability, p.SchemaVersion != SchemaVersion, p.DefaultEnabled,
		p.TextSHA256 != BriefSHA256, p.MaxExpirySeconds != MaxExpirySeconds,
		p.WorkspaceID == uuid.Nil, p.AgentID == uuid.Nil, p.Hostname == "", len(p.AllowedSenderAgents) == 0:
		return nil, fmt.Errorf("hostlocalqa: policy.json is inconsistent with the frozen contract")
	}
	return &p, nil
}

// SenderAllowed reports whether the policy itself lists the sender.
func (p *Policy) SenderAllowed(id uuid.UUID) bool {
	for _, a := range p.AllowedSenderAgents {
		if a == id {
			return true
		}
	}
	return false
}
