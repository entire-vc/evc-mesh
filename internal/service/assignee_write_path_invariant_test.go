package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The invariant for the fourth tenant channel: a principal named by id.
//
// The three channels closed in July all put the workspace INTO the request — in
// the path, in the body, in the query string — so each could be held shut by a
// test that reads routes or request structs and demands a guard on whatever names
// a tenant. TestEveryIdentifiedRouteIsWorkspaceScoped and
// TestBodyTenantFieldsAreDeclared are those tests.
//
// This channel has no such surface. "Assign task T to agent A" names no
// workspace anywhere; the tenant is a property of the object behind A, and A's
// directory is global. Nothing a route-reader or a struct-reader can see
// distinguishes the safe request from the cross-tenant one, which is why all
// three existing invariants were green while this was open.
//
// So the thing to hold shut here is not a route or a field but a CODE PATH: every
// place in the service layer that writes an assignee must pass through the funnel
// that checks tenancy. That is what this file enumerates and enforces.
//
// It is the machine-checkable version of a comment that already existed on
// ensureAssigneeProjectMember — "EVERY write path that sets assignee_id must call
// this ... If you add an eighth, add the call". That comment was true, careful,
// and completely unable to fail a build.

// assigneeWriteVerdict records why one function that writes an assignee field is
// safe. Every such function must appear here.
//
// "funnel:" — the function calls ensureAssigneeProjectMember or
// assertAssigneeInProjectWorkspace itself, and this test verifies that call is
// really in the source rather than taking the declaration's word for it.
//
// "delegated:" — the function only mutates an in-memory struct that a named
// funnel-carrying function persists. The named function must itself be declared
// "funnel:", so a delegation cannot point at another delegation and terminate
// nowhere.
//
// There is no "benign:" verdict, for the same reason the request-body invariant
// refuses one: a write that grants nothing readable today is one join away from
// granting something tomorrow, and the assignment write is exactly the write
// whose payload arrives later through a different code path (the notify service
// posts the task to the assignee's callback_url).
var assigneeWriteVerdict = map[string]string{
	"taskService.Create":                   "funnel: ensureAssigneeProjectMember before taskRepo.Create",
	"taskService.Update":                   "funnel: ensureAssigneeProjectMember when the assignee changes",
	"taskService.MoveTask":                 "funnel: assertAssigneeInProjectWorkspace up front, ensureAssigneeProjectMember at the write",
	"taskService.AssignTask":               "funnel: ensureAssigneeProjectMember before taskRepo.Update",
	"taskService.CreateSubtask":            "funnel: ensureAssigneeProjectMember before taskRepo.Create",
	"taskService.applyReviewAssignee":      "funnel: assertAssigneeInProjectWorkspace before the rotation, ensureAssigneeProjectMember at the write",
	"taskService.restorePreReviewAssignee": "funnel: assertAssigneeInProjectWorkspace re-checks the stash before restoring",
	"taskService.applyAutoAssign":          "delegated: taskService.Create and taskService.CreateSubtask both funnel after calling it",
	"taskService.bulkUpdateOne":            "delegated: taskService.Update performs the write and the funnel",

	// These three write an assignee onto a TEMPLATE or SCHEDULE row, not onto a
	// task. Nothing reads those rows as an authorization; they are materialised
	// later, and materialisation goes through taskService.Create, which refuses a
	// foreign principal there. Known and accepted consequence: a template or
	// schedule CAN store an assignee from another workspace, and the refusal then
	// lands at materialisation time as a failed recurring run in the logs rather
	// than at the moment someone typed the wrong id. That is a loud
	// misconfiguration, not a quiet grant — no task is created and no callback
	// fires — but it is worth closing separately.
	"taskTemplateService.Create":                 "delegated: taskService.Create refuses a foreign principal when the template is materialised",
	"taskTemplateService.CreateTaskFromTemplate": "delegated: taskService.Create performs the write and the funnel",
	"recurringService.Create":                    "delegated: taskService.Create refuses a foreign principal when the schedule fires",
	"recurringService.Update":                    "delegated: taskService.Create refuses a foreign principal when the schedule fires",
	"recurringService.createInstance":            "delegated: taskService.Create performs the write and the funnel",
}

// assigneeFields are the struct fields whose write hands a task to a principal.
// The scan reads internal/service only, because that is where persistence
// happens: every handler write reaches the database through one of these
// functions, so a check here cannot be bypassed by a new handler.
var assigneeFields = map[string]bool{
	"AssigneeID":          true,
	"PreReviewAssigneeID": true,
	// ReviewerID arrived after this guard was written and is the same shape — a
	// principal named by id. It is listed so a future service-side write to it is
	// caught; today it is only set in the handler and checked in Update.
	"ReviewerID": true,
}

// funnelCalls are the calls that satisfy a "funnel:" verdict.
var funnelCalls = map[string]bool{
	"ensureAssigneeProjectMember":      true,
	"assertAssigneeInProjectWorkspace": true,
}

// TestEveryAssigneeWritePathIsWorkspaceChecked fails when a new write path
// appears without a declared, verified verdict.
func TestEveryAssigneeWritePathIsWorkspaceChecked(t *testing.T) {
	writers, callers := assigneeWriters(t)
	require.NotEmpty(t, writers,
		"no assignee write paths discovered — the scan is broken, and a broken scan is a green test "+
			"that checks nothing")

	var undeclared []string
	for fn := range writers {
		if _, ok := assigneeWriteVerdict[fn]; !ok {
			undeclared = append(undeclared, fn)
		}
	}
	sort.Strings(undeclared)
	assert.Empty(t, undeclared,
		"these functions write an assignee field and are not declared in assigneeWriteVerdict.\n"+
			"A task's assignee is the one field that both grants read access to the task and causes "+
			"the notify service to POST the task's contents to that principal's callback_url, so a "+
			"path that sets it without checking tenancy hands another workspace's data out.\n"+
			"Call ensureAssigneeProjectMember (and propagate its error), then declare the path:\n  %s",
		strings.Join(undeclared, "\n  "))

	// A verdict claiming the funnel has to be a function that carries it.
	var unverified []string
	for fn, verdict := range assigneeWriteVerdict {
		if !strings.HasPrefix(verdict, "funnel:") {
			continue
		}
		if !callers[fn] {
			unverified = append(unverified, fmt.Sprintf("%s — %q", fn, verdict))
		}
	}
	sort.Strings(unverified)
	assert.Empty(t, unverified,
		"these paths are declared to go through the tenancy funnel, but no call to %v appears in "+
			"their body. Deleting the call is a one-line edit that leaves every declaration here "+
			"still claiming the path is safe:\n  %s",
		keysOf(funnelCalls), strings.Join(unverified, "\n  "))

	// A delegation must terminate at a funnel, not at another delegation.
	for fn, verdict := range assigneeWriteVerdict {
		rest, ok := strings.CutPrefix(verdict, "delegated: ")
		if !ok {
			continue
		}
		found := false
		for target, targetVerdict := range assigneeWriteVerdict {
			// Match the FULL key. Matching a trimmed short name would let
			// "delegated: ... CreateSubtask" be satisfied by "Create", so a
			// delegation could name a path it does not actually reach.
			if strings.Contains(rest, target) && strings.HasPrefix(targetVerdict, "funnel:") {
				found = true
				break
			}
		}
		assert.True(t, found,
			"%s delegates to something that is not itself a declared funnel path (%q) — a chain of "+
				"delegations that never reaches a check is the same hole with more steps", fn, verdict)
	}
}

// TestAssigneeWriteVerdictsCannotBeExcuses is the test for the test.
//
// The rule above is a prefix comparison, and a prefix comparison is cheap to
// weaken: adding "benign:" to the accepted set makes every future path pass. This
// pins the shape of the excuse that would have covered the defect that shipped —
// the assignment itself returns nothing belonging to the other tenant, so
// "returns nothing" reads as safe — and requires it to be rejected.
func TestAssigneeWriteVerdictsCannotBeExcuses(t *testing.T) {
	const excuse = "benign: assignment returns no data belonging to the other workspace"

	assert.False(t, verdictChecksTenancy(excuse),
		"the excuse that describes the shipped defect is accepted as a verdict — assignment is "+
			"precisely the write whose payload arrives later, through the callback the notify "+
			"service posts to")
	assert.True(t, verdictChecksTenancy("funnel: ensureAssigneeProjectMember before taskRepo.Create"),
		"the verdict the fix records is rejected, so the rule refuses the state it demands")

	for fn, verdict := range assigneeWriteVerdict {
		assert.True(t, verdictChecksTenancy(verdict),
			"%s carries a verdict that claims no tenancy check: %q", fn, verdict)
	}
}

func verdictChecksTenancy(verdict string) bool {
	return strings.HasPrefix(verdict, "funnel:") || strings.HasPrefix(verdict, "delegated: ")
}

// assigneeWriters scans the non-test service sources and returns the set of
// functions that write an assignee field, and the set that call the funnel.
func assigneeWriters(t *testing.T) (writers, callers map[string]bool) {
	t.Helper()
	writers, callers = map[string]bool{}, map[string]bool{}

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, perr, "parse %s", path)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := funcKey(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, isSel := lhs.(*ast.SelectorExpr); isSel && assigneeFields[sel.Sel.Name] {
							writers[name] = true
						}
					}
				case *ast.KeyValueExpr:
					// A composite literal building a task with an assignee, e.g.
					// &domain.Task{AssigneeID: x}.
					if key, isIdent := node.Key.(*ast.Ident); isIdent && assigneeFields[key.Name] {
						writers[name] = true
					}
				case *ast.CallExpr:
					switch callee := node.Fun.(type) {
					case *ast.SelectorExpr:
						if funnelCalls[callee.Sel.Name] {
							callers[name] = true
						}
					case *ast.Ident:
						if funnelCalls[callee.Name] {
							callers[name] = true
						}
					}
				}
				return true
			})
		}
	}
	return writers, callers
}

// funcKey renders a function as "Receiver.Name", or "Name" for a plain function.
func funcKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if ident, ok := typ.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
