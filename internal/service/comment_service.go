package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/presence"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// mentionRegex matches @-slug references in comment bodies.
// Slug pattern mirrors agents.slug and users.username: starts/ends with alnum, middle may contain hyphens, 2-40 chars total.
// The leading boundary allows start-of-string, whitespace, or open bracket/paren/brace.
//
// Case-insensitive (#e0a6ff03, 2026-09-03): `agents.slug`/`users.username` are
// stored lowercase and looked up exact-match, but a HANDLE as written in prose
// is not — "@Daedalus", "@Linus" are ordinary capitalized words to a human or an
// agent typing naturally, same as `blockingMarkerRegex`/`blockingMarkerClassRegex`
// below already treat their own slug/keyword text. Before this fix the character
// class was `[a-z0-9]` only, so an uppercase-led handle did not match this regex
// AT ALL — not "resolved to nobody", simply never became a candidate — and
// `extractMentionSlugs`'s `strings.ToLower` never got a chance to run. That made
// the failure invisible from both ends: `notifyMentions` only ever writes a
// delivery-outcome row for a slug it extracted, so an uppercase handle produced
// no comment_mentions row AND no `recipient_unknown` outcome row — a silent miss
// indistinguishable from "nobody was mentioned", live-measured on prod (`@Linus`
// vs `@linus` on an identical fixture, 0 delivery records vs 1). `(?i)` widens
// only the two `[a-z0-9]` character classes to `[A-Za-z0-9]`; the boundary
// anchors (`\s`, brackets, `\b`) are untouched by it.
var mentionRegex = regexp.MustCompile(`(?i)(?:^|[\s(\[{])@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b`)

// blockingMarkerRegex matches the "❓ **Blocking @<user>**" workflow marker
// (CLAUDE-workflow.md §0b) anchored to the start of a line. It tolerates the optional
// ❓ emoji and markdown bold (`**`), and is case-insensitive/multiline.
//   - The slug subpattern is identical to mentionRegex (and the username/slug DB
//     constraint): starts/ends with alnum, hyphens allowed in the middle, no underscore.
//   - "ℹ️ **FYI @user**" has no "Blocking" keyword → never matches (FYI is a no-op).
//   - A quoted line ("> ❓ **Blocking @x**") is NOT matched: after `^\s*`, the `>` is
//     neither whitespace nor one of ❓/`*`/`Blocking`, so the anchor fails.
var blockingMarkerRegex = regexp.MustCompile(`(?im)^\s*(?:❓\s*)?\*{0,2}\s*Blocking\s+@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b`)

// systemActorID is the author_id used for system-generated comments. uuid.Nil is safe:
// comments.author_id is NOT NULL but carries no foreign key, and the author_name SELECT
// resolves system comments to NULL (rendered as "System" in the UI) regardless of value.
var systemActorID = uuid.Nil

// hasBlockingMarker reports whether body contains a "❓ **Blocking @user**" marker.
//
// The body is passed through stripQuotedSpans first, so a marker that only appears
// inside inline code, a fenced block, or a blockquote does NOT count — quoting the
// template while explaining the mechanism must not arm a gate. The regex is already
// line-anchored (which excluded `> `-quoted markers by accident); the strip makes
// that property deliberate and extends it to code spans and fences.
func hasBlockingMarker(body string) bool {
	return blockingMarkerRegex.MatchString(stripQuotedSpans(body))
}

// stripQuotedSpans blanks out the parts of a markdown body that are QUOTATION rather
// than assertion: fenced code blocks (``` / ~~~), blockquote lines (`>`), and inline
// code spans (`…`). Everything else is preserved verbatim, and every input line yields
// exactly one output line — so line-anchored regexes keep their meaning on the residue.
//
// Why this exists (task #5c69b4e5, live probe #a073a896): both the blocking marker and
// the triage-exit negator vocabulary are matched against the whole comment body, the
// negators as bare substrings. A comment that DESCRIBES the mechanism — a post-mortem
// quoting `не нужен` in backticks — therefore performed it: a live human_gate was
// released 11 ms after a summary comment that had no intent to withdraw anything, and
// the real, intentional withdrawal 1.75 s later became a no-op. Documenting the gate
// disarmed the gate. The same root cause silently moved ask OWNERSHIP when a marker
// comment quoted negators further down its own body.
//
// Ambiguity fails CLOSED (keeps the gate): an unterminated fence swallows the rest of
// the body, an unterminated inline-code run swallows the rest of its line. A negator
// hidden behind malformed markdown is not counted, so the gate stays up — the safe
// direction for a mechanism whose whole job is to say "a human is still needed here".
func stripQuotedSpans(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")

		if inFence {
			// A closing fence is a run of >= fenceLen of the SAME char, per CommonMark.
			if c, n := fenceRun(trimmed); c == fenceChar && n >= fenceLen {
				inFence = false
			}
			out = append(out, "")
			continue
		}
		if c, n := fenceRun(trimmed); n >= 3 {
			inFence = true
			fenceChar = c
			fenceLen = n
			out = append(out, "")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			out = append(out, "")
			continue
		}
		out = append(out, stripInlineCode(line))
	}

	return strings.Join(out, "\n")
}

// fenceRun returns the fence character and the length of the leading run of it, for a
// line already left-trimmed. length == 0 means the line does not begin with a fence char.
func fenceRun(trimmed string) (char byte, length int) {
	if trimmed == "" {
		return 0, 0
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	return c, n
}

// stripInlineCode removes every `…`-delimited code span from a single line, delimiters
// included. A span opened by a run of N backticks closes at the next run of exactly N
// (CommonMark), which is what lets a double-backtick span carry a single backtick
// inside it. An unterminated run drops the rest of the line — see the fail-closed
// note on stripQuotedSpans.
func stripInlineCode(line string) string {
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			out.WriteByte(line[i])
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		n := j - i

		closed := -1
		for k := j; k < len(line); {
			if line[k] != '`' {
				k++
				continue
			}
			m := k
			for m < len(line) && line[m] == '`' {
				m++
			}
			if m-k == n {
				closed = m
				break
			}
			k = m
		}
		if closed == -1 {
			return out.String() // unterminated → fail closed
		}
		i = closed
	}
	return out.String()
}

// autoMarkerSubstrings marks a comment as automation-generated (server or fiddler).
// Such comments must NOT be treated as real "❓ Blocking @user" gates even if they
// incidentally match the blocking marker regex.
var autoMarkerSubstrings = []string{
	"🤖 auto:", "🤖 auto:",
	"[fiddler]",
	"переведена в triage", "moving to triage",
}

// isAutoGeneratedComment returns true when the body contains an automation marker.
func isAutoGeneratedComment(body string) bool {
	lower := strings.ToLower(body)
	for _, m := range autoMarkerSubstrings {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// rawArmMarkerSubstring identifies the system comment task_handler.go's Update
// handler posts whenever human_gate transitions false→true via a raw PATCH or
// UI request — i.e. NOT through a "❓ Blocking @user" comment marker.
//
// Task #a2e2ac72 (follow-up to #9959f201): releaseHumanGateOnWithdrawal grants
// an instant, zero-gap self-clear whenever the withdrawing agent is the ask's
// soleMarkerAuthor (the only agent to have EVER posted a marker on this
// thread). That is correct when the marker is what raised the ask. But a gate
// armed via PATCH/UI has NO marker in the thread at all — so a bystander who
// posts their own marker and immediately negates it also satisfies
// soleMarkerAuthor trivially (there is still only one marker author ever),
// and the two scenarios are byte-identical in the comment log. This system
// comment is the only trace the raw arm leaves; its presence, chronologically
// at-or-before the live marker, is what lets releaseHumanGateOnWithdrawal
// tell "this marker raised the ask" apart from "this marker was fabricated
// onto an already-armed gate".
const rawArmMarkerSubstring = "human_gate взведён напрямую"

// hasRawArmMarker reports whether a comment IS task_handler.go's raw PATCH/UI
// arm system comment. See rawArmMarkerSubstring for why this exists.
//
// Fixed 2026-07-31 (task #15694816, found in cross-verification of #486): the
// original version matched on body substring alone, with no author check.
// task_handler.go's Create/comment_handler.go always derives a comment's
// AuthorType from the caller's OWN authenticated identity (agent or user) for
// anything posted through the public API — AuthorType=system is never
// assignable that way. So requiring authorType == ActorTypeSystem here is the
// one check that makes this unforgeable: without it, ANY agent could post an
// ordinary comment containing this substring before a real ask ever existed,
// permanently pinning lastRawArmAt in the past and stripping every future
// legitimate sole-owner withdrawal on that task of its zero-gap fast path —
// pure griefing (it can only ADD friction, never remove a live gate early,
// so it was never an ownership bypass), but real and worth closing.
func hasRawArmMarker(body string, authorType domain.ActorType) bool {
	if authorType != domain.ActorTypeSystem {
		return false
	}
	return strings.Contains(strings.ToLower(body), strings.ToLower(rawArmMarkerSubstring))
}

// triageExitNegators are lower-cased substrings that, when present in a blocking comment,
// indicate the block was cancelled — so the comment is NOT a live gate.
//
// Extended 2026-08-24 (task #62560d6d, live incident #68df3b62): the
// vocabulary was Russian-only apart from "resolved"/"async",
// which read as process jargon rather than general negation. A genuine
// withdrawal written in English ("Retracting the gate marker... not as a
// second independent ask... nothing further needed here") matched none of
// it and no-opped silently — same shape as the language gap already closed
// for blockerStillOpenMarkers ("still blocked") and askWords/repeatPingWords
// (already bilingual) two sections down, just never carried over to this
// list. CLAUDE.md asks agents to write task comments in Russian, but the
// mechanism should not depend on that policy being followed to fail safely.
var triageExitNegators = []string{
	"не нужен", "не нужно", "не требуется",
	"снят", "снято",
	"resolved", "async",
	"разблок", "уже ответил", "ответил в коммент",
	"not needed", "not required", "no longer needed",
	"withdrawn", "withdrawing", "retracting",
	"unblocked",
}

// hasNegatorInScope reports whether body carries a triageExitNegators substring,
// scoped by negatorScope: text at-or-after the comment's OWN blocking marker
// (the LAST one, if several) — or, when the body carries NO marker, the body's
// OWN LAST PARAGRAPH. Never text that only precedes either boundary.
//
// READ THAT SECOND HALF BEFORE THE HISTORY BELOW. The marker-less case is the
// ordinary shape of a real withdrawal, and the scope there is the last
// paragraph — NOT the whole body. An earlier revision of this comment said
// "the whole body is used", and the paragraph that superseded it (#1e5be182,
// four paragraphs down) was easy to stop short of: an agent whose withdrawal
// silently failed came here to find out why, read the older sentence as
// current, and concluded the mechanism agreed with them. Measured live
// 2026-08-20 on #58d8bb8d (prod-sha a635137, Bill): three withdrawals by the
// marker's own owner, clearable_by_owner=true — a detailed one with the
// negator in its heading and a two-paragraph one with the negator FIRST both
// no-opped; the same words as a single line cleared the gate instantly. The
// superseded sentence is kept below, marked, because the reasoning that
// produced it is still the reasoning behind the marker case — but it no
// longer describes the marker-less one. Filed as #17829fcf.
//
// Fixed 2026-07-30 (task #c375905c, live incident on #7f646f08, found by Riker):
// a whole-body strings.Contains search let a comment self-negate its OWN marker
// via unrelated prose. Riker's comment raised a fresh "❓ Blocking @pavel" as its
// final line, but earlier paragraphs discussed a DIFFERENT, already-resolved ask
// using the word "снят" — a whole-body scan found that substring and treated the
// brand-new marker as pre-cancelled, silently handing ownership of the live ask
// to whoever's marker preceded it. A marker is conventionally the operative ask
// of a comment; analysis before it is context, not itself the thing negated —
// so only text from the marker onward is searched. [SUPERSEDED by #1e5be182,
// below — this revision continued: "A comment with no marker at all (the
// ordinary shape of a real withdrawal — a separate later comment with nothing
// else in it) has no scope to restrict, so the whole body is used." That has
// NOT been true since 2026-07-30; a marker-less body is scoped to its last
// paragraph.]
//
// Extended 2026-07-30 (task #5c69b4e5, live probe #a073a896): the body is first
// passed through stripQuotedSpans, so only text the comment ASSERTS is searched —
// never a negator it merely QUOTES in inline code, a fenced block or a blockquote.
// Marker-scoping alone does not cover this: the comment that disarmed a live gate
// carried NO marker (it was a markerless post-mortem), so its scope was the whole
// body and its backticked `не нужен` counted. Scoping answers "which ask is this
// about"; stripping answers "is this an assertion or a citation". Both are needed.
//
// Extended again 2026-07-30 (task #1e5be182, live incident on #f46d5589, Bill):
// the "no marker → whole body" fallback above assumed a real withdrawal is always
// a short, dedicated comment with "nothing else in it" — but Bill's genuine status
// update was a ~90-line multi-section report that, in an EARLIER section, revised
// away one framing of the SAME still-live ask ("не нужен") and, in another, said a
// completely unrelated product line was "снят" (dropped from scope) — while the
// comment's own LAST paragraph reaffirmed the ask as the sole remaining option.
// A whole-body scan cannot tell "superseded earlier in this body" from "actually
// withdrawn"; mirroring the marker-scoping precedent above (only the LAST relevant
// unit counts, never an earlier one), a marker-less body is now scoped to its own
// last paragraph — the comment's final say, which is the closest marker-less
// equivalent of "at-or-after the marker".
//
// Also switched from strings.Contains to containsNegatorWholeWord: the same real
// comment contained "мне нужно" (ordinary "what I need"), which Contains matched
// as the negator "не нужно" purely because "мне" ends in "не" — a plain substring
// hit spanning two unrelated words. Go's \b is ASCII-only (RE2's \w excludes every
// Cyrillic letter), so a regexp anchor would not have caught this; the whole-word
// check below inspects the actual rune on each side instead.
//
// Search and slice the SAME stripped string — offsets taken from the raw body
// cannot index the stripped one, and mixing them would cut at a meaningless byte.
// blockerStillOpenMarkers are whole-word/phrase assertions that the blocker
// itself is still live. Present anywhere in a negator scope, they override
// any triageExitNegators match found in the same scope — a comment cannot
// simultaneously say "the blocker is not closed" and have that same body
// read as a withdrawal of the ask. See #3948173f / live case #0f4bd758.
var blockerStillOpenMarkers = []string{
	"не закрыт", "не забыт", "still blocked",
}

// repeatPingWords / askWords: see isRepeatPingNegation.
var repeatPingWords = []string{"повторный", "повторно", "дублир", "repeat", "duplicate"}
var askWords = []string{"ask", "пинг", "вопрос", "ping", "question"}

// repeatPingWindowBytes bounds the proximity check in isRepeatPingNegation.
const repeatPingWindowBytes = 60

// isRepeatPingNegation reports whether the text around a triageExitNegators
// match — [start,end) in haystack — is talking about not repeating a PING/ask
// ("повторный ask здесь не нужен") rather than withdrawing the underlying
// blocker. Fixed 2026-08-10 (task #3948173f, live incident on #0f4bd758,
// found by Bill): CLAUDE-workflow.md §0b tells agents not to re-ping Pavel
// about a state he's already seen, so a compliant agent writes exactly this
// phrase — and the same "не нужен" that means "don't ask again" was read by
// hasNegatorInScope as "the ask itself is no longer needed", silently
// releasing a live money-critical gate. The rule and the mechanism
// contradicted each other; this closes the mechanism side by refusing to
// count a negator that sits next to both a "repeat" word and an "ask" word.
func isRepeatPingNegation(haystack string, start, end int) bool {
	winStart := start - repeatPingWindowBytes
	if winStart < 0 {
		winStart = 0
	}
	winEnd := end + repeatPingWindowBytes
	if winEnd > len(haystack) {
		winEnd = len(haystack)
	}
	window := haystack[winStart:winEnd]
	hasRepeat := false
	for _, w := range repeatPingWords {
		if strings.Contains(window, w) {
			hasRepeat = true
			break
		}
	}
	if !hasRepeat {
		return false
	}
	for _, w := range askWords {
		if strings.Contains(window, w) {
			return true
		}
	}
	return false
}

// negatorScope returns the region of an already-stripQuotedSpans'd body that
// hasNegatorInScope searches: from the LAST blocking marker onward if the body
// carries one, else the body's own last paragraph. Split out of
// hasNegatorInScope 2026-08-20 (#17829fcf) so the DIAGNOSIS of a withdrawal
// that did not count (diagnoseNegatorMiss) computes the boundary with the same
// code as the DECISION, rather than a second copy of it — two copies of "where
// does the server look" would drift, and drift here means the server explains
// its refusal by a rule it is no longer applying.
func negatorScope(stripped string) string {
	if matches := blockingMarkerRegex.FindAllStringIndex(stripped, -1); len(matches) > 0 {
		return stripped[matches[len(matches)-1][0]:]
	}
	return lastParagraph(stripped)
}

// blockerStillOpenInScope reports whether an already-lower-cased scope asserts
// that the blocker itself is still live (see blockerStillOpenMarkers).
func blockerStillOpenInScope(lowerScope string) bool {
	for _, m := range blockerStillOpenMarkers {
		if containsNegatorWholeWord(lowerScope, m) {
			return true
		}
	}
	return false
}

// negatorAsserted reports whether an already-lower-cased scope asserts a
// withdrawal negator that survives the isRepeatPingNegation filter. It does
// NOT consider blockerStillOpenInScope — that veto is applied by the caller,
// so a diagnosis can tell "no negator here at all" apart from "a negator that
// this body's own words overrode".
func negatorAsserted(lowerScope string) bool {
	for _, n := range triageExitNegators {
		for _, start := range wholeWordMatches(lowerScope, n) {
			if !isRepeatPingNegation(lowerScope, start, start+len(n)) {
				return true
			}
		}
	}
	return false
}

func hasNegatorInScope(body string) bool {
	lower := strings.ToLower(negatorScope(stripQuotedSpans(body)))
	if blockerStillOpenInScope(lower) {
		return false
	}
	return negatorAsserted(lower)
}

// negatorMissReason names why a body that DOES mention a withdrawal negator was
// nevertheless not read as a withdrawal. Empty means "nothing to explain" —
// either the negator counted, or the body never asserted one at all (an
// ordinary comment, which must stay silent).
type negatorMissReason string

const (
	// negatorMissOutOfScope: the body asserts a negator, but only outside the
	// region negatorScope searches. This is the shape #17829fcf was filed for.
	negatorMissOutOfScope negatorMissReason = "negator-outside-searched-scope"
	// negatorMissBlockerStillOpen: a negator IS in scope, but so is a
	// blockerStillOpenMarkers phrase, which overrides it (#3948173f). A
	// thorough withdrawal that also says what is "не закрыт" kills its own
	// negator this way. NOTE #17829fcf named this as the second cause of the
	// #58d8bb8d failure; measuring it showed otherwise — the phrase there was
	// "не закрытая", and containsNegatorWholeWord needs a word boundary, so no
	// veto ran and paragraph scope alone explains that miss. The trap is real
	// but only for the short forms. See TestDiagnoseNegatorMiss.
	negatorMissBlockerStillOpen negatorMissReason = "blocker-still-open-marker-in-same-scope"
	// negatorMissOnlyQuoted: the negator sits in inline code, a fenced block or
	// a blockquote, so stripQuotedSpans removed it (#5c69b4e5). Deliberate —
	// quoting is not asserting — but indistinguishable from success to the
	// author, so it is reported when the citation sits where an ASSERTION would
	// have counted. Quoted anywhere else it is not reported at all; see
	// diagnoseNegatorMiss for why (pasted logs match "resolved").
	negatorMissOnlyQuoted negatorMissReason = "negator-only-inside-quoted-span"
)

// diagnoseNegatorMiss explains why hasNegatorInScope(body) came back false on a
// body that nevertheless talks about withdrawing. Returns "" when there is
// genuinely nothing to say.
//
// Added 2026-08-20 (#17829fcf). The rejection branch it feeds used to be a bare
// `return` with not one line of log: a withdrawal that missed the scope and a
// withdrawal that was never written produced byte-identical outcomes, and so did
// a withdrawal that WORKED — the author sees their comment published either way.
// Silence in the direction of "the gate stays up forever" is the exact failure
// releaseHumanGateOnWithdrawal exists to prevent (#7f646f08 sat gated 20 days),
// and our own rules push agents toward the thorough comment shape that fails.
//
// Deliberately NOT reported: a negator filtered by isRepeatPingNegation
// ("повторный ask здесь не нужен"). That phrase is what CLAUDE-workflow §0b
// TELLS agents to write when declining to re-ping, so it appears constantly on
// gated cards with no withdrawal intended; announcing a non-withdrawal there
// would be pure noise. See #3948173f for why it stopped counting.
func diagnoseNegatorMiss(body string) negatorMissReason {
	stripped := stripQuotedSpans(body)
	lowerScope := strings.ToLower(negatorScope(stripped))

	if negatorAsserted(lowerScope) {
		if blockerStillOpenInScope(lowerScope) {
			return negatorMissBlockerStillOpen
		}
		return "" // it counted — hasNegatorInScope returned true; caller should not be here
	}
	if negatorAsserted(strings.ToLower(stripped)) {
		return negatorMissOutOfScope
	}
	// The quoted case is scoped on the RAW body deliberately: report it only when
	// the citation sits where an ASSERTION would have counted — i.e. the author
	// put the right words in the right place and merely formatted them as a
	// quote. A negator quoted anywhere else is overwhelmingly a paste, not a
	// withdrawal: triageExitNegators contains "resolved", so a fenced CI log or a
	// JSON status field matches, and reporting those would put a gate notice on
	// every log paste. That noise is the failure mode this whole task is the
	// mirror image of — see the anti-noise controls in the tests.
	if negatorAsserted(strings.ToLower(negatorScope(body))) {
		return negatorMissOnlyQuoted
	}
	return ""
}

// withdrawalMissHint is the agent-facing explanation posted for each
// negatorMissReason. Negator vocabulary inside these strings is deliberately
// wrapped in backticks: stripQuotedSpans drops code spans, so this system
// comment cannot itself be read as a withdrawal by any later scan.
var withdrawalMissHint = map[negatorMissReason]string{
	negatorMissOutOfScope: "слова отзыва есть, но вне области, которую читает сервер — " +
		"при комменте без блокирующего маркера ею является **последний абзац** " +
		"(не считая завершающей подписи вида «— Имя»), и только он.",
	negatorMissBlockerStillOpen: "слова отзыва попали в область, но там же стоит утверждение, " +
		"что блокер всё ещё жив (`не закрыт` / `не забыт` / `still blocked`) — оно перебивает отзыв в той же области.",
	negatorMissOnlyQuoted: "слова отзыва встречаются только внутри кода, цитаты или блока — " +
		"процитированный отзыв не считается заявленным.",
}

// paragraphBreakRegex matches one or more consecutive blank (or whitespace-only)
// lines — the boundary lastParagraph splits on.
var paragraphBreakRegex = regexp.MustCompile(`\n[ \t]*\n`)

// signatureLineRegex matches a paragraph that is nothing but a sign-off: a
// dash (hyphen or em/en dash) followed by one to three short word-tokens and
// nothing else — "— Robert", "- Howard", "— The Fleet". Deliberately does not
// match anything carrying real punctuation or more than a few words, so an
// actual dash-bulleted sentence ("- Fixed the bug and verified live") is
// never mistaken for a signature.
var signatureLineRegex = regexp.MustCompile(`^[-—–]\s*\p{L}[\p{L}'-]*(?:\s+\p{L}[\p{L}'-]*){0,2}$`)

func isSignatureOnlyParagraph(p string) bool {
	return signatureLineRegex.MatchString(p)
}

// lastParagraph returns the final SUBSTANTIVE paragraph of body, trimmed of
// surrounding whitespace. Paragraphs are separated by one or more blank
// lines. A body with no blank line at all is itself a single paragraph and
// is returned unchanged (trimmed) — this keeps every existing short,
// single-paragraph withdrawal comment (the documented ordinary shape)
// behaving exactly as before.
//
// Fixed 2026-08-24 (task #62560d6d, live incident #68df3b62): a genuine
// withdrawal — "Гейт на Павла снят ... нового решения от него не требуется."
// — sat in the SECOND-to-last paragraph of a comment that closed with a
// trailing "— Name" sign-off on its own line. This function returned the
// signature (zero negators), the gate stayed up, and the system said
// nothing wrong had happened because nothing had — by the old rule, the
// signature genuinely was the last paragraph. This convention shows up
// routinely in real withdrawal attempts, so it was never a one-off: any
// withdrawal ending in a sign-off line was silently unreachable through the
// marker-less path. A bare signature asserts nothing, so skipping it to
// reach the paragraph that actually says something is the same "final SAY,
// not an earlier one"
// reasoning #1e5be182 already established for markerless scoping — not a
// loosening of it. Trailing signature paragraphs are skipped; a body that is
// ONLY a signature (or only blank) still correctly returns "".
func lastParagraph(body string) string {
	trimmed := strings.TrimRight(body, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	parts := paragraphBreakRegex.Split(trimmed, -1)
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if p == "" || isSignatureOnlyParagraph(p) {
			continue
		}
		return p
	}
	return ""
}

// containsNegatorWholeWord reports whether needle occurs in haystack as a
// standalone word/phrase: the rune immediately before the match (if any) and
// the rune immediately after it (if any) must not themselves be a letter or
// digit. This is deliberately NOT regexp \b — Go's RE2 defines \w (and hence
// \b) as [0-9A-Za-z_] only, so it never recognises a boundary next to a
// Cyrillic letter; a haystack of "мне нужно" would look, to \b, identical
// mid-word and at a real word edge. Scanning runes directly sidesteps that.
func containsNegatorWholeWord(haystack, needle string) bool {
	return len(wholeWordMatches(haystack, needle)) > 0
}

// wholeWordMatches returns the start byte offset of every standalone
// whole-word/phrase occurrence of needle in haystack, using the same
// boundary rule as containsNegatorWholeWord (see its doc comment for why
// this is hand-rolled rather than regexp \b).
func wholeWordMatches(haystack, needle string) []int {
	if needle == "" {
		return nil
	}
	var out []int
	searchFrom := 0
	for {
		rel := strings.Index(haystack[searchFrom:], needle)
		if rel == -1 {
			return out
		}
		start := searchFrom + rel
		end := start + len(needle)
		if !isWordRuneBefore(haystack, start) && !isWordRuneAfter(haystack, end) {
			out = append(out, start)
		}
		searchFrom = start + 1
	}
}

// isWordRuneBefore reports whether the rune ending exactly at byte offset pos
// (i.e. the last rune of haystack[:pos]) is a letter or digit. pos<=0 (start
// of string) is never a word rune.
func isWordRuneBefore(haystack string, pos int) bool {
	if pos <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(haystack[:pos])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isWordRuneAfter reports whether the rune starting exactly at byte offset pos
// is a letter or digit. pos at or past the end of string is never a word rune.
func isWordRuneAfter(haystack string, pos int) bool {
	if pos < 0 || pos >= len(haystack) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(haystack[pos:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// extractMentionSlugs returns unique lowercase slugs found in body, preserving order.
func extractMentionSlugs(body string) []string {
	matches := mentionRegex.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		slug := strings.ToLower(m[1])
		if !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out
}

// blockingMarkerClassRegex matches an optional "[soft]" class tag (contract
// docs/human-gate-decision-recorded.md §5). Case-insensitive, independent of the
// marker's own anchor so every existing "❓ **Blocking @user**: …" marker keeps working
// unchanged with the implicit default (hard) — a soft class must be requested
// explicitly by whoever asks the question, never inferred.
var blockingMarkerClassRegex = regexp.MustCompile(`(?i)\[\s*soft\s*\]`)

// blockingMarkerClassForSlug returns the human_gate_class the "Blocking @slug" marker
// naming slug requests: soft if THAT marker's own line also carries a "[soft]" tag,
// hard otherwise (fail-closed default — a card is never softened by omission).
//
// Scoped to the specific marker line that names slug, not the whole comment body: a
// body may legitimately carry unrelated bracketed text elsewhere (an aside, a quoted
// example) that must not reclassify a marker it doesn't belong to — same attribution
// hazard blockingMarkerSlugs' own doc calls out for slug resolution.
func blockingMarkerClassForSlug(body, slug string) domain.HumanGateClass {
	stripped := stripQuotedSpans(body)
	for _, line := range strings.Split(stripped, "\n") {
		m := blockingMarkerRegex.FindStringSubmatch(line)
		if len(m) < 2 || strings.ToLower(m[1]) != slug {
			continue
		}
		if blockingMarkerClassRegex.MatchString(line) {
			return domain.HumanGateClassSoft
		}
		return domain.HumanGateClassHard
	}
	return domain.HumanGateClassHard
}

// blockingMarkerSlugs returns unique lowercase slugs captured by blockingMarkerRegex —
// i.e. only the @-mention that directly follows the "Blocking" keyword on each marker
// line, NOT every @-mention anywhere in the comment body. A body can legitimately carry
// unrelated @-mentions (cc'ing someone, referencing an agent) before or after the marker;
// resolving against those via extractMentionSlugs would mis-attribute the triage target
// to whichever mention happens to resolve first, rather than to who "Blocking" actually names.
func blockingMarkerSlugs(body string) []string {
	// Same stripQuotedSpans discipline as hasBlockingMarker: without it a body that
	// carries BOTH a quoted template and a real marker resolves the QUOTED slug first
	// (matches are returned in source order), so firstResolvedUserSlug would triage
	// against whoever the documentation example happened to name.
	matches := blockingMarkerRegex.FindAllStringSubmatch(stripQuotedSpans(body), -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		slug := strings.ToLower(m[1])
		if !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out
}

// truncateDesc truncates a string to maxLen characters.
func truncateDesc(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

type commentService struct {
	commentRepo    repository.CommentRepository
	taskRepo       repository.TaskRepository
	activityRepo   repository.ActivityLogRepository
	agentNotifySvc AgentNotifyService
	agentSvc       AgentService
	notifySvc      NotificationService
	statusRepo     repository.TaskStatusRepository
	projectRepo    repository.ProjectRepository
	ctxCacheInv    ContextCacheInvalidator
	userRepo       repository.UserRepository
	mentionRepo    repository.CommentMentionRepository
	deliveryRepo   repository.CommentDeliveryOutcomeRepository
	wsPublisher    WSPublisher
	taskSvc        TaskService
	hgdRepo        repository.HumanGateDecisionRepository
	depRepo        repository.TaskDependencyRepository
}

// CommentServiceOption configures optional dependencies for CommentService.
type CommentServiceOption func(*commentService)

// WithCommentAgentNotify sets the agent notification service on the comment service.
func WithCommentAgentNotify(ans AgentNotifyService) CommentServiceOption {
	return func(s *commentService) { s.agentNotifySvc = ans }
}

// WithCommentAgentService sets the agent service used to resolve @-mention slugs.
func WithCommentAgentService(as AgentService) CommentServiceOption {
	return func(s *commentService) { s.agentSvc = as }
}

// WithCommentUserRepo sets the user repository used to resolve @username mentions.
func WithCommentUserRepo(r repository.UserRepository) CommentServiceOption {
	return func(s *commentService) { s.userRepo = r }
}

// WithCommentMentionRepo sets the mention repository for persisting comment_mentions rows.
func WithCommentMentionRepo(r repository.CommentMentionRepository) CommentServiceOption {
	return func(s *commentService) { s.mentionRepo = r }
}

// WithCommentDeliveryOutcomeRepo sets the repository that records, for every
// @-addressed handle on a comment, whether it reached anybody and why.
//
// Optional like every other dependency here: when it is nil the delivery
// record is simply not written, and mentions behave exactly as they did
// before. Nothing on the comment path is failed because the verdict about it
// could not be stored.
func WithCommentDeliveryOutcomeRepo(r repository.CommentDeliveryOutcomeRepository) CommentServiceOption {
	return func(s *commentService) { s.deliveryRepo = r }
}

// WithCommentWSPublisher sets the WS publisher used to push badge-update events to users.
func WithCommentWSPublisher(p WSPublisher) CommentServiceOption {
	return func(s *commentService) { s.wsPublisher = p }
}

// WithCommentStatusRepo sets the status repo for building task snapshots.
func WithCommentStatusRepo(r repository.TaskStatusRepository) CommentServiceOption {
	return func(s *commentService) { s.statusRepo = r }
}

// WithCommentProjectRepo sets the project repo for resolving workspace_id.
func WithCommentProjectRepo(r repository.ProjectRepository) CommentServiceOption {
	return func(s *commentService) { s.projectRepo = r }
}

// WithCommentContextCacheInvalidator sets an optional cache invalidator that is
// called after every comment mutation so the parent task's context cache is evicted.
func WithCommentContextCacheInvalidator(inv ContextCacheInvalidator) CommentServiceOption {
	return func(s *commentService) { s.ctxCacheInv = inv }
}

// WithCommentNotificationService sets the notification service for dispatching
// in-app notifications to workspace users when a new comment is created.
func WithCommentNotificationService(ns NotificationService) CommentServiceOption {
	return func(s *commentService) { s.notifySvc = ns }
}

// WithCommentTaskService injects the task service used to auto-move a task to triage
// when a "❓ Blocking @user" marker targeting a human is detected in a comment
// (server-side enforcement of CLAUDE-workflow.md §0b). Optional — if unset, the
// enforcement step is skipped entirely.
func WithCommentTaskService(ts TaskService) CommentServiceOption {
	return func(s *commentService) { s.taskSvc = ts }
}

// WithHumanGateDecisionRepo injects the repository backing the third
// human_gate exit — "decision recorded" (task #c56339b1, contract
// docs/human-gate-decision-recorded.md). Optional — if unset,
// RecordHumanGateDecision/RevokeHumanGateDecision return an error and
// enforceBlockingTriage's repeat-check is skipped (arms as before this
// feature existed).
func WithHumanGateDecisionRepo(repo repository.HumanGateDecisionRepository) CommentServiceOption {
	return func(s *commentService) { s.hgdRepo = repo }
}

// WithCommentDependencyRepo injects the task-dependency repository, used by the
// closed-card follow-up mechanism (comment_closed_task_followup.go) to link the
// follow-up card back to the closed one with a relates_to edge, and to
// recognise its own earlier output when deduping. Optional: unset, follow-up
// cards are still created, just unlinked and undeduped.
func WithCommentDependencyRepo(repo repository.TaskDependencyRepository) CommentServiceOption {
	return func(s *commentService) { s.depRepo = repo }
}

// NewCommentService returns a new CommentService backed by the given repositories.
func NewCommentService(
	commentRepo repository.CommentRepository,
	taskRepo repository.TaskRepository,
	activityRepo repository.ActivityLogRepository,
	opts ...CommentServiceOption,
) CommentService {
	s := &commentService{
		commentRepo:  commentRepo,
		taskRepo:     taskRepo,
		activityRepo: activityRepo,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// maxCommentMetadataBytes caps comment.metadata. The field is a label ("which automation
// posted this, was it an auto-nudge"), not a payload store — 4 KiB is far above any
// legitimate tag set and keeps a JSONB column from becoming an accidental blob dump.
const maxCommentMetadataBytes = 4096

// validateCommentMetadata rejects anything that is not a JSON object, loudly.
//
// Absent/null metadata is legal and means "no metadata" — that is the common case and not
// an error. Anything present but not an object (array, string, number, bool, malformed
// JSON) is refused with 4xx naming the actual shape received.
//
// The refusal is the point. The defect this replaces (#13e391d2) was not that metadata
// was rejected but that it was accepted-and-discarded: the API answered 201 and returned
// `{}`, so a caller could not distinguish "stored" from "thrown away", and neither could
// the detectors reading the field months later. A caller that sends the wrong shape must
// find out from the response, not from a filter that silently never matches.
//
// This validates SHAPE, not provenance. metadata.source is self-declared by whoever posts
// the comment and is not authenticated — it is a cooperative label for distinguishing
// automation from human activity, and must not be treated as proof of origin by anything
// making a security or trust decision.
func validateCommentMetadata(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	if len(raw) > maxCommentMetadataBytes {
		return apierror.ValidationError(map[string]string{
			"metadata": fmt.Sprintf("metadata must be at most %d bytes, got %d", maxCommentMetadataBytes, len(raw)),
		})
	}

	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return apierror.ValidationError(map[string]string{
			"metadata": "metadata must be a valid JSON object: " + err.Error(),
		})
	}

	if _, ok := probe.(map[string]any); !ok {
		return apierror.ValidationError(map[string]string{
			"metadata": fmt.Sprintf("metadata must be a JSON object, got %s", jsonKindName(probe)),
		})
	}

	return nil
}

// jsonKindName names the JSON type of a decoded value for error messages, so a rejected
// caller is told what it actually sent rather than just that something was wrong.
//
// There is deliberately no `case nil`: a bare `null` returns early from
// validateCommentMetadata as legal-and-absent, so nil cannot reach here. The default is
// the only fallback and exists for a value encoding/json does not currently produce when
// decoding into `any` — it is unreachable today rather than a path worth pretending is
// live with its own branch.
func jsonKindName(v any) string {
	switch v.(type) {
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	default:
		return "non-object value"
	}
}

// Create validates and persists a new comment.
// It checks that the task exists, the body is not empty, and if a parent comment
// is specified, the parent exists and belongs to the same task.
func (s *commentService) Create(ctx context.Context, comment *domain.Comment) error {
	if strings.TrimSpace(comment.Body) == "" {
		return apierror.ValidationError(map[string]string{
			"body": "body is required",
		})
	}

	if err := validateCommentMetadata(comment.Metadata); err != nil {
		return err
	}

	// Validate the task exists.
	task, err := s.taskRepo.GetByID(ctx, comment.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return apierror.NotFound("Task")
	}

	// Validate parent comment if provided.
	if comment.ParentCommentID != nil {
		parent, err := s.commentRepo.GetByID(ctx, *comment.ParentCommentID)
		if err != nil {
			return err
		}
		if parent == nil {
			return apierror.NotFound("ParentComment")
		}
		if parent.TaskID != comment.TaskID {
			return apierror.BadRequest("parent comment does not belong to the same task")
		}
	}

	// Resolve workspace ID once — needed below by the mention-handoff gate
	// (must run BEFORE the comment is persisted, not just before it is
	// notified) and again afterwards for agent/user notification.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}

	// Mention-handoff gate (audit §3.1, task #9d8f7606): refuse to persist a
	// comment that @-mentions a fiddler-driven agent lane with no real path
	// for them to see it. Must run before commentRepo.Create — the whole
	// point is that the dead-letter comment never gets written, not merely
	// that it goes un-notified.
	if err := s.enforceMentionHandoffGate(ctx, comment, task, wsID); err != nil {
		return err
	}

	if comment.ID == uuid.Nil {
		comment.ID = uuid.New()
	}

	now := timeNow()
	comment.CreatedAt = now
	comment.UpdatedAt = now

	if err := s.commentRepo.Create(ctx, comment); err != nil {
		return err
	}
	// Re-fetch so the returned comment includes the computed author_name.
	if enriched, err2 := s.commentRepo.GetByID(ctx, comment.ID); err2 == nil && enriched != nil {
		*comment = *enriched
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, comment.TaskID)
	}

	// Notify assigned agent about the new comment, but suppress for terminal tasks
	// (done/cancelled). A task.commented event on a closed task causes the
	// dispatcher to spawn a new agent session whose prompt instructs
	// checkout + move_to_in_progress, silently reopening work that is already
	// shipped (false reactivation churn, incident 2026-06-15 #56a6d5b2).
	// @-mentions in terminal tasks still reach mentioned agents via notifyMentions.
	terminalTask := false
	if s.statusRepo != nil {
		if st, err2 := s.statusRepo.GetByID(ctx, task.StatusID); err2 == nil && st != nil {
			terminalTask = st.Category == domain.StatusCategoryDone || st.Category == domain.StatusCategoryCancelled
		}
	}
	if s.agentNotifySvc != nil && task.AssigneeType == domain.AssigneeTypeAgent && task.AssigneeID != nil && !terminalTask {
		taskSnap := s.buildTaskSnap(ctx, task)

		commentBody := comment.Body
		if len(commentBody) > 500 {
			commentBody = commentBody[:500]
		}

		// Extract actor info from request context.
		actorID, actorType := actorctx.FromContext(ctx)
		actorName := actorctx.NameFromContext(ctx)

		s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
			EventType:   "task.commented",
			Timestamp:   now,
			WorkspaceID: wsID,
			Task:        taskSnap,
			AgentID:     *task.AssigneeID,
			ActorID:     actorID,
			ActorType:   string(actorType),
			ActorName:   actorName,
			Comment: map[string]any{
				"id":        comment.ID,
				"body":      commentBody,
				"author_id": comment.AuthorID,
			},
			TaskID:    task.ID,
			ProjectID: task.ProjectID,
		})
	}

	// Notify @-mentioned agents.
	if wsID != uuid.Nil {
		s.notifyMentions(ctx, comment, task, "", wsID)
		// Server-side enforcement: "❓ Blocking @user" → auto-move task to triage + arm gate.
		s.enforceBlockingTriage(ctx, comment, task, wsID)
		// Symmetric release: user comment after a prior blocking marker → clear human_gate.
		s.releaseHumanGate(ctx, comment, task, wsID)
		// Symmetric release: the SAME agent who raised a still-live ask withdraws it.
		s.releaseHumanGateOnWithdrawal(ctx, comment, task, wsID)
		// General triage EXIT: human user responds on a non-gated triage task → in_progress.
		s.enforceTriageExit(ctx, comment, task, wsID)
	}

	// Closed-card follow-up (audit 1.14, task #754173eb). The suppression a few
	// lines above is correct and stays — a task.commented on a closed card used
	// to reopen shipped work (#56a6d5b2). But suppressing the only channel left
	// the remark with no recipient at all: a comment on a done card reaches
	// nobody, and nothing about writing one looks unusual. So instead of waking
	// the closed card, route the remark onto a card the feed actually polls.
	// Deliberately placed AFTER the comment is persisted: this routes a remark,
	// it never rejects one.
	s.createClosedTaskFollowUp(ctx, comment, task, terminalTask)

	// Dispatch in-app notification to subscribed workspace users for comment.created.
	if s.notifySvc != nil && s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			taskIDCopy := task.ID
			projIDCopy := task.ProjectID
			commentBody := comment.Body
			if len(commentBody) > 200 {
				commentBody = commentBody[:200]
			}
			s.notifySvc.Notify(ctx, domain.NotificationEvent{
				WorkspaceID:     proj.WorkspaceID,
				TaskID:          &taskIDCopy,
				ProjectID:       &projIDCopy,
				EventType:       "comment.created",
				Title:           "New comment on: " + task.Title,
				Body:            commentBody,
				RelevantUserIDs: s.commentParticipants(ctx, comment, task),
				Labels:          []string(task.Labels),
				Metadata: map[string]any{
					"task_id":    task.ID,
					"task_title": task.Title,
					"project_id": task.ProjectID,
					"comment_id": comment.ID,
				},
			})
		}
	}

	return nil
}

// Update validates that the comment exists and persists body changes.
func (s *commentService) Update(ctx context.Context, comment *domain.Comment) error {
	existing, err := s.commentRepo.GetByID(ctx, comment.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Comment")
	}

	actorID, actorType := actorctx.FromContext(ctx)
	if existing.AuthorID != actorID || existing.AuthorType != actorType {
		return apierror.Forbidden("you can only edit your own comments")
	}

	// Only allow body updates; preserve other fields from the existing record.
	oldBody := existing.Body
	existing.Body = comment.Body
	existing.UpdatedAt = timeNow()

	if err := s.commentRepo.Update(ctx, existing); err != nil {
		return err
	}
	// Re-fetch so the caller gets the fully enriched record (author_name etc.).
	if enriched, err2 := s.commentRepo.GetByID(ctx, existing.ID); err2 == nil && enriched != nil {
		*comment = *enriched
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, existing.TaskID)
	}

	// Notify newly @-mentioned agents/users (diff against previous body).
	if s.projectRepo != nil {
		if task, err := s.taskRepo.GetByID(ctx, existing.TaskID); err == nil && task != nil {
			if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
				s.notifyMentions(ctx, existing, task, oldBody, proj.WorkspaceID)
				// Re-evaluate enforcement only when the marker was newly added by this
				// edit (absent in oldBody). The status idempotency check below is a
				// second safeguard against double-triage.
				if !hasBlockingMarker(oldBody) {
					s.enforceBlockingTriage(ctx, existing, task, proj.WorkspaceID)
				}
			}
		}
	}

	return nil
}

// Delete removes a comment after verifying it exists.
func (s *commentService) Delete(ctx context.Context, id uuid.UUID) error {
	existing, err := s.commentRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return apierror.NotFound("Comment")
	}

	actorID, actorType := actorctx.FromContext(ctx)
	if existing.AuthorID != actorID || existing.AuthorType != actorType {
		return apierror.Forbidden("you can only delete your own comments")
	}

	if err := s.commentRepo.Delete(ctx, id); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, existing.TaskID)
	}
	return nil
}

// ListByTask returns a paginated list of comments for the given task, each
// carrying the record of what became of the handles it addressed.
func (s *commentService) ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	pg.Normalize()
	page, err := s.commentRepo.ListByTask(ctx, taskID, filter, pg)
	if err != nil || page == nil {
		return page, err
	}
	s.attachDeliveryOutcomes(ctx, page.Items)
	return page, nil
}

// attachDeliveryOutcomes fills in Delivery for a batch of comments in one
// query, so the record is visible wherever the thread is read rather than
// only to whoever thinks to go looking for it.
//
// Best-effort by design: a thread still renders if the delivery record cannot
// be read. The failure is logged rather than swallowed, because "no rows" and
// "could not read the rows" would otherwise both render as "everything was
// delivered" — the precise ambiguity this feature exists to remove.
func (s *commentService) attachDeliveryOutcomes(ctx context.Context, comments []domain.Comment) {
	if s.deliveryRepo == nil || len(comments) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(comments))
	for i := range comments {
		ids = append(ids, comments[i].ID)
	}
	byComment, err := s.deliveryRepo.ListByCommentIDs(ctx, ids)
	if err != nil {
		log.Printf("[comment-delivery] could not read delivery outcomes for %d comments: %v", len(ids), err)
		return
	}
	for i := range comments {
		if rows, ok := byComment[comments[i].ID]; ok {
			comments[i].Delivery = rows
		}
	}
}

// ListByAuthor returns the caller's own comments, newest first (activity feed).
func (s *commentService) ListByAuthor(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	items, cursor, err := s.commentRepo.ListByAuthor(ctx, authorID, filter)
	if err != nil {
		return nil, err
	}
	return commentViewPage(items, cursor), nil
}

// ListRecentByWorkspace returns workspace-wide recent comments, newest first (activity feed).
func (s *commentService) ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	items, cursor, err := s.commentRepo.ListRecentByWorkspace(ctx, wsID, filter)
	if err != nil {
		return nil, err
	}
	return commentViewPage(items, cursor), nil
}

// commentViewPage splits a repo-level tuple cursor back into the page's two
// JSON fields — NextCursor (RFC3339, read by old and new clients alike) and
// NextCursorID (the tie-breaker new clients should echo back as before_id).
func commentViewPage(items []domain.CommentView, cursor *domain.CommentCursor) *domain.CommentViewPage {
	page := &domain.CommentViewPage{Items: items}
	if cursor != nil {
		page.NextCursor = &cursor.CreatedAt
		page.NextCursorID = &cursor.ID
	}
	return page
}

// buildTaskSnap constructs the task snapshot map used in agent notifications.
func (s *commentService) buildTaskSnap(ctx context.Context, task *domain.Task) map[string]any {
	snap := map[string]any{
		"id":            task.ID,
		"project_id":    task.ProjectID,
		"title":         task.Title,
		"priority":      string(task.Priority),
		"description":   truncateDesc(task.Description, 500),
		"assignee_id":   task.AssigneeID,
		"assignee_type": string(task.AssigneeType),
		"labels":        task.Labels,
	}
	if s.statusRepo != nil {
		if status, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && status != nil {
			snap["status"] = map[string]any{
				"id": status.ID, "name": status.Name, "category": string(status.Category),
			}
		}
	}
	return snap
}

// notifyMentions resolves @-mentioned slugs to agents/users, persists comment_mentions rows,
// sends task.mentioned SSE to agents, and pushes WS badge-update events to users.
// When oldBody is non-empty, only slugs newly added (not in oldBody) are processed.
func (s *commentService) notifyMentions(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	oldBody string,
	workspaceID uuid.UUID,
) {
	newSlugs := extractMentionSlugs(comment.Body)
	if len(newSlugs) == 0 {
		return
	}
	if oldBody != "" {
		oldSet := make(map[string]bool)
		for _, sl := range extractMentionSlugs(oldBody) {
			oldSet[sl] = true
		}
		diff := newSlugs[:0]
		for _, sl := range newSlugs {
			if !oldSet[sl] {
				diff = append(diff, sl)
			}
		}
		newSlugs = diff
		if len(newSlugs) == 0 {
			return
		}
	}

	actorID, actorType := actorctx.FromContext(ctx)
	actorName := actorctx.NameFromContext(ctx)

	commentBody := comment.Body
	if len(commentBody) > 500 {
		commentBody = commentBody[:500]
	}

	var taskSnap map[string]any
	if s.agentNotifySvc != nil {
		taskSnap = s.buildTaskSnap(ctx, task)
	}
	now := timeNow()

	// The task's status category, resolved once: it decides whether the card is
	// in the feed a mentioned agent actually polls, which is the difference
	// between a comment they will be handed and one they will never see.
	taskInTodo := s.taskIsInTodoCategory(ctx, task)

	seenID := make(map[uuid.UUID]bool)
	var dbRows []domain.CommentMention
	// One verdict per addressed handle, recorded whether or not the handle
	// resolved. This slice is the thing that makes a failed mention visible;
	// before it existed, an unresolvable handle wrote nothing anywhere.
	outcomes := make([]domain.CommentDeliveryOutcome, 0, len(newSlugs))

	for _, slug := range newSlugs {
		// Whether this handle named somebody at all. A handle that resolves to
		// nothing is the case with no trace today, so it gets its own row at
		// the bottom of the loop rather than falling off the end silently.
		resolved := false

		// Try agent lookup first.
		//
		// Deliberately does NOT `continue` past a match: an agent slug and a
		// human username live in separate namespaces that nothing keeps
		// disjoint (task f4f47938). A `continue` here used to let the agent
		// match eat the slug — @hugh always resolved to the QA agent, and the
		// person hugh@entire.vc was never notified and never even got a
		// recorded "skipped" row, while enforceBlockingTriage (a fully
		// independent lookup) still resolved the user and froze the card. The
		// two addressing paths do not compete for one delivery slot, so both
		// must run for the same slug — this block falls through into the user
		// lookup below on purpose.
		if s.agentSvc != nil {
			agent, err := s.agentSvc.GetBySlug(ctx, workspaceID, slug)
			if err == nil && agent != nil {
				resolved = true
			}
			if err == nil && agent != nil && !seenID[agent.ID] {
				seenID[agent.ID] = true
				isSelf := actorType == domain.ActorTypeAgent && agent.ID == actorID
				outcomes = append(outcomes, newOutcomeRow(comment.ID, deliveryFacts{
					Slug:            slug,
					Agent:           agent,
					SelfMention:     isSelf,
					StreamConnected: presence.IsConnected(agent.ID),
					InTaskQueue: taskInTodo &&
						task.AssigneeType == domain.AssigneeTypeAgent &&
						task.AssigneeID != nil && *task.AssigneeID == agent.ID,
					Presence: agent.ComputedStatus(presence.IsConnected(agent.ID)),
				}, now))
				dbRows = append(dbRows, domain.CommentMention{
					CommentID:     comment.ID,
					MentionedID:   agent.ID,
					MentionedKind: "agent",
					MentionedSlug: slug,
					ExtractedAt:   now,
				})
				if s.agentNotifySvc != nil && !isSelf {
					s.agentNotifySvc.NotifyAgent(ctx, agent.ID, AgentNotification{
						EventType:   "task.mentioned",
						Timestamp:   now,
						WorkspaceID: workspaceID,
						Task:        taskSnap,
						AgentID:     agent.ID,
						ActorID:     actorID,
						ActorType:   string(actorType),
						ActorName:   actorName,
						Comment: map[string]any{
							"id":        comment.ID,
							"body":      commentBody,
							"author_id": comment.AuthorID,
						},
						TaskID:    task.ID,
						ProjectID: task.ProjectID,
						Payload:   map[string]any{"mentioned_slug": slug},
						// If the durable store rejects the event, the verdict
						// recorded a moment ago is no longer true. Downgrade it
						// rather than leaving a confident "delivered" standing
						// over a write that did not happen — a stale optimistic
						// record is the failure mode this whole table replaces.
						OnPersistErr: s.markDeliveryFailed(comment.ID, slug, domain.RecipientKindAgent),
					})
				}
			}
		}

		// User lookup — always attempted, not just when the agent lookup
		// missed. See the comment above the agent block for why.
		if s.userRepo != nil {
			user, err := s.userRepo.GetByUsername(ctx, workspaceID, slug)
			if err == nil && user != nil {
				resolved = true
			}
			if err == nil && user != nil && !seenID[user.ID] {
				isSelf := actorType == domain.ActorTypeUser && user.ID == actorID
				if !isSelf {
					// Before recording HasSubscription, so a first-time mention
					// is reflected in the very outcome row it produces — not
					// caught up on the next one.
					s.ensureMentionDelivery(ctx, workspaceID, user.ID)
				}
				outcomes = append(outcomes, newOutcomeRow(comment.ID, deliveryFacts{
					Slug:            slug,
					User:            user,
					SelfMention:     isSelf,
					HasSubscription: s.userHasMentionSubscription(ctx, user.ID),
				}, now))
				if isSelf {
					// Recorded above as skipped/self_mention; nothing to send,
					// and no Mention-feed row for naming yourself.
					seenID[user.ID] = true
					continue
				}
				seenID[user.ID] = true
				dbRows = append(dbRows, domain.CommentMention{
					CommentID:     comment.ID,
					MentionedID:   user.ID,
					MentionedKind: "user",
					MentionedSlug: slug,
					ExtractedAt:   now,
				})
				if s.wsPublisher != nil {
					channel := "ws:user:" + user.ID.String()
					_ = s.wsPublisher.Publish(ctx, channel, map[string]any{
						"event":        "mention.badge",
						"workspace_id": workspaceID,
						"task_id":      task.ID,
						"comment_id":   comment.ID,
					})
				}

				// Hand the mention to the notification service as well, so it
				// reaches the channels the mentioned person actually subscribed
				// to — in-app, browser push, email, Telegram.
				//
				// The WS badge above is not a notification: it only lands if
				// that user has the app open at the instant the comment is
				// written, and it is discarded otherwise. Until this dispatch
				// existed, "task.mentioned" was emitted solely to agents via
				// AgentNotifyService, so the "When someone @mentions you"
				// toggle the settings page offers a human could be switched on
				// and would never produce anything on any channel — an
				// advertised subscription with no publisher behind it.
				//
				// TargetUserID is what keeps a mention private to the person
				// mentioned: dispatch delivers a targeted event only to that
				// user's own preference rows, so a comment naming one member
				// is not fanned out to everyone in the workspace who happens
				// to subscribe to task.mentioned.
				s.notifyUserMention(ctx, comment, task, workspaceID, user.ID, actorName)
			}
		}

		if !resolved {
			// The expensive silent case: the comment is published, the handle
			// is highlighted in the rendered body, and it addresses nobody.
			// Until this row, that was indistinguishable from the handle never
			// having been written — a lookup that records only its successes
			// cannot tell "nobody asked" from "every ask missed".
			outcomes = append(outcomes, newOutcomeRow(comment.ID, deliveryFacts{
				Slug: slug,
			}, now))
		}
	}

	if s.mentionRepo != nil && len(dbRows) > 0 {
		_ = s.mentionRepo.InsertBatch(ctx, dbRows)
	}
	if s.deliveryRepo != nil && len(outcomes) > 0 {
		if err := s.deliveryRepo.InsertBatch(ctx, outcomes); err != nil {
			log.Printf("[comment-delivery] failed to record delivery outcomes for comment %s: %v", comment.ID, err)
		}
	}
}

// markDeliveryFailed returns the callback that downgrades one recorded verdict
// to failed when the durable event write errors.
//
// Returns nil when there is no repository to write to, which is what keeps the
// notification payload free of a closure that would do nothing — and keeps the
// hook's own "is it set" check meaningful.
//
// kind is fixed to the caller's recipient, not re-derived here: this hook is
// only ever attached to the agent-notify path (see the call site below), so
// it always downgrades the agent-kind row — never the user row a colliding
// slug may also carry for the same comment_id + slug.
func (s *commentService) markDeliveryFailed(commentID uuid.UUID, slug, kind string) func(error) {
	if s.deliveryRepo == nil {
		return nil
	}
	repo := s.deliveryRepo
	return func(persistErr error) {
		// Deliberately a fresh background context: this runs inside dispatch's
		// own goroutine, long after the request that created the comment has
		// returned and its context has been cancelled. Reusing that context
		// would make the downgrade fail exactly when it is needed.
		if err := repo.MarkFailed(context.Background(), commentID, slug, kind, domain.ReasonEventPersistFailed); err != nil {
			log.Printf("[comment-delivery] could not downgrade %s/@%s(%s) to failed after persist error %v: %v",
				commentID, slug, kind, persistErr, err)
		}
	}
}

// taskIsInTodoCategory reports whether the task currently sits in a
// todo-category status — i.e. whether it is in the feed an assigned agent
// polls via GET /agents/me/tasks?status_category=todo.
//
// Fails CLOSED: if the status cannot be read, the answer is "not in the
// queue". The alternative would let an unreadable status render as a
// confident "delivered", which is the shape of error this record exists to
// eliminate — an unknown must never be reported as a reassurance.
func (s *commentService) taskIsInTodoCategory(ctx context.Context, task *domain.Task) bool {
	if s.statusRepo == nil || task == nil {
		return false
	}
	status, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || status == nil {
		return false
	}
	return status.Category == domain.StatusCategoryTodo
}

// userHasMentionSubscription reports whether the mentioned person has any
// notification preference row that could carry a mention.
//
// Fails CLOSED for the same reason as above, with one extra consideration: a
// person who has never opened notification settings has no preference row at
// all, and dispatch then produces nothing on any channel without erroring.
// That is the state this answer is mostly reporting, and calling it
// "subscribed" because the lookup failed would hide precisely it.
func (s *commentService) userHasMentionSubscription(ctx context.Context, userID uuid.UUID) bool {
	if s.notifySvc == nil {
		return false
	}
	prefs, err := s.notifySvc.GetPreferences(ctx, userID)
	if err != nil {
		return false
	}
	// The same three conditions dispatch applies, in the same order: a row
	// belonging to this user, enabled, and listing this event. Re-deriving the
	// gate loosely here would produce a report that disagrees with the code it
	// is reporting on, which is worse than no report.
	for i := range prefs {
		p := prefs[i]
		if p.UserID == nil || *p.UserID != userID {
			continue
		}
		if !p.IsEnabled {
			continue
		}
		for _, ev := range p.Events {
			if ev == "task.mentioned" {
				return true
			}
		}
	}
	return false
}

// mentionEmailChannel is the notification_preferences channel ensureMentionDelivery
// provisions. Email, not the in-app bell document_watch_service.go uses for
// Watch: a person with no preference row at all also has no app open and no
// Telegram bot bound, and email is the one channel that still reaches them.
const mentionEmailChannel = "email"

// ensureMentionDelivery gives a person who has never configured notification
// preferences somewhere for an @-mention to actually arrive, instead of
// silently recording them as delivered-or-skipped while every channel stays
// empty either way. Root cause measured on prod 2026-08-23 (#4e1d249f):
// notification_preferences had rows for none of the humans being @-mentioned
// in "❓ Blocking" comments, so every one of them landed as skipped/
// no_subscription regardless of how the mention itself was handled.
//
// Deliberately narrow, mirroring documentWatchService.ensureInAppDelivery:
//   - only the email channel. Being mentioned is not consent to be pushed to
//     or messaged on Telegram — those need an explicit opt-in same as today.
//   - only the task.mentioned event, unioned into whatever the row already
//     carries. Never removes an event, never touches another channel.
//   - a row the person has switched OFF is left off. An explicit "no email"
//     outranks the implicit request inside being named in a comment; the
//     mention is still recorded (HasSubscription reflects the real state),
//     and the log says why nothing will arrive.
//
// Runs only for being addressed directly — @-mentioned by name — never for
// merely having commented on the same task. See ensureInAppDelivery's own
// comment for the broader case this deliberately does not cover: silently
// re-adding an event type to the settings of everyone who ever touched a
// task is exactly what an unsubscribe exists to prevent. Being named is a
// narrower, stronger signal than having participated.
//
// Best-effort by construction — a mention that was recorded must not be
// rolled back because the preference row could not be provisioned.
func (s *commentService) ensureMentionDelivery(ctx context.Context, workspaceID, userID uuid.UUID) {
	if s.notifySvc == nil {
		return
	}
	prefs, err := s.notifySvc.GetPreferences(ctx, userID)
	if err != nil {
		log.Printf("[comment-mention] user %s was @-mentioned in workspace %s but their notification preferences could not be read, so email delivery is unconfirmed: %v",
			userID, workspaceID, err)
		return
	}

	var email *domain.NotificationPreference
	for i := range prefs {
		p := &prefs[i]
		if p.UserID == nil || *p.UserID != userID || p.WorkspaceID != workspaceID {
			continue
		}
		// Any enabled channel that already carries task.mentioned is enough —
		// someone already reachable by Telegram does not also need email.
		if p.IsEnabled && coversAll(p.Events, []string{"task.mentioned"}) {
			return
		}
		if p.Channel == mentionEmailChannel {
			email = p
		}
	}

	if email != nil && !email.IsEnabled {
		log.Printf("[comment-mention] user %s was @-mentioned in workspace %s with email notifications switched off — mention recorded, nothing will be delivered there",
			userID, workspaceID)
		return
	}

	pref := &domain.NotificationPreference{
		WorkspaceID: workspaceID,
		UserID:      &userID,
		Channel:     mentionEmailChannel,
		Events:      []string{"task.mentioned"},
		IsEnabled:   true,
	}
	if email != nil {
		pref.ID = email.ID
		pref.Config = email.Config
		pref.Events = unionEvents(email.Events, []string{"task.mentioned"})
	}
	if _, err := s.notifySvc.UpsertPreferences(ctx, pref); err != nil {
		log.Printf("[comment-mention] user %s was @-mentioned in workspace %s but the email channel could not be provisioned, so nothing will be delivered there: %v",
			userID, workspaceID, err)
	}
}

// notifyUserMention dispatches the "task.mentioned" notification event for one
// @-mentioned human, so the mention reaches whichever channels that person
// subscribed to rather than only the live WebSocket badge.
//
// Targeted at the mentioned user via TargetUserID: dispatch's personal-event
// rule then restricts delivery to that user's own preference rows. Without it,
// a mention would be broadcast to every task.mentioned subscriber in the
// workspace — which, since the body carries the comment text, would turn one
// person being named into a workspace-wide disclosure.
//
// Best-effort, like every other notify call on this path: a comment is never
// failed because of what could not be announced about it.
func (s *commentService) notifyUserMention(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	workspaceID, mentionedUserID uuid.UUID,
	actorName string,
) {
	if s.notifySvc == nil {
		return
	}

	body := comment.Body
	if len(body) > 200 {
		body = body[:200]
	}

	title := "You were mentioned on: " + task.Title
	if actorName != "" {
		title = actorName + " mentioned you on: " + task.Title
	}

	taskIDCopy := task.ID
	projIDCopy := task.ProjectID
	mentionedCopy := mentionedUserID

	s.notifySvc.Notify(ctx, domain.NotificationEvent{
		WorkspaceID:  workspaceID,
		TaskID:       &taskIDCopy,
		ProjectID:    &projIDCopy,
		TargetUserID: &mentionedCopy,
		EventType:    "task.mentioned",
		Title:        title,
		Body:         body,
		Labels:       []string(task.Labels),
		Metadata: map[string]any{
			"task_id":    task.ID,
			"task_title": task.Title,
			"project_id": task.ProjectID,
			"comment_id": comment.ID,
		},
	})
}

// enforceBlockingTriage is the server-side defense-in-depth for CLAUDE-workflow.md §0b:
// when a comment contains a "❓ **Blocking @user**" marker pointing at a human user, the
// parent task is auto-moved to the project's triage column with an explanatory system
// comment. Every step is best-effort — failures are logged but never block the comment
// mutation that triggered them.
//
// The marker's own line may also carry an optional "[soft]" tag (contract
// docs/human-gate-decision-recorded.md §5, task #4dc9467b) — e.g.
// "❓ **Blocking @pavel** [soft]: …" — which classifies the resulting human_gate as
// soft (see blockingMarkerClassForSlug) instead of the fail-closed default hard.
//
// Guards (in order):
//   - required deps (taskSvc/statusRepo/userRepo) present, else no-op;
//   - body actually carries a Blocking marker, else no-op;
//   - human-gate: the slug the marker itself names resolves to a user, not just an agent
//     (agents are notified via SSE and do not need a triage move; a typo'd, unregistered,
//     or agent-only slug logs a warning and is a no-op rather than triaging on a bad mention);
//   - idempotency: the task is not already in triage/done/cancelled;
//   - the project actually has a triage status column.
func (s *commentService) enforceBlockingTriage(ctx context.Context, comment *domain.Comment, task *domain.Task, wsID uuid.UUID) {
	if s.taskSvc == nil || s.statusRepo == nil || s.userRepo == nil {
		return
	}
	if !hasBlockingMarker(comment.Body) {
		return
	}

	// Human-gate: only act when the slug the "Blocking" marker actually names (not just
	// any @-mention elsewhere in the body) resolves to a real user, not just an agent.
	// Resolve this BEFORE the auto-mode early return so we can set the sticky flag.
	blockingSlugs := blockingMarkerSlugs(comment.Body)
	userSlug := s.firstResolvedUserSlug(ctx, wsID, blockingSlugs)
	if userSlug == "" {
		if len(blockingSlugs) > 0 {
			log.Printf("[comment-triage] WARNING: Blocking marker on task %s names unresolved slug(s) %v (typo, agent, or unregistered user) — human_gate not armed", task.ID, blockingSlugs)
		}
		return
	}

	// Repeat-question prevention (contract §6, task #c56339b1): if this marker
	// is a reply to an already-answered marker thread (comment.ParentCommentID
	// matches a recorded decision's question_ref), or explicitly cites an
	// already-decided canonical_key in its metadata, do NOT arm — point at the
	// existing record instead and leave the task feedable. This is identity
	// matching by KEY, never by text similarity (contract §8: a reformulated
	// question with neither reference is not caught, by design).
	if existing := s.findLiveHumanGateDecision(ctx, task.ID, comment); existing != nil {
		s.postExistingDecisionNotice(ctx, task, existing)
		return
	}

	// Arm the sticky human_gate flag regardless of delegation level so the MoveTask
	// gate protects the task even after a delegation_level change (audit P0 #3).
	//
	// This is deliberately placed BEFORE the isAssigneeCompletionReport check below.
	// Arming is what DELIVERS the ask (freezes the card, queues it for the named user);
	// the completion-report heuristic exists to suppress the TRIAGE MOVE, and gating
	// delivery on it made a live ask vanish silently — see the check's own note.
	//
	// Since task #4545660b this goes through the SAME ArmHumanGate the explicit
	// set_human_gate API uses, with gate_author taken from the comment's authenticated
	// author — so the marker path is one CALLER of the single arming implementation,
	// not a second implementation of it. Class is included in that one call (contract
	// §5, task #4dc9467b): computed from userSlug's own marker line rather than the
	// whole body (see blockingMarkerClassForSlug), and always set — never only when
	// soft — so a later hard marker on an unreleased task correctly downgrades it back.
	armIn := armInputFromMarker(comment, task, userSlug)
	if setErr := s.taskSvc.ArmHumanGate(ctx, armIn); setErr != nil {
		log.Printf("[comment-triage] WARNING: ArmHumanGate on task %s failed: %v", task.ID, setErr)
	}

	// Completion reports from the task's own assignee must not trigger the triage MOVE —
	// the blocking marker may appear as handoff/escalation context rather than a
	// genuine work blocker (e.g. "Done. ❓ Blocking @pavel: please close manually").
	// The gate is already armed above: a live marker naming a real user always delivers.
	if isAssigneeCompletionReport(comment, task) {
		return
	}

	// Idempotency: never re-notify/re-triage a task already in triage or a terminal
	// category. Checked before the notify below (not just before the move) — a repeat
	// marker on a task already parked in triage is not a new ask.
	curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || curStatus == nil {
		return
	}
	switch curStatus.Category {
	case domain.StatusCategoryTriage, domain.StatusCategoryDone, domain.StatusCategoryCancelled:
		return
	}

	// Emit blocking_triage notification so the named user is actually alerted. Fired
	// regardless of delegation level and regardless of the triage-entry-strict park
	// decision below — human_gate was just armed unconditionally above for the same
	// reason (a live ask naming a real human always delivers), and an auto-mode task's
	// own workflow choice not to self-triage (or a soft-gate's park instead of a full
	// triage move) has nothing to do with whether the person it names should hear about
	// it. Before this moved here, the notify lived after the triage MOVE below and never
	// fired for auto-mode tasks — which was 103 of 107 currently-armed human_gate tasks
	// in prod (measured 2026-09-06, task #4e1d249f), i.e. the notification this whole
	// mechanism exists for essentially never reached anyone.
	if s.notifySvc != nil {
		targetUser, uErr := s.userRepo.GetByUsername(ctx, wsID, userSlug)
		if uErr == nil && targetUser != nil {
			taskIDCopy := task.ID
			projIDCopy := task.ProjectID
			targetIDCopy := targetUser.ID
			s.notifySvc.Notify(ctx, domain.NotificationEvent{
				WorkspaceID:  wsID,
				TaskID:       &taskIDCopy,
				ProjectID:    &projIDCopy,
				EventType:    "task.blocking_triage",
				Title:        "Blocking question: " + task.Title,
				Body:         fmt.Sprintf("@%s asked a blocking question on this task.", userSlug),
				TargetUserID: &targetIDCopy,
				Labels:       []string(task.Labels),
				Metadata: map[string]any{
					"task_id":    task.ID,
					"task_title": task.Title,
					"project_id": task.ProjectID,
					"user_slug":  userSlug,
				},
			})
		}
	}

	// auto-mode tasks self-manage; the triage MOVE below is suppressed for them (the
	// flag and the notification above are already delivered unconditionally).
	if task.DelegationLevel == domain.DelegationLevelAuto {
		return
	}

	// Triage-entry gate (mid_pipeline.triage_entry_strict, MoveTask's own copy
	// of this same rule in task_service.go): a soft-classed gate armed by
	// anyone other than a human is not a reason to occupy the "needs human eyes
	// now" column — it is designed to resolve itself via the default-on-timeout
	// sweep instead. Evaluated from armIn directly rather than re-fetching task,
	// because ArmHumanGate above already committed exactly these values and a
	// re-read would answer the identical question at the cost of a round trip.
	//
	// Checked BEFORE resolving triageID: when disqualified, this task never
	// attempts the triage move at all, so a project with no triage column still
	// gets the backlog park it would get anyway.
	qualifies := armIn.AuthorType == domain.ActorTypeUser || armIn.Class == domain.HumanGateClassHard
	if !qualifies && s.taskSvc.TriageEntryStrict(ctx, task.ProjectID) {
		s.parkDisqualifiedGate(ctx, task, userSlug)
		return
	}

	// Resolve the project's triage column; graceful no-op if it has none.
	triageID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryTriage)
	if err != nil || triageID == uuid.Nil {
		return
	}

	// Move via TaskService so the move gets activity-log + SSE + auto-transition cascade.
	if err := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &triageID}); err != nil {
		log.Printf("[comment-triage] WARNING: move task %s to triage failed: %v", task.ID, err)
		return
	}

	// Append an explanatory system comment. Written directly through the repo (not
	// s.Create) to avoid re-running the marker parser; the body does not start with a
	// Blocking marker, so it cannot re-trigger enforcement regardless.
	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: задача переведена в triage из-за «❓ Blocking @%s» в комментарии выше (per CLAUDE-workflow.md §0b)",
			userSlug,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[comment-triage] WARNING: create system comment on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// parkDisqualifiedGate is enforceBlockingTriage's fallback for a gate that does
// not qualify for triage under mid_pipeline.triage_entry_strict (see
// qualifiesForTriage, triage_entry.go): a soft-classed marker posted by anyone
// other than a human. Rather than leave the task wherever it already was —
// which is what happens if the caller just lets the disqualified MoveTask
// attempt fail — this parks it to backlog with a due_date, mirroring the
// arm-before-move ordering task_lease_reaper.parkTask uses for the same
// reason: a task armed with a due_date but not yet moved is simply a task
// with a due_date, harmless and retryable, whereas moved-but-unarmed is a
// silent, invisible park.
//
// No kind:monitor label is added here, unlike parkTask's stall park: this
// task carries human_gate=true, and Pavel's weekly digest already walks
// gate_scope_backlog (canon-human-gate-digest-weekly-full-backlog) — a second
// wake path would be redundant, not protective.
func (s *commentService) parkDisqualifiedGate(ctx context.Context, task *domain.Task, userSlug string) {
	if s.taskRepo == nil || s.statusRepo == nil || s.taskSvc == nil {
		return
	}
	backlogID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryBacklog)
	if err != nil || backlogID == uuid.Nil {
		log.Printf("[triage-entry] project %s has no backlog status; leaving task %s wherever the disqualified gate found it", task.ProjectID, task.ID)
		return
	}

	dueHours := s.taskSvc.TriageParkDueHours(ctx, task.ProjectID)
	due := timeNow().Add(time.Duration(dueHours) * time.Hour)

	fresh, err := s.taskRepo.GetByID(ctx, task.ID)
	if err != nil || fresh == nil {
		log.Printf("[triage-entry] WARNING: re-fetch of task %s before park failed: %v", task.ID, err)
		return
	}
	updated := *fresh
	updated.DueDate = &due
	if err := s.taskRepo.Update(ctx, &updated); err != nil {
		log.Printf("[triage-entry] WARNING: failed to arm due_date on task %s, leaving it unparked: %v", task.ID, err)
		return
	}

	if err := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &backlogID}); err != nil {
		log.Printf("[triage-entry] WARNING: failed to park task %s to backlog: %v", task.ID, err)
		return
	}

	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: «❓ Blocking @%s» — гейт мягкого класса (soft), поставлен не человеком, поэтому карточка "+
				"НЕ ушла в triage, а припаркована в backlog с `due_date` через %d ч (per audit §2.3 C2 / "+
				"mid_pipeline.triage_entry_strict). Ответ Pavel'я снимет гейт как обычно; жёсткие и человеком "+
				"поставленные гейты по-прежнему уходят прямо в triage.",
			userSlug, dueHours,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[triage-entry] WARNING: create system comment on task %s failed: %v", task.ID, err)
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// releaseHumanGate is the symmetric counterpart to enforceBlockingTriage. When a human
// user (Pavel) posts a comment on a task whose human_gate flag is true, and at least one
// prior comment on that task carries a "❓ Blocking @user" marker, the gate is cleared:
//
//  1. human_gate is set to false in the DB;
//  2. a system comment records the release for the audit trail;
//  3. the assignee agent is notified via AgentNotifyService so fiddler/dispatcher
//     sessions wake and can re-feed the task.
//
// Every step is best-effort — failures are logged but never block the comment mutation.
//
// Guards (in order):
//   - taskSvc must be wired;
//   - task.HumanGate must be true;
//   - comment.AuthorType must be ActorTypeUser (agents/system cannot release the gate);
//   - at least one prior comment on the task (excluding this one) must carry a blocking
//     marker (ensures the gate had a backing signal, not just a manual PATCH).
func (s *commentService) releaseHumanGate(ctx context.Context, comment *domain.Comment, task *domain.Task, wsID uuid.UUID) {
	if s.taskSvc == nil {
		return
	}
	if !task.HumanGate {
		return
	}
	if comment.AuthorType != domain.ActorTypeUser {
		return
	}

	// Scan the most recent comments for a prior blocking marker.
	// PageSize 100 covers realistic task depths; SortDir "desc" returns newest first.
	pg := pagination.Params{Page: 1, PageSize: 100, SortDir: "desc"}
	pg.Normalize()
	page, err := s.commentRepo.ListByTask(ctx, task.ID, repository.CommentFilter{IncludeInternal: true}, pg)
	if err != nil {
		log.Printf("[human-gate] WARNING: ListByTask on task %s failed: %v", task.ID, err)
		return
	}

	foundBlocking := false
	if page != nil {
		for _, c := range page.Items {
			if c.ID == comment.ID {
				continue // skip the comment we just persisted
			}
			if hasBlockingMarker(c.Body) {
				foundBlocking = true
				break
			}
		}
	}
	if !foundBlocking {
		return
	}

	// Release the sticky flag.
	if err := s.taskSvc.SetHumanGate(ctx, task.ID, false); err != nil {
		log.Printf("[human-gate] WARNING: SetHumanGate(false) on task %s failed: %v", task.ID, err)
		return
	}

	// enforceTriageRelease: if the task is currently in triage, auto-return it to
	// in_progress so the assignee can resume work without a manual status edit.
	movedFromTriage := false
	if s.statusRepo != nil {
		if curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && curStatus != nil &&
			curStatus.Category == domain.StatusCategoryTriage {
			if inProgressID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryInProgress); err == nil && inProgressID != uuid.Nil {
				if moveErr := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &inProgressID}); moveErr != nil {
					log.Printf("[human-gate] WARNING: move task %s from triage to in_progress failed: %v", task.ID, moveErr)
				} else {
					movedFromTriage = true
				}
			}
		}
	}

	// Append a system comment to document the release in the task's audit trail.
	now := timeNow()
	releaseBody := "🔓 Auto: human_gate снят — Pavel прокомментировал после блокирующего запроса."
	if movedFromTriage {
		releaseBody = "🔓 Auto: human_gate снят, задача переведена из triage → in_progress — Pavel прокомментировал после блокирующего запроса."
	}
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body:       releaseBody,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate] WARNING: create system comment on task %s failed: %v", task.ID, err)
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}

	// Notify the assignee agent so it re-enters the feed on next fiddler cycle.
	if s.agentNotifySvc != nil && task.AssigneeType == domain.AssigneeTypeAgent && task.AssigneeID != nil {
		taskSnap := s.buildTaskSnap(ctx, task)
		actorID, _ := actorctx.FromContext(ctx)
		commentBody := comment.Body
		if len(commentBody) > 500 {
			commentBody = commentBody[:500]
		}
		s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
			EventType:   "task.human_gate_released",
			Timestamp:   now,
			WorkspaceID: wsID,
			Task:        taskSnap,
			AgentID:     *task.AssigneeID,
			ActorID:     actorID,
			ActorType:   string(domain.ActorTypeUser),
			Comment: map[string]any{
				"id":   comment.ID,
				"body": commentBody,
			},
			TaskID:    task.ID,
			ProjectID: task.ProjectID,
		})
	}
}

// minReaffirmToWithdrawalGap is the minimum real time that must separate a
// reaffirming marker from that same (non-sole) agent's own negator before
// releaseHumanGateOnWithdrawal will honour it — see that function's own
// comment (task #9959f201) for the full rationale. 30 minutes is long enough
// that a scripted two-comment sequence in one turn cannot satisfy it, and
// short enough not to meaningfully delay a genuine hand-off that actually
// did the work (the #7f646f08 case took real investigation, not seconds).
const minReaffirmToWithdrawalGap = 30 * time.Minute

// releaseHumanGateOnWithdrawal is the AGENT-side counterpart to releaseHumanGate,
// closing the gap in task #c375905c: a Pavel-ask has no path to be withdrawn once
// its underlying blocker has resolved but Pavel himself hasn't (and may never)
// comment. Left unaddressed, human_gate stays sticky forever — task #7f646f08 sat
// gated 20 days after its triggering alert had already self-healed.
//
// Only the SAME agent who raised the currently-live ask may withdraw it — allowing
// any other agent to do so would let the fleet learn to silence its own Pavel gate,
// which is strictly worse than the original defect. This mirrors how the ask is
// armed: enforceBlockingTriage arms on whoever posts the marker; this releases on
// that same author retracting it, never on a bystander's say-so.
//
// Guards (in order):
//   - taskSvc must be wired;
//   - task.HumanGate must be true;
//   - comment.AuthorType must be ActorTypeAgent (user withdrawal is releaseHumanGate;
//     system/driver comments cannot withdraw anything on anyone's behalf);
//   - comment.Body must ASSERT a withdrawal negator (hasNegatorInScope: the same
//     triageExitNegators vocabulary enforceTriageExit uses, matched against the body
//     with code spans, fences and blockquotes stripped — a comment that merely QUOTES
//     "не нужен" while explaining the mechanism withdraws nothing). ⚠️ The match is
//     NOT run over the whole body: hasNegatorInScope searches only negatorScope —
//     text at-or-after the body's own LAST blocking marker, or, for the marker-less
//     comment that a real withdrawal usually is, the body's LAST PARAGRAPH. A
//     thorough withdrawal whose negator sits in a heading or a first paragraph is
//     a no-op — measured live on #58d8bb8d, 2026-08-20. A second, narrower trap
//     sits next to it: blockerStillOpenMarkers in the SAME scope override the
//     negator, so "…не нужен, но регресс-тест не закрыт" withdraws nothing.
//     That one matches only the short forms; the inflected "не закрытая" does
//     NOT veto (containsNegatorWholeWord needs a word boundary) — both pinned in
//     TestDiagnoseNegatorMiss. Since #17829fcf neither miss is silent — see
//     reportWithdrawalMiss;
//   - of all prior comments that hasBlockingMarker, the CHRONOLOGICALLY LAST one
//     must be authored by this SAME agent (task #5d3d2402: that marker's OWN body
//     may itself carry a negator — e.g. it doubled as an FYI aside — and that no
//     longer disqualifies it as the live ask; see scanHumanGateOwnership's doc).
//     A live ask with no marker found at all, or one raised/most-recently
//     reaffirmed by someone else, is left gated — this is the negative control:
//     an unwithdrawn or another-agent's ask must not clear.
//   - minReaffirmToWithdrawalGap (below) if this agent is not the ask's SOLE
//     marker-author — see that guard's own comment for why.
//   - minReaffirmToWithdrawalGap ALSO applies, even when this agent IS the sole
//     marker-author, if a raw PATCH/UI arm (hasRawArmMarker) predates the live
//     marker (task #a2e2ac72) — a marker posted onto an already-armed gate
//     proves nothing about who raised the ask, so soleMarkerAuthor's fast path
//     must not apply to it.
//
// ListByTask's SortDir is NOT honoured by the real repository (it hardcodes
// ORDER BY created_at ASC regardless — see CommentRepo.ListByTask), unlike what
// releaseHumanGate's own comment above claims. So "most recent" here means the
// LAST qualifying match found while iterating in that real ascending order, not
// the first — do not copy the break-on-first-match idiom other scans in this
// file use for simple existence checks.
// humanGateMarkerScan is the raw result of scanning a task's comment thread
// for who owns its currently-live human_gate ask. Extracted 2026-07-31 (task
// #040cddcf) so releaseHumanGateOnWithdrawal (which GATES clearing on this)
// and GetHumanGateOwner (which only REPORTS it) share ONE predicate — the
// same reason fleet_gate_labels.py is a single imported module rather than
// two copies: two independently-maintained copies of "who owns this ask"
// would drift, and drift here means the API tells an agent something the
// server itself would refuse.
type humanGateMarkerScan struct {
	found                bool
	markerCommentID      uuid.UUID
	markerAuthorID       uuid.UUID
	markerAuthorType     domain.ActorType
	markerAuthorName     string
	markerCreatedAt      time.Time
	soleMarkerAuthor     bool
	rawArmPrecedesMarker bool
}

// scanHumanGateOwnership finds who owns taskID's currently-live human_gate
// ask: the CHRONOLOGICALLY LAST marker-bearing comment. Mirrors
// enforceBlockingTriage's own arm criterion EXACTLY (hasBlockingMarker, no
// negator carve-out, no auto-generated-comment carve-out) so "who armed it"
// is judged by the same rule that actually armed it — see the fixed defect
// below for what happens when the two rules disagree.
//
// Fixed 2026-08-05 (task #5d3d2402, found by Garfield reproducing #649c966b):
// this scan used to skip a marker-bearing comment when hasNegatorInScope(c.Body)
// found a negator AFTER the marker WITHIN THAT SAME COMMENT — e.g. "❓ Blocking
// @pavel: … отвечать не нужно, отзову сам". enforceBlockingTriage arms on
// hasBlockingMarker alone and has never looked at negators, so that comment
// armed human_gate regardless. The two predicates then disagreed on the ask
// this exact comment raised: armed with no owner (scan.found=false ⇒
// ReasonIfNot="no_live_marker"), which releaseHumanGateOnWithdrawal's own
// !scan.found fail-closed guard (below) makes permanent — not even the
// marker's own author could ever withdraw it, because the withdrawal path
// requires scan.found=true to compare authors against. A live census (48
// gated tasks) found 2 real cards stuck exactly this way, both fulfilled asks
// with no path back down.
//
// For "who owns this ask", a negator inside the marker's own body answers a
// different question (whether the ask is even live) than the one this scan
// asks (who raised it, given that it undisputedly IS live — task.HumanGate is
// already true by the time anything calls this). If the gate is up, it has an
// author; hasNegatorInScope stays exactly as it was for the ONE question it
// still answers here — does a SEPARATE, LATER comment (no marker of its own,
// checked by releaseHumanGateOnWithdrawal directly against comment.Body, not
// through this scan) assert a withdrawal of the marker this scan finds.
// Negator narrowing itself (which substrings count, how quoting is stripped)
// is unchanged and out of scope — task #3d148c21 owns that surface.
//
// While iterating, also tracks (a) that marker's own CreatedAt, for the
// time-gap guard callers apply, and (b) whether every marker-bearing comment
// on the whole thread shares ONE author (soleMarkerAuthor, task #9959f201) —
// both computed from the same page fetched once, no second query — and (c)
// whether a raw PATCH/UI arm (hasRawArmMarker, task #a2e2ac72) predates the
// live marker, meaning the marker did not establish anything new.
//
// excludeCommentID, if non-nil, skips that one comment — used by
// releaseHumanGateOnWithdrawal to exclude the withdrawal comment itself from
// being misread as a marker. GetHumanGateOwner (a pure read, no withdrawal
// comment in flight) passes nil.
//
// ListByTask's SortDir is NOT honoured by the real repository (it hardcodes
// ORDER BY created_at ASC regardless — see CommentRepo.ListByTask). So "most
// recent" here means the LAST qualifying match found while iterating in that
// real ascending order, not the first.
func (s *commentService) scanHumanGateOwnership(ctx context.Context, taskID uuid.UUID, excludeCommentID *uuid.UUID) (*humanGateMarkerScan, error) {
	pg := pagination.Params{Page: 1, PageSize: 100}
	pg.Normalize()
	page, err := s.commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{IncludeInternal: true}, pg)
	if err != nil {
		return nil, err
	}

	scan := &humanGateMarkerScan{soleMarkerAuthor: true}
	haveSeenAnyMarkerAuthor := false
	var firstMarkerAuthorSeen uuid.UUID
	var lastRawArmAt time.Time

	if page != nil {
		for _, c := range page.Items {
			if excludeCommentID != nil && c.ID == *excludeCommentID {
				continue
			}
			// Task #a2e2ac72: track the most recent raw PATCH/UI arm marker
			// regardless of whether this comment also happens to carry a
			// blocking marker (it never does — see hasRawArmMarker's doc —
			// but the two checks are independent on principle).
			if hasRawArmMarker(c.Body, c.AuthorType) && c.CreatedAt.After(lastRawArmAt) {
				lastRawArmAt = c.CreatedAt
			}
			if !hasBlockingMarker(c.Body) {
				continue
			}
			// Task #9959f201's soleMarkerAuthor scan intentionally counts EVERY
			// marker-bearing comment ever posted on this thread, negated or not —
			// unlike the "who owns it right now" scan just below, which only cares
			// about the live one. A negated marker still PROVES someone else once
			// held this ask, which is exactly the fact the time-gap guard needs.
			if !haveSeenAnyMarkerAuthor {
				firstMarkerAuthorSeen = c.AuthorID
				haveSeenAnyMarkerAuthor = true
			} else if c.AuthorID != firstMarkerAuthorSeen {
				scan.soleMarkerAuthor = false
			}
			// Deliberately NOT checking hasNegatorInScope(c.Body) here — see the
			// function doc above (#5d3d2402). enforceBlockingTriage armed this gate
			// on hasBlockingMarker alone; ownership must be judged by the same rule,
			// or an armed gate can end up with no owner at all.
			scan.found = true
			scan.markerCommentID = c.ID
			scan.markerAuthorID = c.AuthorID
			scan.markerAuthorType = c.AuthorType
			if c.AuthorName != nil {
				scan.markerAuthorName = *c.AuthorName
			}
			scan.markerCreatedAt = c.CreatedAt
		}
	}

	// Task #a2e2ac72: was the currently-live marker itself what armed the gate,
	// or was the gate already true (raw PATCH/UI, no comment) before this
	// marker was ever posted? soleMarkerAuthor alone cannot tell — a bystander
	// who fabricates one marker onto an already-armed gate is, by that check
	// alone, indistinguishable from the genuine sole raiser of an ask (see the
	// long comment on rawArmMarkerSubstring). A raw-arm system comment at or
	// before markerCreatedAt is the positive signal that the marker did NOT
	// establish anything new — treat this exactly like "not sole author" and
	// require the same time-gap friction, even though the thread-wide author
	// count alone would have said otherwise. No such comment (the overwhelming
	// majority of real gates, and every pre-existing test fixture, which never
	// simulate task_handler.go's raw-arm path) leaves today's behavior intact.
	scan.rawArmPrecedesMarker = scan.found && !lastRawArmAt.IsZero() && !lastRawArmAt.After(scan.markerCreatedAt)
	return scan, nil
}

// GetHumanGateOwner exposes, READ-ONLY, who currently owns task taskID's live
// human_gate ask (task #040cddcf) — a live measurement found 46 of 47 gated
// tasks had a clearable owner, but nothing ever told them so. Uses the EXACT
// SAME scanHumanGateOwnership that releaseHumanGateOnWithdrawal gates
// clearing on, so this can never report an owner who could not, in fact,
// clear the gate by commenting a negator.
//
// This changes nothing about who CAN clear a gate — see that doc comment for
// the actual rule. ClearableByOwner/ReasonIfNot describe what would happen if
// OwnerAgentID posted a withdrawal negator RIGHT NOW (using timeNow(), so a
// "not yet, wait for the gap" answer becomes "yes" once real time passes,
// with no further action needed on this task).
func (s *commentService) GetHumanGateOwner(ctx context.Context, taskID uuid.UUID) (*domain.HumanGateInfo, error) {
	scan, err := s.scanHumanGateOwnership(ctx, taskID, nil)
	if err != nil {
		return nil, err
	}

	info := &domain.HumanGateInfo{Gated: true}
	if !scan.found {
		info.ReasonIfNot = "no_live_marker"
		return info, nil
	}

	ownerID := scan.markerAuthorID
	markerID := scan.markerCommentID
	markerAt := scan.markerCreatedAt
	info.OwnerAgentID = &ownerID
	info.OwnerName = scan.markerAuthorName
	info.MarkerCommentID = &markerID
	info.MarkerCreatedAt = &markerAt

	gapRequired := !scan.soleMarkerAuthor || scan.rawArmPrecedesMarker
	if gapRequired && timeNow().Sub(scan.markerCreatedAt) < minReaffirmToWithdrawalGap {
		info.ClearableByOwner = false
		if scan.rawArmPrecedesMarker {
			info.ReasonIfNot = "raw_armed"
		} else {
			info.ReasonIfNot = "reaffirm_pending"
		}
	} else {
		info.ClearableByOwner = true
	}
	return info, nil
}

func (s *commentService) releaseHumanGateOnWithdrawal(ctx context.Context, comment *domain.Comment, task *domain.Task, wsID uuid.UUID) {
	if s.taskSvc == nil {
		return
	}
	if !task.HumanGate {
		return
	}
	if comment.AuthorType != domain.ActorTypeAgent {
		return
	}
	if !hasNegatorInScope(comment.Body) {
		s.reportWithdrawalMiss(ctx, comment, task)
		return
	}

	// #081f1354: a comment that withdraws a prior ask AND raises a fresh
	// "❓ Blocking @user" marker of its own is genuinely ambiguous — negatorScope
	// anchors to this comment's OWN last marker when one is present, so a
	// negator word anywhere at-or-after that marker reads as "in scope"
	// regardless of whether the author meant to cancel a DIFFERENT, earlier ask
	// or this brand-new one. Textually these two intents are indistinguishable.
	//
	// Live repro (throwaway task, same-session, 2026-08-21): task already
	// human_gate=true from an earlier marker; one comment posts a fresh marker
	// followed by "Предыдущий вопрос снят — …" → enforceBlockingTriage (which
	// runs first in the same request) reaffirms the gate on the new marker,
	// then this function, unaware a marker was just reasserted, read "снят" as
	// negating it and cleared human_gate=false in the same request — the new
	// ask silently vanished the instant it was raised, and human_gate_armed_at
	// was left holding this comment's timestamp despite the flag ending false.
	//
	// Fail closed (contract's own stated preference, §5 "safer to freeze extra
	// than to thaw needed"): one comment, one intention. When both are present,
	// leave the gate exactly as enforceBlockingTriage already set it — do NOT
	// also process the withdrawal — and tell the author why, so the silence
	// doesn't read as success. This does not touch the case of an ordinary
	// withdrawal-only comment (no marker) or a marker-only comment (no negator
	// word) — hasNegatorInScope above already gates entry to this branch.
	if hasBlockingMarker(comment.Body) {
		log.Printf("[human-gate] task %s: comment %s by agent %s carries both a withdrawal negator and a fresh Blocking marker — ambiguous, gate left as-is (not cleared)",
			task.ID, comment.ID, comment.AuthorID)
		s.reportWithdrawalMarkerConflict(ctx, task)
		return
	}

	scan, err := s.scanHumanGateOwnership(ctx, task.ID, &comment.ID)
	if err != nil {
		log.Printf("[human-gate] WARNING: ListByTask on task %s failed: %v", task.ID, err)
		return
	}
	if !scan.found {
		return // no live marker on record — fail closed, leave the flag as-is
	}
	if scan.markerAuthorType != domain.ActorTypeAgent || scan.markerAuthorID != comment.AuthorID {
		return // a different author owns the live ask — only they (or a human) may release it
	}
	markerAuthorID := scan.markerAuthorID
	markerCreatedAt := scan.markerCreatedAt
	soleMarkerAuthor := scan.soleMarkerAuthor
	rawArmPrecedesMarker := scan.rawArmPrecedesMarker

	// Fixed 2026-07-31 (task #9959f201, live finding by Bill on prod-sha b2a8068):
	// the guard above only checked that the WITHDRAWING agent matches the CURRENT
	// live marker's author — but ownership of "the current live marker" transfers
	// to WHOEVER posts the next fresh, non-negated marker (enforceBlockingTriage's
	// own arm criterion, deliberately reused here so #7f646f08-style orphaned asks
	// stay withdrawable once their original raiser is gone). That is correct and
	// load-bearing on its own. Composed with an UNCONDITIONAL self-negate, it is
	// not: any agent could arm the SAME gate as "themselves" in one comment, then
	// immediately negate their own brand-new marker in the very next — becoming
	// owner and releasing owner in two back-to-back comments, no permission check,
	// no trace in activity_log. Bill's live repro on b2a8068 did exactly this.
	//
	// The two scenarios that must be told apart are structurally identical in the
	// comment data (an agent withdraws a marker that is their own) — the only real
	// difference is INTENT, and intent isn't recoverable from comment text. What
	// IS recoverable: whether this agent has EVER shared this ask with a different
	// author (soleMarkerAuthor, computed above). If nobody else has ever posted a
	// marker here, this agent has always been the sole owner — self-clear stays
	// immediate, exactly AC1 of #c375905c, unaffected. If ownership was transferred
	// via a reaffirm, require the reaffirming marker and this negator to be
	// genuinely separated in time — a same-turn hijack cannot satisfy that; a real
	// hand-off (the #7f646f08 case: read the thread, check Alertmanager, THEN
	// withdraw) always does.
	//
	// This is a mitigation, not a semantic proof of good faith — an attacker who is
	// willing to wait out minReaffirmToWithdrawalGap still succeeds. What it removes
	// is the free, silent, single-turn version Bill demonstrated; what makes even a
	// patient attempt visible is the activity_log entry below (AC4). Whether a
	// deliberate, time-spaced takeover of someone else's live ask should ALSO be
	// blocked outright is a policy call this fix does not make — see the PR/task
	// comment for that open question.
	if !soleMarkerAuthor || rawArmPrecedesMarker {
		if comment.CreatedAt.Sub(markerCreatedAt) < minReaffirmToWithdrawalGap {
			reason := "this agent is not the ask's sole author"
			if rawArmPrecedesMarker {
				reason = "the gate was armed directly (PATCH/UI) before this agent's marker existed"
			}
			log.Printf("[human-gate] WARNING: task %s withdrawal by %s rejected — reaffirmed marker is only %s old (need %s), %s",
				task.ID, comment.AuthorID, comment.CreatedAt.Sub(markerCreatedAt), minReaffirmToWithdrawalGap, reason)
			return
		}
	}

	if err := s.taskSvc.SetHumanGate(ctx, task.ID, false); err != nil {
		log.Printf("[human-gate] WARNING: SetHumanGate(false) on task %s (agent withdrawal) failed: %v", task.ID, err)
		return
	}

	// AC4 of #9959f201: make every release of this specific path visible in the
	// task's activity log — before this, /activity carried no human_gate signal at
	// all (only created/checkout/moved), so a withdrawal — legitimate or not — left
	// no audit trail distinguishing it from any other cause of human_gate=false.
	if s.activityRepo != nil {
		changes, _ := json.Marshal(map[string]any{
			"released_by":             comment.AuthorID,
			"marker_author_id":        markerAuthorID,
			"sole_marker_author":      soleMarkerAuthor,
			"reaffirm_gap":            comment.CreatedAt.Sub(markerCreatedAt).String(),
			"raw_arm_precedes_marker": rawArmPrecedesMarker,
		})
		if logErr := s.activityRepo.Create(ctx, &domain.ActivityLog{
			ID:          uuid.New(),
			WorkspaceID: wsID,
			EntityType:  "task",
			EntityID:    task.ID,
			Action:      "human_gate.released_by",
			ActorID:     comment.AuthorID,
			ActorType:   domain.ActorTypeAgent,
			Changes:     changes,
			CreatedAt:   timeNow(),
		}); logErr != nil {
			log.Printf("[human-gate] WARNING: activity log for task %s withdrawal failed: %v", task.ID, logErr)
		}
	}

	movedFromTriage := false
	if s.statusRepo != nil {
		if curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID); err == nil && curStatus != nil &&
			curStatus.Category == domain.StatusCategoryTriage {
			if inProgressID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryInProgress); err == nil && inProgressID != uuid.Nil {
				if moveErr := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &inProgressID}); moveErr != nil {
					log.Printf("[human-gate] WARNING: move task %s from triage to in_progress failed: %v", task.ID, moveErr)
				} else {
					movedFromTriage = true
				}
			}
		}
	}

	now := timeNow()
	releaseBody := "🔓 Auto: human_gate снят — автор запроса отозвал его сам (blocker самоустранился)."
	if movedFromTriage {
		releaseBody = "🔓 Auto: human_gate снят, задача переведена из triage → in_progress — автор запроса отозвал его сам (blocker самоустранился)."
	}
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body:       releaseBody,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate] WARNING: create system comment on task %s failed: %v", task.ID, err)
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}

	if s.agentNotifySvc != nil && task.AssigneeType == domain.AssigneeTypeAgent && task.AssigneeID != nil {
		taskSnap := s.buildTaskSnap(ctx, task)
		actorID, _ := actorctx.FromContext(ctx)
		commentBody := comment.Body
		if len(commentBody) > 500 {
			commentBody = commentBody[:500]
		}
		s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
			EventType:   "task.human_gate_released",
			Timestamp:   now,
			WorkspaceID: wsID,
			Task:        taskSnap,
			AgentID:     *task.AssigneeID,
			ActorID:     actorID,
			ActorType:   string(domain.ActorTypeAgent),
			Comment: map[string]any{
				"id":   comment.ID,
				"body": commentBody,
			},
			TaskID:    task.ID,
			ProjectID: task.ProjectID,
		})
	}
}

// reportWithdrawalMiss leaves a trace when a comment on a gated task talks about
// withdrawing the ask but did not satisfy hasNegatorInScope — AC2 of #17829fcf.
//
// The log line fires for every such comment. The task-thread notice is narrower:
// only for the agent who OWNS the live marker, i.e. the one agent whose
// withdrawal could have succeeded, so the advice it gives is actionable rather
// than addressed to a bystander who never had that power.
//
// It deliberately does NOT claim the author intended a withdrawal. Intent is not
// recoverable from comment text — that is the whole finding of #1e5be182, where a
// genuine status update carried mid-body negators about other topics while its
// final paragraph reaffirmed the ask. So the notice states two facts that hold in
// BOTH readings: the gate is still up, and here is the region the server read.
// A notice that said "твой отзыв отклонён" would be wrong on every status update.
func (s *commentService) reportWithdrawalMiss(ctx context.Context, comment *domain.Comment, task *domain.Task) {
	reason := diagnoseNegatorMiss(comment.Body)
	if reason == "" {
		return
	}
	log.Printf("[human-gate] task %s: comment %s by agent %s mentions a withdrawal but human_gate stays true — %s",
		task.ID, comment.ID, comment.AuthorID, reason)

	scan, err := s.scanHumanGateOwnership(ctx, task.ID, &comment.ID)
	if err != nil || !scan.found {
		return
	}
	if scan.markerAuthorType != domain.ActorTypeAgent || scan.markerAuthorID != comment.AuthorID {
		return
	}

	hint := withdrawalMissHint[reason]
	if hint == "" {
		return
	}
	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: "🔒 human_gate по-прежнему поднят — этот коммент его не снял: " + hint +
			"\n\nЕсли отзыв был намеренным, повтори его **отдельным комментарием**, где слова отзыва " +
			"(`не нужен` / `не требуется` / `снят` / …) стоят в последнем абзаце и рядом с ними нет " +
			"утверждений, что блокер жив. Разбор и пробы оставляй предыдущим комментарием — они отзыв не портят. " +
			"После — перечитай `human_gate`.",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate] WARNING: create withdrawal-miss notice on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// reportWithdrawalMarkerConflict posts the system notice for the case
// releaseHumanGateOnWithdrawal refuses on purpose (#081f1354): a comment that
// both withdraws a prior ask and raises a fresh Blocking marker of its own.
// Mirrors reportWithdrawalMiss's shape but is a distinct reason — the negator
// DID count, it was refused for a different one, and conflating the two
// notices would tell the author "reword the negator" when the actual fix is
// "split this into two comments".
func (s *commentService) reportWithdrawalMarkerConflict(ctx context.Context, task *domain.Task) {
	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: "🔒 human_gate по-прежнему поднят — этот коммент нёс И слова отзыва, И новый `❓ Blocking @user`, " +
			"это противоречиво, поэтому гейт оставлен как есть (не снят). " +
			"Если хотел снять старый вопрос и задать новый — раздели на два комментария: " +
			"сперва отзыв старого отдельным комментарием (после него гейт снимется), " +
			"затем новый `❓ **Blocking @user**` — он взведёт гейт заново на этот вопрос.",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate] WARNING: create withdrawal-marker-conflict notice on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// enforceTriageExit is the general server-side triage EXIT rule, complementing the
// enforceBlockingTriage ENTRY rule and the human_gate release path.
//
// Fires when a human user comments on a task that:
//  1. Is currently in triage status.
//  2. Does NOT have human_gate=true (that path is handled by releaseHumanGate).
//  3. Is not assigned to a human user (those tasks are kept in triage intentionally).
//  4. Is not delegation_level=supervised (supervised tasks need a human manual-start).
//  5. Has at least one prior REAL blocking comment in its history:
//     non-auto-generated, non-user-authored, hasBlockingMarker, and non-negated.
//
// When all conditions are met, the task is moved from triage → in_progress and a
// system comment is appended. This covers the fiddler-parked (count==3) path and
// any manually-triaged tasks that the human_gate sticky flag never touched.
func (s *commentService) enforceTriageExit(ctx context.Context, comment *domain.Comment, task *domain.Task, wsID uuid.UUID) {
	if s.taskSvc == nil || s.statusRepo == nil {
		return
	}
	if comment.AuthorType != domain.ActorTypeUser {
		return
	}
	// human_gate=true is handled by releaseHumanGate; avoid a double move.
	if task.HumanGate {
		return
	}
	// Tasks assigned to a user (Pavel) live in triage intentionally.
	if task.AssigneeType == domain.AssigneeTypeUser {
		return
	}
	// Supervised tasks need a human manual-start signal; auto-exit would fight fiddler.
	if task.DelegationLevel == domain.DelegationLevelSupervised {
		return
	}
	// Task must currently be in triage.
	curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || curStatus == nil || curStatus.Category != domain.StatusCategoryTriage {
		return
	}

	// Scan the most-recent comments for a REAL blocking marker.
	// Exclude: user-authored comments; auto-generated server/fiddler messages;
	// and blocking mentions that also contain a negator (cancellation signal).
	pg := pagination.Params{Page: 1, PageSize: 100, SortDir: "desc"}
	pg.Normalize()
	page, err := s.commentRepo.ListByTask(ctx, task.ID, repository.CommentFilter{IncludeInternal: true}, pg)
	if err != nil {
		log.Printf("[triage-exit] WARNING: ListByTask on task %s failed: %v", task.ID, err)
		return
	}

	foundRealBlock := false
	if page != nil {
		for _, c := range page.Items {
			if c.ID == comment.ID {
				continue // skip the comment we just persisted
			}
			if c.AuthorType == domain.ActorTypeUser {
				continue // user comments are responses, not owner blocking markers
			}
			if isAutoGeneratedComment(c.Body) {
				continue // server/fiddler auto-messages aren't real questions
			}
			if !hasBlockingMarker(c.Body) {
				continue
			}
			// A blocking mention that ASSERTS a negator is a cancellation, not a live
			// gate — scoped to text at-or-after the marker (hasNegatorInScope), not the
			// whole body: see #c375905c, the same self-negation-by-unrelated-prose
			// defect this function shares with releaseHumanGateOnWithdrawal above.
			// One that merely QUOTES the vocabulary is still a live gate (#5c69b4e5).
			if hasNegatorInScope(c.Body) {
				continue
			}
			foundRealBlock = true
			break
		}
	}
	if !foundRealBlock {
		return
	}

	// Exit: move triage → in_progress.
	inProgressID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryInProgress)
	if err != nil || inProgressID == uuid.Nil {
		return
	}
	if moveErr := s.taskSvc.MoveTask(ctx, task.ID, MoveTaskInput{StatusID: &inProgressID}); moveErr != nil {
		log.Printf("[triage-exit] WARNING: move task %s from triage to in_progress failed: %v", task.ID, moveErr)
		return
	}
	if task.StatusChangedAt != nil {
		pkgmetrics.RecordTriageDwell(task.ProjectID.String(), time.Since(*task.StatusChangedAt))
	}

	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body:       "🔄 Auto: задача выведена из triage → in_progress — человек прокомментировал после блокирующего запроса.",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[triage-exit] WARNING: create system comment on task %s failed: %v", task.ID, err)
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}

	// Notify the assignee agent so it re-enters the feed on the next fiddler cycle.
	if s.agentNotifySvc != nil && task.AssigneeType == domain.AssigneeTypeAgent && task.AssigneeID != nil {
		taskSnap := s.buildTaskSnap(ctx, task)
		actorID, _ := actorctx.FromContext(ctx)
		commentBody := comment.Body
		if len(commentBody) > 500 {
			commentBody = commentBody[:500]
		}
		s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
			EventType:   "task.triage_exit",
			Timestamp:   now,
			WorkspaceID: wsID,
			Task:        taskSnap,
			AgentID:     *task.AssigneeID,
			ActorID:     actorID,
			ActorType:   string(domain.ActorTypeUser),
			Comment: map[string]any{
				"id":   comment.ID,
				"body": commentBody,
			},
			TaskID:    task.ID,
			ProjectID: task.ProjectID,
		})
	}
}

// firstResolvedUserSlug returns the first slug in candidates that resolves to a human
// user in the workspace, or "" if none do. Agent, typo'd, or unregistered slugs return
// (nil, nil) from GetByUsername and are skipped.
func (s *commentService) firstResolvedUserSlug(ctx context.Context, wsID uuid.UUID, candidates []string) string {
	for _, slug := range candidates {
		if user, err := s.userRepo.GetByUsername(ctx, wsID, slug); err == nil && user != nil {
			return slug
		}
	}
	return ""
}

// completionKeywords are lower-cased substrings whose presence near a blocking
// marker indicates a progress/completion report rather than a live work blocker.
var completionKeywords = []string{
	// Russian
	"выполнен", "завершен", "закрыт", "готово", "сделан", "работа завершена",
	"все фикс", "все подзадач", "вся работа",
	// English
	"done", "completed", "finished", "all done", "work done", "task done",
}

// completionKeywordWindowBytes bounds how far back from a blocking-marker match
// isAssigneeCompletionReport scans for a completion keyword. A handoff report
// ("Done. ❓ Blocking @pavel: please close manually") states the keyword right
// before the marker; this window is generous for that shape while excluding an
// unrelated completion word used earlier in a long analytical comment.
const completionKeywordWindowBytes = 500

// completionKeywordSearchWindow returns the slice of body immediately preceding
// the first blocking-marker match, bounded to completionKeywordWindowBytes, so
// isAssigneeCompletionReport only sees text that is actually adjacent to the
// marker. Regression: task #69fbb698 — a comment analyzing an unrelated
// "Helsinki-миграция ... завершена" thousands of bytes before a genuinely live
// "❓ Blocking @pavel" question was wrongly classified as a completion report,
// which suppressed enforceBlockingTriage before it ever reached SetHumanGate.
func completionKeywordSearchWindow(body string) string {
	// Operate wholly on the stripped body: the window must be anchored to the first
	// REAL marker, not to a quoted template that happens to appear earlier. Offsets
	// from one string can't index the other, so slice the same string we searched.
	stripped := stripQuotedSpans(body)
	loc := blockingMarkerRegex.FindStringIndex(stripped)
	if loc == nil {
		return stripped
	}
	start := loc[0] - completionKeywordWindowBytes
	if start < 0 {
		start = 0
	}
	for start > 0 && !utf8.RuneStart(stripped[start]) {
		start++
	}
	return stripped[start:loc[0]]
}

// negationTokens are lower-cased words that, immediately preceding a
// completionKeywords match, invert its meaning ("AC не выполнен" = the AC is
// NOT done). isAssigneeCompletionReport does not do full negation-scope
// parsing — it only checks the single word directly before the match — but
// that is enough to close the failure mode task #3948173f measured live:
// "выполнен" (done) is a bare substring of "не выполнен" (NOT done), and the
// old strings.Contains scan could not tell them apart.
var negationTokens = map[string]bool{
	"не": true, "not": true,
	"isn't": true, "wasn't": true, "doesn't": true, "didn't": true,
	"hasn't": true, "haven't": true, "aren't": true, "weren't": true,
}

// hasImmediateNegationBefore reports whether the word directly preceding byte
// offset pos in lower — skipping whitespace, but no further — is a
// negationTokens entry. Only a word RIGHT BEFORE the match counts: this is a
// local adjacency check, not a clause-level negation parse.
func hasImmediateNegationBefore(lower string, pos int) bool {
	end := pos
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(lower[:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	if end == 0 {
		return false
	}
	start := end
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(lower[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	word := strings.TrimFunc(lower[start:end], func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
	return negationTokens[word]
}

// hasUnnegatedKeyword reports whether kw occurs anywhere in lower WITHOUT an
// immediately-preceding negation token. A completion keyword that only ever
// appears negated ("AC не выполнен") must not count as a completion report —
// see the #90750c66 live regression this closes.
func hasUnnegatedKeyword(lower, kw string) bool {
	searchFrom := 0
	for {
		rel := strings.Index(lower[searchFrom:], kw)
		if rel == -1 {
			return false
		}
		start := searchFrom + rel
		if !hasImmediateNegationBefore(lower, start) {
			return true
		}
		searchFrom = start + 1
	}
}

// isAssigneeCompletionReport returns true when the comment is authored by the
// task's own assignee AND the text immediately preceding its blocking marker
// contains at least one completion keyword that is not itself negated. Used
// to suppress the TRIAGE MOVE on progress/handoff reports that append a
// "❓ Blocking @user" marker as citation/context, without false-positiving on
// unrelated (or negated) completion words used elsewhere in a longer comment.
//
// SCOPE (task #a84b443c, 2026-08-14): this heuristic must NEVER gate SetHumanGate.
// It is a keyword scan over a 500-byte window, and the canonical ask shape from
// CLAUDE-communication.md §5a — a work report, then "---", then the marker at the
// tail — puts a completion word in that window as a matter of course. Whenever it
// misfired it did not merely skip a triage move: it skipped ARMING, so the card was
// never frozen and the ask was never queued, while the comment published normally
// and the author believed the question was handed over. Two live losses, different
// agents and projects, same shape:
//
//   - #8286e487 (Bill, 10.08) — "…в незакрытые треды…" (закрыт ⊂ незакрытые);
//   - #29a0a879 (Deadalus, 13.08) — "**Уже сделано** (авто-intake…)" (сделан ⊂ сделано),
//     a legitimate MIT-violation escalation that sat undelivered for 8 hours.
//
// Both are covered by TestEnforceBlockingTriage_TailMarkerAfterReport_ArmsGate.
// Two prior fixes narrowed this predicate instead of moving it — #69fbb698 bounded
// the window to 500 bytes, #3948173f/#548 added negation tokens — and each left the
// class open, because the defect is not the predicate's width but what it gates.
// Narrowing it a third time is not the fix; keeping it off the arming path is.
func isAssigneeCompletionReport(comment *domain.Comment, task *domain.Task) bool {
	if task.AssigneeID == nil {
		return false
	}
	if comment.AuthorID != *task.AssigneeID || comment.AuthorType != domain.ActorType(task.AssigneeType) {
		return false
	}
	lower := strings.ToLower(completionKeywordSearchWindow(comment.Body))
	for _, kw := range completionKeywords {
		if hasUnnegatedKeyword(lower, kw) {
			return true
		}
	}
	return false
}
