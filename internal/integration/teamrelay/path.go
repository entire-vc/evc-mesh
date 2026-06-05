package teamrelay

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

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
//	subfolder/[projectSlug/]taskShortID__taskTitleSlug/YYYY-MM-DD_artifactNameSlug.ext
func BuildPath(subfolder, projectSlug string, includeProjectSlug bool, taskShortID, taskTitle, artifactName, contentType string, date time.Time) string {
	dateStr := date.UTC().Format("2006-01-02")
	taskSlug := slugify(taskTitle)
	taskDir := fmt.Sprintf("%s__%s", taskShortID, taskSlug)

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

	fileName := fmt.Sprintf("%s_%s.%s", dateStr, slugify(baseName), fileExt)

	parts := []string{subfolder}
	if includeProjectSlug && projectSlug != "" {
		parts = append(parts, projectSlug)
	}
	parts = append(parts, taskDir, fileName)
	return path.Join(parts...)
}
