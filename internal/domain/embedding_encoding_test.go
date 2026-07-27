package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeEmbedding_RoundTrips(t *testing.T) {
	vec := []float32{0, 1, -1, 0.5, -0.5, 3.14159, -3.14159, 1e10, -1e-10}

	encoded := EncodeEmbedding(vec)
	assert.NotEmpty(t, encoded)

	decoded, err := DecodeEmbedding(encoded)
	require.NoError(t, err)
	require.Len(t, decoded, len(vec))
	for i, f := range vec {
		assert.Equal(t, f, decoded[i], "index %d", i)
	}
}

func TestEncodeEmbedding_EmptyVector(t *testing.T) {
	encoded := EncodeEmbedding(nil)
	assert.Empty(t, encoded)

	decoded, err := DecodeEmbedding(encoded)
	require.NoError(t, err)
	assert.Empty(t, decoded)
}

func TestDecodeEmbedding_InvalidBase64(t *testing.T) {
	_, err := DecodeEmbedding("not-valid-base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode embedding")
}

func TestDecodeEmbedding_LengthNotMultipleOfFour(t *testing.T) {
	// "AAA=" base64-decodes to 2 bytes — not a multiple of 4.
	_, err := DecodeEmbedding("AAA=")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a multiple of 4")
}

func TestEncodeEmbedding_384Dim_MatchesTEIOutputShape(t *testing.T) {
	vec := make([]float32, 384)
	for i := range vec {
		vec[i] = float32(i) * 0.001
	}
	encoded := EncodeEmbedding(vec)
	decoded, err := DecodeEmbedding(encoded)
	require.NoError(t, err)
	require.Len(t, decoded, 384)
	assert.Equal(t, vec, decoded)
}
