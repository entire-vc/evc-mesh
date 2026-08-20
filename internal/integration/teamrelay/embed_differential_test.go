package teamrelay

import "testing"

// Differential fixtures against Team Relay's real renderMarkdown() (AC-4).
//
// Every row below was captured by executing apps/web-publish/src/lib/markdown.ts
// (evc-team-relay@b54bce7, vitest, real node_modules) — not traced by eye. See
// task #836ebffe for the full method and citations. Table numbering matches the
// source report so a row here can be looked up back to its trace.
func TestParseEmbedTarget_MatchesTeamRelaysRealRenderer(t *testing.T) {
	cases := []struct {
		row        string
		inner      string
		wantTarget string
		wantSize   string
	}{
		{"1 baseline", "pic.png", "pic.png", ""},
		{"2 space — literal, not encoded at parse time", "my pic.png", "my pic.png", ""},
		{"3 cyrillic", "картинка.png", "картинка.png", ""},
		{"4a ё — ordinary code point, no special-casing", "фото ё.png", "фото ё.png", ""},
		{"4b Ё — case preserved", "Фото Ё.png", "Фото Ё.png", ""},
		{"5 nested path passes through", "assets/sub/pic.png", "assets/sub/pic.png", ""},
		{"6 single pipe size", "pic.png|300", "pic.png", "300"},
		{"7 multiple pipes rejoin with |, size never validated as numeric", "pic.png|300|extra", "pic.png", "300|extra"},
		{"8 whitespace inside [[ ]], no pipe — single .trim()", " pic.png ", "pic.png", ""},
		{"9 double slash — preserved, NOT collapsed", "assets//pic.png", "assets//pic.png", ""},
		{"10a uppercase extension — Target case preserved", "pic.PNG", "pic.PNG", ""},
		{"12 non-image extension target with embedded pipes", "a/b|c|d", "a/b", "c|d"},
	}
	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			got := ParseEmbedTarget(tc.inner)
			if got.Target != tc.wantTarget {
				t.Errorf("Target = %q, want %q", got.Target, tc.wantTarget)
			}
			if got.Size != tc.wantSize {
				t.Errorf("Size = %q, want %q", got.Size, tc.wantSize)
			}
		})
	}
}

// TestIsImageEmbed_ExtensionCaseInsensitiveMatchesTheirRenderer pins row 10a/10b:
// ext-match is case-insensitive (this function returns true for both), but
// callers must keep using the ORIGINAL Target string (not a lowercased copy)
// for the actual fetch — a real object stored as "pic.png" in MinIO is
// case-sensitive and a lowercased request for "pic.PNG" would 404 despite
// IsImageEmbed correctly recognizing it as an image.
func TestIsImageEmbed_ExtensionCaseInsensitiveMatchesTheirRenderer(t *testing.T) {
	upper := ParseEmbedTarget("pic.PNG")
	lower := ParseEmbedTarget("pic.png")
	if !IsImageEmbed(upper.Target) {
		t.Errorf("IsImageEmbed(%q) = false, want true (ext-match is case-insensitive)", upper.Target)
	}
	if !IsImageEmbed(lower.Target) {
		t.Errorf("IsImageEmbed(%q) = false, want true", lower.Target)
	}
	if upper.Target != "pic.PNG" {
		t.Errorf("Target case was altered: got %q, want original case %q preserved", upper.Target, "pic.PNG")
	}
}
