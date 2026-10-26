// Package secretstore is where the x-secret settings of an extension are
// sealed and opened (spec 06 §7). The stored form of a value says which
// provider owns it, so values written before and after a provider switch
// resolve side by side:
//
//   - "enc:v1:..." (or a legacy plaintext row) belongs to the builtin
//     provider, the Mesh-side AES key;
//   - "keep:<secret id>" is a reference to a value held by Keep.
//
// Which provider NEW values go to is the instance setting of the keep
// extension; the default stays builtin until the switch is made on purpose.
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

const (
	ProviderBuiltin = "builtin"
	ProviderKeep    = "keep"

	// KeepPrefix marks a stored value that is a reference into Keep.
	KeepPrefix = "keep:"

	// KeepExtensionID is the extension whose instance settings carry the
	// provider switch, SettingSecretProvider the key of that setting.
	KeepExtensionID       = "keep"
	SettingSecretProvider = "secret_provider"
)

// Ref names the setting a value belongs to. Keep derives the secret's scope
// and name from it.
type Ref struct {
	WorkspaceID uuid.UUID // uuid.Nil for an instance-level setting
	ExtensionID string
	Field       string
}

// Result is what Resolve returns. Degraded means the provider could not
// produce the value; Value is then empty and Reason says why. Callers must
// check Degraded before using Value: an empty token is not "no secret".
type Result struct {
	Value    string
	Degraded bool
	Reason   string
}

// Store seals and opens one setting value.
type Store interface {
	// Put stores plain and returns what is kept in the settings row.
	Put(ctx context.Context, ref Ref, plain string) (stored string, err error)
	// Resolve opens a stored value.
	Resolve(ctx context.Context, stored string) Result
}

// Builtin keeps values in the settings row, encrypted with the Mesh key.
type Builtin struct{}

func (Builtin) Put(_ context.Context, _ Ref, plain string) (string, error) {
	return encryption.Encrypt(plain)
}

func (Builtin) Resolve(_ context.Context, stored string) Result {
	plain, err := encryption.Decrypt(stored)
	if err != nil {
		return Result{Degraded: true, Reason: "builtin: " + err.Error()}
	}
	return Result{Value: plain}
}

// KeepClient is the part of Keep's REST API the provider needs, called as the
// extension's own actor.
type KeepClient interface {
	PutValue(ctx context.Context, ref Ref, plain string) (secretID string, err error)
	GetValue(ctx context.Context, secretID string) (string, error)
}

// ErrKeepNotConfigured is returned by a Keep provider that has no client.
var ErrKeepNotConfigured = errors.New("keep: no client configured for the secret store")

// Keep keeps values in Keep and the reference in the settings row.
type Keep struct{ Client KeepClient }

func (k Keep) Put(ctx context.Context, ref Ref, plain string) (string, error) {
	if k.Client == nil {
		return "", ErrKeepNotConfigured
	}
	id, err := k.Client.PutValue(ctx, ref, plain)
	if err != nil {
		return "", fmt.Errorf("keep: put: %w", err)
	}
	return KeepPrefix + id, nil
}

func (k Keep) Resolve(ctx context.Context, stored string) Result {
	if k.Client == nil {
		return Result{Degraded: true, Reason: ErrKeepNotConfigured.Error()}
	}
	v, err := k.Client.GetValue(ctx, strings.TrimPrefix(stored, KeepPrefix))
	if err != nil {
		return Result{Degraded: true, Reason: "keep unavailable: " + err.Error()}
	}
	return Result{Value: v}
}

// Router sends Put to the selected provider and Resolve to the owner of the
// stored value.
type Router struct {
	Builtin Store
	Keep    Store
	// Selected names the provider for new values; nil means builtin. An
	// error fails the Put: a value never lands on a provider nobody chose.
	Selected func(ctx context.Context) (string, error)
}

func (r *Router) Put(ctx context.Context, ref Ref, plain string) (string, error) {
	provider := ProviderBuiltin
	if r.Selected != nil {
		p, err := r.Selected(ctx)
		if err != nil {
			return "", fmt.Errorf("secret provider: %w", err)
		}
		provider = p
	}
	if provider == ProviderKeep {
		if r.Keep == nil {
			return "", ErrKeepNotConfigured
		}
		return r.Keep.Put(ctx, ref, plain)
	}
	return r.Builtin.Put(ctx, ref, plain)
}

func (r *Router) Resolve(ctx context.Context, stored string) Result {
	if strings.HasPrefix(stored, KeepPrefix) {
		if r.Keep == nil {
			return Result{Degraded: true, Reason: ErrKeepNotConfigured.Error()}
		}
		return r.Keep.Resolve(ctx, stored)
	}
	return r.Builtin.Resolve(ctx, stored)
}

// ParseProvider checks a secret_provider setting value; empty means the
// default, builtin.
func ParseProvider(v string) (string, error) {
	switch v {
	case "", ProviderBuiltin:
		return ProviderBuiltin, nil
	case ProviderKeep:
		return ProviderKeep, nil
	}
	return "", fmt.Errorf("%s must be %s or %s, got %q", SettingSecretProvider, ProviderBuiltin, ProviderKeep, v)
}

// ProviderSetting is a Router.Selected that reads the provider switch through
// Read and keeps the answer for TTL, so a Put does not cost a settings read
// while a change still takes effect within TTL. A failed read is returned,
// not replaced by a guess.
type ProviderSetting struct {
	Read func(ctx context.Context) (string, error)
	TTL  time.Duration
	Now  func() time.Time // nil means time.Now

	mu  sync.Mutex
	val string
	at  time.Time
}

func (p *ProviderSetting) Selected(ctx context.Context) (string, error) {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	p.mu.Lock()
	if p.val != "" && now().Sub(p.at) < p.TTL {
		v := p.val
		p.mu.Unlock()
		return v, nil
	}
	p.mu.Unlock()

	raw, err := p.Read(ctx)
	if err != nil {
		return "", err
	}
	v, err := ParseProvider(raw)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.val, p.at = v, now()
	p.mu.Unlock()
	return v, nil
}

var (
	mu  sync.RWMutex
	def Store = &Router{Builtin: Builtin{}}
)

// Default is the process-wide store; main replaces it once Keep is wired.
func Default() Store {
	mu.RLock()
	defer mu.RUnlock()
	return def
}

// SetDefault installs s and returns the previous store.
func SetDefault(s Store) Store {
	mu.Lock()
	defer mu.Unlock()
	prev := def
	def = s
	return prev
}

// Open resolves a stored value with the default store. A degraded result
// returns "" and the reason; the caller must not treat "" as a usable value.
func Open(ctx context.Context, stored string) (value, degradedReason string) {
	r := Default().Resolve(ctx, stored)
	if r.Degraded {
		return "", r.Reason
	}
	return r.Value, ""
}
