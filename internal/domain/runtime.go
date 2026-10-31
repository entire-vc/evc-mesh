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
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	apicontract "github.com/entire-vc/evc-mesh/docs/api"
)

// IntegrationProviderAgentRuntime is the legacy provider name of the runtime
// catalog row; IntegrationProviderRunner is the extension id it is renamed to.
// Until the rename migration has run both names are accepted.
const (
	IntegrationProviderAgentRuntime IntegrationProvider = "agent_runtime"
	IntegrationProviderRunner       IntegrationProvider = "runner"
)

// IsRuntimeProvider reports whether p names the runtime catalog connection
// under either of its names.
func IsRuntimeProvider(p IntegrationProvider) bool {
	return p == IntegrationProviderRunner || p == IntegrationProviderAgentRuntime
}

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
	// Version is the controller's own build version. Optional: a controller
	// that does not send it is shown with the catalog's version.
	Version string `json:"version,omitempty"`
	// Excluded is written by the server, never taken from the controller: the
	// bindings of this controller that strict workspace isolation leaves out.
	Excluded []RuntimeExclusion `json:"excluded,omitempty"`
}

// Reasons a binding is left out under strict workspace isolation.
const (
	RuntimeExcludedForeignWorkspace    = "foreign_workspace"
	RuntimeExcludedRunnerOff           = "runner_off"
	RuntimeExcludedProgramNotConnected = "program_not_connected"
)

// RuntimeExclusion is one binding the Runner does not run, and why.
type RuntimeExclusion struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
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

// RunnerExtensionID is the installed extension that every catalog controller
// runs. Its Installed version is the one version source for controllers:
// Update moves it, and the catalog entry's own version is not consulted.
const RunnerExtensionID = "agent-runner"

// WithControllerVersion sets every controller's version to v, the Installed
// version of the runner extension. An empty v (not installed) keeps the
// catalog's own entries.
func (c *RuntimeCatalog) WithControllerVersion(v string) {
	if c == nil || v == "" {
		return
	}
	for ref, ctrl := range c.Controllers {
		ctrl.Version = v
		c.Controllers[ref] = ctrl
	}
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

var runtimeVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)

// ValidRuntimeVersion reports whether value is a plausible build version
// ("0.1.3", "1.2.0-rc.1+abc"): no spaces, at most 64 characters.
func ValidRuntimeVersion(value string) bool { return runtimeVersionPattern.MatchString(value) }

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

// RuntimeFieldErrors names what is wrong with a catalog, field by field: a
// dotted path from the catalog root ("pools.<name>.max_concurrency") mapped to
// a stable reason code (see the RuntimeField* constants). It wraps
// ErrRuntimeInput, so callers that only need "invalid" keep working. Codes, not
// values: a submitted value (possibly a pasted secret) is never echoed.
type RuntimeFieldErrors map[string]string

const (
	RuntimeFieldUnknown   = "unknown_field"
	RuntimeFieldRequired  = "required"
	RuntimeFieldInvalid   = "invalid"
	RuntimeFieldMissing   = "missing_reference"
	RuntimeFieldProvider  = "provider_mismatch"
	RuntimeFieldDuplicate = "duplicate"
	RuntimeFieldNoMode    = "mode_without_pools"
	RuntimeFieldCapable   = "capability_not_on_controller"
	RuntimeFieldPermitted = "not_permitted"
)

func (e RuntimeFieldErrors) Error() string { return ErrRuntimeInput.Error() }
func (e RuntimeFieldErrors) Unwrap() error { return ErrRuntimeInput }

func (e RuntimeFieldErrors) add(code string, path ...string) {
	key := ""
	for i, p := range path {
		if i > 0 {
			key += "."
		}
		key += p
	}
	if _, seen := e[key]; !seen {
		e[key] = code
	}
}

func (e RuntimeFieldErrors) orNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// schemaFieldErrors flattens a schema validation failure to its leaves.
func schemaFieldErrors(err error) RuntimeFieldErrors {
	out := RuntimeFieldErrors{}
	var walk func(v *jsonschema.ValidationError)
	walk = func(v *jsonschema.ValidationError) {
		if len(v.Causes) > 0 {
			for _, c := range v.Causes {
				walk(c)
			}
			return
		}
		at := v.InstanceLocation
		switch k := v.ErrorKind.(type) {
		case *kind.AdditionalProperties:
			for _, name := range k.Properties {
				out.add(RuntimeFieldUnknown, append(slices.Clone(at), name)...)
			}
		case *kind.Required:
			for _, name := range k.Missing {
				out.add(RuntimeFieldRequired, append(slices.Clone(at), name)...)
			}
		default:
			out.add(RuntimeFieldInvalid, at...)
		}
	}
	var v *jsonschema.ValidationError
	if errors.As(err, &v) {
		walk(v)
	}
	if len(out) == 0 {
		out.add(RuntimeFieldInvalid)
	}
	return out
}

// ParseRuntimeCatalog decodes and validates a catalog. A well-formed JSON
// document that breaks the schema or a reference rule fails with
// RuntimeFieldErrors; anything else with plain ErrRuntimeInput.
func ParseRuntimeCatalog(data []byte) (*RuntimeCatalog, error) {
	if len(data) > 1<<20 {
		return nil, ErrRuntimeInput
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := runtimeJSONValue(d, 0); err != nil {
		return nil, ErrRuntimeInput
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrRuntimeInput
	}
	// The schema runs first: it names the field (an unknown key, a missing one),
	// where the strict decoder below only says "invalid".
	schema, err := catalogSchema()
	if err != nil {
		return nil, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, ErrRuntimeInput
	}
	if err = schema.Validate(value); err != nil {
		return nil, schemaFieldErrors(err)
	}
	var catalog RuntimeCatalog
	if err := DecodeRuntimeJSON(data, &catalog); err != nil {
		return nil, err
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

// ValidateReferences checks what the schema cannot: that every reference
// names an existing resource of the right provider. Every offending field is
// reported (RuntimeFieldErrors), not only the first.
func (c *RuntimeCatalog) ValidateReferences() error {
	errs := RuntimeFieldErrors{}
	aliases, physical := map[string]bool{}, map[string]bool{}
	for _, ref := range sortedKeys(c.Pools) {
		p := c.Pools[ref]
		key := p.Provider + "\x00" + p.ResourceRef
		if physical[key] {
			errs.add(RuntimeFieldDuplicate, "pools", ref, "resource_ref")
		}
		physical[key] = true
		for _, a := range p.Aliases {
			_, named := c.Pools[a]
			if a == ref || named || aliases[a] {
				errs.add(RuntimeFieldDuplicate, "pools", ref, "aliases")
			}
			aliases[a] = true
		}
	}
	for name, a := range c.Accounts {
		for mode, refs := range a.QuotaPoolsByMode {
			for _, ref := range refs {
				pool, ok := c.ResolvePool(ref)
				if !ok {
					errs.add(RuntimeFieldMissing, "accounts", name, "quota_pools_by_mode", mode)
				} else if c.Pools[pool].Provider != a.Provider {
					errs.add(RuntimeFieldProvider, "accounts", name, "quota_pools_by_mode", mode)
				}
			}
		}
	}
	for name, p := range c.Profiles {
		controller, ok := c.Controllers[p.ControllerRef]
		if !ok {
			errs.add(RuntimeFieldMissing, "profiles", name, "controller_ref")
		}
		account, aok := c.Accounts[p.AccountRef]
		if !aok {
			errs.add(RuntimeFieldMissing, "profiles", name, "account_ref")
		} else if len(account.QuotaPoolsByMode[p.ExecutionMode]) == 0 {
			errs.add(RuntimeFieldNoMode, "profiles", name, "execution_mode")
		}
		for _, cap := range p.Capabilities {
			if ok && !slices.Contains(controller.Capabilities, cap) {
				errs.add(RuntimeFieldCapable, "profiles", name, "capabilities")
			}
		}
	}
	identities := map[RuntimeIdentity]bool{}
	for _, name := range sortedKeys(c.Bindings) {
		b := c.Bindings[name]
		if identities[b.Binding] {
			errs.add(RuntimeFieldDuplicate, "bindings", name, "binding")
		}
		identities[b.Binding] = true
		for _, ref := range b.PermittedProfiles {
			if _, ok := c.Profiles[ref]; !ok {
				errs.add(RuntimeFieldMissing, "bindings", name, "permitted_profiles")
			}
		}
		for _, ref := range b.Policy.PrimaryProfiles {
			if !slices.Contains(b.PermittedProfiles, ref) {
				errs.add(RuntimeFieldPermitted, "bindings", name, "policy", "primary_profiles")
			}
		}
		for from, targets := range b.Policy.QuotaEdges {
			if !slices.Contains(b.PermittedProfiles, from) {
				errs.add(RuntimeFieldPermitted, "bindings", name, "policy", "quota_edges")
			}
			for _, target := range targets {
				if !slices.Contains(b.PermittedProfiles, target) {
					errs.add(RuntimeFieldPermitted, "bindings", name, "policy", "quota_edges")
				}
			}
		}
		for provider, account := range b.Policy.PreferredAccounts {
			a, ok := c.Accounts[account]
			if !ok {
				errs.add(RuntimeFieldMissing, "bindings", name, "policy", "preferred_accounts")
				continue
			}
			if a.Provider != provider {
				errs.add(RuntimeFieldProvider, "bindings", name, "policy", "preferred_accounts")
			}
			permitted := false
			for _, ref := range b.PermittedProfiles {
				if c.Profiles[ref].AccountRef == account {
					permitted = true
				}
			}
			if !permitted {
				errs.add(RuntimeFieldPermitted, "bindings", name, "policy", "preferred_accounts")
			}
		}
	}
	return errs.orNil()
}

// sortedKeys orders checks whose outcome depends on visiting order (the
// second of two duplicates is the one reported), so the same catalog always
// names the same field.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// RuntimeDesired is one controller's slice of the desired catalog. It holds
// only what that controller executes: its own profiles, the accounts those
// profiles use (with credential_ref, a reference and never a secret value),
// the pools those accounts draw from, and the bindings that may run on it.
type RuntimeDesired struct {
	Revision       int64                 `json:"revision"`
	Digest         string                `json:"digest"`
	Enabled        bool                  `json:"enabled"`
	DrainRequested bool                  `json:"drain_requested"`
	ControllerRef  string                `json:"controller_ref"`
	Controller     RuntimeController     `json:"controller"`
	Catalog        RuntimeDesiredCatalog `json:"catalog"`
	Excluded       []RuntimeExclusion    `json:"excluded,omitempty"`
}

type RuntimeDesiredCatalog struct {
	Accounts map[string]RuntimeAccount `json:"accounts"`
	Pools    map[string]RuntimePool    `json:"pools"`
	Profiles map[string]RuntimeProfile `json:"profiles"`
	Bindings map[string]RuntimeBinding `json:"bindings"`
}

// DesiredFor projects the catalog onto one controller. A binding is included
// when it permits at least one profile of this controller; the references it
// carries to profiles or accounts of other controllers are dropped so nothing
// about a sibling controller leaks through policy.
func (c *RuntimeCatalog) DesiredFor(ref string) (RuntimeDesiredCatalog, bool) {
	if _, ok := c.Controllers[ref]; !ok {
		return RuntimeDesiredCatalog{}, false
	}
	out := RuntimeDesiredCatalog{
		Accounts: map[string]RuntimeAccount{},
		Pools:    map[string]RuntimePool{},
		Profiles: map[string]RuntimeProfile{},
		Bindings: map[string]RuntimeBinding{},
	}
	for name, p := range c.Profiles {
		if p.ControllerRef != ref {
			continue
		}
		out.Profiles[name] = p
		account, ok := c.Accounts[p.AccountRef]
		if !ok {
			continue
		}
		// Only the modes this controller's profiles run in: the account's
		// pool lists for other modes belong to nobody here.
		narrowed, seen := out.Accounts[p.AccountRef]
		if !seen {
			narrowed = account
			narrowed.QuotaPoolsByMode = map[string][]string{}
		}
		narrowed.QuotaPoolsByMode[p.ExecutionMode] = account.QuotaPoolsByMode[p.ExecutionMode]
		out.Accounts[p.AccountRef] = narrowed
		for _, pool := range account.QuotaPoolsByMode[p.ExecutionMode] {
			if canonical, found := c.ResolvePool(pool); found {
				out.Pools[canonical] = c.Pools[canonical]
			}
		}
	}
	own := func(profile string) bool { _, ok := out.Profiles[profile]; return ok }
	keep := func(in []string) []string {
		kept := []string{}
		for _, v := range in {
			if own(v) {
				kept = append(kept, v)
			}
		}
		return kept
	}
	for name, b := range c.Bindings {
		permitted := keep(b.PermittedProfiles)
		if len(permitted) == 0 {
			continue
		}
		b.PermittedProfiles = permitted
		b.Policy.PrimaryProfiles = keep(b.Policy.PrimaryProfiles)
		edges := map[string][]string{}
		for from, to := range b.Policy.QuotaEdges {
			if own(from) {
				edges[from] = keep(to)
			}
		}
		b.Policy.QuotaEdges = edges
		preferred := map[string]string{}
		for provider, account := range b.Policy.PreferredAccounts {
			if _, ok := out.Accounts[account]; ok {
				preferred[provider] = account
			}
		}
		b.Policy.PreferredAccounts = preferred
		providers := []string{}
		for _, provider := range b.Policy.APIReserveProviders {
			for _, account := range out.Accounts {
				if account.Provider == provider {
					providers = append(providers, provider)
					break
				}
			}
		}
		b.Policy.APIReserveProviders = providers
		out.Bindings[name] = b
	}
	return out, true
}
