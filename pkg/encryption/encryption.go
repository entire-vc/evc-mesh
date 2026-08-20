// Package encryption provides AES-256-GCM protection for the integration
// credentials Mesh stores in Postgres (project_integrations.agent_key, the
// Telegram bot_token inside integration_configs.config).
//
// # Why stored values carry a version prefix
//
// Until 2026-08 an encrypted value was a bare base64 blob, which made
// "is this row encrypted?" unanswerable by looking at it. Decrypt papered over
// the ambiguity: anything that failed to AEAD-open was assumed to be a legacy
// plaintext row and returned verbatim. That made four very different states
// indistinguishable to every caller and to every operator:
//
//  1. no key configured               → plaintext stored, silently
//  2. key configured but malformed    → plaintext stored, silently
//  3. key fine, row predates it       → plaintext returned, silently
//  4. key ROTATED or simply wrong     → the ciphertext itself returned as if
//     it were the credential, silently
//
// State 4 is the dangerous one. The caller receives a well-formed string,
// hands it to Team Relay as an agent key, and gets a 401 — a symptom that
// looks exactly like "the remote party revoked our credential" and sends the
// investigation to the wrong side of the network (this is the shape of
// incident #c55556fa).
//
// New values are therefore written as "enc:v1:<base64(nonce||ciphertext)>".
// The prefix turns encryption into a property of the value that both code and
// an operator with a psql prompt can read directly, and it lets Decrypt fail
// LOUDLY for state 4 while still tolerating state 3.
package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// EnvKey is the environment variable holding the base64 32-byte AES key.
const EnvKey = "MESH_INTEGRATION_ENCRYPTION_KEY"

// EnvRequired, when truthy, makes a missing or malformed key a hard failure
// instead of a silent downgrade to plaintext storage. Deployments that hold
// real credentials (prod) set it; local dev and tests leave it unset so the
// stack still runs without key material.
const EnvRequired = "MESH_INTEGRATION_ENCRYPTION_REQUIRED"

// Prefix marks a value written by the current encryption scheme. Anything
// carrying it MUST decrypt; anything without it is a pre-2026-08 value.
const Prefix = "enc:v1:"

// KeyState describes how the process is configured, for startup logging and
// for the mesh_integration_encryption_key_configured gauge.
type KeyState int

const (
	// KeyAbsent means EnvKey is unset — new writes are stored as plaintext.
	KeyAbsent KeyState = iota
	// KeyInvalid means EnvKey is set but is not a base64 32-byte value —
	// new writes are stored as plaintext. Almost always a deploy typo.
	KeyInvalid
	// KeyOK means encryption is active.
	KeyOK
)

func (s KeyState) String() string {
	switch s {
	case KeyOK:
		return "ok"
	case KeyInvalid:
		return "invalid"
	default:
		return "absent"
	}
}

// ErrKeyRequired is returned when EnvRequired is set but no usable key is.
var ErrKeyRequired = errors.New("encryption: " + EnvKey + " is missing or malformed and " + EnvRequired + " is set")

// ErrUndecryptable is returned when a value carries Prefix but cannot be
// opened with the configured key. It is deliberately NOT silently passed
// through: a caller that receives ciphertext where it expected a credential
// will produce a misleading downstream authentication failure.
var ErrUndecryptable = errors.New("encryption: value is marked encrypted but could not be decrypted (wrong or rotated " + EnvKey + "?)")

var (
	keyOnce  sync.Once
	aesKey   []byte
	keyState = KeyAbsent
	required bool
)

func loadKey() []byte {
	keyOnce.Do(func() {
		required = isTruthy(os.Getenv(EnvRequired))
		raw := os.Getenv(EnvKey)
		if raw == "" {
			keyState = KeyAbsent
			return
		}
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(b) != 32 {
			keyState = KeyInvalid
			return
		}
		aesKey = b
		keyState = KeyOK
	})
	return aesKey
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ResetForTest clears the process-wide key singleton so a test can install a
// different key, or none. It exists because the key is deliberately read once
// per process; tests in other packages (the repository-layer DB tests) need to
// exercise both the configured and unconfigured paths in one run.
//
// Not for use outside tests.
func ResetForTest() {
	keyOnce = sync.Once{}
	aesKey = nil
	keyState = KeyAbsent
	required = false
}

// Status reports how encryption is configured in this process. Callers use it
// to log once at startup and to publish a gauge, so that "the security control
// is off" is visible on a dashboard rather than only in the shape of rows in
// the database.
func Status() (state KeyState, keyRequired bool) {
	loadKey()
	return keyState, required
}

// Validate is called once at startup. It returns ErrKeyRequired when the
// deployment demands encryption but cannot do it, and otherwise logs the
// state — including the not-configured case, which previously only surfaced
// lazily on the first Encrypt/Decrypt call, potentially days after boot and
// buried in request logs.
func Validate() error {
	state, req := Status()
	switch {
	case state == KeyOK:
		log.Printf("encryption: %s configured, integration credentials are encrypted at rest", EnvKey)
		return nil
	case req:
		return ErrKeyRequired
	case state == KeyInvalid:
		log.Printf("SECURITY WARNING: %s is set but is not a base64-encoded 32-byte key; "+
			"integration credentials will be stored as PLAINTEXT. Set %s=true to make this fatal.", EnvKey, EnvRequired)
	default:
		log.Printf("SECURITY WARNING: %s is not set; integration credentials will be stored as PLAINTEXT. "+
			"Set %s=true to make this fatal.", EnvKey, EnvRequired)
	}
	return nil
}

// IsEncrypted reports whether a stored value was written by the current
// scheme. It is a pure string check — it does not need the key — which is what
// makes the same predicate expressible as a Postgres CHECK constraint, closing
// the provisioning path that writes rows with direct SQL and never reaches
// this package at all.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, Prefix)
}

// Encrypt encrypts plaintext with AES-256-GCM and returns a Prefix-tagged
// value. With no usable key it returns the plaintext unchanged, unless
// EnvRequired is set, in which case it returns ErrKeyRequired rather than
// quietly downgrading.
func Encrypt(plaintext string) (string, error) {
	key := loadKey()
	if key == nil {
		if required {
			return "", ErrKeyRequired
		}
		return plaintext, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. It handles three classes of stored value:
//
//   - Prefix-tagged: MUST open. A failure returns ErrUndecryptable instead of
//     the ciphertext, so a wrong or rotated key is reported as itself rather
//     than travelling onward disguised as a credential.
//   - untagged but openable: a value written before the prefix existed
//     (other deployments, e.g. the partner instance). Returned decrypted.
//   - untagged and not openable: a legacy plaintext row. Returned as-is —
//     this is the case the 2026-06 project_integrations rows fall into, and
//     it stays supported until the backfill has run everywhere.
func Decrypt(stored string) (string, error) {
	key := loadKey()

	if IsEncrypted(stored) {
		if key == nil {
			return "", fmt.Errorf("%w: no key configured", ErrUndecryptable)
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrUndecryptable, err)
		}
		plain, err := open(key, data)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrUndecryptable, err)
		}
		return plain, nil
	}

	// Untagged. Without a key there is nothing to try: it is plaintext.
	if key == nil {
		return stored, nil
	}
	data, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return stored, nil // not base64 → legacy plaintext
	}
	plain, err := open(key, data)
	if err != nil {
		return stored, nil // base64 but not ours → legacy plaintext
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return gcm, nil
}

func open(key, data []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
