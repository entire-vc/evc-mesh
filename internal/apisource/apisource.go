// Package apisource hands tests the source of the API server (cmd/api) as one
// unit.
//
// Route and wiring registrations used to live in cmd/api/main.go alone, and a
// number of tests read that one file to check them (every write route carries
// a guard, every :ws_id route is tenant-checked, a service gets its option).
// The registrations are now split over one file per module (see apiModules in
// cmd/api/modules.go), so "the registrations" means every non-test Go file of
// cmd/api. Files come in the order the process runs them, so text offsets and
// token positions still compare the way they did in the single main():
// a guard mounted before the first route still has the smaller position.
package apisource

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Files returns the non-test Go files of the API package in dir, in run order:
// the files of the modules listed in apiModules (modules.go), in list order,
// then main.go (main and serve), then every other file by name.
func Files(dir string) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, n := range names {
		if !strings.HasSuffix(n, "_test.go") {
			files = append(files, n)
		}
	}
	sort.Strings(files)

	fset := token.NewFileSet()
	funcFile := map[string]string{}
	var order []string
	for _, f := range files {
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, d := range parsed.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					funcFile[d.Name.Name] = f
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					vs, ok := s.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "apiModules" || len(vs.Values) != 1 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, el := range lit.Elts {
						if id, ok := el.(*ast.Ident); ok {
							order = append(order, id.Name)
						}
					}
				}
			}
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("apisource: no apiModules list found in %s", dir)
	}

	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, fn := range order {
		f, ok := funcFile[fn]
		if !ok {
			return nil, fmt.Errorf("apisource: module %s has no file in %s", fn, dir)
		}
		add(f)
	}
	add(funcFile["serve"])
	for _, f := range files {
		add(f)
	}
	return out, nil
}

// Read returns the API source files of dir concatenated in run order.
func Read(dir string) ([]byte, error) {
	files, err := Files(dir)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// Parse parses the API source files of dir in run order and returns one file
// holding all their declarations, imports and comments. Positions resolve
// through fset to the real file and line.
func Parse(fset *token.FileSet, dir string, mode parser.Mode) (*ast.File, error) {
	files, err := Files(dir)
	if err != nil {
		return nil, err
	}
	merged := &ast.File{}
	for _, f := range files {
		parsed, err := parser.ParseFile(fset, f, nil, mode)
		if err != nil {
			return nil, err
		}
		if merged.Name == nil {
			merged.Name = parsed.Name
			merged.Package = parsed.Package
		}
		merged.Decls = append(merged.Decls, parsed.Decls...)
		merged.Imports = append(merged.Imports, parsed.Imports...)
		merged.Comments = append(merged.Comments, parsed.Comments...)
	}
	return merged, nil
}
