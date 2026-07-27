package domain

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
)

// EncodeEmbedding encodes a float32 vector as base64(little-endian bytes) —
// the storage format for MemoryChunk.Embedding (ADR-0002). Benchmarked ~66x
// faster to decode than a JSON array for a 384-dim vector (618ns vs 41028ns),
// which matters because chunking multiplies the number of stored vectors —
// decode cost, not the cosine math itself, dominates at that row count.
func EncodeEmbedding(vec []float32) string {
	buf := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// DecodeEmbedding reverses EncodeEmbedding.
func DecodeEmbedding(s string) ([]float32, error) {
	buf, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode embedding: %w", err)
	}
	if len(buf)%4 != 0 {
		return nil, fmt.Errorf("decode embedding: %d bytes is not a multiple of 4", len(buf))
	}
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec, nil
}
