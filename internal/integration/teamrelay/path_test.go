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
		"Fix the auth bug",
		"output.log",
		"text/plain",
		fixedDate,
	)
	// .log extension is preserved; only the base "output" is slugified.
	assert.Equal(t, "artifacts/my-project/abc12345__fix-the-auth-bug/2026-05-22_output.log", result)
}

func TestBuildPath_NoProjectSlug(t *testing.T) {
	result := BuildPath(
		"shared",
		"my-project",
		false, // no project slug in path
		"deadbeef",
		"Deploy report",
		"report",
		"application/json",
		fixedDate,
	)
	assert.Equal(t, "shared/deadbeef__deploy-report/2026-05-22_report.json", result)
}

func TestBuildPath_SpecialCharsInTitle(t *testing.T) {
	result := BuildPath(
		"drops",
		"proj",
		true,
		"f00dcafe",
		"Task #42: Update API (v2.0) -- urgent!",
		"artifact",
		"text/plain",
		fixedDate,
	)
	// Special characters → replaced with dashes and trimmed.
	assert.Equal(t, "drops/proj/f00dcafe__task-42-update-api-v2-0-urgent/2026-05-22_artifact.txt", result)
}

func TestBuildPath_MarkdownContentType(t *testing.T) {
	result := BuildPath(
		"notes",
		"proj",
		false,
		"00000001",
		"Release notes",
		"release",
		"text/markdown",
		fixedDate,
	)
	assert.Equal(t, "notes/00000001__release-notes/2026-05-22_release.md", result)
}

func TestBuildPath_HTMLContentType(t *testing.T) {
	result := BuildPath(
		"reports",
		"proj",
		false,
		"cafebabe",
		"Status report",
		"summary",
		"text/html",
		fixedDate,
	)
	assert.Equal(t, "reports/cafebabe__status-report/2026-05-22_summary.html", result)
}

func TestBuildPath_UnknownContentType(t *testing.T) {
	result := BuildPath(
		"misc",
		"proj",
		false,
		"12345678",
		"Binary blob",
		"data",
		"application/octet-stream",
		fixedDate,
	)
	assert.Equal(t, "misc/12345678__binary-blob/2026-05-22_data.bin", result)
}

func TestBuildPath_LongTitle(t *testing.T) {
	longTitle := "This is an extremely long task title that goes well beyond sixty characters and should be truncated accordingly by the slugify function"
	result := BuildPath(
		"folder",
		"proj",
		false,
		"shortid1",
		longTitle,
		"file",
		"text/plain",
		fixedDate,
	)
	// Slug is capped at 60 chars.
	taskDir := result[len("folder/shortid1__"):]
	slashIdx := len(taskDir)
	for i, c := range taskDir {
		if c == '/' {
			slashIdx = i
			break
		}
	}
	slug := taskDir[:slashIdx]
	assert.LessOrEqual(t, len(slug), 60, "slug should be at most 60 characters")
}

func TestBuildPath_EmptySubfolder(t *testing.T) {
	result := BuildPath(
		"",
		"proj",
		true,
		"aabbccdd",
		"Simple task",
		"output",
		"text/plain",
		fixedDate,
	)
	// path.Join strips leading slash when subfolder is empty.
	assert.Equal(t, "proj/aabbccdd__simple-task/2026-05-22_output.txt", result)
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
		"TR smoke",
		"tr-smoke-2026-06-05.md",
		"application/octet-stream",
		fixedDate,
	)
	assert.Equal(t, "mesh/b4822eb8__tr-smoke/2026-05-22_tr-smoke-2026-06-05.md", result)
}

// TestBuildPath_ExtensionPreservedOverMime checks that an explicit .json extension
// wins over a conflicting content-type (e.g. text/plain passed by the caller).
func TestBuildPath_ExtensionPreservedOverMime(t *testing.T) {
	result := BuildPath(
		"drops",
		"proj",
		false,
		"deadbeef",
		"Config dump",
		"config.json",
		"text/plain",
		fixedDate,
	)
	assert.Equal(t, "drops/deadbeef__config-dump/2026-05-22_config.json", result)
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
