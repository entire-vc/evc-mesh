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
	fileName := fmt.Sprintf("%s_%s.%s", dateStr, slugify(artifactName), ext(contentType))

	parts := []string{subfolder}
	if includeProjectSlug && projectSlug != "" {
		parts = append(parts, projectSlug)
	}
	parts = append(parts, taskDir, fileName)
	return path.Join(parts...)
}
