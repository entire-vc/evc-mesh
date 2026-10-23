package storage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAddCharset(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"text/markdown", "text/markdown; charset=utf-8"},
		{"text/plain", "text/plain; charset=utf-8"},
		{"text/csv", "text/csv; charset=utf-8"},
		{"application/json", "application/json; charset=utf-8"},
		{"text/html; charset=utf-8", "text/html; charset=utf-8"}, // already has charset — no-op
		{"application/pdf", "application/pdf"},
		{"image/png", "image/png"},
		{"", ""},
	}
	for _, tt := range tests {
		got := addCharset(tt.in)
		if got != tt.want {
			t.Errorf("addCharset(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	tests := []struct {
		filename    string
		wantPrefix  string
		wantEncoded bool // true if filename* form expected
	}{
		{"report.pdf", `attachment; filename="report.pdf"`, false},
		{"document.md", `attachment; filename="document.md"`, false},
		{"отчёт.md", `attachment; filename="`, true},
	}
	for _, tt := range tests {
		got := contentDisposition(tt.filename)
		if !strings.HasPrefix(got, tt.wantPrefix) {
			t.Errorf("contentDisposition(%q) = %q, want prefix %q", tt.filename, got, tt.wantPrefix)
		}
		hasRFC5987 := strings.Contains(got, "filename*=UTF-8''")
		if tt.wantEncoded != hasRFC5987 {
			t.Errorf("contentDisposition(%q): RFC5987 presence = %v, want %v; got %q", tt.filename, hasRFC5987, tt.wantEncoded, got)
		}
	}

	// Cyrillic filename must have percent-encoded form and ASCII fallback.
	cyrillic := contentDisposition("Документ PRD.md")
	if !strings.Contains(cyrillic, "filename*=UTF-8''") {
		t.Errorf("Cyrillic filename missing RFC5987 encoding: %q", cyrillic)
	}
	if !strings.Contains(cyrillic, "%D0%94%D0%BE%D0%BA%D1%83%D0%BC%D0%B5%D0%BD%D1%82") {
		t.Errorf("Cyrillic filename missing percent-encoded Cyrillic chars: %q", cyrillic)
	}
}

func TestPresignPutURL_SignsTheDeclaredHeaders(t *testing.T) {
	s, err := NewS3Client("localhost:9000", "ak", "sk", "bkt", "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	s.bucketReady.Store(true) // no network: the bucket is taken as existing
	h := http.Header{}
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", "1234")
	u, err := s.PresignPutURL(context.Background(), "chat/ws/id", time.Minute, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/bkt/chat/ws/id", "X-Amz-Signature=", "content-length", "content-type"} {
		if !strings.Contains(strings.ToLower(u), strings.ToLower(want)) {
			t.Errorf("presigned URL %q lacks %q", u, want)
		}
	}

	s.SetPublicURL("https://mesh.example.com/s3")
	u, err = s.PresignPutURL(context.Background(), "chat/ws/id", time.Minute, h)
	if err != nil || !strings.HasPrefix(u, "https://mesh.example.com/s3/bkt/chat/ws/id?") {
		t.Errorf("public URL rewrite: %q, %v", u, err)
	}
}

func TestStatObject_UnreachableStorageIsClassified(t *testing.T) {
	s, err := NewS3Client("127.0.0.1:1", "ak", "sk", "bkt", "us-east-1", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.StatObject(ctx, "chat/ws/id"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("StatObject on a dead endpoint = %v, want ErrUnreachable", err)
	}
}
