package teamrelay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var fixedDate = time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)

func TestBuildPath_Standard(t *testing.T) {
	result := BuildPath(
		"artifacts",
		"my-project",
		true,
		"abc12345",
		"output.log",
		"text/plain",
		fixedDate,
	)
	// .log extension is preserved; dir is just the short ID.
	assert.Equal(t, "artifacts/my-project/abc12345/2026-05-22_output.log", result)
}

func TestBuildPath_NoProjectSlug(t *testing.T) {
	result := BuildPath(
		"shared",
		"my-project",
		false, // no project slug in path
		"deadbeef",
		"report",
		"application/json",
		fixedDate,
	)
	assert.Equal(t, "shared/deadbeef/2026-05-22_report.json", result)
}

func TestBuildPath_SpecialCharsInArtifactName(t *testing.T) {
	result := BuildPath(
		"drops",
		"proj",
		true,
		"f00dcafe",
		"artifact",
		"text/plain",
		fixedDate,
	)
	assert.Equal(t, "drops/proj/f00dcafe/2026-05-22_artifact.txt", result)
}

func TestBuildPath_MarkdownContentType(t *testing.T) {
	result := BuildPath(
		"notes",
		"proj",
		false,
		"00000001",
		"release",
		"text/markdown",
		fixedDate,
	)
	assert.Equal(t, "notes/00000001/2026-05-22_release.md", result)
}

func TestBuildPath_HTMLContentType(t *testing.T) {
	result := BuildPath(
		"reports",
		"proj",
		false,
		"cafebabe",
		"summary",
		"text/html",
		fixedDate,
	)
	assert.Equal(t, "reports/cafebabe/2026-05-22_summary.html", result)
}

func TestBuildPath_UnknownContentType(t *testing.T) {
	result := BuildPath(
		"misc",
		"proj",
		false,
		"12345678",
		"data",
		"application/octet-stream",
		fixedDate,
	)
	assert.Equal(t, "misc/12345678/2026-05-22_data.bin", result)
}

func TestBuildPath_DirIsShortIDOnly(t *testing.T) {
	result := BuildPath(
		"folder",
		"proj",
		false,
		"shortid1",
		"file",
		"text/plain",
		fixedDate,
	)
	// Directory is just the short ID — no title slug appended.
	assert.Equal(t, "folder/shortid1/2026-05-22_file.txt", result)
}

func TestBuildPath_EmptySubfolder(t *testing.T) {
	result := BuildPath(
		"",
		"proj",
		true,
		"aabbccdd",
		"output",
		"text/plain",
		fixedDate,
	)
	// path.Join strips leading slash when subfolder is empty.
	assert.Equal(t, "proj/aabbccdd/2026-05-22_output.txt", result)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"Fix bug #42", "fix-bug-42"},
		{"  spaces  ", "spaces"},
		{"UPPER CASE", "upper-case"},
		{"multiple---dashes", "multiple-dashes"},
		{"unicode_and_dashes-ok", "unicode-and-dashes-ok"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.expected, slugify(tc.input))
		})
	}
}

// TestBuildPath_MarkdownFilename reproduces the production bug:
// uploading "tr-smoke-2026-06-05.md" with application/octet-stream should yield .md, not .bin.
func TestBuildPath_MarkdownFilename(t *testing.T) {
	result := BuildPath(
		"mesh",
		"dev",
		false,
		"b4822eb8",
		"tr-smoke-2026-06-05.md",
		"application/octet-stream",
		fixedDate,
	)
	assert.Equal(t, "mesh/b4822eb8/2026-05-22_tr-smoke-2026-06-05.md", result)
}

// TestBuildPath_ExtensionPreservedOverMime checks that an explicit .json extension
// wins over a conflicting content-type (e.g. text/plain passed by the caller).
func TestBuildPath_ExtensionPreservedOverMime(t *testing.T) {
	result := BuildPath(
		"drops",
		"proj",
		false,
		"deadbeef",
		"config.json",
		"text/plain",
		fixedDate,
	)
	assert.Equal(t, "drops/deadbeef/2026-05-22_config.json", result)
}

// TestBuildPath_ArtifactNameWithDatePrefix verifies that a date-prefixed artifact
// name does not get a second date prepended.
func TestBuildPath_ArtifactNameWithDatePrefix(t *testing.T) {
	date := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	result := BuildPath(
		"artifacts",
		"mesh-dev",
		true,
		"7367f205",
		"2026-06-11-roundtrip-test-riker.md",
		"text/markdown",
		date,
	)
	// Date must not be duplicated.
	assert.Equal(t, "artifacts/mesh-dev/7367f205/2026-06-11-roundtrip-test-riker.md", result)
}

func TestExt(t *testing.T) {
	tests := []struct {
		contentType string
		expected    string
	}{
		{"text/markdown", "md"},
		{"text/x-markdown", "md"},
		{"application/json", "json"},
		{"text/plain", "txt"},
		{"text/html", "html"},
		{"application/octet-stream", "bin"},
		{"image/png", "bin"},
	}
	for _, tc := range tests {
		t.Run(tc.contentType, func(t *testing.T) {
			assert.Equal(t, tc.expected, ext(tc.contentType))
		})
	}
}
