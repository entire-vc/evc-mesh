package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestCollectFuncs_MatchingAndDrifted is the negative control required by
// task #46233e08's acceptance criteria: it proves the gate actually goes red
// on a real divergence, not just that it stays green on identical trees.
func TestCollectFuncs_MatchingAndDrifted(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	writeGoFile(t, dirA, "client.go", `package mcp

func Identical(x int) int {
	return x + 1
}

func (c *Client) Drifted(x int) int {
	return x + 1
}

type Client struct{}
`)
	writeGoFile(t, dirB, "client.go", `package mcp

func Identical(x int) int {
	return x + 1
}

func (c *Client) Drifted(x int) int {
	return x + 2 // deliberately different
}

type Client struct{}
`)

	funcsA, err := collectFuncs(dirA)
	if err != nil {
		t.Fatalf("collectFuncs(A): %v", err)
	}
	funcsB, err := collectFuncs(dirB)
	if err != nil {
		t.Fatalf("collectFuncs(B): %v", err)
	}

	if _, ok := funcsA["Identical"]; !ok {
		t.Fatal("expected Identical in A")
	}
	if funcsA["Identical"].text != funcsB["Identical"].text {
		t.Errorf("Identical: expected matching text, got:\nA: %q\nB: %q", funcsA["Identical"].text, funcsB["Identical"].text)
	}

	if _, ok := funcsA["Client.Drifted"]; !ok {
		t.Fatal("expected Client.Drifted key (receiver-qualified) in A")
	}
	if funcsA["Client.Drifted"].text == funcsB["Client.Drifted"].text {
		t.Error("Client.Drifted: expected DIFFERENT text between A and B (this is the negative control — it must fail if the comparator stops detecting real drift)")
	}
}

// TestCollectFuncs_CommentsIgnored ensures a comment-only edit never counts
// as drift — the gate exists to catch behavior divergence, not prose churn.
func TestCollectFuncs_CommentsIgnored(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	writeGoFile(t, dirA, "f.go", `package mcp

// Add returns the sum.
func Add(a, b int) int {
	return a + b
}
`)
	writeGoFile(t, dirB, "f.go", `package mcp

// Add sums two ints. Different comment, same body.
func Add(a, b int) int {
	return a + b
}
`)

	funcsA, err := collectFuncs(dirA)
	if err != nil {
		t.Fatalf("collectFuncs(A): %v", err)
	}
	funcsB, err := collectFuncs(dirB)
	if err != nil {
		t.Fatalf("collectFuncs(B): %v", err)
	}
	if funcsA["Add"].text != funcsB["Add"].text {
		t.Errorf("expected comment-only difference to be invisible, got:\nA: %q\nB: %q", funcsA["Add"].text, funcsB["Add"].text)
	}
}

// TestCollectFuncs_SkipsTestFiles ensures _test.go files are never compared
// — test helpers legitimately differ between the two repos' test suites.
func TestCollectFuncs_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "f.go", `package mcp

func Real() {}
`)
	writeGoFile(t, dir, "f_test.go", `package mcp

func TestReal(t *testing.T) {}
`)

	funcs, err := collectFuncs(dir)
	if err != nil {
		t.Fatalf("collectFuncs: %v", err)
	}
	if _, ok := funcs["Real"]; !ok {
		t.Error("expected Real to be collected")
	}
	if _, ok := funcs["TestReal"]; ok {
		t.Error("expected TestReal (in _test.go) to be skipped")
	}
}

func TestLoadAllowlist_MissingFileIsEmpty(t *testing.T) {
	allow, err := loadAllowlist(filepath.Join(t.TempDir(), "does-not-exist.txt"))
	if err != nil {
		t.Fatalf("expected no error for a missing allowlist, got: %v", err)
	}
	if len(allow) != 0 {
		t.Errorf("expected empty allowlist, got %d entries", len(allow))
	}
}

func TestLoadAllowlist_ParsesReasonsAndSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allow.txt")
	content := "# a comment\n\nFoo.Bar\tbecause reasons\nBaz\tanother reason\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	allow, err := loadAllowlist(path)
	if err != nil {
		t.Fatalf("loadAllowlist: %v", err)
	}
	if allow["Foo.Bar"] != "because reasons" {
		t.Errorf("Foo.Bar reason = %q", allow["Foo.Bar"])
	}
	if allow["Baz"] != "another reason" {
		t.Errorf("Baz reason = %q", allow["Baz"])
	}
	if len(allow) != 2 {
		t.Errorf("expected 2 entries, got %d", len(allow))
	}
}

func TestLoadAllowlist_RejectsLineWithoutReason(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allow.txt")
	if err := os.WriteFile(path, []byte("Foo.Bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllowlist(path); err == nil {
		t.Error("expected an error for a func-key with no reason (bare key, no tab)")
	}
}

// TestCollectFuncs_EmptyDirIsAnError guards against a misconfigured CI path
// (e.g. a checkout that landed empty, or a wrong -a/-b directory) silently
// reporting "0 shared, no drift" instead of failing loudly.
func TestCollectFuncs_EmptyDirIsAnError(t *testing.T) {
	if _, err := collectFuncs(t.TempDir()); err == nil {
		t.Error("expected an error for a directory with no .go files")
	}
}
