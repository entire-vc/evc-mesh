package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The probe handler must not be able to reach a secret value. This reads the
// real source of keep_metadata.go (comments are not code) and fails if it names
// anything that reads, writes or materializes a value, or the wide store types.
func TestKeepMetadataHandlerNamesNoValuePath(t *testing.T) {
	forbidden := map[string]bool{
		"GetValue": true, "PutValue": true, "Resolve": true, "Open": true, "Put": true,
		"Materialize": true, "ResolveCurrentValues": true, "SecretStore": true, "Store": true,
		"SecretMaterializer": true, "SecretService": true, "KeepClient": true, "HTTPKeepClient": true,
	}
	f, err := parser.ParseFile(token.NewFileSet(), "keep_metadata.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && forbidden[id.Name] {
			t.Errorf("keep_metadata.go names %q: the probe must not touch a value path", id.Name)
		}
		return true
	})
}
