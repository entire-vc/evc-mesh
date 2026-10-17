package repoinstall

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/schema"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Stable error codes the interface translates.
const (
	CodeManifestInvalid = "extension_manifest_invalid"
	CodeBuiltinRuntime  = "extension_builtin_runtime"
	CodeIncompatible    = "extension_incompatible"
	CodeIDConflict      = "extension_id_conflict"
	CodeRepoUnavailable = "extension_repo_unavailable"
	CodeConsentRequired = "consent_required"
)

func apiErr(status int, code, msg string) *apierror.Error {
	return &apierror.Error{Code: status, ErrorCode: code, Message: msg}
}

var (
	schemaOnce sync.Once
	schemaVal  *jsonschema.Schema
	schemaErr  error
)

func manifestSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.ManifestV1()))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err = c.AddResource("manifest.v1.json", doc); err != nil {
			schemaErr = err
			return
		}
		schemaVal, schemaErr = c.Compile("manifest.v1.json")
	})
	return schemaVal, schemaErr
}

// ValidateManifest parses and checks a manifest read from a repository: JSON
// Schema, not builtin, compatible with this Mesh's extension API version.
func ValidateManifest(raw []byte) (extensions.Manifest, error) {
	m, err := extensions.ParseManifest(raw)
	if err != nil {
		return m, apiErr(400, CodeManifestInvalid, "mesh-extension.json is not a valid manifest: "+err.Error())
	}
	if m.Runtime == "builtin" {
		return m, apiErr(400, CodeBuiltinRuntime, "builtin extensions ship with Mesh and cannot be installed from a repository")
	}
	sch, err := manifestSchema()
	if err != nil {
		return m, fmt.Errorf("compile manifest schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return m, apiErr(400, CodeManifestInvalid, "mesh-extension.json is not valid JSON")
	}
	if verr := sch.Validate(inst); verr != nil {
		return m, apiErr(400, CodeManifestInvalid, "mesh-extension.json does not match the manifest schema: "+firstLine(verr.Error()))
	}
	if !MeshAPICompatible(m.MeshAPI, extensions.ExtensionsAPIVersion) {
		return m, apiErr(400, CodeIncompatible, fmt.Sprintf("incompatible: extension needs mesh_api %q, this Mesh provides %d", m.MeshAPI, extensions.ExtensionsAPIVersion))
	}
	return m, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// MeshAPICompatible reports whether version satisfies expr: space-separated
// comparators (>=, <=, >, <, =, bare = equal), all of which must hold.
func MeshAPICompatible(expr string, version int) bool {
	fields := strings.Fields(expr)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		op := "="
		for _, p := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(f, p) {
				op, f = p, strings.TrimPrefix(f, p)
				break
			}
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return false
		}
		ok := false
		switch op {
		case ">=":
			ok = version >= n
		case "<=":
			ok = version <= n
		case ">":
			ok = version > n
		case "<":
			ok = version < n
		default:
			ok = version == n
		}
		if !ok {
			return false
		}
	}
	return true
}

// Capabilities flattens the declared permissions into "<group>:<value>" strings,
// the same shape as consented_capabilities.
func Capabilities(m extensions.Manifest) []string {
	var out []string
	add := func(group string, vals []string) {
		for _, v := range vals {
			out = append(out, group+":"+v)
		}
	}
	add("api", m.Capabilities.API)
	add("events", m.Capabilities.Events)
	add("webhooks_in", m.Capabilities.WebhooksIn)
	add("ui", m.Capabilities.UI)
	add("secrets", m.Capabilities.Secrets)
	sort.Strings(out)
	return out
}

// NewCapabilities returns what next asks for beyond prev.
func NewCapabilities(prev, next []string) []string {
	have := make(map[string]bool, len(prev))
	for _, p := range prev {
		have[p] = true
	}
	out := []string{}
	for _, n := range next {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}

// semver is a parsed vX.Y.Z tag; pre-release tags sort below the release.
type semver struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(tag string) (semver, bool) {
	t := strings.TrimPrefix(tag, "v")
	pre := ""
	if i := strings.IndexByte(t, '-'); i >= 0 {
		t, pre = t[:i], t[i+1:]
	}
	parts := strings.Split(t, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return semver{}, false
		}
		n[i] = v
	}
	return semver{n[0], n[1], n[2], pre}, true
}

// less reports a < b.
func (a semver) less(b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	if a.patch != b.patch {
		return a.patch < b.patch
	}
	if a.pre == b.pre {
		return false
	}
	if a.pre == "" {
		return false
	}
	if b.pre == "" {
		return true
	}
	return a.pre < b.pre
}

// NewestTag returns the highest semver tag, if any.
func NewestTag(tags []Ref) (Ref, bool) {
	var best Ref
	var bv semver
	found := false
	for _, t := range tags {
		v, ok := parseSemver(t.Name)
		if !ok {
			continue
		}
		if !found || bv.less(v) {
			best, bv, found = t, v, true
		}
	}
	return best, found
}

// IsNewerTag reports whether tag a is a higher semver than b.
func IsNewerTag(a, b string) bool {
	av, aok := parseSemver(a)
	bv, bok := parseSemver(b)
	return aok && bok && bv.less(av)
}
