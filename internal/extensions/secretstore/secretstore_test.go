package secretstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeKeep struct {
	vals map[string]string
	down bool
}

func (f *fakeKeep) PutValue(_ context.Context, _ Ref, plain string) (string, error) {
	if f.down {
		return "", errors.New("connection refused")
	}
	id := uuid.NewString()
	f.vals[id] = plain
	return id, nil
}

func (f *fakeKeep) GetValue(_ context.Context, id string) (string, error) {
	if f.down {
		return "", errors.New("connection refused")
	}
	return f.vals[id], nil
}

func router(k *fakeKeep, sel string) *Router {
	return &Router{Builtin: Builtin{}, Keep: Keep{Client: k}, Selected: func(context.Context) string { return sel }}
}

func TestBuiltinRoundTrip(t *testing.T) {
	r := router(&fakeKeep{vals: map[string]string{}}, ProviderBuiltin)
	st, err := r.Put(context.Background(), Ref{}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(context.Background(), st); got.Degraded || got.Value != "tok" {
		t.Fatalf("got %+v", got)
	}
}

func TestKeepRoundTripAndMixedRows(t *testing.T) {
	k := &fakeKeep{vals: map[string]string{}}
	old, _ := router(k, ProviderBuiltin).Put(context.Background(), Ref{}, "old")
	r := router(k, ProviderKeep)
	st, err := r.Put(context.Background(), Ref{ExtensionID: "gitlab", Field: "token"}, "new")
	if err != nil || !strings.HasPrefix(st, KeepPrefix) {
		t.Fatalf("put: %q %v", st, err)
	}
	if got := r.Resolve(context.Background(), st); got.Value != "new" {
		t.Fatalf("keep: %+v", got)
	}
	if got := r.Resolve(context.Background(), old); got.Value != "old" {
		t.Fatalf("row written before the switch: %+v", got)
	}
}

// Red control from the acceptance: Keep down must be degraded with a reason,
// never an empty token passed off as a value.
func TestKeepDownIsDegraded(t *testing.T) {
	k := &fakeKeep{vals: map[string]string{}}
	r := router(k, ProviderKeep)
	st, _ := r.Put(context.Background(), Ref{}, "secret")
	k.down = true
	got := r.Resolve(context.Background(), st)
	if !got.Degraded || got.Value != "" || !strings.Contains(got.Reason, "connection refused") {
		t.Fatalf("want degraded with reason, got %+v", got)
	}
	if _, err := r.Put(context.Background(), Ref{}, "x"); err == nil {
		t.Fatal("put into a down Keep must fail")
	}
}

func TestKeepWithoutClientIsDegraded(t *testing.T) {
	r := &Router{Builtin: Builtin{}, Selected: func(context.Context) string { return ProviderKeep }}
	if got := r.Resolve(context.Background(), KeepPrefix+"x"); !got.Degraded {
		t.Fatalf("got %+v", got)
	}
	if _, err := r.Put(context.Background(), Ref{}, "x"); !errors.Is(err, ErrKeepNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

// Prod stays on builtin until the provider is switched on purpose: the
// process default seals as enc:v1 and never produces a keep: reference.
func TestDefaultStoreIsBuiltin(t *testing.T) {
	st, err := Default().Put(context.Background(), Ref{ExtensionID: "gitlab", Field: "token"}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(st, KeepPrefix) {
		t.Fatalf("default store must be builtin, got %q", st)
	}
	if got := Default().Resolve(context.Background(), st); got.Degraded || got.Value != "tok" {
		t.Fatalf("got %+v", got)
	}
}

func TestBuiltinResolveRejectsGarbage(t *testing.T) {
	got := Builtin{}.Resolve(context.Background(), "enc:v1:!!!not-base64!!!")
	if !got.Degraded || got.Value != "" || !strings.HasPrefix(got.Reason, "builtin: ") {
		t.Fatalf("got %+v", got)
	}
}

func TestKeepWithoutClientRefusesPutAndResolve(t *testing.T) {
	if _, err := (Keep{}).Put(context.Background(), Ref{}, "x"); !errors.Is(err, ErrKeepNotConfigured) {
		t.Fatalf("put: %v", err)
	}
	if got := (Keep{}).Resolve(context.Background(), KeepPrefix+"x"); !got.Degraded || got.Value != "" {
		t.Fatalf("resolve: %+v", got)
	}
}

type fixedStore struct{ res Result }

func (fixedStore) Put(context.Context, Ref, string) (string, error) { return "", nil }
func (f fixedStore) Resolve(context.Context, string) Result         { return f.res }

func TestOpenReturnsValueOrReason(t *testing.T) {
	prev := SetDefault(fixedStore{Result{Value: "v"}})
	t.Cleanup(func() { SetDefault(prev) })
	if v, reason := Open(context.Background(), "x"); v != "v" || reason != "" {
		t.Fatalf("ok: %q %q", v, reason)
	}
	SetDefault(fixedStore{Result{Degraded: true, Reason: "down"}})
	if v, reason := Open(context.Background(), "x"); v != "" || reason != "down" {
		t.Fatalf("degraded: %q %q", v, reason)
	}
}
