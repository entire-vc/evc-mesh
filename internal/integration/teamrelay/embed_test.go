package teamrelay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseEmbedTarget(t *testing.T) {
	cases := []struct {
		name       string
		inner      string
		wantTarget string
		wantSize   string
	}{
		{"plain ascii", "photo.png", "photo.png", ""},
		{"with a space", "my photo.png", "my photo.png", ""},
		{"cyrillic", "Проект/схема.png", "Проект/схема.png", ""},
		{"yo letter", "Ёлка.png", "Ёлка.png", ""},
		{"nested folder", "assets/sub/pic.png", "assets/sub/pic.png", ""},
		{"single pipe size", "pic.png|300", "pic.png", "300"},
		{"multiple pipes — everything after the first stays in size", "pic.png|300|extra", "pic.png", "300|extra"},
		{"leading and trailing whitespace", " pic.png ", "pic.png", ""},
		{"whitespace around the pipe", " pic.png | 300 ", "pic.png", "300"},
		{"double slash — NOT collapsed", "assets//pic.png", "assets//pic.png", ""},
		{"uppercase extension — NOT lowercased in Target", "pic.PNG", "pic.PNG", ""},
		{"percent in name", "50%off.png", "50%off.png", ""},
		{"hash in name", "release#2.png", "release#2.png", ""},
		{"question mark in name", "is-this-final?.png", "is-this-final?.png", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseEmbedTarget(tc.inner)
			assert.Equal(t, tc.wantTarget, got.Target)
			assert.Equal(t, tc.wantSize, got.Size)
		})
	}
}

func TestFindEmbedTargets_ScansAWholeDocument(t *testing.T) {
	body := "See ![[diagram.png]] and also ![[photo.jpg|500]].\n\nA [[bare wikilink]] is NOT matched — no leading `!`."
	targets := FindEmbedTargets(body)

	assert.Len(t, targets, 2)
	assert.Equal(t, "diagram.png", targets[0].Target)
	assert.Equal(t, "photo.jpg", targets[1].Target)
	assert.Equal(t, "500", targets[1].Size)
}

func TestFindEmbedTargets_BareWikilinkNeverMatches(t *testing.T) {
	// R2 explicitly does not resolve `[[...]]` without `!` — Team Relay's own
	// renderer treats that form as a dead link on real documents, so copying
	// its resolution rule here would ship links that work on neither side.
	targets := FindEmbedTargets("[[Some Note]] and [[Some Note|alias]]")
	assert.Empty(t, targets)
}

func TestIsImageEmbed(t *testing.T) {
	cases := []struct {
		target string
		want   bool
	}{
		{"photo.png", true},
		{"photo.PNG", true}, // extension is lowercased before the lookup
		{"photo.jpg", true},
		{"photo.jpeg", true},
		{"photo.gif", true},
		{"photo.svg", true},
		{"photo.webp", true},
		{"photo.bmp", true},
		{"photo.ico", true},
		{"clip.mp4", false}, // video — placeholder, not an image fetch
		{"song.mp3", false}, // audio — placeholder, not an image fetch
		{"Notes/Welcome.md", false},
		{"no-extension", false},
		{"trailing.dot.", false},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			assert.Equal(t, tc.want, IsImageEmbed(tc.target))
		})
	}
}
