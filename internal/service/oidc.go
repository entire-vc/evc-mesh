package service

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// OpenID Connect identity flow: Mesh proves to a statically
// configured client (Keep) who the signed-in person is. Same authorization
// server and endpoints as the MCP flow; scope `openid` without `mesh` selects
// this flow, which issues an RS256 id_token and nothing that can call the Mesh
// API. The whole flow is off unless a signing key is configured.

const (
	// oidcIdentityCodePrefix marks an identity-flow authorization code, so
	// /oauth/token knows which table to redeem it from. MCP-flow codes are
	// bare hex and can never start with it.
	oidcIdentityCodePrefix = "mic_"
	// OIDCIdentityAccessTokenPrefix marks the access token of the identity
	// flow. It is required by RFC 6749 §5.1 but is not stored anywhere and no
	// Mesh API accepts it: the client's proof is the id_token.
	OIDCIdentityAccessTokenPrefix = "moi_"
	oidcIDTokenTTL                = 5 * time.Minute
	oidcScope                     = "openid"
	// oidcNonceMaxLen bounds the nonce stored with a code and echoed into the
	// id_token; real clients send 16-64 characters.
	oidcNonceMaxLen = 512
	// oidcMinRSABits is the smallest signing key accepted (RFC 7518 §3.3).
	oidcMinRSABits = 2048
	// oidcClientIDMaxLen keeps a configured client_id a short identifier.
	oidcClientIDMaxLen = 200
)

// --- Signing keys ---

// OIDCKeySet is the signing key that id_tokens are signed with plus every
// public key the JWKS publishes: the active one and earlier ones, so a key
// can be rotated without invalidating id_tokens a client is still verifying.
type OIDCKeySet struct {
	signer    *rsa.PrivateKey
	signerKID string
	published []oidcPublishedKey
}

type oidcPublishedKey struct {
	kid string
	pub *rsa.PublicKey
}

// JWK is one RSA public key of the JWKS (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKSDocument is GET /oauth/jwks's body.
type JWKSDocument struct {
	Keys []JWK `json:"keys"`
}

// NewOIDCKeySet builds a key set from the active private key and any number of
// earlier public keys. The active key is always published first; a published
// key equal to it is listed once.
func NewOIDCKeySet(active *rsa.PrivateKey, earlier ...*rsa.PublicKey) (*OIDCKeySet, error) {
	if active == nil {
		return nil, errors.New("oidc: active signing key is required")
	}
	if err := checkOIDCKeySize(&active.PublicKey); err != nil {
		return nil, err
	}
	ks := &OIDCKeySet{signer: active, signerKID: RSAKeyThumbprint(&active.PublicKey)}
	ks.published = append(ks.published, oidcPublishedKey{kid: ks.signerKID, pub: &active.PublicKey})
	seen := map[string]bool{ks.signerKID: true}
	for _, pub := range earlier {
		if err := checkOIDCKeySize(pub); err != nil {
			return nil, err
		}
		kid := RSAKeyThumbprint(pub)
		if seen[kid] {
			continue
		}
		seen[kid] = true
		ks.published = append(ks.published, oidcPublishedKey{kid: kid, pub: pub})
	}
	return ks, nil
}

func checkOIDCKeySize(pub *rsa.PublicKey) error {
	if pub == nil || pub.N == nil {
		return errors.New("oidc: empty RSA key")
	}
	if bits := pub.N.BitLen(); bits < oidcMinRSABits {
		return fmt.Errorf("oidc: RSA key is %d bits, at least %d required", bits, oidcMinRSABits)
	}
	return nil
}

// LoadOIDCKeySet reads MESH_OIDC_SIGNING_KEY_FILE (PEM RSA private key, PKCS#1
// or PKCS#8) and MESH_OIDC_PUBLISHED_KEY_FILES (PEM RSA public or private
// keys). An empty activeFile means OIDC is off: (nil, nil). Any unreadable or
// unusable file is an error: a configured key that cannot be used must stop
// the start instead of silently turning the provider off.
func LoadOIDCKeySet(activeFile string, publishedFiles []string) (*OIDCKeySet, error) {
	activeFile = strings.TrimSpace(activeFile)
	if activeFile == "" {
		if len(nonEmpty(publishedFiles)) > 0 {
			return nil, errors.New("oidc: MESH_OIDC_PUBLISHED_KEY_FILES is set without MESH_OIDC_SIGNING_KEY_FILE")
		}
		return nil, nil
	}
	raw, err := os.ReadFile(activeFile) //nolint:gosec // operator-configured path
	if err != nil {
		return nil, fmt.Errorf("oidc: read MESH_OIDC_SIGNING_KEY_FILE %s: %w", activeFile, err)
	}
	active, err := parseRSAPrivateKeyPEM(raw)
	if err != nil {
		return nil, fmt.Errorf("oidc: MESH_OIDC_SIGNING_KEY_FILE %s: %w", activeFile, err)
	}
	var earlier []*rsa.PublicKey
	for _, f := range nonEmpty(publishedFiles) {
		raw, err := os.ReadFile(f) //nolint:gosec // operator-configured path
		if err != nil {
			return nil, fmt.Errorf("oidc: read MESH_OIDC_PUBLISHED_KEY_FILES entry %s: %w", f, err)
		}
		pub, err := parseRSAPublicKeyPEM(raw)
		if err != nil {
			return nil, fmt.Errorf("oidc: MESH_OIDC_PUBLISHED_KEY_FILES entry %s: %w", f, err)
		}
		earlier = append(earlier, pub)
	}
	return NewOIDCKeySet(active, earlier...)
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseRSAPrivateKeyPEM(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, an RSA key is required", k)
		}
		return rk, nil
	default:
		return nil, fmt.Errorf("PEM block %q is not an RSA private key", block.Type)
	}
}

func parseRSAPublicKeyPEM(raw []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY", "PRIVATE KEY":
		k, err := parseRSAPrivateKeyPEM(raw)
		if err != nil {
			return nil, err
		}
		return &k.PublicKey, nil
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	case "PUBLIC KEY":
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("public key is %T, an RSA key is required", k)
		}
		return rk, nil
	default:
		return nil, fmt.Errorf("PEM block %q is not an RSA key", block.Type)
	}
}

// RSAKeyThumbprint is the RFC 7638 JWK thumbprint of an RSA public key
// (SHA-256 of the canonical {"e","kty","n"} JSON, base64url), used as kid.
func RSAKeyThumbprint(pub *rsa.PublicKey) string {
	canonical := `{"e":"` + jwkE(pub) + `","kty":"RSA","n":"` + jwkN(pub) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func jwkN(pub *rsa.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
}

func jwkE(pub *rsa.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
}

// ActiveKID is the kid of the key id_tokens are signed with.
func (k *OIDCKeySet) ActiveKID() string { return k.signerKID }

// JWKS returns every published public key, the active one first.
func (k *OIDCKeySet) JWKS() JWKSDocument {
	doc := JWKSDocument{Keys: make([]JWK, 0, len(k.published))}
	for _, p := range k.published {
		doc.Keys = append(doc.Keys, JWK{Kty: "RSA", Use: "sig", Alg: "RS256", Kid: p.kid, N: jwkN(p.pub), E: jwkE(p.pub)})
	}
	return doc
}

// PublicKey returns the published key with the given kid, nil when unknown.
func (k *OIDCKeySet) PublicKey(kid string) *rsa.PublicKey {
	for _, p := range k.published {
		if p.kid == kid {
			return p.pub
		}
	}
	return nil
}

// signIDToken signs an id_token with the active key: header alg RS256, kid,
// typ JWT. nonce is included only when the authorize request carried one.
func (k *OIDCKeySet) signIDToken(issuer, subject, audience, nonce string, now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iss": issuer,
		"sub": subject,
		"aud": audience,
		"iat": now.Unix(),
		"exp": now.Add(oidcIDTokenTTL).Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = k.signerKID
	tok.Header["typ"] = "JWT"
	return tok.SignedString(k.signer)
}

// --- Static clients ---

// OIDCStaticClient is one entry of MESH_OIDC_CLIENTS: a client the operator
// allows to use the identity flow. Public client (no secret, PKCE S256),
// authorization_code only, redirect_uri matched exactly.
type OIDCStaticClient struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

// ParseOIDCClients parses MESH_OIDC_CLIENTS, a JSON array of
// {"client_id","client_name","redirect_uris"}. Empty input is no clients.
// Unknown fields, duplicates and invalid values are errors: a typo in the
// operator's config must stop the start, not drop a client silently.
func ParseOIDCClients(raw string) ([]OIDCStaticClient, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var clients []OIDCStaticClient
	if err := dec.Decode(&clients); err != nil {
		return nil, fmt.Errorf("oidc: MESH_OIDC_CLIENTS is not a JSON array of {client_id, client_name, redirect_uris}: %w", err)
	}
	seen := map[string]bool{}
	for i, c := range clients {
		if err := validateOIDCStaticClient(c); err != nil {
			return nil, fmt.Errorf("oidc: MESH_OIDC_CLIENTS[%d]: %w", i, err)
		}
		if seen[c.ClientID] {
			return nil, fmt.Errorf("oidc: MESH_OIDC_CLIENTS[%d]: duplicate client_id %q", i, c.ClientID)
		}
		seen[c.ClientID] = true
	}
	return clients, nil
}

func validateOIDCStaticClient(c OIDCStaticClient) error {
	switch {
	case c.ClientID == "":
		return errors.New("client_id is required")
	case len(c.ClientID) > oidcClientIDMaxLen:
		return fmt.Errorf("client_id is longer than %d characters", oidcClientIDMaxLen)
	case strings.Contains(c.ClientID, "://"), strings.HasPrefix(c.ClientID, dcrClientIDPrefix):
		// Those shapes belong to CIMD and DCR clients.
		return fmt.Errorf("client_id %q looks like a CIMD URL or a DCR id", c.ClientID)
	}
	for _, r := range c.ClientID {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("client_id %q must be printable ASCII without spaces", c.ClientID)
		}
	}
	if strings.TrimSpace(c.ClientName) == "" {
		return errors.New("client_name is required: the consent screen shows it")
	}
	if len(c.RedirectURIs) == 0 {
		return errors.New("redirect_uris is required")
	}
	if oerr := checkClientMetadataLimits(c.ClientName, c.RedirectURIs); oerr != nil {
		return errors.New(oerr.Description)
	}
	for _, ru := range c.RedirectURIs {
		if !validRedirectURI(ru) {
			return fmt.Errorf("invalid redirect_uri %q (https://, or http:// on loopback, no fragment)", ru)
		}
	}
	return nil
}

// OIDCSettings switches the identity flow on. Keys nil = off.
type OIDCSettings struct {
	// Issuer is the iss claim; it must equal the issuer the discovery
	// document and RFC 8414 metadata publish.
	Issuer  string
	Keys    *OIDCKeySet
	Clients []OIDCStaticClient
}

// OIDCConfigurable is implemented by the OAuth service: cmd/api calls it once
// at start, after the key set and MESH_OIDC_CLIENTS are parsed.
type OIDCConfigurable interface {
	// ConfigureOIDC upserts the static clients and enables the identity flow
	// when Keys is set. A client_id already taken by a DCR or CIMD client is
	// an error.
	ConfigureOIDC(ctx context.Context, settings OIDCSettings) error
}
