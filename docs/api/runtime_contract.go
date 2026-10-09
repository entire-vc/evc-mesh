// Package apicontract embeds the published runtime contract used by the API.
package apicontract

import _ "embed"

// RuntimeSchema is the sole catalog schema; documentation and validation share it.
//
//go:embed agent-runtime.schema.json
var RuntimeSchema []byte
