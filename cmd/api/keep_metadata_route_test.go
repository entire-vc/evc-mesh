package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Route inventory for the Keep metadata probe (route and boundary controls). The
// route reads no secret value and must stay a human-session, PermManageSecrets
// route whose handler is built from nothing wider than the ref lookup and the
// metadata-only Keep view. Read from the real main.go by AST, so a guard that
// is renamed or only mentioned in a comment does not count.
func TestKeepMetadataRouteInventory(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	callee := func(e ast.Expr) string {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return ""
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok {
				return x.Name + "." + fn.Sel.Name
			}
		}
		return ""
	}
	methods := map[string]bool{}
	envelopeFirst, groupUse := false, []string{}
	var handlerArgs []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, _ := sel.X.(*ast.Ident)
		switch {
		case x != nil && x.Name == "api" && sel.Sel.Name == "Use" && len(call.Args) == 1:
			groupUse = append(groupUse, callee(call.Args[0]))
		case x != nil && x.Name == "api" && (sel.Sel.Name == "GET" || sel.Sel.Name == "HEAD") && len(call.Args) >= 2:
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || !strings.HasSuffix(strings.Trim(lit.Value, `"`), "/keep-metadata") {
				return true
			}
			var guards []string
			for _, a := range call.Args[2:] {
				name := callee(a)
				if name == "rbac" {
					if c := a.(*ast.CallExpr); len(c.Args) == 1 {
						if s, ok := c.Args[0].(*ast.SelectorExpr); ok {
							name += "(" + s.Sel.Name + ")"
						}
					}
				}
				guards = append(guards, name)
			}
			if len(guards) != 2 || guards[0] != "mw.RequireUserAuth" || guards[1] != "rbac(PermManageSecrets)" {
				t.Errorf("%s keep-metadata guards = %v, want [mw.RequireUserAuth rbac(PermManageSecrets)]", sel.Sel.Name, guards)
			}
			methods[sel.Sel.Name] = true
		case sel.Sel.Name == "NewKeepMetadataHandler" && x != nil && x.Name == "handler":
			for _, a := range call.Args {
				switch v := a.(type) {
				case *ast.Ident:
					handlerArgs = append(handlerArgs, v.Name)
				default:
					handlerArgs = append(handlerArgs, callee(a))
				}
			}
		}
		return true
	})
	if !methods["GET"] || !methods["HEAD"] {
		t.Fatalf("keep-metadata routes registered: %v, want GET and HEAD", methods)
	}
	if len(groupUse) > 0 && groupUse[0] == "mw.KeepMetadataEnvelope" {
		envelopeFirst = true
	}
	if !envelopeFirst {
		t.Errorf("the closed envelope must be the first middleware of the protected group; got %v", groupUse)
	}
	if len(handlerArgs) != 2 || handlerArgs[0] != "secretRepo" || handlerArgs[1] != "keepMetadataReader" {
		t.Errorf("NewKeepMetadataHandler args = %v, want [secretRepo keepMetadataReader]", handlerArgs)
	}
}
