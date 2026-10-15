package extensions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// The manifest schema is pinned by digest (the ADR), so the schema's event enum
// and the public list can only drift apart silently. Every event a manifest may
// declare must be on the public list, and every public event can be declared
// (task.gate.decided was added to the enum by the 2026-10-15 amendment).
func TestPublicEventsCoverManifestSchemaEnum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("schema", "manifest.v1.json"))
	require.NoError(t, err)
	var doc struct {
		Properties struct {
			Capabilities struct {
				Properties struct {
					Events struct {
						Items struct {
							Enum []string `json:"enum"`
						} `json:"items"`
					} `json:"events"`
				} `json:"properties"`
			} `json:"capabilities"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	enum := doc.Properties.Capabilities.Properties.Events.Items.Enum
	require.NotEmpty(t, enum, "events enum not found in manifest.v1.json")

	for _, e := range enum {
		assert.True(t, extensions.IsPublicEvent(e), "manifest event %q is missing from the public list", e)
	}
	var extra []string
	for _, e := range extensions.PublicEvents() {
		if !slices.Contains(enum, e) {
			extra = append(extra, e)
		}
	}
	assert.Empty(t, extra, "public events a manifest cannot declare")
}

func TestPublicEventsAreUniqueAndCopied(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range extensions.PublicEvents() {
		assert.False(t, seen[e], "duplicate event %q", e)
		seen[e] = true
	}
	list := extensions.PublicEvents()
	list[0] = "mutated"
	assert.NotEqual(t, "mutated", extensions.PublicEvents()[0])
	assert.False(t, extensions.IsPublicEvent("task.exploded"))
}
