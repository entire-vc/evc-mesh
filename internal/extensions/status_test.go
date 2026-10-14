package extensions_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// notMigrated lists extensions still on the old resolver path. Each moves into
// BuiltinProviders with its transfer card; the skip is removed then.
var notMigrated = map[string]string{
	"spark":     "перенос Spark — карточка W1.5 #df8ee66a",
	"gitlab":    "перенос VCS-провайдеров — карточки W1.6–W1.9",
	"github":    "перенос VCS-провайдеров — карточки W1.6–W1.9",
	"teamrelay": "перенос Team Relay — карточки W1.6–W1.9",
}

type healthProvider struct {
	stubProvider
	calls *int
	ok    bool
}

func (p healthProvider) Health(context.Context, extensions.Resolved) extensions.HealthReport {
	*p.calls++
	if p.ok {
		return extensions.HealthReport{OK: true}
	}
	return extensions.HealthReport{Message: "boom"}
}

type comboStores struct {
	conn *extensions.Connection
	inst *extensions.InstanceSetting
}

func (c *comboStores) Connection(context.Context, uuid.UUID, string) (*extensions.Connection, error) {
	return c.conn, nil
}

func (c *comboStores) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return c.inst, nil
}

type combo struct {
	name string
	conn *extensions.Connection
	inst *extensions.InstanceSetting
}

func combos() []combo {
	rows := map[string]*extensions.Connection{
		"row-none":     nil,
		"row-active":   {Settings: map[string]any{"k": "ws"}, IsActive: true},
		"row-inactive": {Settings: map[string]any{"k": "ws"}, IsActive: false},
	}
	insts := map[string]*extensions.InstanceSetting{
		"inst-none":     nil,
		"inst-enabled":  {Settings: map[string]any{"k": "inst"}, Enabled: true},
		"inst-disabled": {Settings: map[string]any{"k": "inst"}, Enabled: false},
	}
	var out []combo
	for rn, r := range rows {
		for in, i := range insts {
			out = append(out, combo{name: rn + "/" + in, conn: r, inst: i})
		}
	}
	return out
}

// providersUnderTest is every registered builtin plus the stub, built over the
// shared combo stores.
func providersUnderTest(t *testing.T, st *comboStores) []extensions.Provider {
	t.Helper()
	ps := extensions.BuiltinProviders(extensions.Deps{Conns: st, Inst: st})
	stub, _ := newStub(t, nil, nil)
	stub.Conns, stub.Inst = st, st
	return append(ps, stub)
}

// parityMismatches runs every combination for every provider and reports where
// the status the API shows differs from what the runtime (Resolve) acts on.
func parityMismatches(t *testing.T, status func(reg *extensions.Registry, ws uuid.UUID, id string) (extensions.Resolved, error)) []string {
	t.Helper()
	ws := uuid.New()
	var bad []string
	for _, c := range combos() {
		st := &comboStores{conn: c.conn, inst: c.inst}
		reg := extensions.NewRegistry()
		for _, p := range providersUnderTest(t, st) {
			require.NoError(t, reg.Register(p))
		}
		for _, p := range reg.List() {
			id := p.Manifest().ID
			runtime, err := p.Resolve(context.Background(), ws)
			require.NoError(t, err)
			api, err := status(reg, ws, id)
			require.NoError(t, err)
			if api.Enabled != runtime.Enabled || api.Source != runtime.Source {
				bad = append(bad, fmt.Sprintf("%s %s: api{enabled=%v source=%s} runtime{enabled=%v source=%s}",
					id, c.name, api.Enabled, api.Source, runtime.Enabled, runtime.Source))
			}
		}
	}
	return bad
}

// Extensions not yet on the registry are listed as skipped subtests with the
// card that moves them. Once an extension is in BuiltinProviders its entry
// here must go, otherwise this test fails.
func TestResolveStatus_NotMigratedAreSkipped(t *testing.T) {
	for _, p := range extensions.BuiltinProviders(extensions.Deps{}) {
		if why, still := notMigrated[p.Manifest().ID]; still {
			t.Errorf("%s is registered but still in notMigrated (%s): remove it", p.Manifest().ID, why)
		}
	}
	for id, why := range notMigrated {
		t.Run(id, func(t *testing.T) { t.Skip(why) })
	}
}

func TestResolveStatus_MatchesRuntime(t *testing.T) {
	bad := parityMismatches(t, func(reg *extensions.Registry, ws uuid.UUID, id string) (extensions.Resolved, error) {
		s, err := reg.ResolveStatus(context.Background(), ws, id)
		return extensions.Resolved{Enabled: s.Enabled, Source: s.Source}, err
	})
	require.Empty(t, bad)
}

// Red control: a UI that decides "by the row only" must be caught.
func TestResolveStatus_RedControl_RowOnlyUI(t *testing.T) {
	bad := parityMismatches(t, func(reg *extensions.Registry, ws uuid.UUID, id string) (extensions.Resolved, error) {
		s, err := reg.ResolveStatus(context.Background(), ws, id)
		if s.Source != extensions.SourceWorkspace {
			return extensions.Resolved{Enabled: false, Source: extensions.SourceNone}, err
		}
		return extensions.Resolved{Enabled: s.Enabled, Source: s.Source}, err
	})
	require.NotEmpty(t, bad, "row-only UI must disagree with the runtime for instance-level cases")
}

func newHealthReg(t *testing.T, ok bool) (reg *extensions.Registry, calls *int, id string, now *time.Time) {
	t.Helper()
	n := 0
	stub, _ := newStub(t, &extensions.Connection{IsActive: true}, nil)
	reg = extensions.NewRegistry()
	require.NoError(t, reg.Register(healthProvider{stubProvider: stub, calls: &n, ok: ok}))
	t0 := time.Unix(1_000_000, 0)
	extensions.SetClock(reg, func() time.Time { return t0 })
	return reg, &n, stub.Manifest().ID, &t0
}

func TestResolveStatus_HealthCachedFor60s(t *testing.T) {
	reg, calls, id, now := newHealthReg(t, false)
	ws := uuid.New()
	ctx := context.Background()

	s, err := reg.ResolveStatus(ctx, ws, id)
	require.NoError(t, err)
	require.False(t, s.Health.OK)
	require.Equal(t, "boom", s.LastError)

	*now = now.Add(extensions.HealthTTL - time.Second)
	_, err = reg.ResolveStatus(ctx, ws, id)
	require.NoError(t, err)
	require.Equal(t, 1, *calls, "within TTL health is served from cache")

	*now = now.Add(2 * time.Second)
	_, err = reg.ResolveStatus(ctx, ws, id)
	require.NoError(t, err)
	require.Equal(t, 2, *calls, "after TTL health is recomputed")

	_, err = reg.TestConnection(ctx, ws, id)
	require.NoError(t, err)
	require.Equal(t, 3, *calls, "TestConnection forces a recheck inside the TTL")
}

func TestResolveStatus_DisabledSkipsHealthAndFillsFields(t *testing.T) {
	calls := 0
	stub, _ := newStub(t, &extensions.Connection{IsActive: false}, nil)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(healthProvider{stubProvider: stub, calls: &calls}))
	s, err := reg.ResolveStatus(context.Background(), uuid.New(), stub.Manifest().ID)
	require.NoError(t, err)
	require.False(t, s.Enabled)
	require.Equal(t, extensions.SourceWorkspace, s.Source)
	require.Equal(t, stub.Manifest().Version, s.Version)
	require.Zero(t, calls)
	require.NotEmpty(t, s.ConsentedCapabilities, "declared capabilities are consented for builtin")
}

type fakeConsents struct {
	caps []string
	err  error
}

func (f fakeConsents) Consented(context.Context, uuid.UUID, string) ([]string, error) {
	return f.caps, f.err
}

func TestResolveStatus_ConsentStoreAndErrors(t *testing.T) {
	stub, _ := newStub(t, &extensions.Connection{IsActive: true}, nil)
	reg := extensions.NewRegistry().WithConsentStore(fakeConsents{caps: []string{"api:read"}})
	require.NoError(t, reg.Register(stub))
	s, err := reg.ResolveStatus(context.Background(), uuid.New(), stub.Manifest().ID)
	require.NoError(t, err)
	require.Equal(t, []string{"api:read"}, s.ConsentedCapabilities)

	_, err = reg.ResolveStatus(context.Background(), uuid.New(), "nope")
	require.Error(t, err)

	bad := extensions.NewRegistry().WithConsentStore(fakeConsents{err: errors.New("db down")})
	require.NoError(t, bad.Register(stub))
	_, err = bad.ResolveStatus(context.Background(), uuid.New(), stub.Manifest().ID)
	require.ErrorContains(t, err, "db down")

	failing, _ := newStub(t, nil, nil)
	failing.Conns = fakeConns{err: errors.New("conn down")}
	freg := extensions.NewRegistry()
	require.NoError(t, freg.Register(failing))
	_, err = freg.ResolveStatus(context.Background(), uuid.New(), failing.Manifest().ID)
	require.ErrorContains(t, err, "conn down")
}
