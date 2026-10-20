package catalog

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The catalog client refuses every kind of bad answer and keeps what it had.
func TestRefresh_RefusalsLeaveStateAlone(t *testing.T) {
	pub, priv := keypair(t)
	good := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	cases := map[string]struct {
		base func(srv *httptest.Server) string
		key  func() string
		spk  *fakeSpark
		want string
	}{
		"no spark url":  {base: func(*httptest.Server) string { return "" }, spk: good, want: "not configured"},
		"bad key":       {base: func(s *httptest.Server) string { return s.URL }, key: func() string { return "zzz" }, spk: good, want: ErrBadKey.Error()},
		"server error":  {base: func(s *httptest.Server) string { return s.URL }, spk: &fakeSpark{status: 500}, want: "500"},
		"no signature":  {base: func(s *httptest.Server) string { return s.URL }, spk: &fakeSpark{body: bodyV1}, want: ErrNoSignature.Error()},
		"not modified":  {base: func(s *httptest.Server) string { return s.URL }, spk: &fakeSpark{status: http.StatusNotModified}, want: "304"},
		"unknown shape": {base: func(s *httptest.Server) string { return s.URL }, spk: &fakeSpark{body: `{"schema":2}`, sig: sign(priv, `{"schema":2}`)}, want: ErrBadSchema.Error()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.spk)
			defer srv.Close()
			k := func() string { return base64Key(pub) }
			if tc.key != nil {
				k = tc.key
			}
			c := NewClient(func() string { return tc.base(srv) }, k)
			err := c.Refresh(context.Background(), 1)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			_, cerr := c.Current()
			assert.ErrorIs(t, cerr, ErrNoCatalog)
			_, lastErr := c.Status()
			assert.Error(t, lastErr)
		})
	}
}

func TestRefresh_EtagMismatchAndUnreachable(t *testing.T) {
	pub, priv := keypair(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"not-the-hash"`)
		w.Header().Set(SignatureHeader, sign(priv, bodyV1))
		_, _ = w.Write([]byte(bodyV1))
	}))
	c := newTestClient(srv, pub)
	err := c.Refresh(context.Background(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etag")

	srv.Close()
	require.Error(t, c.Refresh(context.Background(), 1), "a closed server is an error, not a crash")
}

func TestRun_RefreshesThenStopsWithContext(t *testing.T) {
	pub, priv := keypair(t)
	f := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newTestClient(srv, pub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, 1); close(done) }()
	require.Eventually(t, func() bool { _, err := c.Current(); return err == nil }, 3*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run must stop when its context ends")
	}
}

func TestCatalogLookupsOnNil(t *testing.T) {
	var c *Catalog
	_, hit := c.Blocks("x", "1")
	assert.False(t, hit)
	_, ok := c.Entry("x")
	assert.False(t, ok)
	cat := &Catalog{Extensions: []Entry{{ID: "a"}}}
	_, ok = cat.Entry("b")
	assert.False(t, ok)
	assert.Nil(t, Release{Capabilities: []byte(`["not","an","object"]`)}.Flatten())
	assert.Nil(t, Release{}.Flatten())
}

func base64Key(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

func (f *installFixture) actor() uuid.UUID { return uuid.New() }

func TestClientDetail_NoSparkURL(t *testing.T) {
	_, err := NewClient(func() string { return "" }, nil).Detail(context.Background(), "x")
	assert.Error(t, err)
}

func TestClientDetail_BadAnswers(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"not json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			_, err := NewClient(func() string { return srv.URL }, nil).Detail(context.Background(), "x")
			assert.Error(t, err)
		})
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := NewClient(func() string { return url }, nil).Detail(context.Background(), "x")
	assert.Error(t, err, "unreachable Spark")
}

func TestChecksResult(t *testing.T) {
	assert.Nil(t, checksResult(nil))
	assert.Equal(t, "passed", checksResult(map[string]any{"a": map[string]any{"status": "pass"}, "b": "skip"}).Result)
	assert.Equal(t, "warnings", checksResult(map[string]any{"a": map[string]any{"status": "warn"}}).Result)
	assert.Equal(t, "warnings", checksResult(map[string]any{"a": map[string]any{"status": "pass", "reasons": []any{"x"}}}).Result)
	assert.Equal(t, "failed", checksResult(map[string]any{"a": map[string]any{"status": "fail"}}).Result)
}

func TestInstallerSparkFailures(t *testing.T) {
	f := newInstallFixture(t, nil)
	ctx := context.Background()

	f.inst.SparkURL = func() string { return "" }
	_, err := f.inst.Detail(ctx, f.id())
	assert.Error(t, err, "no Spark url")

	for name, h := range map[string]http.HandlerFunc{
		"404":      http.NotFound,
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"bad json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("nope")) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			f.inst.SparkURL = func() string { return srv.URL }
			_, derr := f.inst.Detail(ctx, f.id())
			assert.Error(t, derr)
			_, ierr := f.inst.Install(ctx, f.actor(), f.id())
			assert.Error(t, ierr)
			assert.Empty(t, f.store.rows, "a failed install stores nothing")
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, 64)) }))
	defer srv.Close()
	f.inst.SparkURL = func() string { return srv.URL }
	_, err = f.inst.get(ctx, srv.URL, 8)
	assert.Error(t, err, "response over the limit")

	gone := httptest.NewServer(http.NotFoundHandler())
	u := gone.URL
	gone.Close()
	_, err = f.inst.get(ctx, u, 8)
	assert.Error(t, err, "unreachable")
}

func TestInstallAndUpdate_NoCatalogOrWrongRow(t *testing.T) {
	f := newInstallFixture(t, nil)
	ctx := context.Background()

	_, err := f.inst.Update(ctx, nil, "ghost", "")
	assert.Error(t, err, "unknown extension")

	f.store.rows["tg"] = &domain.InstalledExtension{ExtensionID: "tg", Source: domain.ExtensionSourceRepo}
	_, err = f.inst.Update(ctx, nil, "tg", "")
	assert.Error(t, err, "only catalog installs update from the catalog")

	f.installedAt("0.0.9", true)
	cur, id := f.inst.Catalog.current, f.id()
	f.inst.Catalog.current = nil
	_, err = f.inst.Update(ctx, nil, id, "")
	assert.Error(t, err, "no verified catalog yet")
	_, err = f.inst.Install(ctx, f.actor(), "another")
	assert.Error(t, err)
	list, err := f.inst.Updates(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.NotEmpty(t, list[0].Error)
	f.inst.Catalog.current = cur

	res, err := f.inst.UpdateAll(ctx, nil)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "updated", string(res[0].Outcome))
}

type failingStore struct {
	memStore
	listErr, setErr error
}

func (s *failingStore) List(ctx context.Context) ([]domain.InstalledExtension, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memStore.List(ctx)
}

func (s *failingStore) SetBlocked(ctx context.Context, id string, b bool, r string) (bool, error) {
	if s.setErr != nil {
		return false, s.setErr
	}
	return s.memStore.SetBlocked(ctx, id, b, r)
}

type failingJournal struct{}

func (failingJournal) AppendJournal(context.Context, *domain.InstanceExtensionJournalEntry) error {
	return errors.New("journal down")
}

func TestEnforcer_StoreAndJournalFailuresDoNotStopThePass(t *testing.T) {
	rows := map[string]*domain.InstalledExtension{"hello": {ExtensionID: "hello", Version: "1", Source: domain.ExtensionSourceCatalog}}
	cat := &Catalog{Blocklist: []Block{{ID: "hello", Reason: "x"}}}
	ctx := context.Background()

	(&Enforcer{Store: &failingStore{memStore: memStore{rows: rows}, listErr: errors.New("db")}}).Apply(ctx, cat)
	(&Enforcer{Store: &failingStore{memStore: memStore{rows: rows}, setErr: errors.New("db")}}).Apply(ctx, cat)
	assert.False(t, rows["hello"].Blocked)

	notified := 0
	(&Enforcer{Store: &failingStore{memStore: memStore{rows: rows}}, Journal: failingJournal{},
		Notify: func(context.Context, string, string, bool) { notified++ }}).Apply(ctx, cat)
	assert.True(t, rows["hello"].Blocked, "a journal failure must not undo the switch-off")
	assert.Equal(t, 1, notified)
}
