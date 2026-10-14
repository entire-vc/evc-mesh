package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Error-response forms and who guards them:
//   - apierror.Error (handlers, middleware, echo's own errors via
//     NewHTTPErrorHandler): MarshalJSON guarantees error_code. Not a ratchet.
//   - plain maps keyed "error" or a string "code", typed structs tagged
//     json:"error": caught by the count below.
//   - handleError shapes: TestHandleError_AdHocShapesCarryErrorCode asserts each.
//   - tr_mount result envelope on a non-2xx status: asserted in
//     TestTrMountHandler_EachStatusGetsItsOwnHTTPCode.
//
// Deliberately not caught: maps built in a variable and returned later (no such
// error path exists today; a count cannot see data flow), the OAuth
// token/registration bodies (RFC 6749 fixes their shape: error, error_description),
// the PR/MR webhook replies to GitHub/GitLab (vcs_link_handler.go,
// {"status":"error","retry":true}: a retry contract for a machine caller, not
// shown by the UI, shape must not change), 2xx responses whose status comes from a variable,
// and websocket event frames (events, not HTTP errors).
//
// Ratchet: an error response written as a plain map ("error": ...) bypasses
// apierror.Error and its guaranteed error_code, so every such literal must
// carry an "error_code" key beside it. Applies to handlers and the websocket
// upgrade endpoint (ws/client.go frames are events, not HTTP errors).
// stringCodeKey matches an ad-hoc error map keyed by a "code": a string
// identifier or the numeric HTTP status. Either way the map is an error
// envelope and must also carry the stable "error_code".
var stringCodeKey = regexp.MustCompile(`"code":\s+("[a-z_]+"|\d+|http\.Status[A-Za-z]+)`)

func TestPlainMapErrorsCarryErrorCode(t *testing.T) {
	var files []string
	for _, g := range []string{"*.go", filepath.Join("..", "ws", "handler.go")} {
		m, err := filepath.Glob(g)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		plain := max(strings.Count(string(src), `"error":`)+strings.Count(string(src), "`json:\"error\"`"), len(stringCodeKey.FindAllString(string(src), -1)))
		coded := strings.Count(string(src), `"error_code":`)
		if plain > coded {
			t.Errorf("%s: %d plain-map error responses (\"error\" or a string \"code\") but %d \"error_code\" keys; add error_code (pkg/apierror/catalog.go)", f, plain, coded)
		}
	}
}
