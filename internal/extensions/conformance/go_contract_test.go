package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Each test scans the same immutable repository inputs. Cache by root so the
// red fixtures share the real scan, while temporary modules remain independent.
var contractScans sync.Map

func typedContractConsumers(t *testing.T, root string) map[string]bool {
	t.Helper()
	if cached, ok := contractScans.Load(root); ok {
		return cached.(map[string]bool)
	}
	type listedPackage struct {
		Dir, ImportPath, Export string
		GoFiles                 []string
	}
	// Compiler export data resolves imported receiver types without new analyzer
	// dependencies. No tests are run by go list; build errors fail the audit.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-export", "-deps", "-json", "./internal/...", "./cmd/...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve Go contract receivers: %v: %s", err, stderr.String())
	}
	exports := map[string]string{}
	var local []listedPackage
	providerPath := ""
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		exports[p.ImportPath] = p.Export
		if p.Dir == filepath.Join(root, "internal", "extensions") {
			providerPath = p.ImportPath
		}
		if len(p.GoFiles) != 0 && (strings.HasPrefix(p.Dir, filepath.Join(root, "internal")+string(filepath.Separator)) ||
			strings.HasPrefix(p.Dir, filepath.Join(root, "cmd")+string(filepath.Separator))) {
			local = append(local, p)
		}
	}
	if providerPath == "" {
		t.Fatal("Go contract scan did not find internal/extensions")
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		if exports[path] == "" {
			return nil, fmt.Errorf("missing export data for %s", path)
		}
		return os.Open(exports[path])
	})
	callers := map[string][]string{}
	providerCalls := map[string][]string{}
	hookReads := map[string][]string{}
	names := map[string]string{}
	for _, p := range local {
		var files []*ast.File
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		info := &types.Info{
			Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		conf := types.Config{Importer: imp}
		if _, err := conf.Check(p.ImportPath, fset, files, info); err != nil {
			t.Fatalf("type-check contract consumers in %s: %v", p.ImportPath, err)
		}
		for _, f := range files {
			for _, decl := range f.Decls {
				enclosing := ""
				if fd, ok := decl.(*ast.FuncDecl); ok {
					fn, _ := info.Defs[fd.Name].(*types.Func)
					enclosing = functionID(fn)
					names[enclosing] = fd.Name.Name
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						fn := calledFunction(x.Fun, info)
						if fn == nil {
							break
						}
						id := functionID(fn)
						callers[id] = append(callers[id], enclosing)
						if receiverID(fn) == providerPath+".Provider" {
							providerCalls[fn.Name()] = append(providerCalls[fn.Name()], enclosing)
						}
					case *ast.SelectorExpr:
						if sel := info.Selections[x]; sel != nil && sel.Kind() == types.FieldVal &&
							namedTypeID(sel.Recv()) == providerPath+".Hooks" {
							hookReads[x.Sel.Name] = append(hookReads[x.Sel.Name], enclosing)
						}
					}
					return true
				})
			}
		}
	}
	calledOutside := func(id string) bool {
		for _, caller := range callers[id] {
			if caller != id {
				return true
			}
		}
		return false
	}
	used := map[string]bool{}
	for method, enclosing := range providerCalls {
		for _, id := range enclosing {
			if names[id] != method || calledOutside(id) {
				used["Provider."+method+"()"] = true
			}
		}
	}
	for field, enclosing := range hookReads {
		for _, id := range enclosing {
			if id == "" || calledOutside(id) {
				used["Hooks."+field] = true
			}
		}
	}
	contractScans.Store(root, used)
	return used
}

func namedTypeID(typ types.Type) string {
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(ptr.Elem())
	}
	if named, ok := typ.(*types.Named); ok && named.Obj().Pkg() != nil {
		return named.Obj().Pkg().Path() + "." + named.Obj().Name()
	}
	return ""
}

func receiverID(fn *types.Func) string {
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return namedTypeID(sig.Recv().Type())
	}
	return ""
}

func functionID(fn *types.Func) string {
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	if recv := receiverID(fn); recv != "" {
		return recv + "." + fn.Name()
	}
	return fn.Pkg().Path() + "." + fn.Name()
}

func calledFunction(expr ast.Expr, info *types.Info) *types.Func {
	switch x := expr.(type) {
	case *ast.Ident:
		fn, _ := info.Uses[x].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if sel := info.Selections[x]; sel != nil {
			fn, _ := sel.Obj().(*types.Func)
			return fn
		}
		fn, _ := info.Uses[x.Sel].(*types.Func)
		return fn
	case *ast.IndexExpr:
		return calledFunction(x.X, info)
	case *ast.IndexListExpr:
		return calledFunction(x.X, info)
	}
	return nil
}
