package runner_test

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apicontract "github.com/entire-vc/evc-mesh/docs/api"
)

// shape lists every field path of a schema ("policy.qa.mode", "policy.quota_edges.*"),
// with "!" marking a required one, resolving local $refs against root.
func shape(root, node map[string]any, prefix string, out map[string]bool) {
	for i := 0; i < 8; i++ {
		ref, _ := node["$ref"].(string)
		if ref == "" {
			break
		}
		var cur any = root
		for _, k := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = cur.(map[string]any)[k]
		}
		node = cur.(map[string]any)
	}
	required := map[string]bool{}
	if req, ok := node["required"].([]any); ok {
		for _, r := range req {
			required[r.(string)] = true
		}
	}
	props, _ := node["properties"].(map[string]any)
	for name, v := range props {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		mark := ""
		if required[name] {
			mark = "!"
		}
		out[path+mark] = true
		shape(root, v.(map[string]any), path, out)
	}
	if extra, ok := node["additionalProperties"].(map[string]any); ok && prefix != "" {
		out[prefix+".*"] = true
		shape(root, extra, prefix+".*", out)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The form on My Runner is built from the manifest's schemas, the server
// validates with the published contract. A field the contract has and the
// manifest lacks would be dropped by the form on the next save, so the two
// must name the same fields, nested ones and required marks included.
func TestManifestSchemaCoversTheCatalogContract(t *testing.T) {
	raw, err := os.ReadFile("mesh-extension.json")
	require.NoError(t, err)
	var manifest, contract map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	require.NoError(t, json.Unmarshal(apicontract.RuntimeSchema, &contract))

	catalog := manifest["schemas"].(map[string]any)["catalog"].(map[string]any)
	for _, section := range []string{"controllers", "accounts", "pools", "profiles", "bindings"} {
		t.Run(section, func(t *testing.T) {
			want, got := map[string]bool{}, map[string]bool{}
			entry := contract["properties"].(map[string]any)[section].(map[string]any)["additionalProperties"].(map[string]any)
			shape(contract, entry, "", want)
			mentry, ok := catalog["properties"].(map[string]any)[section].(map[string]any)["additionalProperties"].(map[string]any)
			require.True(t, ok, "manifest: %s entries have no schema", section)
			shape(manifest, mentry, "", got)
			require.Equal(t, keys(want), keys(got))
		})
	}
}

// Every field the entry form shows carries a label and a help line in English
// and Russian: the form is the only way to edit the catalog, so a bare field
// name is all an owner would have to go on.
func TestManifestEntryFieldsAreLabelledInBothLanguages(t *testing.T) {
	raw, err := os.ReadFile("mesh-extension.json")
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest))
	schemas := manifest["schemas"].(map[string]any)

	var missing []string
	var walk func(node map[string]any, prefix string)
	walk = func(node map[string]any, prefix string) {
		props, _ := node["properties"].(map[string]any)
		for name, v := range props {
			field := v.(map[string]any)
			path := prefix + "." + name
			for _, key := range []string{"title", "x-help"} {
				if s, _ := field[key].(string); s == "" {
					missing = append(missing, path+" "+key)
				}
				tr, _ := field[key+"_i18n"].(map[string]any)
				if s, _ := tr["ru"].(string); s == "" {
					missing = append(missing, path+" "+key+"_i18n.ru")
				}
			}
			walk(field, path)
		}
	}
	for _, entry := range []string{"account", "pool", "profile", "binding"} {
		walk(schemas[entry].(map[string]any), entry)
	}
	sort.Strings(missing)
	require.Empty(t, missing, "fields without an English and Russian label and help")
}
