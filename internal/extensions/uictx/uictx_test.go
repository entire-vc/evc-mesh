package uictx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// memStore is an in-memory Store with the same single-active-key rule as the table.
type memStore struct {
	mu   sync.Mutex
	rows []KeyRow
}

func (m *memStore) List(context.Context) ([]KeyRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]KeyRow(nil), m.rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memStore) Activate(_ context.Context, row KeyRow, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].RetiredAt == nil {
			t := at
			m.rows[i].RetiredAt = &t
		}
	}
	m.rows = append(m.rows, row)
	return nil
}

func (m *memStore) PurgeRetiredBefore(_ context.Context, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.rows[:0]
	for _, r := range m.rows {
		if r.RetiredAt == nil || !r.RetiredAt.Before(t) {
			kept = append(kept, r)
		}
	}
	m.rows = kept
	return nil
}

func useKey(t *testing.T) {
	t.Helper()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	encryption.ResetForTest()
	t.Cleanup(encryption.ResetForTest)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSvc(t *testing.T) (*Service, *clock, *memStore) {
	t.Helper()
	useKey(t)
	st := &memStore{}
	clk := &clock{t: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	s := New(st, "https://mesh.example").WithTiming(30*24*time.Hour, 24*time.Hour)
	s.now = clk.now
	return s, clk, st
}

// verify is what an extension does: fetch the JWKS, pick the key by kid, check
// signature, issuer, audience and exp.
func verify(t *testing.T, jwks []byte, token, aud string, now time.Time) (jwt.MapClaims, error) {
	t.Helper()
	var doc struct {
		Keys []struct{ Kid, X, Y string } `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(jwks, &doc))
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(tok *jwt.Token) (any, error) {
		kid, _ := tok.Header["kid"].(string)
		for _, k := range doc.Keys {
			if k.Kid != kid {
				continue
			}
			x, _ := base64.RawURLEncoding.DecodeString(k.X)
			y, _ := base64.RawURLEncoding.DecodeString(k.Y)
			return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, nil
		}
		return nil, errors.New("unknown kid")
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer("https://mesh.example"), jwt.WithAudience(aud),
		jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithExpirationRequired())
	return claims, err
}

func claimsFor() Claims {
	proj, task := uuid.New(), uuid.New()
	return Claims{UserID: uuid.New(), Email: "a@b.c", WorkspaceID: uuid.New(), ProjectID: &proj, TaskID: &task, Roles: []string{"admin"}, Audience: "hello"}
}

func TestIssue_VerifiesAgainstJWKS(t *testing.T) {
	s, clk, _ := newSvc(t)
	c := claimsFor()
	tok, exp, err := s.Issue(context.Background(), c)
	require.NoError(t, err)
	jwks, err := s.JWKS(context.Background())
	require.NoError(t, err)

	got, err := verify(t, jwks, tok, "hello", clk.t)
	require.NoError(t, err)
	assert.Equal(t, c.UserID.String(), got["sub"])
	assert.Equal(t, "a@b.c", got["email"])
	assert.Equal(t, c.WorkspaceID.String(), got["workspace_id"])
	assert.Equal(t, c.ProjectID.String(), got["project_id"])
	assert.Equal(t, c.TaskID.String(), got["task_id"])
	assert.Equal(t, []any{"admin"}, got["roles"])
	assert.LessOrEqual(t, exp.Sub(clk.t), 60*time.Second)
	assert.NotContains(t, string(jwks), `"d"`, "the JWKS must never carry private key material")
}

func TestIssue_RedControls(t *testing.T) {
	s, clk, _ := newSvc(t)
	tok, _, err := s.Issue(context.Background(), claimsFor())
	require.NoError(t, err)
	jwks, _ := s.JWKS(context.Background())

	_, err = verify(t, jwks, tok, "hello", clk.t.Add(61*time.Second))
	assert.Error(t, err, "a token past exp must be rejected")
	_, err = verify(t, jwks, tok, "other-extension", clk.t)
	assert.Error(t, err, "a token minted for one extension must not verify for another")
	_, err = verify(t, jwks, tok[:len(tok)-3]+"AAA", "hello", clk.t)
	assert.Error(t, err, "a tampered signature must be rejected")
}

func TestRotation_OverlapThenDrop(t *testing.T) {
	s, clk, st := newSvc(t)
	old, _, err := s.Issue(context.Background(), claimsFor())
	require.NoError(t, err)

	require.NoError(t, s.Rotate(context.Background()))
	fresh, _, err := s.Issue(context.Background(), claimsFor())
	require.NoError(t, err)
	assert.NotEqual(t, kid(t, old), kid(t, fresh), "rotation must change the signing key")

	jwks, _ := s.JWKS(context.Background())
	_, err = verify(t, jwks, old, "hello", clk.t)
	assert.NoError(t, err, "a token signed just before the rotation still verifies during the overlap")
	_, err = verify(t, jwks, fresh, "hello", clk.t)
	assert.NoError(t, err)

	clk.t = clk.t.Add(25 * time.Hour) // overlap over
	s.mu.Lock()
	s.loadedAt = time.Time{}
	s.mu.Unlock()
	jwks, err = s.JWKS(context.Background())
	require.NoError(t, err)
	_, err = verify(t, jwks, old, "hello", clk.t.Add(-24*time.Hour))
	assert.Error(t, err, "the replaced key leaves the JWKS after the overlap")
	assert.Len(t, st.rows, 2)
}

func TestAgedKeyRotatesOnIssue(t *testing.T) {
	s, clk, st := newSvc(t)
	first, _, err := s.Issue(context.Background(), claimsFor())
	require.NoError(t, err)
	clk.t = clk.t.Add(31 * 24 * time.Hour)
	second, _, err := s.Issue(context.Background(), claimsFor())
	require.NoError(t, err)
	assert.NotEqual(t, kid(t, first), kid(t, second))
	active := 0
	for _, r := range st.rows {
		if r.RetiredAt == nil {
			active++
		}
	}
	assert.Equal(t, 1, active)
}

func TestNoEncryptionKey_FailsClosed(t *testing.T) {
	t.Setenv(encryption.EnvKey, "")
	encryption.ResetForTest()
	t.Cleanup(encryption.ResetForTest)
	st := &memStore{}
	s := New(st, "https://mesh.example")
	_, _, err := s.Issue(context.Background(), claimsFor())
	assert.ErrorIs(t, err, ErrKeyUnavailable)
	assert.Empty(t, st.rows, "a private key must never be stored in plaintext")
}

func kid(t *testing.T, token string) string {
	t.Helper()
	tok, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	require.NoError(t, err)
	k, _ := tok.Header["kid"].(string)
	return k
}

// flakyStore wraps memStore and fails the calls asked for.
type flakyStore struct {
	*memStore
	listErr, activateErr error
}

func (f *flakyStore) List(ctx context.Context) ([]KeyRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.memStore.List(ctx)
}

func (f *flakyStore) Activate(ctx context.Context, row KeyRow, at time.Time) error {
	if f.activateErr != nil {
		return f.activateErr
	}
	return f.memStore.Activate(ctx, row, at)
}

func TestStoreFailuresAreSurfaced(t *testing.T) {
	useKey(t)
	t.Run("list fails", func(t *testing.T) {
		s := New(&flakyStore{memStore: &memStore{}, listErr: errors.New("db down")}, "https://mesh.example")
		_, _, err := s.Issue(context.Background(), claimsFor())
		require.Error(t, err)
		_, err = s.JWKS(context.Background())
		require.Error(t, err)
	})
	t.Run("activate fails", func(t *testing.T) {
		s := New(&flakyStore{memStore: &memStore{}, activateErr: errors.New("db down")}, "https://mesh.example")
		_, _, err := s.Issue(context.Background(), claimsFor())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "store signing key")
		require.Error(t, s.Rotate(context.Background()))
	})
	t.Run("a lost race loads the winner", func(t *testing.T) {
		st := &flakyStore{memStore: &memStore{}, activateErr: ErrConflict}
		s := New(st, "https://mesh.example")
		_, _, err := s.Issue(context.Background(), claimsFor())
		require.Error(t, err, "a conflict with no winner to load must be an error, never a nil key")
	})
}

func TestIssuerAndExplicitRotate(t *testing.T) {
	s, clk, _ := newSvc(t)
	assert.Equal(t, "https://mesh.example", s.Issuer())
	ctx := context.Background()
	tok, _, err := s.Issue(ctx, claimsFor())
	require.NoError(t, err)
	require.NoError(t, s.Rotate(ctx))
	jwks, err := s.JWKS(ctx)
	require.NoError(t, err)
	_, err = verify(t, jwks, tok, "hello", clk.t)
	require.NoError(t, err, "a token signed before the rotation still verifies during the overlap")
}

func TestUnusableStoredKeysFailClosed(t *testing.T) {
	useKey(t)
	good, err := newKeyRow(time.Now())
	require.NoError(t, err)

	badBase64, err := encryption.Encrypt("%%% not base64 %%%")
	require.NoError(t, err)
	badDER, err := encryption.Encrypt(base64.StdEncoding.EncodeToString([]byte("not a key")))
	require.NoError(t, err)

	for name, enc := range map[string]string{
		"not decryptable": "enc:v1:AAAA",
		"not base64":      badBase64,
		"not a DER key":   badDER,
	} {
		t.Run(name, func(t *testing.T) {
			row := good
			row.PrivateEnc = enc
			s := New(&memStore{rows: []KeyRow{row}}, "https://mesh.example")
			_, _, err := s.Issue(context.Background(), claimsFor())
			require.Error(t, err, "a key that cannot be opened must never fall back to a new one silently")
		})
	}
}

// A signing key already in the DB must not turn a missing or wrong encryption
// key into an opaque 500: both paths report ErrKeyUnavailable (handler: 503).
func TestStoredKey_EncryptionKeyMissingOrWrong_Unavailable(t *testing.T) {
	s, _, st := newSvc(t)
	_, _, err := s.Issue(context.Background(), claimsFor())
	require.NoError(t, err)
	require.Len(t, st.rows, 1)

	cases := map[string]string{
		"missing": "",
		"wrong":   "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(encryption.EnvKey, key)
			encryption.ResetForTest()
			t.Cleanup(encryption.ResetForTest)
			fresh := New(st, "https://mesh.example")
			_, _, err := fresh.Issue(context.Background(), claimsFor())
			assert.ErrorIs(t, err, ErrKeyUnavailable)
			_, err = New(st, "https://mesh.example").JWKS(context.Background())
			assert.ErrorIs(t, err, ErrKeyUnavailable)
		})
	}
}
