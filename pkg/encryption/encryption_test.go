package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetKeyState resets the package-level key singleton so each test starts
// fresh; the key is otherwise loaded once per process via sync.Once.
func resetKeyState() { ResetForTest() }

func testKey(t *testing.T, fill byte) string {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill + byte(i)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// useKey installs a key for the duration of the test and guarantees the
// singleton is reset afterwards, so ordering between tests cannot leak state.
func useKey(t *testing.T, encoded string) {
	t.Helper()
	resetKeyState()
	t.Setenv(EnvKey, encoded)
	t.Cleanup(resetKeyState)
}

// legacyEncrypt reproduces the pre-2026-08 wire format: a bare base64 blob
// with no version prefix. Values in this shape exist on deployments that were
// running before the prefix landed, so Decrypt must keep reading them.
func legacyEncrypt(t *testing.T, encodedKey, plaintext string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	block, err := aes.NewCipher(raw)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, gcm.NonceSize())
	_, err = io.ReadFull(rand.Reader, nonce)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil))
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	useKey(t, testKey(t, 1))

	plaintext := "tr_agent_0123456789abcdef0123456789abcdef0123456789abcdef"
	ciphertext, err := Encrypt(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)
	assert.True(t, IsEncrypted(ciphertext), "stored value must carry the version prefix")

	decrypted, err := Decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

// The prefix is what makes "is this row encrypted?" answerable from a psql
// prompt and from a CHECK constraint, so its exact shape is part of the
// contract, not an implementation detail.
func TestEncryptOutputShapeIsRecognisableWithoutTheKey(t *testing.T) {
	useKey(t, testKey(t, 3))

	ciphertext, err := Encrypt("some-credential")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(ciphertext, "enc:v1:"))

	body := strings.TrimPrefix(ciphertext, "enc:v1:")
	_, err = base64.StdEncoding.DecodeString(body)
	require.NoError(t, err, "body after the prefix must be valid base64")

	// The predicate the DB constraint uses does not need key material.
	resetKeyState()
	assert.True(t, IsEncrypted(ciphertext))
	assert.False(t, IsEncrypted("tr_agent_deadbeef"))
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	useKey(t, testKey(t, 0xAB))

	ct1, err := Encrypt("same-plaintext")
	require.NoError(t, err)
	ct2, err := Encrypt("same-plaintext")
	require.NoError(t, err)
	assert.NotEqual(t, ct1, ct2, "each call must use a fresh nonce")
}

// A value written before the version prefix existed still has to decrypt,
// otherwise deploying this change would brick any instance already encrypting.
func TestDecryptReadsLegacyUnprefixedCiphertext(t *testing.T) {
	encoded := testKey(t, 5)
	useKey(t, encoded)

	plaintext := "credential-from-before-the-prefix"
	legacy := legacyEncrypt(t, encoded, plaintext)
	require.False(t, IsEncrypted(legacy))

	got, err := Decrypt(legacy)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// The 2026-06 project_integrations rows: a key IS configured, but the stored
// value never went through Encrypt. It must read back verbatim until the
// backfill has run.
func TestDecryptPassesThroughLegacyPlaintext(t *testing.T) {
	useKey(t, testKey(t, 7))

	for _, plaintext := range []string{
		"tr_agent_" + strings.Repeat("a", 48), // the real prod shape
		"not-base64!!",
	} {
		got, err := Decrypt(plaintext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, got)
	}
}

// The regression this whole change exists for: a rotated or mistyped key used
// to make Decrypt hand the raw ciphertext back as if it were the credential.
// Downstream that becomes a 401 from the remote party, which reads as "they
// revoked our key" and points the investigation at the wrong system.
func TestDecryptFailsLoudlyOnWrongKey(t *testing.T) {
	writeKey := testKey(t, 11)
	useKey(t, writeKey)
	ciphertext, err := Encrypt("tr_agent_realcredential")
	require.NoError(t, err)

	// Simulate a key rotation that left the stored values behind.
	useKey(t, testKey(t, 200))

	got, err := Decrypt(ciphertext)
	require.ErrorIs(t, err, ErrUndecryptable)
	assert.Empty(t, got, "must not return the ciphertext as if it were the credential")
}

func TestDecryptFailsLoudlyWhenKeyDisappears(t *testing.T) {
	useKey(t, testKey(t, 13))
	ciphertext, err := Encrypt("tr_agent_realcredential")
	require.NoError(t, err)

	// Key dropped from the environment (bad deploy, missing EnvironmentFile).
	resetKeyState()
	t.Setenv(EnvKey, "")
	t.Cleanup(resetKeyState)

	_, err = Decrypt(ciphertext)
	require.ErrorIs(t, err, ErrUndecryptable)
}

func TestDecryptRejectsCorruptedCiphertext(t *testing.T) {
	useKey(t, testKey(t, 17))
	_, err := Decrypt(Prefix + "!!!not-base64!!!")
	require.ErrorIs(t, err, ErrUndecryptable)
}

func TestPlaintextFallbackWhenNoKey(t *testing.T) {
	resetKeyState()
	t.Setenv(EnvKey, "")
	t.Cleanup(resetKeyState)

	plaintext := "my-secret"
	ct, err := Encrypt(plaintext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, ct, "unset key degrades to plaintext for local dev")

	state, req := Status()
	assert.Equal(t, KeyAbsent, state)
	assert.False(t, req)
	require.NoError(t, Validate(), "a warning, not a failure, when encryption is not required")
}

func TestInvalidKeyIsReportedAsInvalidNotAbsent(t *testing.T) {
	resetKeyState()
	t.Setenv(EnvKey, base64.StdEncoding.EncodeToString([]byte("tooshort")))
	t.Cleanup(resetKeyState)

	ct, err := Encrypt("hello")
	require.NoError(t, err)
	assert.Equal(t, "hello", ct)

	state, _ := Status()
	assert.Equal(t, KeyInvalid, state,
		"a mistyped key must be distinguishable from an unset one — they need different fixes")
}

// Fail-closed mode: a deployment holding real credentials can refuse to store
// them in the clear rather than degrade silently.
func TestRequiredModeRefusesToDegrade(t *testing.T) {
	for name, keyValue := range map[string]string{
		"absent":  "",
		"invalid": base64.StdEncoding.EncodeToString([]byte("tooshort")),
	} {
		t.Run(name, func(t *testing.T) {
			resetKeyState()
			t.Setenv(EnvKey, keyValue)
			t.Setenv(EnvRequired, "true")
			t.Cleanup(resetKeyState)

			_, err := Encrypt("must-not-be-stored-in-the-clear")
			require.ErrorIs(t, err, ErrKeyRequired)
			require.ErrorIs(t, Validate(), ErrKeyRequired)
		})
	}
}

func TestRequiredModeIsSatisfiedByAValidKey(t *testing.T) {
	resetKeyState()
	t.Setenv(EnvKey, testKey(t, 23))
	t.Setenv(EnvRequired, "1")
	t.Cleanup(resetKeyState)

	require.NoError(t, Validate())
	ct, err := Encrypt("credential")
	require.NoError(t, err)
	require.True(t, IsEncrypted(ct))
}

func TestValidateReportsStateAccurately(t *testing.T) {
	useKey(t, testKey(t, 29))
	state, req := Status()
	assert.Equal(t, KeyOK, state)
	assert.False(t, req)
	assert.Equal(t, "ok", state.String())
	require.NoError(t, Validate())
}
