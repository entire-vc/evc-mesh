package handler

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var cyrillicRE = regexp.MustCompile(`[А-Яа-яЁё]`)

// Ratchet for the error-code contract (pkg/apierror/catalog.go): responses
// carry a stable code and an English message, so no string literal in this
// package may contain Russian. Comments are not scanned; natural-language
// vocabularies live in internal/service.
func TestHandlersHaveNoRussianStringLiterals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no handler sources found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		file := fset.AddFile(f, fset.Base(), len(src))
		var s scanner.Scanner
		s.Init(file, src, nil, 0) // mode 0: comments are skipped
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.STRING && cyrillicRE.MatchString(lit) {
				t.Errorf("%s:%d: Russian string literal %.60s — add a code to the catalog and keep the message English",
					f, fset.Position(pos).Line, lit)
			}
		}
	}
}
