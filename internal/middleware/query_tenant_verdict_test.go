package middleware

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers the third channel a tenant id arrives through: the query
// string.
//
// The first two are already closed. TestEveryIdentifiedRouteIsWorkspaceScoped
// reads the router and requires every path parameter to be one the middleware
// resolves or contains. TestBodyTenantFieldsAreDeclared parses the handler
// package and requires every tenant-shaped field in a request body to carry a
// written verdict. Between them they cover /workspaces/:ws_id/... and
// {"workspace_id": ...}.
//
// ?workspace_id= is neither. RequireWorkspaceMemberScoped resolves nothing from
// /memories or /me/tasks, so those routes look exactly like /auth/me to
// everything upstream of the handler, and the body invariant's AST walk only
// inspects json tags. Nothing in the package sees the query string at all.
//
// Nothing is leaking through it today: every site below was read down to the SQL
// and is either authorized in the handler or can only add a conjunct to a query
// already pinned by something the caller does not control. This file exists so
// that the twentieth site is not joined by a twenty-first that nobody read.

// -----------------------------------------------------------------------------
// What the verdicts have to say
// -----------------------------------------------------------------------------

// The verdict grammar here is deliberately not the body channel's.
//
// That one accepts "guarded:", "checked:" and "response:" and rejects "benign:",
// which was the right correction for a workspace_id in a body — it IS the tenant,
// so there is no benign reading of one. A query parameter is different: most of
// these are a project_id or task_id narrowing a list, and "narrows the result set"
// really is the whole story for them. Banning the word would only push the same
// sentence into a "checked:" that checks nothing.
//
// So the requirement is not a vocabulary. It is that the verdict names something
// this test can go and read:
//
//	checked:  <Type>.<method>                     — a method that must exist in the handler package
//	narrows:  <conjunct> — pinned by <predicate>  — both must appear verbatim under internal/,
//	                                                and the route must not be able to write
//	keyed:    <symbol> — pinned by <predicate>    — for a value stored in the key of something
//	                                                durable, naming what in that key pins the tenant
//
// The "must not be able to write" clause is the load-bearing one, and it is there
// because of what happened on the body channel. updatePreferencesRequest.
// workspace_id was declared:
//
//	"benign: writes a preference row keyed on (workspace_id, the caller's own user_id)"
//
// — a true sentence with the wrong conclusion. Read across the response the
// request really is harmless; what it leaves behind is a subscription, and the
// notification dispatcher reads that subscription afterwards and posts the
// workspace's comment bodies to whoever the row names. The consumer was in
// another subsystem and the payload arrived later, so "returns nothing" was never
// the question to ask.
//
// Asking a reviewer to also write down "and who else reads this" does not fix
// that. The entry above would have been re-typed as "nothing else reads it, it is
// just a preference row", and a mechanism that only requires a sentence cannot
// tell that from the truth — which is the whole complaint against the previous
// one. What it can tell is whether a later consumer is possible at all: a route
// that cannot write leaves nothing behind for another subsystem to read. That is
// a property of the router, and this test reads the router.
//
// A GET that writes anyway does not get to hide behind its method — see
// queryTenantWritingReads.

// queryVerdictPrefixes are the three available verdicts. "benign:" is not among
// them, for the same reason as on the body channel: it states a conclusion instead
// of naming the thing that makes the conclusion true.
var queryVerdictPrefixes = []string{"checked:", "narrows:", "keyed:"}

// checkedVerdict requires a "checked:" verdict to name a method this test can find.
var checkedVerdict = regexp.MustCompile(`^checked: ([A-Za-z]\w*)\.(\w+)$`)

// pinnedVerdict requires "narrows:" and "keyed:" to name both halves: what the
// caller's value adds, and the thing it is added to that the caller does not
// control. A conjunct with no pin is not a verdict, it is a description.
var pinnedVerdict = regexp.MustCompile(`^(?:narrows|keyed): (.+?) — pinned by (.+)$`)

// writingMethods are the HTTP methods that can leave something behind for another
// subsystem to read later.
var writingMethods = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// declaredQueryTenantParams is the reviewed verdict on every tenant-identifying id
// that arrives in the query string. The key is "<file>:<owner>.<param>", where the
// owner is the handler function that reads it or the struct whose tag binds it.
//
// Every entry below was arrived at by reading the call down to the SQL, not by
// reading the handler. The interesting half of that reading is which term is the
// pin and which is the narrowing one, and it is not always the one the name
// suggests — see GetCurrentUserTasks.
var declaredQueryTenantParams = map[string]string{
	// --- Authorized in the handler -------------------------------------------
	//
	// requireWorkspaceID is the only tenant boundary in front of memories: that
	// table carries no RLS policy, so nothing downstream would catch a workspace
	// the caller was never entitled to. It honours a client-supplied workspace_id
	// only after workspaceAllowed proves access to it, and it answers for an agent
	// key as well as a user — the case rbac() cannot, since rbac() short-circuits
	// to a static capability map and never looks at the target workspace.
	"memory_handler.go:listMemoriesQuery.workspace_id":   "checked: MemoryHandler.requireWorkspaceID",
	"memory_handler.go:searchMemoriesQuery.workspace_id": "checked: MemoryHandler.requireWorkspaceID",
	"memory_handler.go:recallGraphQuery.workspace_id":    "checked: MemoryHandler.requireWorkspaceID",
	"memory_handler.go:ExportMemories.workspace_id":      "checked: MemoryHandler.requireWorkspaceID",
	"memory_handler.go:ImportMemories.workspace_id":      "checked: MemoryHandler.requireWorkspaceID",
	"memory_handler.go:Reindex.workspace_id":             "checked: MemoryHandler.requireWorkspaceID",
	"memory_handler.go:BackfillChunks.workspace_id":      "checked: MemoryHandler.requireWorkspaceID",

	// --- Conjuncts on a query pinned elsewhere -------------------------------
	//
	// A project_id inside an already-checked workspace can only make the answer
	// smaller: the workspace predicate stays in the WHERE clause either way, so a
	// foreign project id yields an empty set rather than another tenant's rows.
	"memory_handler.go:listMemoriesQuery.project_id":   "narrows: project_id = $%d — pinned by workspace_id = $1",
	"memory_handler.go:searchMemoriesQuery.project_id": "narrows: project_id = $%d — pinned by workspace_id = $1",
	"memory_handler.go:ExportMemories.project_id":      "narrows: project_id = $%d — pinned by workspace_id = $1",

	// GET /workspaces/:ws_id/analytics — the workspace comes from the path, where
	// WorkspaceRLS resolves and checks it; project_id only adds a conjunct.
	"analytics_handler.go:GetMetrics.project_id":    "narrows: AND t.project_id = $2 — pinned by WHERE p.workspace_id = $1",
	"analytics_handler.go:ExportMetrics.project_id": "narrows: AND t.project_id = $2 — pinned by WHERE p.workspace_id = $1",

	// GET /me/comments — the pin is authorship, not tenancy. Both filters below
	// narrow a set that already contains only rows the caller wrote, so passing a
	// foreign workspace_id returns the caller's own comments in it, if they have
	// any, and nothing otherwise.
	"comment_handler.go:GetMyComments.workspace_id": "narrows: AND p.workspace_id = $%d — pinned by c.author_id = $1",
	"comment_handler.go:GetMyComments.project_id":   "narrows: AND t.project_id = $%d — pinned by c.author_id = $1",

	// GET /me/mentions — same shape, pinned to the mention's recipient.
	"mention_handler.go:List.project_id": "narrows: AND t.project_id = $ — pinned by cm.mentioned_id = $1",

	// GET /projects/:proj_id/events — pinned by the path parameter, which projAccess
	// and WorkspaceRLS have both already resolved.
	"event_handler.go:listEventsQuery.agent_id": "narrows: e.agent_id = $%d — pinned by e.project_id = $1",
	"event_handler.go:listEventsQuery.task_id":  "narrows: e.task_id = $%d — pinned by e.project_id = $1",

	// GET /me/tasks is the one worth reading twice. Nothing authorizes the
	// workspace_id here — it goes straight from the query string into the SQL —
	// and the reason that is sound is the reverse of everywhere else above: the
	// caller-supplied workspace is the NARROWING term and the pin is the assignee,
	// which the handler takes from the auth context and the caller cannot set.
	// A stranger's workspace id returns the caller's own tasks in it, which is
	// nothing unless they are a member. Read in the other direction — "workspace_id
	// is unchecked here" — this looks like the hole; it is not, and the distinction
	// is which of the two predicates the caller controls.
	"task_handler.go:GetCurrentUserTasks.workspace_id": "narrows: WHERE p.workspace_id = $1 — pinned by AND t.assignee_id = $2",

	// --- Stored in a key -----------------------------------------------------
	//
	// GET /memories/recall_graph writes: results are memoised in a package-level
	// sync.Map before the response is written. The method is GET, so the rule that
	// carries every entry above — a route that cannot write has no later consumer —
	// does not apply, and saying "it's a GET" here would be the same wrong
	// conclusion as "it returns nothing" was on the body channel. What makes it
	// sound is inside the key: recallGraphCacheKey hashes opts.WorkspaceID together
	// with the query, so a caller in another workspace addresses a different bucket
	// and can neither read nor poison this one. project_id and task_id vary the key
	// within a workspace that requireWorkspaceID has already checked.
	"memory_handler.go:recallGraphQuery.project_id": "keyed: recallGraphCacheKey — pinned by opts.WorkspaceID.String()",
	"memory_handler.go:recallGraphQuery.task_id":    "keyed: recallGraphCacheKey — pinned by opts.WorkspaceID.String()",
}

// -----------------------------------------------------------------------------
// Detection
// -----------------------------------------------------------------------------

// queryTenantParams returns every "<file>:<owner>.<param>" in the handler package
// where a tenant-identifying id is read out of the query string.
//
// Both spellings are read, because the channel has two and a grep sees one. The
// eleven c.QueryParam("workspace_id") calls are the visible half; the other nine
// arrive through echo's struct binding — a `query:"workspace_id"` tag on a struct
// passed to c.Bind — and those handlers contain no QueryParam call to find. The
// enumeration this file was written from was made with a grep and missed all nine,
// in memory_handler.go and event_handler.go, for exactly that reason. It is the
// same failure the body invariant already documents: the shape that hides best
// from a grep hides best from review.
//
// The tenant-shaped names come from bodyTenantFieldNames rather than a second list
// of the same words. Two copies would drift, and the first sign of the drift would
// be a channel quietly not checking a name the other one does.
func queryTenantParams(t *testing.T) []string {
	t.Helper()
	return queryTenantParamsIn(t, filepath.Join("..", "handler"))
}

func queryTenantParamsIn(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "cannot read %s", dir)

	fset := token.NewFileSet()
	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, parseErr, "cannot parse %s", name)

		var enclosing []string
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				enclosing = append(enclosing, node.Name.Name)
			case *ast.FuncDecl:
				enclosing = append(enclosing, node.Name.Name)

			case *ast.CallExpr:
				// The direct spelling: a QueryParam call with a literal name.
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "QueryParam" || len(node.Args) != 1 {
					return true
				}
				lit, isLit := node.Args[0].(*ast.BasicLit)
				if !isLit || lit.Kind != token.STRING {
					return true
				}
				param, unquoteErr := strconv.Unquote(lit.Value)
				if unquoteErr != nil || !bodyTenantFieldNames[param] {
					return true
				}
				found = append(found, fmt.Sprintf("%s:%s.%s", name, innermostName(enclosing), param))

			case *ast.StructType:
				// Fields carrying `query:"workspace_id"`, bound by c.Bind.
				for _, f := range node.Fields.List {
					if f.Tag == nil {
						continue
					}
					tag, unquoteErr := strconv.Unquote(f.Tag.Value)
					if unquoteErr != nil {
						continue
					}
					param, _, _ := strings.Cut(reflect.StructTag(tag).Get("query"), ",")
					if bodyTenantFieldNames[param] {
						found = append(found, fmt.Sprintf("%s:%s.%s", name, innermostName(enclosing), param))
					}
				}
			}
			return true
		})
	}
	sort.Strings(found)
	return dedupeStrings(found)
}

func innermostName(enclosing []string) string {
	if len(enclosing) == 0 {
		return "anonymous"
	}
	return enclosing[len(enclosing)-1]
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// Detection invariant
// -----------------------------------------------------------------------------

// TestQueryTenantParamsAreDeclared is the detection half: a new tenant-shaped
// query parameter fails until somebody writes down what makes it safe.
func TestQueryTenantParamsAreDeclared(t *testing.T) {
	found := queryTenantParams(t)
	require.NotEmpty(t, found,
		"no query-string tenant parameters discovered — the handler style has probably changed "+
			"and this test is no longer reading anything")

	var undeclared []string
	for _, f := range found {
		if _, ok := declaredQueryTenantParams[f]; !ok {
			undeclared = append(undeclared, f)
		}
	}
	sort.Strings(undeclared)
	assert.Empty(t, undeclared,
		"these handlers take a tenant-identifying id out of the query string, where neither the "+
			"route parameters the workspace guard reads nor the body invariant's json tags can see "+
			"it. Authorize the caller against it, or establish that it can only narrow a query "+
			"already pinned to something the caller does not control, then record it in "+
			"declaredQueryTenantParams:\n  %s",
		strings.Join(undeclared, "\n  "))

	present := make(map[string]bool, len(found))
	for _, f := range found {
		present[f] = true
	}
	for f := range declaredQueryTenantParams {
		assert.True(t, present[f],
			"%s is declared but no longer exists — a stale entry silently excuses the next "+
				"parameter that takes its name", f)
	}
}

// -----------------------------------------------------------------------------
// Verdict invariants
// -----------------------------------------------------------------------------

// TestQueryTenantVerdictsNameSomethingReadable is what separates this from the
// mechanism that let the notification-preferences hole through: every verdict has
// to name a symbol that exists, so writing one requires having read the code.
//
// A "checked:" verdict names a method, and the method has to be in the handler
// package. A "narrows:" or "keyed:" verdict names both the conjunct the caller's
// value adds and the predicate it is added to, and both have to appear verbatim
// somewhere under internal/ — which is where the queries live, so the only way to
// write one is to have opened the query.
//
// It follows that changing a query breaks the verdicts that quote it. That is the
// intended cost: the moment a WHERE clause moves is exactly the moment somebody
// should re-read whether the conjunct still only narrows.
func TestQueryTenantVerdictsNameSomethingReadable(t *testing.T) {
	methods := handlerMethodNames(t)
	sources := internalSources(t)

	for _, field := range sortedKeys(declaredQueryTenantParams) {
		verdict := declaredQueryTenantParams[field]

		require.True(t, hasAnyPrefix(verdict, queryVerdictPrefixes),
			"%s carries a verdict that is none of %v: %q.\n"+
				"\"benign\" and its synonyms are not available here — say what makes it safe, not "+
				"that it is", field, queryVerdictPrefixes, verdict)

		if m := checkedVerdict.FindStringSubmatch(verdict); m != nil {
			require.True(t, methods[m[1]+"."+m[2]],
				"%s is declared checked by %s.%s, but the handler package has no such method",
				field, m[1], m[2])
			continue
		}
		if strings.HasPrefix(verdict, "checked:") {
			require.Fail(t, "unreadable checked verdict",
				"%s claims a handler check but does not name it as <Type>.<method>: %q", field, verdict)
		}

		m := pinnedVerdict.FindStringSubmatch(verdict)
		require.NotNil(t, m,
			"%s does not name both halves. Write it as \"<conjunct> — pinned by <predicate>\", "+
				"where the pin is the term the caller does NOT control: %q", field, verdict)

		conjunct, pin := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		require.NotEqual(t, conjunct, pin,
			"%s pins its conjunct with itself, which establishes nothing: %q", field, verdict)
		assert.True(t, strings.Contains(sources, conjunct),
			"%s says the caller's value adds %q, but that appears nowhere under internal/ — "+
				"either the query moved and the verdict needs re-reading, or it was never read",
			field, conjunct)
		assert.True(t, strings.Contains(sources, pin),
			"%s says %q is what pins the tenant, but that appears nowhere under internal/ — "+
				"either the query moved and the verdict needs re-reading, or it was never read",
			field, pin)
	}
}

// TestANarrowingQueryParamCannotReachAWrite is the rule the previous mechanism did
// not have.
//
// "narrows:" claims that the worst a caller can do with a foreign id is get an
// empty answer. That claim is about the response, and on the body channel a claim
// about the response is precisely what was wrong: PUT /notifications/preferences
// answered with nothing and persisted a subscription that a different subsystem
// read afterwards and acted on. So a narrowing verdict is only available where
// there is no afterwards — a route that cannot write.
//
// A handler that does write while serving a GET is not covered by the method and
// has to say so with "keyed:", naming what pins the tenant in whatever it stored.
func TestANarrowingQueryParamCannotReachAWrite(t *testing.T) {
	routes := handlerRoutes(t)
	owners := queryParamOwners(t)

	verified := 0
	for _, field := range sortedKeys(declaredQueryTenantParams) {
		if !strings.HasPrefix(declaredQueryTenantParams[field], "narrows:") {
			continue
		}

		owner := ownerOf(field)
		serving := owners[owner]
		require.NotEmpty(t, serving,
			"%s is declared narrows:, but no handler method could be resolved for %q — the "+
				"resolution this rule depends on has broken, so it is checking nothing",
			field, owner)

		for _, method := range serving {
			regs := routes[method]
			require.NotEmpty(t, regs,
				"%s is served by %s, which is registered on no route in cmd/api/main.go — the "+
					"route lookup has broken, so this rule is checking nothing", field, method)

			for _, r := range regs {
				assert.False(t, writingMethods[r.method],
					"%s is declared narrows:, but %s serves %s %s, which can write.\n"+
						"A narrowing verdict says the worst case is an empty answer — that is a "+
						"statement about the response, and the notification-preferences hole was a "+
						"request that answered with nothing and left a row behind for the dispatcher "+
						"to read. Authorize the caller (checked:), or name what pins the tenant in "+
						"what gets stored (keyed:).",
					field, method, r.method, r.path)
				verified++
			}
		}
	}

	require.Positive(t, verified,
		"no narrowing verdict was checked against a route — the declaration format or the router "+
			"has drifted and this rule is no longer reading anything")
}

// TestQueryTenantWritingReadsAreDeclaredKeyed is the other side of the same rule:
// a GET that writes must not be sitting under a narrowing verdict.
//
// The method is not proof of anything on its own. RecallGraph is a GET and it
// memoises into a package-level sync.Map, which is a later consumer in the same
// sense the notification dispatcher was. This pins the handlers known to do that,
// so if one of them is ever re-declared as a plain narrowing filter the reasoning
// fails here instead of shipping.
func TestQueryTenantWritingReadsAreDeclaredKeyed(t *testing.T) {
	for _, owner := range queryTenantWritingReads {
		checked := 0
		for _, field := range sortedKeys(declaredQueryTenantParams) {
			if ownerOf(field) != owner {
				continue
			}
			verdict := declaredQueryTenantParams[field]
			if strings.HasPrefix(verdict, "checked:") {
				// The tenant itself, authorized before anything is stored.
				checked++
				continue
			}
			assert.True(t, strings.HasPrefix(verdict, "keyed:"),
				"%s reads %s, which stores what it computes, so a narrowing verdict does not "+
					"cover it — the value outlives the response. Say what pins the tenant in the "+
					"stored key: %q", field, owner, verdict)
			checked++
		}
		assert.Positive(t, checked,
			"%s is listed as a writing read but declares no tenant-shaped query parameter — "+
				"the list is stale", owner)
	}
}

// queryTenantWritingReads are the query-string readers that persist something
// beyond the response they serve. Being a GET does not exempt a handler from the
// rule; being unable to write does, and these can.
var queryTenantWritingReads = []string{
	// recallGraphCache: a package-level sync.Map, written on every miss.
	"memory_handler.go:recallGraphQuery",
}

// -----------------------------------------------------------------------------
// Tests for the tests
// -----------------------------------------------------------------------------

// TestQueryTenantDetectorSeesBothSpellings runs the detector against a synthetic
// handler carrying one of each spelling, and requires both to be reported.
//
// Without this, "the invariant is green" and "the invariant reads nothing" look
// identical from the outside — and the struct-tag spelling is exactly the half a
// detector is most likely to lose, because it is the half the original grep lost.
func TestQueryTenantDetectorSeesBothSpellings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "synthetic_handler.go"), []byte(`package handler

type newThingQuery struct {
	WorkspaceID string `+"`query:\"workspace_id\"`"+`
	Limit       string `+"`query:\"limit\"`"+`
}

func (h *Thing) ReadsItDirectly(c echo.Context) error {
	_ = c.QueryParam("project_id")
	_ = c.QueryParam("cursor")
	return nil
}
`), 0o600))

	found := queryTenantParamsIn(t, dir)

	assert.Contains(t, found, "synthetic_handler.go:newThingQuery.workspace_id",
		"the detector no longer sees a tenant id bound through a query struct tag — that is the "+
			"spelling with no QueryParam call to grep for, and losing it silently un-covers "+
			"memory_handler.go and event_handler.go entirely")
	assert.Contains(t, found, "synthetic_handler.go:ReadsItDirectly.project_id",
		"the detector no longer sees a direct c.QueryParam call")
	assert.NotContains(t, found, "synthetic_handler.go:newThingQuery.limit",
		"the detector reports parameters that identify no tenant")
	assert.NotContains(t, found, "synthetic_handler.go:ReadsItDirectly.cursor",
		"the detector reports parameters that identify no tenant")
}

// TestQueryTenantVerdictRejectsTheDeclarationThatShipped pins the sentence that
// got through on the body channel and requires this grammar to refuse it.
//
// The rule that refuses it is a handful of string comparisons, which is also how
// cheap it would be to weaken: add "benign:" to queryVerdictPrefixes and every
// declaration passes again. This is what makes that edit fail.
func TestQueryTenantVerdictRejectsTheDeclarationThatShipped(t *testing.T) {
	const shipped = "benign: writes a preference row keyed on (workspace_id, the caller's own user_id)"

	assert.False(t, hasAnyPrefix(shipped, queryVerdictPrefixes),
		"the verdict that shipped with the notification-preferences hole would be accepted here")

	// The same claim rewritten with an accepted prefix still has to fail, because
	// it names no pin — otherwise the grammar is a spelling requirement.
	assert.Nil(t, pinnedVerdict.FindStringSubmatch(
		"narrows: writes a preference row keyed on the caller's own user_id"),
		"a narrowing verdict with no \"pinned by\" clause is accepted — the grammar has been "+
			"weakened to a prefix check")

	assert.NotNil(t, pinnedVerdict.FindStringSubmatch(
		"narrows: AND t.project_id = $2 — pinned by WHERE p.workspace_id = $1"),
		"the form every real verdict in this file uses is rejected, so the rule refuses the "+
			"state it demands")
	assert.NotNil(t, checkedVerdict.FindStringSubmatch("checked: MemoryHandler.requireWorkspaceID"),
		"the form the memory verdicts use is rejected")
}

// TestANarrowingQueryParamCannotReachAWrite_CatchesAWriteRoute runs the rule
// against a fabricated declaration on a real POST route and requires it to fail.
//
// The production declarations are all on GET routes, so the rule passes whether or
// not it works. This is the negative control: it proves the check would fire.
func TestANarrowingQueryParamCannotReachAWrite_CatchesAWriteRoute(t *testing.T) {
	routes := handlerRoutes(t)
	owners := queryParamOwners(t)

	// ImportMemories is POST /memories/import and takes ?workspace_id=.
	serving := owners["memory_handler.go:ImportMemories"]
	require.NotEmpty(t, serving, "ImportMemories no longer resolves to a handler method")

	sawWrite := false
	for _, method := range serving {
		for _, r := range routes[method] {
			if writingMethods[r.method] {
				sawWrite = true
			}
		}
	}
	assert.True(t, sawWrite,
		"ImportMemories no longer resolves to a writing route, so TestANarrowingQueryParam"+
			"CannotReachAWrite would accept a narrowing verdict on a POST — the route lookup it "+
			"depends on has stopped working")
}

// -----------------------------------------------------------------------------
// Reading the router and the handler package
// -----------------------------------------------------------------------------

// queryRouteRegistration matches "api.GET("/me/tasks", taskHandler.GetCurrentUserTasks"
// on the authenticated group.
var queryRouteRegistration = regexp.MustCompile(
	`api\.(GET|POST|PUT|PATCH|DELETE)\("(/[^"]*)",\s*(\w+)\.(\w+)`)

type registeredRoute struct{ method, path string }

// handlerRoutes maps "<Type>.<method>" to the routes it serves.
//
// The registration names a variable, not a type, so the two are matched by folded
// case — memoryHandler.List is MemoryHandler.List. That is a convention rather than
// a guarantee, which is why every rule that uses this map requires its lookup to be
// non-empty: a broken mapping has to fail loudly, not quietly excuse everything.
func handlerRoutes(t *testing.T) map[string][]registeredRoute {
	t.Helper()

	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "api", "main.go"))
	require.NoError(t, err, "cannot read route registrations")

	receivers := handlerMethodReceivers(t)

	out := map[string][]registeredRoute{}
	for _, m := range queryRouteRegistration.FindAllStringSubmatch(string(src), -1) {
		httpMethod, path, varName, funcName := m[1], m[2], m[3], m[4]
		for _, recv := range receivers[funcName] {
			if strings.EqualFold(recv, varName) {
				key := recv + "." + funcName
				out[key] = append(out[key], registeredRoute{method: httpMethod, path: path})
			}
		}
	}
	require.NotEmpty(t, out,
		"no handler routes resolved from cmd/api/main.go — the registration style has changed")
	return out
}

// queryParamOwners maps a declaration owner — a handler function, or a struct whose
// tags bind query parameters — to the "<Type>.<method>" handlers that serve it.
//
// A struct is resolved to the functions that mention it, which is how
// listMemoriesQuery reaches MemoryHandler.List: the parameter is declared on the
// struct and read by whatever binds it.
//
// Owners are keyed "<file>:<owner>", the same as the declarations, because bare
// method names are not unique across the package — List belongs to the mention,
// event, and auto-transition handlers among others, and resolving the wrong one
// would answer questions about the wrong route.
func queryParamOwners(t *testing.T) map[string][]string {
	t.Helper()

	handlerDir := filepath.Join("..", "handler")
	entries, err := os.ReadDir(handlerDir)
	require.NoError(t, err, "cannot read the handler package")

	fset := token.NewFileSet()
	out := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(handlerDir, name), nil, 0)
		require.NoError(t, parseErr, "cannot parse %s", name)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recv := receiverTypeName(fn)
			if recv == "" {
				continue
			}
			qualified := recv + "." + fn.Name.Name

			// The function is its own owner.
			self := name + ":" + fn.Name.Name
			out[self] = appendUnique(out[self], qualified)

			// And it owns every query struct it mentions.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				id, isIdent := n.(*ast.Ident)
				if !isIdent {
					return true
				}
				key := name + ":" + id.Name
				out[key] = appendUnique(out[key], qualified)
				return true
			})
		}
	}
	return out
}

// handlerMethodReceivers maps a method name to the receiver types declaring it.
func handlerMethodReceivers(t *testing.T) map[string][]string {
	t.Helper()

	handlerDir := filepath.Join("..", "handler")
	entries, err := os.ReadDir(handlerDir)
	require.NoError(t, err, "cannot read the handler package")

	fset := token.NewFileSet()
	out := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(handlerDir, name), nil, 0)
		require.NoError(t, parseErr, "cannot parse %s", name)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if recv := receiverTypeName(fn); recv != "" {
				out[fn.Name.Name] = appendUnique(out[fn.Name.Name], recv)
			}
		}
	}
	return out
}

// handlerMethodNames returns every "<Type>.<method>" in the handler package, so a
// "checked:" verdict can be held to naming one that exists.
func handlerMethodNames(t *testing.T) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for method, receivers := range handlerMethodReceivers(t) {
		for _, recv := range receivers {
			out[recv+"."+method] = true
		}
	}
	require.NotEmpty(t, out, "no handler methods found — the package layout has changed")
	return out
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch expr := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := expr.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return expr.Name
	}
	return ""
}

// internalSources is every non-test Go source under internal/, concatenated, so a
// verdict can be held to quoting a predicate that really is in the code.
func internalSources(t *testing.T) string {
	t.Helper()

	var sb strings.Builder
	err := filepath.Walk("..", func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path) //nolint:gosec // walking our own source tree
		if readErr != nil {
			return readErr
		}
		sb.Write(b)
		sb.WriteByte('\n')
		return nil
	})
	require.NoError(t, err, "cannot read internal/ sources")
	require.NotEmpty(t, sb.String(), "no sources read — the verdict check would pass vacuously")
	return sb.String()
}

// ownerOf returns the "<file>:<owner>" prefix of a "<file>:<owner>.<param>" key.
func ownerOf(field string) string {
	file, rest, _ := strings.Cut(field, ":")
	owner, _, _ := strings.Cut(rest, ".")
	return file + ":" + owner
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func appendUnique(in []string, v string) []string {
	for _, existing := range in {
		if existing == v {
			return in
		}
	}
	return append(in, v)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
