package refext

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient(t *testing.T) {
	var gotAuth, gotPath, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotType = r.Header.Get("Authorization"), r.Method+" "+r.URL.EscapedPath(), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(raw, &gotBody)
		switch r.URL.Path {
		case "/api/v1/ext/me":
			_, _ = w.Write([]byte(`{"actor":"ext:reference-ext@w1","extension_id":"reference-ext","workspace_id":"w1","capabilities":{"api":["comments:write"]}}`))
		case "/api/v1/tasks/t/1/comments": // Path is unescaped; EscapedPath is checked below
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":403,"error_code":"extension_route_forbidden","message":"route not open to extensions"}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	ctx := context.Background()

	me, err := c.Me(ctx, "ext_tok")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ext_tok" || gotPath != "GET /api/v1/ext/me" || gotType != "" {
		t.Fatalf("request: %q %q %q", gotAuth, gotPath, gotType)
	}
	if me.Actor != "ext:reference-ext@w1" || me.WorkspaceID != "w1" || me.Capabilities["api"][0] != "comments:write" {
		t.Fatalf("me: %+v", me)
	}

	if err = c.AddComment(ctx, "ext_tok", "t/1", "hi"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /api/v1/tasks/t%2F1/comments" || gotType != "application/json" || gotBody["body"] != "hi" {
		t.Fatalf("comment request: %q %q %v", gotPath, gotType, gotBody)
	}

	// A route Mesh refuses comes back as the error envelope.
	err = c.do(ctx, "ext_tok", http.MethodGet, "/api/v1/workspaces", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.ErrorCode != "extension_route_forbidden" {
		t.Fatalf("refused route: %v", err)
	}
	if apiErr.Error() != "mesh answered 403 extension_route_forbidden: route not open to extensions" {
		t.Fatalf("message: %q", apiErr.Error())
	}

	if _, err := c.Me(ctx, ""); err == nil {
		t.Fatal("no token: must not call Mesh")
	}
	if err := c.do(ctx, "ext_tok", http.MethodPost, "/x", map[string]any{"f": func() {}}, nil); err == nil {
		t.Fatal("unencodable body: no error")
	}
	if err := c.do(ctx, "ext_tok", "BAD METHOD", "/x", nil, nil); err == nil {
		t.Fatal("bad request: no error")
	}
	down := &Client{BaseURL: "http://127.0.0.1:1"}
	if _, err := down.Me(ctx, "ext_tok"); err == nil {
		t.Fatal("unreachable Mesh: no error")
	}
}
