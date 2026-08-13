package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTaskServiceWiresTheAssigneeTenancyGuard holds shut a gap that is invisible
// from inside the service package.
//
// assertAssigneeInProjectWorkspace decides the USER half of the assignee tenancy
// check through s.wsMembership, and that dependency is optional at construction:
// wired here in main.go, absent in most unit tests. The guard fails closed when it
// is missing, which is the right default — but it means the two states are
// distinguishable only in production. Every test in internal/service passes with
// the wiring present and with it removed, because those tests supply their own.
//
// So the fact under test lives nowhere but this file: a future edit that drops
// service.WithWorkspaceMembershipReader from the option list compiles, ships, and
// takes the whole service suite green with it.
//
// What such a regression would actually do is worth stating, because it is not the
// obvious one. It would not silently reopen the cross-tenant hole — fail-closed
// means it would refuse EVERY user assignment on the instance, loudly. That is an
// outage rather than a breach, and it is still a regression this repository should
// catch in CI rather than in prod.
//
// This is an AST walk of the NewTaskService call, not a grep for the identifier: a
// grep matches the option named in a comment, in a neighbouring call, or in a
// deleted line still present in the file, and would keep passing after the wiring
// moved somewhere that never runs.
func TestTaskServiceWiresTheAssigneeTenancyGuard(t *testing.T) {
	const (
		constructor = "NewTaskService"
		option      = "WithWorkspaceMembershipReader"
	)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	// selectorName reports the `X.Sel` name of a selector expression — the shape of
	// both `service.NewTaskService` and `service.WithWorkspaceMembershipReader`.
	selectorName := func(e ast.Expr) string {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		return sel.Sel.Name
	}

	var (
		foundConstructor bool
		foundOption      bool
	)

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || selectorName(call.Fun) != constructor {
			return true
		}
		foundConstructor = true

		// The option must appear among THIS call's arguments. Finding it anywhere
		// else in the file — another constructor, a comment, dead code — is exactly
		// the false pass this test exists to avoid.
		for _, arg := range call.Args {
			inner, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}
			if selectorName(inner.Fun) == option {
				foundOption = true
				return false
			}
		}
		return false
	})

	if !foundConstructor {
		t.Fatalf("no %s(...) call found in main.go — this test can no longer see the wiring "+
			"it guards, which is a failure of the test, not a pass of the invariant", constructor)
	}
	if !foundOption {
		t.Errorf("%s is not passed to %s in main.go.\n\n"+
			"The user half of the assignee tenancy guard (assertAssigneeInProjectWorkspace) "+
			"reads s.wsMembership. Unwired, it fails closed and refuses every user assignment "+
			"on the instance. The service-package tests cannot catch this: they wire their own "+
			"reader, so they stay green either way.", option, constructor)
	}
}
