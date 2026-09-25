package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callback_url is validated in agentService.Update (ValidateAgentCallbackURL).
// That only protects the value if every path that writes it goes through
// there. rulesService.UpdateAgentProfile already writes an agent row through
// agentRepo.Update directly; it cannot change callback_url today only because
// domain.AgentProfileUpdate has no such field. This test makes that a checked
// property instead of an accident: it fails when a new writer of an agent row
// or of the CallbackURL field appears, so whoever adds it has to decide,
// in review, whether the write is validated.
//
// It is a syntactic check (go/ast, no type information): it matches a call
// named agentRepo.<Method> and an assignment to a field named CallbackURL.
// That is deliberate and cheap, and it is the reason for the two allowlists
// below being (file, function) pairs rather than package paths.

// agentRowWriters are the AgentRepo methods that persist callback_url.
var agentRowWriters = map[string]bool{
	"Create": true, "CreateWithHomeGrant": true, "Update": true, "RotateHomeGrantKey": true,
}

// allowedAgentRowWriteCallers: (file, enclosing function) that may call an
// agent-row writer on agentRepo. Register creates an agent that has no
// callback_url input; RotateAPIKey rewrites the key columns of a loaded row;
// Update validates; UpdateAgentProfile carries no callback field.
var allowedAgentRowWriteCallers = map[string]bool{
	"internal/service/agent_service.go:agentService.Register":           true,
	"internal/service/agent_service.go:agentService.Update":             true,
	"internal/service/agent_service.go:agentService.RotateAPIKey":       true,
	"internal/service/rules_service.go:rulesService.UpdateAgentProfile": true,
}

// allowedCallbackURLAssignFiles: non-test files that may assign the
// CallbackURL field. The handler copies the request onto an agent and then
// hands it to agentService.Update; the repository maps a row onto the struct.
var allowedCallbackURLAssignFiles = map[string]bool{
	"internal/handler/agent_handler.go":          true,
	"internal/repository/postgres/agent_repo.go": true,
}

var uniqueAgentWriters = map[string]bool{"CreateWithHomeGrant": true, "RotateHomeGrantKey": true}

// rightmostName returns the last identifier of an expression like a.b.c.
func rightmostName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

// agentRepoNames are the receiver names an agent repository goes by. Exact
// names, not a substring: "agent" also matches agentService (whose Update
// validates) and agentActivityLogRepo (a different table). A repository held
// under some other name is caught by the interface pin below, and an alias of
// agentRepo by the assignment check.
var agentRepoNames = map[string]bool{
	"agentrepo": true, "agentrepository": true, "agentsrepo": true, "agents": true,
}

func nameMentionsAgent(n string) bool { return agentRepoNames[strings.ToLower(n)] }

type callbackWriteFinding struct{ kind, where string }

// scanCallbackWrites reports, for one parsed file, every agent-row writer
// call and every CallbackURL assignment it contains. path is the
// repo-relative slash path used to key the allowlists.
func scanCallbackWrites(fset *token.FileSet, f *ast.File, path string) (rowWrites, fieldWrites []callbackWriteFinding) {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fn := funcKey(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || !agentRowWriters[sel.Sel.Name] {
					return true
				}
				// CreateWithHomeGrant / RotateHomeGrantKey are unique to the
				// agent repository, so any receiver counts. Create / Update
				// are common names, so the receiver must be named like an agent
				// repository (agentRepo, agentRepository, agents), as a field
				// or as a bare identifier.
				if uniqueAgentWriters[sel.Sel.Name] || nameMentionsAgent(rightmostName(sel.X)) {
					rowWrites = append(rowWrites, callbackWriteFinding{"agentRepo." + sel.Sel.Name, path + ":" + fn})
				}
			case *ast.AssignStmt:
				// repo := s.agentRepo hides the receiver from the call check
				// above. Aliasing the repository is itself worth a review.
				for _, rhs := range x.Rhs {
					if sel, ok := rhs.(*ast.SelectorExpr); ok && strings.EqualFold(sel.Sel.Name, "agentRepo") {
						rowWrites = append(rowWrites, callbackWriteFinding{"alias of agentRepo", path + ":" + fn})
					}
				}
				for _, lhs := range x.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "CallbackURL" {
						fieldWrites = append(fieldWrites, callbackWriteFinding{"assign CallbackURL", path + ":" + fn})
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name == "CallbackURL" {
					fieldWrites = append(fieldWrites, callbackWriteFinding{"literal CallbackURL", path + ":" + fn})
				}
			}
			return true
		})
	}
	return rowWrites, fieldWrites
}

func parseForScan(t *testing.T, path, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	require.NoError(t, err)
	return fset, f
}

// Red control for the scanner itself: a synthetic file with a new writer is
// reported. If this ever passes vacuously the invariant test below proves
// nothing.
func TestScanCallbackWrites_DetectsWriters(t *testing.T) {
	fset, f := parseForScan(t, "x.go", `package service
type svc struct{ agentRepo interface{ Update(a int) } }
func (s *svc) Sneaky(a *agent) { a.CallbackURL = "http://x"; s.agentRepo.Update(1) }
func lit() agent { return agent{CallbackURL: "http://x"} }
`)
	rows, fields := scanCallbackWrites(fset, f, "internal/service/x.go")

	assert.Equal(t, []callbackWriteFinding{{"agentRepo.Update", "internal/service/x.go:svc.Sneaky"}}, rows)

	// The shapes a name-only match would miss: an aliased repository, a bare
	// identifier receiver, a differently named field, a unique method on any
	// receiver.
	fset2, f2 := parseForScan(t, "y.go", `package service
func alias(s *svc) { r := s.agentRepo; r.Update(nil) }
func bare(agentRepo repo) { agentRepo.Update(nil) }
func renamed(s *svc) { s.agentRepository.Create(nil) }
func plural(s *svc) { s.agents.Update(nil) }
func home(x repo) { x.CreateWithHomeGrant(nil) }
func notAgent(s *svc) { s.taskRepo.Update(nil) }
`)
	rows2, _ := scanCallbackWrites(fset2, f2, "internal/service/y.go")
	var got []string
	for _, w := range rows2 {
		got = append(got, w.where+" "+w.kind)
	}
	assert.ElementsMatch(t, []string{
		"internal/service/y.go:alias alias of agentRepo",
		"internal/service/y.go:bare agentRepo.Update",
		"internal/service/y.go:renamed agentRepo.Create",
		"internal/service/y.go:plural agentRepo.Update",
		"internal/service/y.go:home agentRepo.CreateWithHomeGrant",
	}, got, "taskRepo.Update must not be flagged")
	assert.ElementsMatch(t, []callbackWriteFinding{
		{"assign CallbackURL", "internal/service/x.go:svc.Sneaky"},
		{"literal CallbackURL", "internal/service/x.go:lit"},
	}, fields)
}

func TestAgentCallbackURL_EveryWritePathIsReviewed(t *testing.T) {
	roots := []string{"../../internal", "../../cmd"}
	seenRows := map[string]bool{}
	var violations []string
	scanned := 0

	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(p), "../../"))
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, p, nil, 0)
			require.NoError(t, perr, p)
			scanned++

			rows, fields := scanCallbackWrites(fset, f, rel)
			for _, w := range rows {
				seenRows[w.where] = true
				if !allowedAgentRowWriteCallers[w.where] {
					violations = append(violations, w.kind+" called from "+w.where+
						" — validate callback_url on that path (see agentService.Update) and add it to allowedAgentRowWriteCallers")
				}
			}
			for _, w := range fields {
				file, _, _ := strings.Cut(w.where, ":")
				if !allowedCallbackURLAssignFiles[file] {
					violations = append(violations, w.kind+" in "+w.where+
						" — writes must go through agentService.Update, which validates; add the file to allowedCallbackURLAssignFiles only if it does")
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	// Positive control: the walk really covered the tree and really saw the
	// known writers. An empty result from a scan that found nothing would
	// look exactly like a clean tree.
	require.Greater(t, scanned, 100, "scan looks too small — wrong working directory?")
	for k := range allowedAgentRowWriteCallers {
		assert.True(t, seenRows[k], "allowlisted writer %s not found — renamed or removed? update the allowlist", k)
	}

	sort.Strings(violations)
	assert.Empty(t, violations)
}

// reviewedAgentRepositoryMethods pins the AgentRepository interface. A new
// method is a new way to write (or read) an agent row; the row-writer check
// above only knows the four names in agentRowWriters, so a method added under
// another name (UpdateProfile, SetCallback, ...) would be invisible to it.
// Adding one must be a conscious edit here: say whether it can persist
// callback_url and, if so, validate it.
var reviewedAgentRepositoryMethods = []string{
	"Create", "CreateWithHomeGrant", "Delete", "GetByAPIKeyPrefix", "GetByID", "GetBySlug",
	"GetSubAgentTree", "List", "ListWithProjects", "RotateHomeGrantKey",
	"SearchByPrefix", "SetAPIKeySHA256", "TouchLastSeenBatch", "Update", "UpdateHeartbeat", "UpdateStatus",
}

func TestAgentRepositoryInterface_MethodSetIsReviewed(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../repository/interfaces.go", nil, 0)
	require.NoError(t, err)

	var methods []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "AgentRepository" {
			return true
		}
		it, ok := ts.Type.(*ast.InterfaceType)
		require.True(t, ok)
		for _, m := range it.Methods.List {
			for _, name := range m.Names {
				methods = append(methods, name.Name)
			}
		}
		return false
	})
	require.NotEmpty(t, methods, "AgentRepository not found — moved or renamed? update this test")

	sort.Strings(methods)
	want := append([]string(nil), reviewedAgentRepositoryMethods...)
	sort.Strings(want)
	assert.Equal(t, want, methods,
		"AgentRepository changed — decide whether the new method can persist callback_url, "+
			"validate it if so, then update reviewedAgentRepositoryMethods (and agentRowWriters)")
}

// The repository is the only place SQL may name the callback_url column.
// A second file writing it would be a write path no Go-level check sees.
func TestAgentCallbackURL_OnlyAgentRepoSQLNamesTheColumn(t *testing.T) {
	dir := "../repository/postgres"
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	sawAgentRepo := false
	for _, p := range entries {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, nil, 0)
		require.NoError(t, perr, p)
		named := false
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "callback_url") {
				named = true
			}
			return true
		})
		base := filepath.Base(p)
		if base == "agent_repo.go" {
			sawAgentRepo = named
			continue
		}
		assert.False(t, named, "%s names callback_url in SQL — writes to it must go through agentService.Update", base)
	}
	assert.True(t, sawAgentRepo, "positive control: agent_repo.go must be seen naming callback_url")
}
