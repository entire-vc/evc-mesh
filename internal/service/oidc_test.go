package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unit tests of the OpenID Connect key set and static-client config (card
// an internal ticket). No database.

func newTestRSAKey(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	return k
}

func writePEM(t *testing.T, dir, name, typ string, der []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600))
	return p
}

// RFC 7638 §3.1 example: the thumbprint of this key is fixed by the RFC, so
// the kid is the standard one any client library computes.
func TestOIDC_KIDIsRFC7638Thumbprint(t *testing.T) {
	n, err := base64.RawURLEncoding.DecodeString("0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw")
	require.NoError(t, err)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}
	assert.Equal(t, "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs", RSAKeyThumbprint(pub))
}

// Rotation: two keys, the active one signs, the JWKS publishes both kids, and
// a token signed by the earlier key still verifies against the JWKS.
func TestOIDC_JWKSRotationPublishesBothKIDs(t *testing.T) {
	oldKey := newTestRSAKey(t, 2048)
	newKey := newTestRSAKey(t, 2048)

	before, err := NewOIDCKeySet(oldKey)
	require.NoError(t, err)
	oldToken, err := before.signIDToken("https://mesh.example", "u", "keep", "", time.Now())
	require.NoError(t, err)

	ks, err := NewOIDCKeySet(newKey, &oldKey.PublicKey, &newKey.PublicKey)
	require.NoError(t, err)
	jwks := ks.JWKS()
	require.Len(t, jwks.Keys, 2, "active + earlier key, the duplicate of the active key listed once")
	assert.Equal(t, RSAKeyThumbprint(&newKey.PublicKey), jwks.Keys[0].Kid, "active key first")
	assert.Equal(t, RSAKeyThumbprint(&oldKey.PublicKey), jwks.Keys[1].Kid)
	for _, k := range jwks.Keys {
		assert.Equal(t, "RSA", k.Kty)
		assert.Equal(t, "sig", k.Use)
		assert.Equal(t, "RS256", k.Alg)
	}

	newToken, err := ks.signIDToken("https://mesh.example", "u", "keep", "n1", time.Now())
	require.NoError(t, err)
	keyFunc := func(tok *jwt.Token) (interface{}, error) {
		kid, _ := tok.Header["kid"].(string)
		if pub := ks.PublicKey(kid); pub != nil {
			return pub, nil
		}
		return nil, jwt.ErrTokenUnverifiable
	}
	parsed, err := jwt.Parse(newToken, keyFunc, jwt.WithValidMethods([]string{"RS256"}))
	require.NoError(t, err)
	assert.Equal(t, ks.ActiveKID(), parsed.Header["kid"], "signed by the active key")
	assert.Equal(t, "JWT", parsed.Header["typ"])

	_, err = jwt.Parse(oldToken, keyFunc, jwt.WithValidMethods([]string{"RS256"}))
	assert.NoError(t, err, "a token of the earlier key still verifies through the JWKS")

	// Negative control: without the earlier key published it does not.
	only, err := NewOIDCKeySet(newKey)
	require.NoError(t, err)
	_, err = jwt.Parse(oldToken, func(tok *jwt.Token) (interface{}, error) {
		kid, _ := tok.Header["kid"].(string)
		if pub := only.PublicKey(kid); pub != nil {
			return pub, nil
		}
		return nil, jwt.ErrTokenUnverifiable
	}, jwt.WithValidMethods([]string{"RS256"}))
	assert.Error(t, err)
}

func TestOIDC_IDTokenClaims(t *testing.T) {
	ks, err := NewOIDCKeySet(newTestRSAKey(t, 2048))
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	tok, err := ks.signIDToken("https://mesh.example", "8c5f…user", "keep-console", "abc", now)
	require.NoError(t, err)
	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tok, claims)
	require.NoError(t, err)
	assert.Equal(t, "https://mesh.example", claims["iss"])
	assert.Equal(t, "8c5f…user", claims["sub"])
	assert.Equal(t, "keep-console", claims["aud"])
	assert.Equal(t, "abc", claims["nonce"])
	assert.EqualValues(t, now.Unix(), claims["iat"])
	assert.EqualValues(t, now.Add(5*time.Minute).Unix(), claims["exp"])
	assert.NotContains(t, claims, "email")

	noNonce, err := ks.signIDToken("https://mesh.example", "u", "keep", "", now)
	require.NoError(t, err)
	claims = jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(noNonce, claims)
	require.NoError(t, err)
	assert.NotContains(t, claims, "nonce", "no nonce in authorize, none in the token")
}

func TestOIDC_LoadKeySet(t *testing.T) {
	dir := t.TempDir()
	active := newTestRSAKey(t, 2048)
	earlier := newTestRSAKey(t, 2048)

	pkcs8, err := x509.MarshalPKCS8PrivateKey(active)
	require.NoError(t, err)
	activePath := writePEM(t, dir, "active.pem", "PRIVATE KEY", pkcs8)
	pkcs1Path := writePEM(t, dir, "active-pkcs1.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(active))
	pubDER, err := x509.MarshalPKIXPublicKey(&earlier.PublicKey)
	require.NoError(t, err)
	earlierPath := writePEM(t, dir, "earlier.pub.pem", "PUBLIC KEY", pubDER)

	t.Run("no key is off", func(t *testing.T) {
		ks, err := LoadOIDCKeySet("", nil)
		require.NoError(t, err)
		assert.Nil(t, ks)
	})
	t.Run("PKCS#8 active + PKIX earlier", func(t *testing.T) {
		ks, err := LoadOIDCKeySet(activePath, []string{earlierPath, " "})
		require.NoError(t, err)
		require.NotNil(t, ks)
		assert.Equal(t, RSAKeyThumbprint(&active.PublicKey), ks.ActiveKID())
		assert.Len(t, ks.JWKS().Keys, 2)
	})
	t.Run("PKCS#1 active", func(t *testing.T) {
		ks, err := LoadOIDCKeySet(pkcs1Path, nil)
		require.NoError(t, err)
		assert.Equal(t, RSAKeyThumbprint(&active.PublicKey), ks.ActiveKID())
	})
	t.Run("missing file fails", func(t *testing.T) {
		_, err := LoadOIDCKeySet(filepath.Join(dir, "nope.pem"), nil)
		assert.ErrorContains(t, err, "MESH_OIDC_SIGNING_KEY_FILE")
	})
	t.Run("not a PEM fails", func(t *testing.T) {
		p := filepath.Join(dir, "garbage.pem")
		require.NoError(t, os.WriteFile(p, []byte("not a key"), 0o600))
		_, err := LoadOIDCKeySet(p, nil)
		assert.ErrorContains(t, err, "no PEM block")
	})
	t.Run("public key as active fails", func(t *testing.T) {
		_, err := LoadOIDCKeySet(earlierPath, nil)
		assert.ErrorContains(t, err, "not an RSA private key")
	})
	t.Run("short key fails", func(t *testing.T) {
		small := writePEM(t, dir, "small.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(newTestRSAKey(t, 1024)))
		_, err := LoadOIDCKeySet(small, nil)
		assert.ErrorContains(t, err, "at least 2048")
	})
	t.Run("published keys without an active key fail", func(t *testing.T) {
		_, err := LoadOIDCKeySet("", []string{earlierPath})
		assert.ErrorContains(t, err, "without MESH_OIDC_SIGNING_KEY_FILE")
	})
	t.Run("broken published key fails", func(t *testing.T) {
		_, err := LoadOIDCKeySet(activePath, []string{filepath.Join(dir, "nope.pem")})
		assert.ErrorContains(t, err, "MESH_OIDC_PUBLISHED_KEY_FILES")
	})
}

func TestOIDC_ParseClients(t *testing.T) {
	keep := `[{"client_id":"keep-console","client_name":"Keep","redirect_uris":["https://keep.example.com/console/login/oidc/callback"]}]`
	clients, err := ParseOIDCClients(keep)
	require.NoError(t, err)
	require.Len(t, clients, 1)
	assert.Equal(t, "keep-console", clients[0].ClientID)
	assert.Equal(t, []string{"https://keep.example.com/console/login/oidc/callback"}, clients[0].RedirectURIs)

	none, err := ParseOIDCClients("  ")
	require.NoError(t, err)
	assert.Empty(t, none)

	bad := map[string]string{
		"not JSON":          `{`,
		"unknown field":     `[{"client_id":"a","client_name":"A","redirect_uris":["https://a.example/cb"],"client_secret":"x"}]`,
		"no client_id":      `[{"client_name":"A","redirect_uris":["https://a.example/cb"]}]`,
		"CIMD-shaped id":    `[{"client_id":"https://a.example/client.json","client_name":"A","redirect_uris":["https://a.example/cb"]}]`,
		"DCR-shaped id":     `[{"client_id":"mcpc_123","client_name":"A","redirect_uris":["https://a.example/cb"]}]`,
		"space in id":       `[{"client_id":"a b","client_name":"A","redirect_uris":["https://a.example/cb"]}]`,
		"no name":           `[{"client_id":"a","redirect_uris":["https://a.example/cb"]}]`,
		"no redirect":       `[{"client_id":"a","client_name":"A","redirect_uris":[]}]`,
		"http non-loopback": `[{"client_id":"a","client_name":"A","redirect_uris":["http://a.example/cb"]}]`,
		"fragment":          `[{"client_id":"a","client_name":"A","redirect_uris":["https://a.example/cb#x"]}]`,
		"duplicate":         `[{"client_id":"a","client_name":"A","redirect_uris":["https://a.example/cb"]},{"client_id":"a","client_name":"B","redirect_uris":["https://b.example/cb"]}]`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := ParseOIDCClients(raw)
			assert.Error(t, err)
		})
	}
}

func TestOIDC_KeyValidationAndPublishedFormats(t *testing.T) {
	key := newTestRSAKey(t, 2048)
	_, err := NewOIDCKeySet(nil)
	require.ErrorContains(t, err, "required")
	_, err = NewOIDCKeySet(key, nil)
	require.ErrorContains(t, err, "empty RSA key")
	_, err = NewOIDCKeySet(key, &rsa.PublicKey{})
	require.ErrorContains(t, err, "empty RSA key")
	_, err = NewOIDCKeySet(key, &newTestRSAKey(t, 1024).PublicKey)
	require.ErrorContains(t, err, "at least 2048")
	ks, err := NewOIDCKeySet(key, &key.PublicKey, &key.PublicKey)
	require.NoError(t, err)
	assert.Len(t, ks.JWKS().Keys, 1, "rotation must not duplicate an already published kid")

	dir := t.TempDir()
	active := writePEM(t, dir, "active.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	earlier := newTestRSAKey(t, 2048)
	pubPKCS1 := writePEM(t, dir, "earlier-public.pem", "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&earlier.PublicKey))
	privPKCS1 := writePEM(t, dir, "earlier-private.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(earlier))
	ks, err = LoadOIDCKeySet(active, []string{pubPKCS1, privPKCS1})
	require.NoError(t, err)
	assert.Len(t, ks.JWKS().Keys, 2)
	assert.Equal(t, RSAKeyThumbprint(&earlier.PublicKey), ks.JWKS().Keys[1].Kid)

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecPrivate, err := x509.MarshalPKCS8PrivateKey(ec)
	require.NoError(t, err)
	ecPublic, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, typ string
		der       []byte
	}{
		{"broken-pkcs8", "PRIVATE KEY", []byte("broken")},
		{"ec-private", "PRIVATE KEY", ecPrivate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePEM(t, dir, tc.name, tc.typ, tc.der)
			_, loadErr := LoadOIDCKeySet(path, nil)
			require.ErrorContains(t, loadErr, "MESH_OIDC_SIGNING_KEY_FILE")
			_, loadErr = LoadOIDCKeySet(active, []string{path})
			require.ErrorContains(t, loadErr, "MESH_OIDC_PUBLISHED_KEY_FILES")
		})
	}
	for _, tc := range []struct {
		name, typ string
		der       []byte
	}{
		{"broken-pkix", "PUBLIC KEY", []byte("broken")},
		{"ec-public", "PUBLIC KEY", ecPublic},
		{"wrong-pem-type", "CERTIFICATE", []byte("broken")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePEM(t, dir, tc.name, tc.typ, tc.der)
			_, loadErr := LoadOIDCKeySet(active, []string{path})
			require.ErrorContains(t, loadErr, "MESH_OIDC_PUBLISHED_KEY_FILES")
		})
	}
	garbage := dir + "/not-pem"
	require.NoError(t, os.WriteFile(garbage, []byte("not PEM"), 0o600))
	_, err = LoadOIDCKeySet(active, []string{garbage})
	require.ErrorContains(t, err, "no PEM block")
}
