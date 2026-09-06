package service

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// recommendedDefaultRegex matches the OPTIONAL line by which a marker's author states
// what they will do if nobody answers — the field task #4545660b puts on the task so a
// reader never has to open the comments to find it, and the field task #060ccaae's
// default-on-timeout sweep consumes.
//
// Both languages are listed because markers are written in both across the fleet, and a
// pattern that only understood English would silently record NULL for most real asks —
// the same "absent is indistinguishable from empty" shape this card exists to remove.
// Anchored at line start (?m) so a mention of the words mid-sentence is not harvested as
// a default; the leading decoration (bold markers, a bullet) is optional because agents
// format these by hand.
var recommendedDefaultRegex = regexp.MustCompile(
	`(?im)^[\s>*\-•]*\**\s*(?:recommended[ _-]?default|default(?:\s+if\s+no\s+answer)?|` +
		`по\s+умолчанию|рекомендую\s+по\s+умолчанию|дефолт)\**\s*[:—-]\s*(.+)$`)

// gateDeadlineRegex matches an optional explicit deadline on its own line, e.g.
// "Deadline: 2026-09-09T12:00:00Z" or "Дедлайн: 2026-09-09". Left deliberately narrow —
// only a machine-parseable timestamp counts. A prose deadline ("к пятнице") is NOT
// harvested: guessing a timestamp from prose would put a wrong date on the field the
// timeout sweep acts on, and a wrong deadline is worse than no deadline.
var gateDeadlineRegex = regexp.MustCompile(
	`(?im)^[\s>*\-•]*\**\s*(?:deadline|дедлайн|срок)\**\s*[:—-]\s*` +
		`(\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2})?(?:Z|[+-]\d{2}:?\d{2})?)?)\s*$`)

// extractRecommendedDefault returns the marker's stated default, or "" when the author
// named none. Quoted spans are stripped first so a post-mortem QUOTING the convention
// ("write `По умолчанию: …`") does not have its example harvested as a real default —
// the same defence stripQuotedSpans already gives the negator scan.
func extractRecommendedDefault(body string) string {
	m := recommendedDefaultRegex.FindStringSubmatch(stripQuotedSpans(body))
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), "*"))
}

// extractGateDeadline returns the marker's stated deadline, or nil. Parse failures
// return nil rather than an error: a malformed deadline means "no deadline stated", and
// the arm must still go through — dropping a live ask over a typo'd date would be the
// silent-refusal failure mode all over again.
func extractGateDeadline(body string) *time.Time {
	m := gateDeadlineRegex.FindStringSubmatch(stripQuotedSpans(body))
	if len(m) < 2 {
		return nil
	}
	raw := strings.TrimSpace(m[1])
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04",
		"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

// gateReasonFromMarker returns the ask itself: the text following the marker on its own
// line, falling back to a truncated body when the marker line carries nothing after the
// colon (a common shape — the marker is a header and the question is the paragraph
// below it).
func gateReasonFromMarker(body, slug string) string {
	stripped := stripQuotedSpans(body)
	for _, line := range strings.Split(stripped, "\n") {
		m := blockingMarkerRegex.FindStringSubmatch(line)
		if len(m) < 2 || strings.ToLower(m[1]) != slug {
			continue
		}
		// Everything after the marker match on this line is the inline ask.
		loc := blockingMarkerRegex.FindStringIndex(line)
		tail := strings.TrimSpace(strings.Trim(strings.TrimSpace(line[loc[1]:]), "*:—- "))
		if tail != "" {
			return truncateGateReason(tail)
		}
		break
	}
	return truncateGateReason(strings.TrimSpace(stripped))
}

// maxGateReasonLen bounds what lands in the column. The reason is a pointer to the ask,
// not a second copy of the thread — the comment remains the full record.
const maxGateReasonLen = 2000

func truncateGateReason(s string) string {
	if len(s) <= maxGateReasonLen {
		return s
	}
	// Cut on a rune boundary so the stored text stays valid UTF-8.
	cut := maxGateReasonLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// armInputFromMarker builds the single ArmHumanGateInput a "❓ Blocking @slug" comment
// requests. gate_author is the comment's AUTHENTICATED author — comment_handler.go
// derives AuthorID/AuthorType from the identity on the request, so this is not
// forgeable by editing the body, unlike everything the old text-grepping clients read.
func armInputFromMarker(comment *domain.Comment, task *domain.Task, slug string) domain.ArmHumanGateInput {
	return domain.ArmHumanGateInput{
		TaskID:             task.ID,
		Author:             comment.AuthorID,
		AuthorType:         comment.AuthorType,
		Reason:             gateReasonFromMarker(comment.Body, slug),
		RecommendedDefault: extractRecommendedDefault(comment.Body),
		Deadline:           extractGateDeadline(comment.Body),
		Class:              blockingMarkerClassForSlug(comment.Body, slug),
		Source:             domain.ArmHumanGateSourceMarker,
	}
}
