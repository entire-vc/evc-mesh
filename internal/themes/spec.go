package themes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Error codes of the theme API.
const (
	CodeThemeInvalid      = "theme_invalid"
	CodeHashMismatch      = "theme_hash_mismatch"
	CodeSettingsInvalid   = "theme_settings_invalid"
	CodeNotInstalled      = "theme_not_installed"
	CodeAssetsUnavailable = "theme_assets_unavailable"
)

func unprocessable(code, msg string, fields map[string]string) *apierror.Error {
	return &apierror.Error{Code: http.StatusUnprocessableEntity, ErrorCode: code, Message: msg, Validation: fields}
}

// Asset is a file of the theme bundle with the digest the manifest declares.
type Asset struct {
	Path   string
	SHA256 string
}

// ContractReport is the author's report of the slot contract tests (ADR D9).
type ContractReport struct {
	SDKVersion   string            `json:"sdk_version"`
	BundleSHA256 string            `json:"bundle_sha256"`
	Passed       map[string]string `json:"passed"`
}

// Spec is the typed theme block of a manifest.
type Spec struct {
	TokensContract string                       `json:"tokens_contract"`
	Modes          []string                     `json:"modes"`
	Tokens         map[string]map[string]string `json:"tokens"`
	CSS            *struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"css"`
	Views *struct {
		Bundle string `json:"bundle"`
		SHA256 string `json:"sha256"`
	} `json:"views"`
	Slots          map[string]string `json:"slots"`
	ContractReport *ContractReport   `json:"contract_report"`
}

// CSSAsset is the stylesheet file, nil when the theme has none.
func (s *Spec) CSSAsset() *Asset {
	if s.CSS == nil {
		return nil
	}
	return &Asset{Path: s.CSS.Path, SHA256: s.CSS.SHA256}
}

// ViewsAsset is the views bundle, nil when the theme has none.
func (s *Spec) ViewsAsset() *Asset {
	if s.Views == nil {
		return nil
	}
	return &Asset{Path: s.Views.Bundle, SHA256: s.Views.SHA256}
}

// ParseSpec reads the theme block of a manifest and runs the checks the JSON
// Schema cannot express (see Check). It returns the spec only when all pass.
func ParseSpec(m extensions.Manifest) (*Spec, error) {
	if m.Kind != KindTheme {
		return nil, unprocessable(CodeThemeInvalid, "extension is not a theme", map[string]string{"kind": "must be theme"})
	}
	var s Spec
	if err := json.Unmarshal(m.Theme, &s); err != nil {
		return nil, unprocessable(CodeThemeInvalid, "theme block is not readable", map[string]string{"theme": err.Error()})
	}
	problems := s.Check()
	_, badBindings := bindings(m)
	for k, v := range badBindings {
		problems[k] = v
	}
	if len(problems) > 0 {
		return nil, unprocessable(CodeThemeInvalid, "theme does not satisfy the contract", problems)
	}
	return &s, nil
}

// Check lists what the schema cannot say: tokens belong to contract v1 and
// carry a safe value, every slot exists and its contract is served, and the
// contract report covers every declared slot and names the declared bundle.
// The result is empty when the theme is acceptable.
func (s *Spec) Check() map[string]string {
	bad := map[string]string{}
	if s.TokensContract != TokensContractV1 {
		bad["theme.tokens_contract"] = fmt.Sprintf("this Mesh serves tokens contract %q, the theme asks for %q", TokensContractV1, s.TokensContract)
	}
	declared := map[string]bool{}
	for _, mode := range s.Modes {
		declared[mode] = true
	}
	for mode, tokens := range s.Tokens {
		if !declared[mode] {
			bad["theme.tokens."+mode] = "mode is not listed in theme.modes"
		}
		for name, value := range tokens {
			key := "theme.tokens." + mode + "." + name
			switch {
			case !IsToken(name):
				bad[key] = "not a token of contract v1"
			case !checkTokenValue(name, value):
				bad[key] = fmt.Sprintf("value %q is not valid for this token", value)
			}
		}
	}
	for slot, rng := range s.Slots {
		key, ok := slotKey(slot)
		if !ok {
			bad["theme.slots."+slot] = "no such slot"
			continue
		}
		want := parseCaret(rng)
		cur, _ := parseSemver(slotContracts[key])
		if want != cur.major && want != cur.major-1 {
			bad["theme.slots."+slot] = fmt.Sprintf("this Mesh serves slot contract %s, theme asks for %s", slotContracts[key], rng)
		}
	}
	if s.Views != nil {
		s.checkReport(bad)
	}
	return bad
}

func (s *Spec) checkReport(bad map[string]string) {
	r := s.ContractReport
	if r == nil {
		bad["theme.contract_report"] = "required when the theme has views"
		return
	}
	if r.BundleSHA256 != s.Views.SHA256 {
		bad["theme.contract_report.bundle_sha256"] = "report was built for another bundle than theme.views.sha256"
	}
	for slot, rng := range s.Slots {
		got, ok := r.Passed[slot]
		if !ok {
			bad["theme.contract_report.passed."+slot] = "slot is declared but the report does not cover it"
			continue
		}
		v, vok := parseSemver(got)
		want := parseCaret(rng)
		if !vok || v.major != want {
			bad["theme.contract_report.passed."+slot] = fmt.Sprintf("passed version %s does not satisfy %s", got, rng)
		}
	}
	for slot := range r.Passed {
		if _, ok := s.Slots[slot]; !ok {
			bad["theme.contract_report.passed."+slot] = "report names a slot the theme does not declare"
		}
	}
}

// modeNames returns the declared modes in a stable order (light first).
func (s *Spec) modeNames() []string {
	out := append([]string(nil), s.Modes...)
	sort.Strings(out)
	return out
}
