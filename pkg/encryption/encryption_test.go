package encryption

import (
	"encoding/base64"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetKeyState resets the package-level key singleton so each test starts fresh.
// This is necessary because sync.Once is used to load the key once per process.
func resetKeyState() {
	keyOnce = sync.Once{}
	aesKey = nil
	noKeyWarn = sync.Once{}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Generate a valid 32-byte key.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	encoded := base64.StdEncoding.EncodeToString(key)

	resetKeyState()
	t.Setenv("MESH_INTEGRATION_ENCRYPTION_KEY", encoded)

	plaintext := "super-secret-agent-key-value-12345"
	ciphertext, err := Encrypt(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext, "ciphertext should differ from plaintext")

	decrypted, err := Decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}
	encoded := base64.StdEncoding.EncodeToString(key)

	resetKeyState()
	t.Setenv("MESH_INTEGRATION_ENCRYPTION_KEY", encoded)

	plaintext := "same-plaintext"
	ct1, err := Encrypt(plaintext)
	require.NoError(t, err)
	ct2, err := Encrypt(plaintext)
	require.NoError(t, err)
	// Different nonces → different ciphertexts.
	assert.NotEqual(t, ct1, ct2, "each encryption call should produce a unique ciphertext")
}

func TestPlaintextFallbackWhenNoKey(t *testing.T) {
	resetKeyState()
	os.Unsetenv("MESH_INTEGRATION_ENCRYPTION_KEY")

	plaintext := "my-secret"
	ct, err := Encrypt(plaintext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, ct, "should return plaintext when key is not configured")

	decrypted, err := Decrypt(ct)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestDecryptPlaintextFallback(t *testing.T) {
	// When a key IS configured but the stored value is plaintext (from before encryption was enabled),
	// Decrypt should return the plaintext rather than an error.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	encoded := base64.StdEncoding.EncodeToString(key)

	resetKeyState()
	t.Setenv("MESH_INTEGRATION_ENCRYPTION_KEY", encoded)

	// "not-base64!!" is not valid base64 → treated as legacy plaintext.
	plaintext := "not-base64!!"
	decrypted, err := Decrypt(plaintext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestInvalidKeyIgnored(t *testing.T) {
	resetKeyState()
	// Set an invalid key (not 32 bytes after decode).
	t.Setenv("MESH_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("tooshort")))

	plaintext := "hello"
	ct, err := Encrypt(plaintext)
	require.NoError(t, err)
	// Falls back to plaintext passthrough.
	assert.Equal(t, plaintext, ct)
}
