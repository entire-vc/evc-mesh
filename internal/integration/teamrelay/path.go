package teamrelay

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// dateLeadRe matches a YYYY-MM-DD prefix at the start of an already-dated filename
// so we can skip prepending the date a second time.
var dateLeadRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

// slugify converts a string to a URL-friendly slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ext maps a MIME content type to a file extension.
func ext(contentType string) string {
	switch {
	case strings.Contains(contentType, "markdown") || strings.HasSuffix(contentType, "/md"):
		return "md"
	case strings.Contains(contentType, "json"):
		return "json"
	case strings.Contains(contentType, "plain"):
		return "txt"
	case strings.Contains(contentType, "html"):
		return "html"
	default:
		return "bin"
	}
}

// BuildPath constructs the relay file path in the format:
//
//	subfolder/[projectSlug/]taskShortID/[YYYY-MM-DD_]artifactNameSlug.ext
//
// The date prefix is omitted when artifactName already starts with YYYY-MM-DD.
func BuildPath(subfolder, projectSlug string, includeProjectSlug bool, taskShortID, artifactName, contentType string, date time.Time) string {
	dateStr := date.UTC().Format("2006-01-02")
	taskDir := taskShortID

	// Preserve the original file extension so "report.md" → "report.md", not "report-md.bin".
	// Slugify only the base name; fall back to mime-derived extension when absent.
	origExt := path.Ext(artifactName)
	baseName := artifactName
	if origExt != "" {
		baseName = artifactName[:len(artifactName)-len(origExt)]
	}
	fileExt := origExt
	if fileExt != "" {
		fileExt = fileExt[1:] // strip leading dot
	} else {
		fileExt = ext(contentType)
	}

	sluggedBase := slugify(baseName)
	var fileName string
	if dateLeadRe.MatchString(sluggedBase) {
		fileName = fmt.Sprintf("%s.%s", sluggedBase, fileExt)
	} else {
		fileName = fmt.Sprintf("%s_%s.%s", dateStr, sluggedBase, fileExt)
	}

	parts := []string{subfolder}
	if includeProjectSlug && projectSlug != "" {
		parts = append(parts, projectSlug)
	}
	parts = append(parts, taskDir, fileName)
	return path.Join(parts...)
}
