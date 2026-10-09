package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	apicontract "github.com/entire-vc/evc-mesh/docs/api"
)

const IntegrationProviderAgentRuntime IntegrationProvider = "agent_runtime"

type RuntimeIdentity struct {
	AgentID     uuid.UUID `json:"agent_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	GrantID     uuid.UUID `json:"grant_id"`
}

type RuntimeController struct {
	Host                   string    `json:"host"`
	Version                string    `json:"version"`
	ReporterGrantID        uuid.UUID `json:"reporter_grant_id"`
	Capabilities           []string  `json:"capabilities"`
	Enabled                bool      `json:"enabled"`
	HeartbeatMaxAgeSeconds int       `json:"heartbeat_max_age_seconds"`
}

type RuntimePool struct {
	Provider       string   `json:"provider"`
	ResourceRef    string   `json:"resource_ref"`
	Aliases        []string `json:"aliases"`
	MaxConcurrency int      `json:"max_concurrency"`
	ThresholdRef   string   `json:"threshold_ref,omitempty"`
}

type RuntimeAccount struct {
	Provider         string              `json:"provider"`
	CredentialRef    string              `json:"credential_ref"`
	QuotaPoolsByMode map[string][]string `json:"quota_pools_by_mode"`
}

type RuntimeProfile struct {
	ControllerRef  string   `json:"controller_ref"`
	AccountRef     string   `json:"account_ref"`
	Harness        string   `json:"harness"`
	Model          string   `json:"model"`
	ModelDeveloper string   `json:"model_developer"`
	ModelFamily    string   `json:"model_family"`
	ExecutionMode  string   `json:"execution_mode"`
	Capabilities   []string `json:"capabilities"`
	Enabled        bool     `json:"enabled"`
}

type RuntimeQA struct {
	Mode          string `json:"mode"`
	QuotaFallback bool   `json:"quota_fallback"`
}

type RuntimePolicy struct {
	PrimaryProfiles       []string            `json:"primary_profiles"`
	QuotaEdges            map[string][]string `json:"quota_edges"`
	PreferredAccounts     map[string]string   `json:"preferred_accounts"`
	APIReserveProviders   []string            `json:"api_reserve_providers"`
	MaxAttempts           int                 `json:"max_attempts"`
	EvidenceMaxAgeSeconds int                 `json:"evidence_max_age_seconds"`
	QA                    RuntimeQA           `json:"qa"`
}

type RuntimeBinding struct {
	Binding           RuntimeIdentity `json:"binding"`
	PermittedProfiles []string        `json:"permitted_profiles"`
	Policy            RuntimePolicy   `json:"policy"`
	Enabled           bool            `json:"enabled"`
}

type RuntimeCatalog struct {
	SchemaVersion int                          `json:"schema_version"`
	Controllers   map[string]RuntimeController `json:"controllers"`
	Accounts      map[string]RuntimeAccount    `json:"accounts"`
	Pools         map[string]RuntimePool       `json:"pools"`
	Profiles      map[string]RuntimeProfile    `json:"profiles"`
	Bindings      map[string]RuntimeBinding    `json:"bindings"`
}

// RuntimePoolObservation represents evidence, never a client admission decision.
type RuntimePoolObservation struct {
	State        string    `json:"state"` // available, exhausted, threshold, unknown, auth_error, network_error
	ObservedAt   time.Time `json:"observed_at"`
	Verified     bool      `json:"verified"`
	EvidenceKind string    `json:"evidence_kind,omitempty"`
	ThresholdRef string    `json:"threshold_ref,omitempty"`
}

type RuntimeModelObservation struct {
	Model          string    `json:"model"`
	ModelDeveloper string    `json:"model_developer"`
	ModelFamily    string    `json:"model_family"`
	ObservedAt     time.Time `json:"observed_at"`
}

type RuntimeReport struct {
	SchemaVersion   int                                `json:"schema_version"`
	Revision        int64                              `json:"revision"`
	Digest          string                             `json:"digest"`
	Status          string                             `json:"status"` // applied or rejected
	Capabilities    []string                           `json:"capabilities"`
	EmergencyPaused bool                               `json:"emergency_paused"`
	Pools           map[string]RuntimePoolObservation  `json:"pools"`
	Profiles        map[string]RuntimeModelObservation `json:"profiles,omitempty"`
}

type RuntimeControllerState struct {
	ControllerRef string        `json:"controller_ref"`
	Report        RuntimeReport `json:"report"`
	ReceivedAt    time.Time     `json:"received_at"`
	Current       bool          `json:"current"`
}

type RuntimeSnapshot struct {
	Mode           string                   `json:"mode"`
	Revision       int64                    `json:"revision"`
	Digest         string                   `json:"digest"`
	Enabled        bool                     `json:"enabled"`
	DrainRequested bool                     `json:"drain_requested"`
	Catalog        *RuntimeCatalog          `json:"config,omitempty"`
	Controllers    []RuntimeControllerState `json:"controllers"`
}

// Redacted returns a copy of the catalog that is safe for REST responses:
// credential references stay with the controller and are never echoed back.
func (c *RuntimeCatalog) Redacted() *RuntimeCatalog {
	if c == nil {
		return nil
	}
	out := *c
	out.Accounts = make(map[string]RuntimeAccount, len(c.Accounts))
	for ref, account := range c.Accounts {
		account.CredentialRef = ""
		out.Accounts[ref] = account
	}
	return &out
}

var catalogSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(apicontract.RuntimeSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err = c.AddResource("urn:entire:agent-runtime:catalog:v2", value); err != nil {
		return nil, err
	}
	return c.Compile("urn:entire:agent-runtime:catalog:v2")
})

var ErrRuntimeInput = errors.New("invalid runtime request")

var runtimeRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,199}$`)

func ValidRuntimeRef(value string) bool { return runtimeRefPattern.MatchString(value) }

// DecodeRuntimeJSON rejects duplicate keys, unknown fields and trailing documents.
// Error text deliberately excludes submitted values (including accidental secrets).
func DecodeRuntimeJSON(data []byte, target any) error {
	if len(data) > 1<<20 {
		return ErrRuntimeInput
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := runtimeJSONValue(d, 0); err != nil {
		return ErrRuntimeInput
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrRuntimeInput
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrRuntimeInput
	}
	return nil
}

func runtimeJSONValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrRuntimeInput
	}
	t, err := d.Token()
	if err != nil || t == nil {
		return ErrRuntimeInput
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return ErrRuntimeInput
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrRuntimeInput
			}
			seen[name] = true
			e = runtimeJSONValue(d, depth+1)
			if e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrRuntimeInput
		}
	case json.Delim('['):
		for d.More() {
			if err := runtimeJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrRuntimeInput
		}
	}
	return nil
}

func ParseRuntimeCatalog(data []byte) (*RuntimeCatalog, error) {
	var catalog RuntimeCatalog
	if err := DecodeRuntimeJSON(data, &catalog); err != nil {
		return nil, err
	}
	schema, err := catalogSchema()
	if err != nil {
		return nil, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil || schema.Validate(value) != nil {
		return nil, ErrRuntimeInput
	}
	if err := catalog.ValidateReferences(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

func (c *RuntimeCatalog) ResolvePool(ref string) (string, bool) {
	if _, ok := c.Pools[ref]; ok {
		return ref, true
	}
	for name, p := range c.Pools {
		if slices.Contains(p.Aliases, ref) {
			return name, true
		}
	}
	return "", false
}

func (c *RuntimeCatalog) ValidateReferences() error {
	aliases, physical := map[string]bool{}, map[string]bool{}
	for ref, p := range c.Pools {
		key := p.Provider + "\x00" + p.ResourceRef
		if physical[key] {
			return ErrRuntimeInput
		}
		physical[key] = true
		for _, a := range p.Aliases {
			_, named := c.Pools[a]
			if a == ref || named || aliases[a] {
				return ErrRuntimeInput
			}
			aliases[a] = true
		}
	}
	for _, a := range c.Accounts {
		for _, refs := range a.QuotaPoolsByMode {
			for _, ref := range refs {
				name, ok := c.ResolvePool(ref)
				if !ok || c.Pools[name].Provider != a.Provider {
					return ErrRuntimeInput
				}
			}
		}
	}
	for _, p := range c.Profiles {
		controller, ok := c.Controllers[p.ControllerRef]
		account, aok := c.Accounts[p.AccountRef]
		if !ok || !aok || len(account.QuotaPoolsByMode[p.ExecutionMode]) == 0 {
			return ErrRuntimeInput
		}
		for _, cap := range p.Capabilities {
			if !slices.Contains(controller.Capabilities, cap) {
				return ErrRuntimeInput
			}
		}
	}
	identities := map[RuntimeIdentity]bool{}
	for _, b := range c.Bindings {
		if identities[b.Binding] {
			return ErrRuntimeInput
		}
		identities[b.Binding] = true
		for _, ref := range b.PermittedProfiles {
			if _, ok := c.Profiles[ref]; !ok {
				return ErrRuntimeInput
			}
		}
		for _, ref := range b.Policy.PrimaryProfiles {
			if !slices.Contains(b.PermittedProfiles, ref) {
				return ErrRuntimeInput
			}
		}
		for from, targets := range b.Policy.QuotaEdges {
			if !slices.Contains(b.PermittedProfiles, from) {
				return ErrRuntimeInput
			}
			for _, target := range targets {
				if !slices.Contains(b.PermittedProfiles, target) {
					return ErrRuntimeInput
				}
			}
		}
		for provider, account := range b.Policy.PreferredAccounts {
			a, ok := c.Accounts[account]
			if !ok || a.Provider != provider {
				return ErrRuntimeInput
			}
			permitted := false
			for _, ref := range b.PermittedProfiles {
				if c.Profiles[ref].AccountRef == account {
					permitted = true
				}
			}
			if !permitted {
				return ErrRuntimeInput
			}
		}
	}
	return nil
}
