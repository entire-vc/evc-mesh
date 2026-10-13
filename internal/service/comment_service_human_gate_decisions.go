package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ErrHumanGateDecisionRepoUnavailable is returned when hgdRepo was never
// wired via WithHumanGateDecisionRepo — this feature is optional-by-default
// the same way agentNotifySvc/wsPublisher are.
var ErrHumanGateDecisionRepoUnavailable = errors.New("human gate decision repository not configured")

// ErrHumanGateDecisionNotFound is returned by RevokeHumanGateDecision when
// the referenced decision does not exist.
var ErrHumanGateDecisionNotFound = errors.New("human gate decision not found")

// ErrHumanGateDecisionAlreadyRevoked is returned by RevokeHumanGateDecision
// on a decision that already has a revocation row — the unique index on
// revokes_id makes a second one a DB conflict; this is the friendlier
// pre-check.
var ErrHumanGateDecisionAlreadyRevoked = errors.New("human gate decision already revoked")

// ErrHumanGateDecisionCannotRevokeRevocation is returned when the given ID
// names a revocation row rather than a decision — a revocation cannot itself
// be revoked (contract: append-only, no second-order corrections).
var ErrHumanGateDecisionCannotRevokeRevocation = errors.New("cannot revoke a revocation row")

// HumanGateDecisionValidationError wraps a validateDecisionInput failure
// (missing question_ref/canonical_key, unknown provenance/channel, missing
// quote, etc.) so the handler layer can map it to 400 via errors.As, instead
// of letting a bare error fall through handleError's *apierror.Error type
// assertion into the generic 500 fallback. That fallthrough was the actual
// bug behind #62560d6d: POST /tasks/:id/human-gate-decisions returned
// Internal Server Error for any malformed-but-otherwise-valid body (e.g.
// {"decided_by": "<uuid>"} with no question_ref/canonical_key) instead of a
// 400 naming the missing field — validation was correct, only its HTTP
// status was wrong.
type HumanGateDecisionValidationError struct {
	Msg string
}

func (e *HumanGateDecisionValidationError) Error() string { return e.Msg }

// RecordHumanGateDecision appends a decision record and, if the task's gate
// is currently live, releases it as a CONSEQUENCE of the write (contract
// docs/human-gate-decision-recorded.md §3, P1: "гейт снимается следствием
// записи и никогда сам по себе"). This is the third human_gate exit: unlike
// releaseHumanGate (Pavel commented) and releaseHumanGateOnWithdrawal (the
// asker retracted), this records that the question WAS answered, through
// whatever channel, without pretending the question never existed.
func (s *commentService) RecordHumanGateDecision(ctx context.Context, input domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
	if s.hgdRepo == nil {
		return nil, ErrHumanGateDecisionRepoUnavailable
	}
	if s.taskSvc == nil {
		return nil, errors.New("task service not configured")
	}
	if err := validateDecisionInput(input); err != nil {
		return nil, &HumanGateDecisionValidationError{Msg: err.Error()}
	}
	if input.QuestionRef == nil {
		// A decision without question_ref never covers any marker (contract §6
		// matches by comment id), so the gate would stay open — #cb3f5274.
		ref, err := s.resolveQuestionRef(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		input.QuestionRef = ref
	}

	d := &domain.HumanGateDecision{
		ID:           uuid.New(),
		TaskID:       input.TaskID,
		QuestionRef:  input.QuestionRef,
		CanonicalKey: input.CanonicalKey,
		DecidedBy:    input.DecidedBy,
		Provenance:   &input.Provenance,
		Channel:      &input.Channel,
		Quote:        input.Quote,
		ChannelRef:   input.ChannelRef,
		RecordedBy:   input.RecordedBy,
	}
	if err := s.hgdRepo.Create(ctx, d); err != nil {
		return nil, fmt.Errorf("record decision: %w", err)
	}

	task, err := s.taskSvc.GetByID(ctx, input.TaskID)
	if err != nil {
		log.Printf("[human-gate-decision] WARNING: task %s lookup after recording decision %s failed: %v", input.TaskID, d.ID, err)
		return d, nil
	}
	if task == nil || !task.HumanGate {
		return d, nil // recorded, nothing to release
	}

	if err := s.taskSvc.SetHumanGate(ctx, task.ID, false); err != nil {
		log.Printf("[human-gate-decision] WARNING: SetHumanGate(false) on task %s failed: %v", task.ID, err)
		return d, nil
	}

	// default_applied gets its own wording (task #060ccaae): the generic template below
	// reads as "someone answered" (recorderLabel defaults an absent RecordedBy to "Pavel
	// напrямую", which would be an outright lie here), and the task's own acceptance
	// criterion names this exact phrase so a reader — human or the pavel-digest.py
	// counter — can find it without parsing provenance out of the ledger row.
	var body string
	if input.Provenance == domain.HumanGateProvenanceDefaultApplied {
		body = fmt.Sprintf(
			"⏰ Auto: дефолт применён: %s\n\nГейт не получил ответа до дедлайна — сработал "+
				"заявленный при постановке recommended_default. Решение зафиксировано (%s), "+
				"human_gate снят.",
			quoteOrDash(input.Quote), provenanceLabel(input.Provenance),
		)
	} else {
		body = fmt.Sprintf(
			"🔓 Auto: human_gate снят — решение зафиксировано (%s, канал %s, зафиксировал %s).\n\n> %s",
			provenanceLabel(input.Provenance), input.Channel, recorderLabel(input.RecordedBy), quoteOrDash(input.Quote),
		)
	}
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		SystemCode: SysCodePtr(SysCodeGateDecisionRecorded),
		Body:       body,
		CreatedAt:  timeNow(),
		UpdatedAt:  timeNow(),
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate-decision] WARNING: create system comment on task %s failed: %v", task.ID, err)
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
	actor := input.DecidedBy
	if input.RecordedBy != nil {
		actor = *input.RecordedBy
	}
	releaseEvent := "task.human_gate_released"
	if input.Provenance == domain.HumanGateProvenanceDefaultApplied {
		releaseEvent = "task.human_gate_default_applied"
	}
	s.notifyHumanGateDecision(ctx, task, releaseEvent, actor, sysComment)

	return d, nil
}

// RevokeHumanGateDecision appends a revocation row for an existing decision
// and re-freezes the task's gate — contract §3/P3: Pavel can always revoke
// an attested (or any) decision, and the card freezes back so the dispute is
// recorded rather than silently resolved either way.
//
// Callers MUST have already verified input.RevokedByType is
// domain.ActorTypeUser before calling this — mirrored on the handler, the
// same place task_handler.go's PATCH {human_gate:false} 403 guard lives, so
// this is defense-in-depth, not the only check.
func (s *commentService) RevokeHumanGateDecision(ctx context.Context, input domain.RevokeHumanGateDecisionInput) error {
	if s.hgdRepo == nil {
		return ErrHumanGateDecisionRepoUnavailable
	}
	if s.taskSvc == nil {
		return errors.New("task service not configured")
	}
	if input.Reason == "" {
		return errors.New("revocation reason is required")
	}
	// Defense-in-depth mirror of task_handler.go's PATCH {human_gate:false}
	// guard: only a human user may clear a gate. The handler is the primary
	// enforcement point (it populates RevokedByType from the authenticated
	// actor); this exists so a future caller that skips the handler cannot
	// silently bypass the rule.
	if input.RevokedByType != domain.ActorTypeUser {
		return errors.New("only a human user may revoke a human_gate decision")
	}

	decision, err := s.hgdRepo.GetByID(ctx, input.DecisionID)
	if err != nil {
		return fmt.Errorf("lookup decision: %w", err)
	}
	if decision == nil {
		return ErrHumanGateDecisionNotFound
	}
	if !decision.IsDecision() {
		return ErrHumanGateDecisionCannotRevokeRevocation
	}
	if decision.RevokedAt != nil {
		return ErrHumanGateDecisionAlreadyRevoked
	}

	revocation := &domain.HumanGateDecision{
		ID:            uuid.New(),
		TaskID:        decision.TaskID,
		DecidedBy:     input.RevokedBy,
		RevokesID:     &decision.ID,
		RevokedReason: &input.Reason,
	}
	if createErr := s.hgdRepo.Create(ctx, revocation); createErr != nil {
		return fmt.Errorf("record revocation: %w", createErr)
	}

	task, err := s.taskSvc.GetByID(ctx, decision.TaskID)
	if err != nil || task == nil {
		log.Printf("[human-gate-decision] WARNING: task %s lookup after revoking decision %s failed: %v", decision.TaskID, decision.ID, err)
		return nil
	}
	if err := s.taskSvc.SetHumanGate(ctx, task.ID, true); err != nil {
		log.Printf("[human-gate-decision] WARNING: SetHumanGate(true) on task %s (revocation) failed: %v", task.ID, err)
		return nil
	}

	sysComment := &domain.Comment{
		ID:           uuid.New(),
		TaskID:       task.ID,
		AuthorID:     systemActorID,
		AuthorType:   domain.ActorTypeSystem,
		SystemCode:   SysCodePtr(SysCodeGateDecisionRevoked),
		SystemParams: SysParamsJSON(map[string]any{"reason": input.Reason}),
		Body:         fmt.Sprintf("🔒 Auto: human_gate взведён снова — запись решения отозвана (%s). Спор зафиксирован.", input.Reason),
		CreatedAt:    timeNow(),
		UpdatedAt:    timeNow(),
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate-decision] WARNING: create system comment on task %s failed: %v", task.ID, err)
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
	s.notifyHumanGateDecision(ctx, task, "task.human_gate_revoked", input.RevokedBy, sysComment)

	return nil
}

// ListHumanGateDecisions returns every decision/revocation row for a task,
// newest first.
func (s *commentService) ListHumanGateDecisions(ctx context.Context, taskID uuid.UUID) ([]domain.HumanGateDecision, error) {
	if s.hgdRepo == nil {
		return nil, ErrHumanGateDecisionRepoUnavailable
	}
	return s.hgdRepo.ListByTask(ctx, taskID)
}

// findLiveHumanGateDecision is the repeat-question check enforceBlockingTriage
// runs before arming human_gate (contract §6). It matches by KEY, never by
// text similarity (the contract is explicit that a reformulated question is
// not caught — §8): the new marker's question_ref candidate is its own
// parent_comment_id (a reply to an earlier, already-answered marker is the
// same "marker thread"), and/or an explicit canonical_key the poster cited
// in the comment's metadata (`{"canonical_key": "..."}`) — never inferred
// from the comment body's prose.
func (s *commentService) findLiveHumanGateDecision(ctx context.Context, taskID uuid.UUID, comment *domain.Comment) *domain.HumanGateDecision {
	if s.hgdRepo == nil {
		return nil
	}
	canonicalKey := extractCanonicalKeyFromMetadata(comment.Metadata)
	if comment.ParentCommentID == nil && canonicalKey == nil {
		return nil
	}
	existing, err := s.hgdRepo.FindLiveByRef(ctx, taskID, comment.ParentCommentID, canonicalKey)
	if err != nil {
		log.Printf("[human-gate-decision] WARNING: FindLiveByRef on task %s failed: %v", taskID, err)
		return nil
	}
	return existing
}

// postExistingDecisionNotice is what fires instead of arming the gate when
// findLiveHumanGateDecision finds a match — contract §6: "не взводить, а
// опубликовать системный комментарий с существующей записью и оставить
// карточку подаваемой." Posted directly through commentRepo (not s.Create)
// so it cannot re-trigger enforceBlockingTriage — same reasoning as the
// triage-move system comment a few lines below this function's caller.
func (s *commentService) postExistingDecisionNotice(ctx context.Context, task *domain.Task, existing *domain.HumanGateDecision) {
	provenance := "unknown"
	if existing.Provenance != nil {
		provenance = provenanceLabel(*existing.Provenance)
	}
	now := timeNow()
	sysComment := &domain.Comment{
		ID:           uuid.New(),
		TaskID:       task.ID,
		AuthorID:     systemActorID,
		AuthorType:   domain.ActorTypeSystem,
		SystemCode:   SysCodePtr(SysCodeGateDecisionExisting),
		SystemParams: SysParamsJSON(map[string]any{"decision_id": existing.ID.String(), "provenance": provenance}),
		Body: fmt.Sprintf(
			"🔁 Auto: этот вопрос уже отвечен — запись решения %s (%s, %s) от %s. human_gate не взведён повторно.",
			existing.ID, provenance, existing.CreatedAt.Format("2006-01-02"), recorderLabel(existing.RecordedBy),
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate-decision] WARNING: create existing-decision notice on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// extractCanonicalKeyFromMetadata reads an explicit canonical_key citation
// from a comment's metadata JSON. Returns nil on any absence/parse failure —
// this is an opt-in signal, not a required field, so a malformed or missing
// value degrades to "no citation" rather than an error.
func extractCanonicalKeyFromMetadata(metadata json.RawMessage) *string {
	if len(metadata) == 0 {
		return nil
	}
	var parsed struct {
		CanonicalKey string `json:"canonical_key"`
	}
	if err := json.Unmarshal(metadata, &parsed); err != nil || parsed.CanonicalKey == "" {
		return nil
	}
	return &parsed.CanonicalKey
}

func validateDecisionInput(input domain.RecordHumanGateDecisionInput) error {
	if input.TaskID == uuid.Nil {
		return errors.New("task_id is required")
	}
	if input.DecidedBy == uuid.Nil {
		return errors.New("decided_by is required")
	}
	if input.QuestionRef == nil && (input.CanonicalKey == nil || *input.CanonicalKey == "") {
		return errors.New("question_ref or canonical_key is required")
	}
	switch input.Provenance {
	case domain.HumanGateProvenanceDirect, domain.HumanGateProvenanceBridged, domain.HumanGateProvenanceAttested,
		domain.HumanGateProvenanceDefaultApplied:
	default:
		return fmt.Errorf("invalid provenance %q", input.Provenance)
	}
	switch input.Channel {
	case domain.HumanGateChannelMesh, domain.HumanGateChannelTelegram, domain.HumanGateChannelChat,
		domain.HumanGateChannelVoice, domain.HumanGateChannelInPerson:
	default:
		return fmt.Errorf("invalid channel %q", input.Channel)
	}
	if input.Provenance != domain.HumanGateProvenanceDirect && (input.Quote == nil || *input.Quote == "") {
		return fmt.Errorf("quote is required for provenance %q", input.Provenance)
	}
	return nil
}

func provenanceLabel(p domain.HumanGateProvenance) string {
	switch p {
	case domain.HumanGateProvenanceDirect:
		return "direct"
	case domain.HumanGateProvenanceBridged:
		return "bridged"
	case domain.HumanGateProvenanceAttested:
		return "attested — агент переписал ответ, не аутентифицировано криптографически"
	case domain.HumanGateProvenanceDefaultApplied:
		return "default_applied — ответа не было, применён заявленный дефолт"
	default:
		return string(p)
	}
}

func recorderLabel(recordedBy *uuid.UUID) string {
	if recordedBy == nil {
		return "Pavel напрямую"
	}
	return recordedBy.String()
}

func quoteOrDash(quote *string) string {
	if quote == nil || *quote == "" {
		return "—"
	}
	return *quote
}

// notifyHumanGateDecision mirrors the notify call in releaseHumanGate /
// releaseHumanGateOnWithdrawal so the assignee agent's session wakes on
// either a decision-release or a revocation-refreeze. Best-effort like its
// siblings: a failure to resolve the workspace or to notify never blocks the
// write that already happened.
func (s *commentService) notifyHumanGateDecision(ctx context.Context, task *domain.Task, eventType string, actorID uuid.UUID, sysComment *domain.Comment) {
	if s.agentNotifySvc == nil || task.AssigneeType != domain.AssigneeTypeAgent || task.AssigneeID == nil {
		return
	}
	var wsID uuid.UUID
	if s.projectRepo != nil {
		if proj, err := s.projectRepo.GetByID(ctx, task.ProjectID); err == nil && proj != nil {
			wsID = proj.WorkspaceID
		}
	}
	taskSnap := s.buildTaskSnap(ctx, task)
	s.agentNotifySvc.NotifyAgent(ctx, *task.AssigneeID, AgentNotification{
		EventType:   eventType,
		Timestamp:   timeNow(),
		WorkspaceID: wsID,
		Task:        taskSnap,
		AgentID:     *task.AssigneeID,
		ActorID:     actorID,
		ActorType:   string(domain.ActorTypeSystem),
		Comment: map[string]any{
			"id":   sysComment.ID,
			"body": sysComment.Body,
		},
		TaskID:    task.ID,
		ProjectID: task.ProjectID,
	})
}

// liveBlockingMarkerIDs returns the ids of the task's blocking markers that
// still await an answer. A marker is NOT live when it is covered (by
// question_ref) by an unrevoked decision, superseded by a newer marker after a human comment (the ordinary release path clears the gate
// without a decision row), or retracted by a later negating comment from its
// own author. Oldest first.
func (s *commentService) liveBlockingMarkerIDs(ctx context.Context, taskID uuid.UUID, gateUp bool) ([]uuid.UUID, error) {
	answered := map[uuid.UUID]bool{}
	decisions, err := s.hgdRepo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list decisions: %w", err)
	}
	for _, d := range decisions {
		if d.IsDecision() && d.RevokedAt == nil && d.QuestionRef != nil {
			answered[*d.QuestionRef] = true
		}
	}

	type marker struct {
		id      uuid.UUID
		author  uuid.UUID
		created time.Time
	}
	var markers []marker
	var lastHuman time.Time
	withdrawnAt := map[uuid.UUID]time.Time{} // author -> latest negating comment
	pg := pagination.Params{Page: 1, PageSize: pagination.MaxPageSize}
	pg.Normalize()
	for {
		page, err := s.commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{IncludeInternal: true}, pg)
		if err != nil {
			return nil, fmt.Errorf("list comments: %w", err)
		}
		if page == nil {
			break
		}
		for _, c := range page.Items {
			switch {
			case c.AuthorType == domain.ActorTypeUser:
				if c.CreatedAt.After(lastHuman) {
					lastHuman = c.CreatedAt
				}
			case c.AuthorType == domain.ActorTypeSystem:
			case hasBlockingMarker(c.Body):
				// Not filtered by hasNegatorInScope: the server arms the gate on the marker
				// alone (see scanHumanGateOwnership), so a marker carrying a negator in its
				// own body can still be the live gate.
				if !answered[c.ID] {
					markers = append(markers, marker{c.ID, c.AuthorID, c.CreatedAt})
				}
			case !isServiceComment(c.Body) && hasNegatorInScope(c.Body):
				if c.CreatedAt.After(withdrawnAt[c.AuthorID]) {
					withdrawnAt[c.AuthorID] = c.CreatedAt
				}
			}
		}
		if !page.HasMore {
			break
		}
		pg.Page++
	}

	var live []uuid.UUID
	var latestNotWithdrawn *uuid.UUID
	for i, m := range markers {
		if withdrawnAt[m.author].After(m.created) {
			continue
		}
		latestNotWithdrawn = &markers[i].id
		if m.created.After(lastHuman) {
			live = append(live, m.id)
		}
	}
	// Every marker precedes some human comment: that comment may have been
	// unrelated discussion rather than an answer, so a gate that is still up
	// belongs to the newest marker. Fall back to it instead of binding nothing —
	// but only while the gate is actually up; with it down the comment was an
	// answer and the markers are history.
	if len(live) == 0 && gateUp && latestNotWithdrawn != nil {
		live = append(live, *latestNotWithdrawn)
	}
	return live, nil
}

// resolveQuestionRef picks the question_ref for a decision recorded without
// one: the single live marker on the task. No live marker -> nil (the decision
// is recorded canonical_key-only, as before). Several -> a validation error
// naming them, so the caller passes question_ref explicitly instead of the
// server guessing which question was answered.
func (s *commentService) resolveQuestionRef(ctx context.Context, taskID uuid.UUID) (*uuid.UUID, error) {
	// Unknown gate state (lookup failed) is treated as up: binding the newest
	// marker is harmless, while skipping the fallback would leave it unanswered.
	gateUp := true
	if s.taskSvc != nil {
		if task, err := s.taskSvc.GetByID(ctx, taskID); err == nil && task != nil {
			gateUp = task.HumanGate
		}
	}
	live, err := s.liveBlockingMarkerIDs(ctx, taskID, gateUp)
	if err != nil {
		return nil, err
	}
	switch len(live) {
	case 0:
		return nil, nil
	case 1:
		return &live[0], nil
	}
	ids := make([]string, len(live))
	for i, id := range live {
		ids[i] = id.String()
	}
	return nil, &HumanGateDecisionValidationError{Msg: fmt.Sprintf(
		"question_ref is required: the task has %d live blocking markers (%s)", len(live), strings.Join(ids, ", "))}
}
