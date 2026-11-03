package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCatalogSnapshot_DigestSurvivesJSONBKeyReorder(t *testing.T) {
	s := CatalogSnapshot{Version: "1", Manifest: []byte(`{"b":1,"a":{"y":2,"x":[1,2]}}`), CommitSHA: "c", ArtifactSHA256: "d", TrustLevel: "verified", Source: "catalog"}
	s.Seal()
	assert.True(t, s.Intact())

	// Postgres JSONB hands the manifest back with sorted keys and other spacing
	s.Manifest = []byte(`{ "a": {"x": [1, 2], "y": 2}, "b": 1 }`)
	assert.True(t, s.Intact(), "same manifest, different key order and spacing")
}

func TestCatalogSnapshot_AnyFieldChangeBreaksIntegrity(t *testing.T) {
	base := CatalogSnapshot{Version: "1", Manifest: []byte(`{"a":1}`), CommitSHA: "c", ArtifactSHA256: "d", TrustLevel: "verified", Source: "catalog", SupersededVersion: "2", SupersededCommit: "c2"}
	base.Seal()
	for name, mut := range map[string]func(s *CatalogSnapshot){
		"version":    func(s *CatalogSnapshot) { s.Version = "x" },
		"manifest":   func(s *CatalogSnapshot) { s.Manifest = []byte(`{"a":2}`) },
		"commit":     func(s *CatalogSnapshot) { s.CommitSHA = "x" },
		"digest":     func(s *CatalogSnapshot) { s.ArtifactSHA256 = "x" },
		"trust":      func(s *CatalogSnapshot) { s.TrustLevel = "x" },
		"source":     func(s *CatalogSnapshot) { s.Source = "repo" },
		"superseded": func(s *CatalogSnapshot) { s.SupersededVersion = "x" },
		"sup commit": func(s *CatalogSnapshot) { s.SupersededCommit = "x" },
		"unsealed":   func(s *CatalogSnapshot) { s.Digest = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			mut(&s)
			assert.False(t, s.Intact())
		})
	}
}

func TestCatalogSnapshot_UnparseableManifestStillHashes(t *testing.T) {
	s := CatalogSnapshot{Version: "1", Manifest: []byte(`not json`)}
	s.Seal()
	assert.True(t, s.Intact())
	s.Manifest = []byte(`not json!`)
	assert.False(t, s.Intact())
}
