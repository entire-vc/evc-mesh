package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newDepsServer serves body at GET /api/v1/tasks/<id>/dependencies.
func newDepsServer(t *testing.T, body string) *RESTClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewRESTClient(srv.URL, "agk_test")
}

func lists(t *testing.T, m map[string]any, key string) []map[string]any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("key %q missing from %v", key, m)
	}
	got, ok := v.([]map[string]any)
	if !ok {
		t.Fatalf("key %q is %T, want []map[string]any", key, v)
	}
	return got
}

// TestGetTaskDependencies_ObjectShape covers the post-#544 server: the endpoint
// returns both sides of the graph as an object.
func TestGetTaskDependencies_ObjectShape(t *testing.T) {
	c := newDepsServer(t, `{"outgoing":[{"id":"a","dependency_type":"blocks"}],"incoming":[{"id":"b","dependency_type":"is_child_of"}]}`)

	got, err := c.GetTaskDependencies(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out := lists(t, got, "outgoing"); len(out) != 1 || out[0]["id"] != "a" {
		t.Errorf("outgoing = %v, want the single edge a", out)
	}
	if in := lists(t, got, "incoming"); len(in) != 1 || in[0]["id"] != "b" {
		t.Errorf("incoming = %v, want the single edge b", in)
	}
}

// TestGetTaskDependencies_LegacyArrayShape is the compatibility case that makes
// this client safe to hand to someone running a pre-#544 server: the bare array
// is what the endpoint used to return, and it must still decode.
func TestGetTaskDependencies_LegacyArrayShape(t *testing.T) {
	c := newDepsServer(t, `[{"id":"a","dependency_type":"blocks"}]`)

	got, err := c.GetTaskDependencies(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := lists(t, got, "outgoing")
	if len(out) != 1 || out[0]["id"] != "a" {
		t.Errorf("outgoing = %v, want the legacy array reported as outgoing", out)
	}
	if in := lists(t, got, "incoming"); len(in) != 0 {
		t.Errorf("incoming = %v, want empty: a pre-#544 server never reported the other side", in)
	}
}

// TestGetTaskDependencies_ObjectShapeIsNotDecodableAsArray is the negative
// control: it pins the exact failure this fix repairs. Run the pre-fix client
// (var result []map[string]any) against the object body and it errors — which
// is why get_task(include_dependencies=true) returned an error for the whole
// call, not a missing section.
func TestGetTaskDependencies_ObjectShapeIsNotDecodableAsArray(t *testing.T) {
	c := newDepsServer(t, `{"outgoing":[],"incoming":[]}`)

	var asArray []map[string]any
	err := c.doJSON(context.Background(), http.MethodGet, "/api/v1/tasks/t1/dependencies", nil, &asArray)
	if err == nil {
		t.Fatal("decoding the object shape into a slice must fail — if it stopped failing, this test no longer proves the regression it guards")
	}

	// Same body, through the fixed path: no error.
	if _, err := c.GetTaskDependencies(context.Background(), "t1"); err != nil {
		t.Fatalf("fixed client must handle the shape the slice decode chokes on, got: %v", err)
	}
}

// TestGetTaskDependencies_EmptyRendersAsLists guards the JSON the agent sees:
// absent lists must serialize as [] rather than null, so "no dependencies" and
// "field missing" are not the same value on the wire.
func TestGetTaskDependencies_EmptyRendersAsLists(t *testing.T) {
	c := newDepsServer(t, `{}`)

	got, err := c.GetTaskDependencies(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out := lists(t, got, "outgoing"); out == nil || len(out) != 0 {
		t.Errorf("outgoing = %v, want an empty list", out)
	}
	if in := lists(t, got, "incoming"); in == nil || len(in) != 0 {
		t.Errorf("incoming = %v, want an empty list", in)
	}
}
