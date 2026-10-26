package secretstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
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
	return &Router{Builtin: Builtin{}, Keep: Keep{Client: k}, Selected: func(context.Context) (string, error) { return sel, nil }}
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
	r := &Router{Builtin: Builtin{}, Selected: func(context.Context) (string, error) { return ProviderKeep, nil }}
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

func TestProviderSettingSelected(t *testing.T) {
	setting, reads, readErr := "", 0, error(nil)
	now := time.Unix(1_700_000_000, 0)
	p := &ProviderSetting{
		Read: func(context.Context) (string, error) { reads++; return setting, readErr },
		TTL:  30 * time.Second,
		Now:  func() time.Time { return now },
	}
	ctx := context.Background()

	if v, err := p.Selected(ctx); err != nil || v != ProviderBuiltin {
		t.Fatalf("unset setting is builtin, got %q %v", v, err)
	}
	setting = ProviderKeep
	if v, _ := p.Selected(ctx); v != ProviderBuiltin || reads != 1 {
		t.Fatalf("within TTL the cached answer serves: %q after %d reads", v, reads)
	}
	now = now.Add(31 * time.Second)
	if v, _ := p.Selected(ctx); v != ProviderKeep || reads != 2 {
		t.Fatalf("after TTL the switch takes effect: %q after %d reads", v, reads)
	}

	now = now.Add(time.Minute)
	readErr = errors.New("db down")
	if _, err := p.Selected(ctx); err == nil {
		t.Fatal("a failed read must fail, not fall back to builtin")
	}
	readErr, setting = nil, "vault"
	if _, err := p.Selected(ctx); err == nil || !strings.Contains(err.Error(), SettingSecretProvider) {
		t.Fatalf("an unknown provider must be refused, got %v", err)
	}
}

func TestRouterPutFollowsProviderSetting(t *testing.T) {
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	t.Cleanup(encryption.ResetForTest)
	k := &fakeKeep{vals: map[string]string{}}
	setting := ""
	p := &ProviderSetting{Read: func(context.Context) (string, error) { return setting, nil }}
	r := &Router{Builtin: Builtin{}, Keep: Keep{Client: k}, Selected: p.Selected}
	ctx := context.Background()

	old, err := r.Put(ctx, Ref{ExtensionID: "gitlab", Field: "token"}, "old")
	if err != nil || !strings.HasPrefix(old, "enc:v1:") {
		t.Fatalf("default builtin seals enc:v1, got %q %v", old, err)
	}
	setting = ProviderKeep
	st, err := r.Put(ctx, Ref{ExtensionID: "gitlab", Field: "token"}, "new")
	if err != nil || !strings.HasPrefix(st, KeepPrefix) {
		t.Fatalf("secret_provider=keep writes keep:<id>, got %q %v", st, err)
	}
	if got := r.Resolve(ctx, old); got.Degraded || got.Value != "old" {
		t.Fatalf("enc:v1 written before the switch still opens: %+v", got)
	}
	if got := r.Resolve(ctx, st); got.Degraded || got.Value != "new" {
		t.Fatalf("keep value opens: %+v", got)
	}

	k.down = true
	if st, err := r.Put(ctx, Ref{}, "x"); err == nil || st != "" {
		t.Fatalf("keep selected and down: Put fails, got %q %v", st, err)
	}
	noClient := &Router{Builtin: Builtin{}, Selected: p.Selected}
	if _, err := noClient.Put(ctx, Ref{}, "x"); !errors.Is(err, ErrKeepNotConfigured) {
		t.Fatalf("keep selected without MESH_KEEP_*: want ErrKeepNotConfigured, got %v", err)
	}
}
