package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Artifact content encodings accepted by upload_artifact.
const (
	encodingText   = "text"
	encodingBase64 = "base64"
)

// decodeArtifactContent turns the inline `content` string of an upload_artifact
// call into the bytes that should land in the file.
//
// Until 2026-08-21 there was no decoding step at all: the handler did
// []byte(content) unconditionally while the tool description advertised
// "text or base64-encoded". An agent sending a PNG — base64 is the only way to
// move bytes through a JSON tool call — got a file containing the ASCII string
// "iVBORw0KGgo..." and an HTTP 200. The failure was invisible at the call site
// (plausible size, mime_type image/png from the filename), so it could only be
// caught by downloading the artifact and looking inside.
//
// base64 decoding is deliberately Strict(): the standard decoder tolerates
// non-canonical trailing bits, which is exactly the shape a truncated payload
// has. Inline content crosses a model context on its way here and truncation is
// a live failure mode, so a payload that does not decode cleanly must fail the
// upload rather than be written as a short file.
func decodeArtifactContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", encodingText:
		return []byte(content), nil

	case encodingBase64:
		// Whitespace is common in wrapped base64 and is not corruption.
		cleaned := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
				return -1
			}
			return r
		}, content)

		if len(cleaned)%4 != 0 {
			return nil, fmt.Errorf(
				"content is not valid base64: length %d is not a multiple of 4 (payload looks truncated)",
				len(cleaned))
		}

		data, err := base64.StdEncoding.Strict().DecodeString(cleaned)
		if err != nil {
			return nil, fmt.Errorf("content is not valid base64: %w", err)
		}
		return data, nil

	default:
		return nil, fmt.Errorf("invalid encoding %q: must be %q or %q", encoding, encodingText, encodingBase64)
	}
}

// magicSignatures maps a declared MIME type to the byte prefixes a file of that
// type must start with. Only formats with an unambiguous magic number are
// listed; anything absent is not checked rather than guessed at.
var magicSignatures = map[string][][]byte{
	"image/png":       {{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}},
	"image/jpeg":      {{0xFF, 0xD8, 0xFF}},
	"image/gif":       {[]byte("GIF87a"), []byte("GIF89a")},
	"application/pdf": {[]byte("%PDF-")},
	"application/zip": {{'P', 'K', 0x03, 0x04}, {'P', 'K', 0x05, 0x06}, {'P', 'K', 0x07, 0x08}},
}

// validateArtifactMagic rejects content whose bytes contradict the declared MIME
// type. This is the check that turns the silent failure loud: a declared
// image/png whose bytes are the ASCII of a base64 string is refused instead of
// being stored and reported as a successful upload.
//
// It is deliberately one-directional. A MIME type with no known signature, or
// empty content, is passed through untouched — the goal is to catch content that
// provably is not what it claims, not to police every upload.
func validateArtifactMagic(mimeType string, data []byte) error {
	// Strip any parameters: "image/png; charset=binary" is still image/png.
	base := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.Index(base, ";"); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}

	sigs, known := magicSignatures[base]
	if !known || len(data) == 0 {
		return nil
	}

	for _, sig := range sigs {
		if bytes.HasPrefix(data, sig) {
			return nil
		}
	}

	return fmt.Errorf(
		"content does not match declared mime_type %q: expected the file to start with the %s signature, got %s. "+
			"If you are sending binary data, pass encoding=\"base64\"; if this really is text, correct mime_type or name",
		mimeType, base, describePrefix(data))
}

// describePrefix renders the first bytes of a payload for an error message,
// printable-as-text where possible so the common failure (an undecoded base64
// string sitting where a PNG header belongs) is legible in the message itself.
func describePrefix(data []byte) string {
	const n = 12
	head := data
	if len(head) > n {
		head = head[:n]
	}

	printable := true
	for _, b := range head {
		if b < 0x20 || b > 0x7E {
			printable = false
			break
		}
	}
	if printable {
		return fmt.Sprintf("text starting %q", string(head))
	}
	return fmt.Sprintf("bytes starting %x", head)
}

// verifyArtifactChecksum compares the bytes about to be uploaded against a
// caller-supplied sha256.
//
// Magic-byte checks catch a payload that is the wrong *kind* of thing; they say
// nothing about a payload of the right kind that lost its tail — a truncated PNG
// still starts with a valid PNG header. The checksum is the only end-to-end
// guard against that, so callers moving binaries are encouraged to send one.
func verifyArtifactChecksum(expected string, data []byte) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return nil
	}

	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf(
			"sha256 mismatch: expected %s, got %s over %d decoded bytes (content was altered or truncated in transit)",
			expected, actual, len(data))
	}
	return nil
}
