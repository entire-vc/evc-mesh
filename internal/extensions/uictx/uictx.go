// Package uictx is Mesh as an identity provider for the iframe screens of
// external extensions: an instance signing key (ES256) that rotates with an
// overlap, the JWKS that publishes it, and the short-lived context JWT that
// tells a screen who opened it and where.
//
// The private key is stored encrypted and never leaves this package; the JWKS
// carries public keys only. A key that has been replaced stays in the JWKS for
// the overlap, so a token signed just before a rotation still verifies.
package uictx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

const (
	// TokenTTL is the lifetime of a context token. A screen exchanges it on load,
	// so a minute is plenty and bounds a leaked URL.
	TokenTTL = 60 * time.Second
	// DefaultRotateEvery is how old the active key gets before it is replaced.
	DefaultRotateEvery = 30 * 24 * time.Hour
	// DefaultOverlap is how long a replaced key stays published. It must exceed
	// TokenTTL plus the caches of the verifiers; a day is generous and cheap.
	DefaultOverlap = 24 * time.Hour

	cacheTTL = time.Minute
)

// ErrKeyUnavailable means no signing key can be produced: the instance has no
// encryption key, so a private key cannot be stored safely.
var ErrKeyUnavailable = errors.New("instance signing key unavailable: encryption key is not configured")

// KeyRow is one stored signing key.
type KeyRow struct {
	KID        string
	PrivateEnc string
	PublicJWK  []byte
	CreatedAt  time.Time
	RetiredAt  *time.Time
}

// Store persists signing keys.
type Store interface {
	// List returns every key that has not been purged, newest first.
	List(ctx context.Context) ([]KeyRow, error)
	// Activate retires the current active key (if any) at `at` and inserts row as
	// the new active one, atomically. It returns ErrConflict when another
	// instance activated a key first.
	Activate(ctx context.Context, row KeyRow, at time.Time) error
	// PurgeRetiredBefore deletes keys retired before t.
	PurgeRetiredBefore(ctx context.Context, t time.Time) error
}

// ErrConflict is returned by Store.Activate when it lost a race.
var ErrConflict = errors.New("signing key activated concurrently")

// Claims is what a screen learns about the person who opened it.
type Claims struct {
	UserID      uuid.UUID
	Email       string
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	TaskID      *uuid.UUID
	Roles       []string
	Audience    string // extension id
}

// Service signs context tokens and publishes the keys.
type Service struct {
	store       Store
	issuer      string
	rotateEvery time.Duration
	overlap     time.Duration
	now         func() time.Time

	mu        sync.Mutex
	loadedAt  time.Time
	active    *activeKey
	published []byte
}

type activeKey struct {
	kid     string
	key     *ecdsa.PrivateKey
	created time.Time
}

// New builds a Service. issuer is the public origin of this Mesh instance.
func New(store Store, issuer string) *Service {
	return &Service{store: store, issuer: issuer, rotateEvery: DefaultRotateEvery, overlap: DefaultOverlap, now: time.Now}
}

// WithTiming overrides rotation age and overlap (tests, operators).
func (s *Service) WithTiming(rotateEvery, overlap time.Duration) *Service {
	s.rotateEvery, s.overlap = rotateEvery, overlap
	return s
}

// Issuer is the `iss` of every token.
func (s *Service) Issuer() string { return s.issuer }

// Issue signs a context token. It returns the token and its expiry.
func (s *Service) Issue(ctx context.Context, c Claims) (string, time.Time, error) {
	ak, err := s.activeKey(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	exp := now.Add(TokenTTL)
	roles := c.Roles
	if roles == nil {
		roles = []string{}
	}
	m := jwt.MapClaims{
		"iss":          s.issuer,
		"sub":          c.UserID.String(),
		"aud":          c.Audience,
		"email":        c.Email,
		"workspace_id": c.WorkspaceID.String(),
		"roles":        roles,
		"iat":          now.Unix(),
		"nbf":          now.Unix(),
		"exp":          exp.Unix(),
		"jti":          uuid.NewString(),
	}
	if c.ProjectID != nil {
		m["project_id"] = c.ProjectID.String()
	}
	if c.TaskID != nil {
		m["task_id"] = c.TaskID.String()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, m)
	tok.Header["kid"] = ak.kid
	signed, err := tok.SignedString(ak.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign context token: %w", err)
	}
	return signed, exp, nil
}

// JWKS returns the public key set: the active key and any key replaced within
// the overlap. It rotates an aged key first, so the set a verifier fetches
// always contains what Issue is about to sign with.
func (s *Service) JWKS(ctx context.Context) ([]byte, error) {
	if _, err := s.activeKey(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published, nil
}

// Rotate replaces the active key now. The old one stays published for the overlap.
func (s *Service) Rotate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked(ctx)
}

func (s *Service) activeKey(ctx context.Context) (*activeKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.active != nil && now.Sub(s.loadedAt) < cacheTTL && now.Sub(s.active.created) < s.rotateEvery {
		return s.active, nil
	}
	if err := s.loadLocked(ctx); err != nil {
		return nil, err
	}
	if s.active == nil || now.Sub(s.active.created) >= s.rotateEvery {
		if err := s.rotateLocked(ctx); err != nil {
			return nil, err
		}
	}
	if s.active == nil {
		return nil, errors.New("no active signing key after rotation")
	}
	return s.active, nil
}

func (s *Service) rotateLocked(ctx context.Context) error {
	row, err := newKeyRow(s.now())
	if err != nil {
		return err
	}
	err = s.store.Activate(ctx, row, s.now())
	if err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("store signing key: %w", err)
	}
	// On a lost race the winner's key is the active one: load it.
	return s.loadLocked(ctx)
}

func (s *Service) loadLocked(ctx context.Context) error {
	rows, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("list signing keys: %w", err)
	}
	now := s.now()
	var act *activeKey
	keys := make([]json.RawMessage, 0, len(rows))
	for _, r := range rows {
		if r.RetiredAt == nil {
			if act == nil {
				k, kerr := openKey(r)
				if kerr != nil {
					return kerr
				}
				act = &activeKey{kid: r.KID, key: k, created: r.CreatedAt}
				keys = append(keys, r.PublicJWK)
			}
			continue
		}
		if now.Sub(*r.RetiredAt) < s.overlap {
			keys = append(keys, r.PublicJWK)
		}
	}
	doc, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		return err
	}
	s.active, s.published, s.loadedAt = act, doc, now
	// Housekeeping: a key past its overlap is of no use to anyone.
	_ = s.store.PurgeRetiredBefore(ctx, now.Add(-2*s.overlap))
	return nil
}

func newKeyRow(now time.Time) (KeyRow, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return KeyRow{}, fmt.Errorf("generate signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return KeyRow{}, err
	}
	enc, err := encryption.Encrypt(base64.StdEncoding.EncodeToString(der))
	if err != nil {
		return KeyRow{}, fmt.Errorf("encrypt signing key: %w", err)
	}
	if !encryption.IsEncrypted(enc) {
		return KeyRow{}, ErrKeyUnavailable
	}
	x, y := b64(k.X.FillBytes(make([]byte, 32))), b64(k.Y.FillBytes(make([]byte, 32)))
	sum := sha256.Sum256([]byte(x + "." + y))
	kid := hex.EncodeToString(sum[:8])
	jwk, err := json.Marshal(map[string]string{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid, "x": x, "y": y,
	})
	if err != nil {
		return KeyRow{}, err
	}
	return KeyRow{KID: kid, PrivateEnc: enc, PublicJWK: jwk, CreatedAt: now}, nil
}

func openKey(r KeyRow) (*ecdsa.PrivateKey, error) {
	plain, err := encryption.Decrypt(r.PrivateEnc)
	if err != nil {
		if errors.Is(err, encryption.ErrUndecryptable) {
			// Missing or rotated instance key: the stored key is unusable
			// until it is restored, which is "unavailable", not a server bug.
			return nil, fmt.Errorf("open signing key %s: %w (%v)", r.KID, ErrKeyUnavailable, err)
		}
		return nil, fmt.Errorf("open signing key %s: %w", r.KID, err)
	}
	der, err := base64.StdEncoding.DecodeString(plain)
	if err != nil {
		return nil, fmt.Errorf("decode signing key %s: %w", r.KID, err)
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse signing key %s: %w", r.KID, err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key %s is not ECDSA", r.KID)
	}
	return ek, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
