package apisource

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtures are written at test time rather than kept under testdata/: two of
// them are deliberately not valid packages, and tooling that vets every
// directory would trip over them.
var fixtures = map[string]map[string]string{
	"ok": {
		"modules.go":   "package main\n\nvar apiModules = []func(*apiDeps){\n\tzWire,\n\taRoutes,\n}\n",
		"z_wire.go":    "package main\n\nfunc zWire(deps *apiDeps) { _ = deps }\n",
		"a_routes.go":  "package main\n\nimport \"strings\"\n\nfunc aRoutes(deps *apiDeps) { _ = strings.ToUpper(\"route\") }\n",
		"main.go":      "package main\n\nfunc main() {}\n\nfunc serve(deps *apiDeps) { _ = deps }\n",
		"deps.go":      "package main\n\ntype apiDeps struct{}\n\nfunc (d *apiDeps) atExit(fn func()) { _ = fn }\n",
		"deps_test.go": "package main\n",
	},
	"nolist":  {"main.go": "package main\n\nfunc main() {}\n"},
	"missing": {"modules.go": "package main\n\nvar apiModules = []func(*apiDeps){\n\tgone,\n}\n"},
	"broken":  {"main.go": "package main\n\nfunc main() {\n"},
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	for file, src := range fixtures[name] {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(src), 0o600))
	}
	return dir
}

func base(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// The fixture names its modules against file-name order (z_wire runs before
// a_routes), so a plain sort cannot pass for run order.
func TestFiles_RunOrderThenServeThenRest(t *testing.T) {
	files, err := Files(fixture(t, "ok"))
	require.NoError(t, err)
	assert.Equal(t, []string{"z_wire.go", "a_routes.go", "main.go", "deps.go", "modules.go"}, base(files),
		"modules in apiModules order, then the file holding serve, then the rest by name; _test.go files left out")
}

func TestFiles_Errors(t *testing.T) {
	_, err := Files(fixture(t, "nolist"))
	assert.ErrorContains(t, err, "no apiModules list")

	_, err = Files(fixture(t, "missing"))
	assert.ErrorContains(t, err, "module gone has no file")

	_, err = Files(fixture(t, "broken"))
	assert.Error(t, err, "a file that does not parse is an error, not a silently shorter source")

	_, err = Read(fixture(t, "nolist"))
	assert.Error(t, err)

	_, err = Parse(token.NewFileSet(), fixture(t, "nolist"), 0)
	assert.Error(t, err)
}

func TestRead_ConcatenatesInRunOrder(t *testing.T) {
	src, err := Read(fixture(t, "ok"))
	require.NoError(t, err)
	s := string(src)
	wire := strings.Index(s, "func zWire(")
	routes := strings.Index(s, "func aRoutes(")
	serve := strings.Index(s, "func serve(")
	require.True(t, wire >= 0 && routes >= 0 && serve >= 0, "every module is in the source")
	assert.Less(t, wire, routes)
	assert.Less(t, routes, serve)
	assert.NotContains(t, s, "deps_test.go")
}

func TestParse_MergesDeclarationsWithRealPositions(t *testing.T) {
	fset := token.NewFileSet()
	file, err := Parse(fset, fixture(t, "ok"), parser.ParseComments)
	require.NoError(t, err)
	require.NotNil(t, file.Name)
	assert.Equal(t, "main", file.Name.Name)

	pos := map[string]token.Pos{}
	ast.Inspect(file, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Recv == nil {
			pos[fd.Name.Name] = fd.Pos()
		}
		return true
	})
	for _, fn := range []string{"zWire", "aRoutes", "main", "serve"} {
		require.Contains(t, pos, fn)
	}
	assert.Less(t, pos["zWire"], pos["aRoutes"], "positions follow run order")
	assert.Less(t, pos["aRoutes"], pos["serve"])
	assert.Equal(t, "a_routes.go", filepath.Base(fset.Position(pos["aRoutes"]).Filename),
		"a position resolves to the real file")
	require.Len(t, file.Imports, 1)
	assert.Equal(t, `"strings"`, file.Imports[0].Path.Value)
}

// The real API package: what the route and wiring tests read.
func TestFiles_APIPackage(t *testing.T) {
	files, err := Files(filepath.Join("..", "..", "cmd", "api"))
	require.NoError(t, err)
	names := base(files)
	require.NotEmpty(t, names)
	assert.Equal(t, "wire_storage.go", names[0], "the first module runs first")
	assert.Contains(t, names, "main.go")
	for _, n := range names {
		assert.False(t, strings.HasSuffix(n, "_test.go"), n)
	}
}
