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
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// mentionRegex matches @-slug references in comment bodies.
// Slug pattern mirrors agents.slug and users.username: starts/ends with alnum, middle may contain hyphens, 2-40 chars total.
// The leading boundary allows start-of-string, whitespace, or open bracket/paren/brace.
var mentionRegex = regexp.MustCompile(`(?:^|[\s(\[{])@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b`)

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

// hasRawArmMarker reports whether body is (or contains) task_handler.go's raw
// PATCH/UI arm system comment. See rawArmMarkerSubstring for why this exists.
func hasRawArmMarker(body string) bool {
	return strings.Contains(strings.ToLower(body), strings.ToLower(rawArmMarkerSubstring))
}

// triageExitNegators are lower-cased substrings that, when present in a blocking comment,
// indicate the block was cancelled — so the comment is NOT a live gate.
var triageExitNegators = []string{
	"не нужен", "не нужно", "не требуется",
	"снят", "снято",
	"resolved", "async",
	"разблок", "уже ответил", "ответил в коммент",
}

// hasNegatorInScope reports whether body carries a triageExitNegators substring,
// scoped to text at-or-after the comment's OWN blocking marker (the LAST one, if
// several) — never text that only precedes it.
//
// Fixed 2026-07-30 (task #c375905c, live incident on #7f646f08, found by Riker):
// a whole-body strings.Contains search let a comment self-negate its OWN marker
// via unrelated prose. Riker's comment raised a fresh "❓ Blocking @pavel" as its
// final line, but earlier paragraphs discussed a DIFFERENT, already-resolved ask
// using the word "снят" — a whole-body scan found that substring and treated the
// brand-new marker as pre-cancelled, silently handing ownership of the live ask
// to whoever's marker preceded it. A marker is conventionally the operative ask
// of a comment; analysis before it is context, not itself the thing negated —
// so only text from the marker onward is searched. A comment with no marker at
// all (the ordinary shape of a real withdrawal — a separate later comment with
// nothing else in it) has no scope to restrict, so the whole body is used.
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
func hasNegatorInScope(body string) bool {
	scope := stripQuotedSpans(body)
	if matches := blockingMarkerRegex.FindAllStringIndex(scope, -1); len(matches) > 0 {
		scope = scope[matches[len(matches)-1][0]:]
	} else {
		scope = lastParagraph(scope)
	}
	lower := strings.ToLower(scope)
	for _, n := range triageExitNegators {
		if containsNegatorWholeWord(lower, n) {
			return true
		}
	}
	return false
}

// paragraphBreakRegex matches one or more consecutive blank (or whitespace-only)
// lines — the boundary lastParagraph splits on.
var paragraphBreakRegex = regexp.MustCompile(`\n[ \t]*\n`)

// lastParagraph returns the final non-blank paragraph of body, trimmed of
// surrounding whitespace. Paragraphs are separated by one or more blank lines.
// A body with no blank line at all is itself a single paragraph and is returned
// unchanged (trimmed) — this keeps every existing short, single-paragraph
// withdrawal comment (the documented ordinary shape) behaving exactly as before.
func lastParagraph(body string) string {
	trimmed := strings.TrimRight(body, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	parts := paragraphBreakRegex.Split(trimmed, -1)
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return p
		}
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
	if needle == "" {
		return false
	}
	searchFrom := 0
	for {
		rel := strings.Index(haystack[searchFrom:], needle)
		if rel == -1 {
			return false
		}
		start := searchFrom + rel
		end := start + len(needle)
		if !isWordRuneBefore(haystack, start) && !isWordRuneAfter(haystack, end) {
			return true
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
	wsPublisher    WSPublisher
	taskSvc        TaskService
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

	// Resolve workspace ID once for all agent notifications.
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
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
				WorkspaceID: proj.WorkspaceID,
				TaskID:      &taskIDCopy,
				ProjectID:   &projIDCopy,
				EventType:   "comment.created",
				Title:       "New comment on: " + task.Title,
				Body:        commentBody,
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

// ListByTask returns a paginated list of comments for the given task.
func (s *commentService) ListByTask(ctx context.Context, taskID uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
	pg.Normalize()
	return s.commentRepo.ListByTask(ctx, taskID, filter, pg)
}

// ListByAuthor returns the caller's own comments, newest first (activity feed).
func (s *commentService) ListByAuthor(ctx context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	items, nextCursor, err := s.commentRepo.ListByAuthor(ctx, authorID, filter)
	if err != nil {
		return nil, err
	}
	return &domain.CommentViewPage{Items: items, NextCursor: nextCursor}, nil
}

// ListRecentByWorkspace returns workspace-wide recent comments, newest first (activity feed).
func (s *commentService) ListRecentByWorkspace(ctx context.Context, wsID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	items, nextCursor, err := s.commentRepo.ListRecentByWorkspace(ctx, wsID, filter)
	if err != nil {
		return nil, err
	}
	return &domain.CommentViewPage{Items: items, NextCursor: nextCursor}, nil
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

	seenID := make(map[uuid.UUID]bool)
	var dbRows []domain.CommentMention

	for _, slug := range newSlugs {
		// Try agent lookup first.
		if s.agentSvc != nil {
			agent, err := s.agentSvc.GetBySlug(ctx, workspaceID, slug)
			if err == nil && agent != nil && !seenID[agent.ID] {
				seenID[agent.ID] = true
				dbRows = append(dbRows, domain.CommentMention{
					CommentID:     comment.ID,
					MentionedID:   agent.ID,
					MentionedKind: "agent",
					MentionedSlug: slug,
					ExtractedAt:   now,
				})
				if s.agentNotifySvc != nil && (actorType != domain.ActorTypeAgent || agent.ID != actorID) {
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
					})
				}
				continue
			}
		}

		// Fall back to user lookup.
		if s.userRepo != nil {
			user, err := s.userRepo.GetByUsername(ctx, workspaceID, slug)
			if err == nil && user != nil && !seenID[user.ID] &&
				(actorType != domain.ActorTypeUser || user.ID != actorID) {
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
			}
		}
	}

	if s.mentionRepo != nil && len(dbRows) > 0 {
		_ = s.mentionRepo.InsertBatch(ctx, dbRows)
	}
}

// enforceBlockingTriage is the server-side defense-in-depth for CLAUDE-workflow.md §0b:
// when a comment contains a "❓ **Blocking @user**" marker pointing at a human user, the
// parent task is auto-moved to the project's triage column with an explanatory system
// comment. Every step is best-effort — failures are logged but never block the comment
// mutation that triggered them.
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

	// Completion reports from the task's own assignee must not trigger triage —
	// the blocking marker may appear as handoff/escalation context rather than a
	// genuine work blocker (e.g. "Done. ❓ Blocking @pavel: please close manually").
	if isAssigneeCompletionReport(comment, task) {
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

	// Arm the sticky human_gate flag regardless of delegation level so the MoveTask
	// gate protects the task even after a delegation_level change (audit P0 #3).
	if setErr := s.taskSvc.SetHumanGate(ctx, task.ID, true); setErr != nil {
		log.Printf("[comment-triage] WARNING: SetHumanGate on task %s failed: %v", task.ID, setErr)
	}

	// auto-mode tasks self-manage; triage escalation is suppressed (flag already set above).
	if task.DelegationLevel == domain.DelegationLevelAuto {
		return
	}

	// Idempotency: never re-triage a task already in triage or a terminal category.
	curStatus, err := s.statusRepo.GetByID(ctx, task.StatusID)
	if err != nil || curStatus == nil {
		return
	}
	switch curStatus.Category {
	case domain.StatusCategoryTriage, domain.StatusCategoryDone, domain.StatusCategoryCancelled:
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

	// Emit blocking_triage notification so the mentioned user is actually alerted.
	if s.notifySvc != nil {
		taskIDCopy := task.ID
		projIDCopy := task.ProjectID
		s.notifySvc.Notify(ctx, domain.NotificationEvent{
			WorkspaceID: wsID,
			TaskID:      &taskIDCopy,
			ProjectID:   &projIDCopy,
			EventType:   "task.blocking_triage",
			Title:       "Task moved to triage: " + task.Title,
			Body:        fmt.Sprintf("@%s marked this task as blocking — auto-moved to triage.", userSlug),
			Metadata: map[string]any{
				"task_id":    task.ID,
				"task_title": task.Title,
				"project_id": task.ProjectID,
				"user_slug":  userSlug,
			},
		})
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
//     "не нужен" while explaining the mechanism withdraws nothing);
//   - of all prior comments that both hasBlockingMarker AND are not themselves
//     already negated, the CHRONOLOGICALLY LAST one must be authored by this SAME
//     agent. A live ask with no marker found at all, or one raised/most-recently
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
		return
	}

	// Find who owns the currently-live ask: the CHRONOLOGICALLY LAST marker-bearing
	// comment that is not itself already negated. Mirrors enforceBlockingTriage's
	// own arm criterion (hasBlockingMarker, no auto-generated-comment carve-out) so
	// "who armed it" is judged by the same rule that actually armed it.
	//
	// While iterating, also track (a) that marker's own CreatedAt (markerCreatedAt,
	// for the time-gap guard below) and (b) whether every marker-bearing comment on
	// the whole thread shares ONE author (soleMarkerAuthor) — both computed from the
	// same page fetched once, no second query.
	pg := pagination.Params{Page: 1, PageSize: 100}
	pg.Normalize()
	page, err := s.commentRepo.ListByTask(ctx, task.ID, repository.CommentFilter{IncludeInternal: true}, pg)
	if err != nil {
		log.Printf("[human-gate] WARNING: ListByTask on task %s failed: %v", task.ID, err)
		return
	}

	var markerAuthorID uuid.UUID
	var markerAuthorType domain.ActorType
	var markerCreatedAt time.Time
	found := false
	soleMarkerAuthor := true
	haveSeenAnyMarkerAuthor := false
	var firstMarkerAuthorSeen uuid.UUID
	var lastRawArmAt time.Time
	if page != nil {
		// Real order is created_at ASC (see doc comment above) — keep overwriting so
		// the LAST qualifying match, not the first, wins.
		for _, c := range page.Items {
			if c.ID == comment.ID {
				continue
			}
			// Task #a2e2ac72: track the most recent raw PATCH/UI arm marker
			// regardless of whether this comment also happens to carry a
			// blocking marker (it never does — see hasRawArmMarker's doc —
			// but the two checks are independent on principle).
			if hasRawArmMarker(c.Body) && c.CreatedAt.After(lastRawArmAt) {
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
				soleMarkerAuthor = false
			}
			if hasNegatorInScope(c.Body) {
				continue // already-negated marker isn't the live ask
			}
			markerAuthorID = c.AuthorID
			markerAuthorType = c.AuthorType
			markerCreatedAt = c.CreatedAt
			found = true
		}
	}
	if !found {
		return // no live marker on record — fail closed, leave the flag as-is
	}
	if markerAuthorType != domain.ActorTypeAgent || markerAuthorID != comment.AuthorID {
		return // a different author owns the live ask — only they (or a human) may release it
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
	rawArmPrecedesMarker := !lastRawArmAt.IsZero() && !lastRawArmAt.After(markerCreatedAt)

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

// isAssigneeCompletionReport returns true when the comment is authored by the
// task's own assignee AND the text immediately preceding its blocking marker
// contains at least one completion keyword. Used to suppress enforceBlockingTriage
// on progress/handoff reports that append a "❓ Blocking @user" marker as
// citation/context, without false-positiving on unrelated completion words used
// elsewhere in a longer comment.
func isAssigneeCompletionReport(comment *domain.Comment, task *domain.Task) bool {
	if task.AssigneeID == nil {
		return false
	}
	if comment.AuthorID != *task.AssigneeID || comment.AuthorType != domain.ActorType(task.AssigneeType) {
		return false
	}
	lower := strings.ToLower(completionKeywordSearchWindow(comment.Body))
	for _, kw := range completionKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
