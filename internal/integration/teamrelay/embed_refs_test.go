package teamrelay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Both editor syntaxes must discover local attachments without importing
// external URLs, notes, or video placeholders into document storage.
func TestFindImageRefsLocalAttachments(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []ImageRef
	}{
		{"wiki image", "![[assets/photo.PNG|300]]", []ImageRef{{Target: "assets/photo.PNG", Wiki: true}}},
		{"wiki pdf", "![[report.PDF]]", []ImageRef{{Target: "report.PDF", Wiki: true}}},
		{"markdown image and alt", "![A diagram](assets/diagram.png)", []ImageRef{{Target: "assets/diagram.png", Alt: "A diagram"}}},
		{"markdown title", "![Logo](logo.webp \"Company logo\")", []ImageRef{{Target: "logo.webp", Alt: "Logo"}}},
		{"angle path with spaces", "![Plan](<plans/my plan.png>)", []ImageRef{{Target: "plans/my plan.png", Alt: "Plan"}}},
		{"angle whitespace", "![](< photo.jpg >)", []ImageRef{{Target: "photo.jpg"}}},
		{"relative directories", "![](../images/chart.png)", []ImageRef{{Target: "../images/chart.png"}}},
		{"colon inside folder", "![[Notes/draft:v2.png]]", []ImageRef{{Target: "Notes/draft:v2.png", Wiki: true}}},
		{"wiki external", "![[https://example.invalid/photo.png]]", nil},
		{"markdown external", "![remote](https://example.invalid/photo.png)", nil},
		{"protocol relative", "![](//example.invalid/photo.png)", nil},
		{"absolute local", "![[/tmp/photo.png]]", nil},
		{"fragment", "![](#photo.png)", nil},
		{"other scheme", "![](obsidian:photo.png)", nil},
		{"wiki note", "![[Notes/readme.md]]", nil},
		{"markdown non binary", "![](readme.md)", nil},
		{"video placeholder", "![[movie.mp4]]", nil},
		{"no extension", "![[image]]", nil},
		{"empty angle target", "![](<>)", nil},
		{"plain link", "[report](report.pdf)", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindImageRefs(tc.body)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestIsEmbeddableFileAllowsImagesAndPDFOnly(t *testing.T) {
	for _, name := range []string{"photo.png", "photo.JPG", "report.pdf", "report.PDF"} {
		assert.True(t, IsEmbeddableFile(name), name)
	}
	for _, name := range []string{"photo.svg", "note.md", "movie.mp4", "archive.zip", "image", "image."} {
		assert.False(t, IsEmbeddableFile(name), name)
	}
}
