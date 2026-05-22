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
	"sync"
)

var (
	keyOnce   sync.Once
	aesKey    []byte
	noKeyWarn sync.Once
)

func loadKey() []byte {
	keyOnce.Do(func() {
		raw := os.Getenv("MESH_INTEGRATION_ENCRYPTION_KEY")
		if raw == "" {
			noKeyWarn.Do(func() {
				log.Println("WARNING: MESH_INTEGRATION_ENCRYPTION_KEY not set; agent_key stored as plaintext")
			})
			return
		}
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(b) != 32 {
			log.Printf("WARNING: MESH_INTEGRATION_ENCRYPTION_KEY invalid (need 32-byte base64); falling back to plaintext")
			return
		}
		aesKey = b
	})
	return aesKey
}

// Encrypt encrypts the given plaintext using AES-256-GCM.
// If the encryption key is not configured, the plaintext is returned as-is (graceful degradation).
func Encrypt(plaintext string) (string, error) {
	key := loadKey()
	if key == nil {
		return plaintext, nil // graceful degradation
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("encryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("encryption gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts the given ciphertext using AES-256-GCM.
// If the encryption key is not configured, the ciphertext is returned as-is (graceful degradation).
// If the ciphertext was stored before encryption was enabled (plaintext), it is returned as-is.
func Decrypt(ciphertext string) (string, error) {
	key := loadKey()
	if key == nil {
		return ciphertext, nil // graceful degradation
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		// might be plaintext from before encryption was enabled
		return ciphertext, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("decryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("decryption gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// not encrypted (plaintext fallback from no-key era)
		return ciphertext, nil
	}
	return string(plain), nil
}
