// Package schema embeds the extension manifest JSON Schema so the API can
// serve it. The file itself is digest-pinned (see manifest_test.go).
package schema

import _ "embed"

//go:embed manifest.v1.json
var manifestV1 []byte

// ManifestV1 returns the manifest JSON Schema document (a copy).
func ManifestV1() []byte {
	return append([]byte(nil), manifestV1...)
}
