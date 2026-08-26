package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRuleServiceWiresTaskStatusRepoForAllowCancelled holds shut a gap that is
// invisible from inside the service package, the same class of bug as
// TestTaskServiceWiresTheAssigneeTenancyGuard in tenancy_wiring_test.go.
//
// transition_gate.require_subtasks_done has an allow_cancelled config option
// (evalRequireSubtasksDone in internal/service/rule_evaluators.go) meant to let a
// legitimately-cancelled subtask count as "done enough" for the parent to close.
// That check only runs when deps.taskStatusRepo is non-nil — and taskStatusRepo is
// an OPTIONAL dependency of ruleService, wired only via WithRuleTaskStatusRepo.
//
// Until this fix (#204c0311), main.go constructed ruleService WITHOUT that option,
// so in production deps.taskStatusRepo was always nil, allow_cancelled was silently
// a no-op, and any parent task with even one cancelled subtask could never reach
// done/review — no matter how the rule was configured. Live incident: task
// #815f703b, workspace rule "Require subtasks done before close" had
// allow_cancelled:true the whole time and it was never honored.
//
// internal/service tests cannot catch this: they construct evaluatorDeps directly
// and wire their own taskStatusRepo, so they stay green whether or not main.go
// remembers the option. This is an AST walk of the NewRuleService call (not a grep)
// for the same reason tenancy_wiring_test.go uses one — a grep matches the option
// named in a comment or a different call and keeps passing after the real wiring
// is removed.
func TestRuleServiceWiresTaskStatusRepoForAllowCancelled(t *testing.T) {
	const (
		constructor = "NewRuleService"
		option      = "WithRuleTaskStatusRepo"
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
			"transition_gate.require_subtasks_done's allow_cancelled option "+
			"(evalRequireSubtasksDone) reads deps.taskStatusRepo. Unwired, it is silently "+
			"skipped, and a parent task with even one cancelled subtask can never reach "+
			"done/review regardless of how the rule is configured (live incident #815f703b). "+
			"The service-package tests cannot catch this: they wire their own status repo, "+
			"so they stay green either way.", option, constructor)
	}
}
