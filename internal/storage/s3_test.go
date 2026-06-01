package storage

import (
	"strings"
	"testing"
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
