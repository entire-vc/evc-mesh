package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRulesServiceWiresAgentWorkspaceGrantRepo holds shut a gap of the same
// class as TestTaskServiceWiresTheAssigneeTenancyGuard (tenancy_wiring_test.go)
// and TestRuleServiceWiresTaskStatusRepoForAllowCancelled
// (rule_service_wiring_test.go) — a dependency that is optional at
// construction, present here in main.go, absent from most unit tests, and
// whose loss is invisible to the service-package suite because those tests
// wire their own.
//
// GetTeamDirectory (internal/service/rules_service.go) reads s.agentGrantRepo
// to list GUEST agents — home workspace elsewhere, reached into this one via
// an active agent_workspace_grants connection (task U3/#71627c5a). Unwired,
// GET /workspaces/:ws_id/team reverts to home-only: a correctly invited,
// actively-granted agent is simply missing from the directory, with no error
// to notice — the exact "не видно" repro #71627c5a was filed against, not an
// outage this time but a silent narrowing.
//
// AST walk of the NewRulesServiceWithOptions call, not a grep, for the same
// reason the two sibling tests are: a grep matches the option named in a
// comment or a different call and keeps passing after the real wiring moves
// or is removed.
func TestRulesServiceWiresAgentWorkspaceGrantRepo(t *testing.T) {
	const (
		constructor = "NewRulesServiceWithOptions"
		option      = "WithRulesAgentGrantRepo"
	)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

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

		// The option must appear among THIS call's arguments — finding it in a
		// different call, a comment, or dead code is exactly the false pass this
		// test exists to avoid.
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
			"GetTeamDirectory (internal/service/rules_service.go) reads s.agentGrantRepo to "+
			"list GUEST agents — home workspace elsewhere, reached into this one via an active "+
			"agent_workspace_grants connection (task U3/#71627c5a). Unwired, GET "+
			"/workspaces/:ws_id/team reverts to home-only, silently: a correctly invited, "+
			"actively-granted agent is just missing, with no error to notice. The "+
			"service-package tests cannot catch this: they wire their own grant repo, so they "+
			"stay green either way.", option, constructor)
	}
}
