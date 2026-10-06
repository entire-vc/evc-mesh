package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Task #a36fa51d (analysis of #6443ff98): fiddler and the balancer post
// bookkeeping comments under the KEY OF THE ASSIGNEE, so to the comment parser
// they are the marker author "writing again". Their text routinely contains
// negator words ("брошенной", "восстановление"), and releaseHumanGateOnWithdrawal
// read them as the author withdrawing their own ask: the gate flipped three
// times in 30 minutes. A service comment carries no intent of the author, so it
// is never a withdrawal.
var serviceCommentPrefixes = []string{
	"[fiddler]",
	"[intake",
	"[balancer",
	"[fleet-balancer",
	"🤖 auto:",
	"🔀 **fleet-balancer",
	"🔀 fleet-balancer",
	"**no-stall",
	"no-stall",
}

// isServiceComment reports whether body is machinery output (fiddler, intake,
// balancer, no-stall, server Auto notices) rather than the author's own words.
func isServiceComment(body string) bool {
	head := strings.ToLower(strings.TrimSpace(body))
	for _, p := range serviceCommentPrefixes {
		if strings.HasPrefix(head, p) {
			return true
		}
	}
	return false
}

var supersedesRe = regexp.MustCompile(`(?im)\bsupersedes\b\s*[:=]\s*\S`)

// citesSupersedes reports whether a marker comment explicitly says it asks about
// a NEW subject: metadata {"supersedes": "..."} or a `supersedes: <ref>` line.
func citesSupersedes(comment *domain.Comment) bool {
	if len(comment.Metadata) > 0 {
		var m struct {
			Supersedes string `json:"supersedes"`
		}
		if err := json.Unmarshal(comment.Metadata, &m); err == nil && strings.TrimSpace(m.Supersedes) != "" {
			return true
		}
	}
	return supersedesRe.MatchString(comment.Body)
}

// priorAnswer describes an answer given after the previous marker.
type priorAnswer struct {
	at    time.Time
	quote string
}

// findAnswerSincePriorMarker looks for an answer to the previous ask on the
// thread: a human comment, or a live recorded decision, newer than the last
// earlier marker. nil when there is no earlier marker or no answer. Recorded
// decisions always count; a plain human comment counts only per the rule below.
func (s *commentService) findAnswerSincePriorMarker(ctx context.Context, task *domain.Task, current *domain.Comment) *priorAnswer {
	taskID := task.ID
	pg := pagination.Params{Page: 1, PageSize: pagination.MaxPageSize}
	pg.Normalize()
	var all []domain.Comment
	for {
		page, err := s.commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{IncludeInternal: true}, pg)
		if err != nil {
			log.Printf("[human-gate] WARNING: ListByTask on task %s failed during repeat-ask check: %v", taskID, err)
			return nil
		}
		if page != nil {
			all = append(all, page.Items...)
		}
		if page == nil || !page.HasMore {
			break
		}
		pg.Page++
	}

	var lastMarkerAt time.Time
	var lastMarkerID uuid.UUID
	haveMarker := false
	for _, c := range all {
		if c.ID == current.ID || isServiceComment(c.Body) || !hasBlockingMarker(c.Body) {
			continue
		}
		if c.CreatedAt.After(lastMarkerAt) {
			lastMarkerAt = c.CreatedAt
			lastMarkerID = c.ID
			haveMarker = true
		}
	}
	if !haveMarker {
		return nil
	}

	var best *priorAnswer
	consider := func(a priorAnswer) {
		if best == nil || a.at.After(best.at) {
			best = &a
		}
	}
	for _, c := range all {
		if c.ID == current.ID || c.AuthorType != domain.ActorTypeUser || !c.CreatedAt.After(lastMarkerAt) {
			continue
		}
		if isServiceComment(c.Body) || hasBlockingMarker(c.Body) {
			continue
		}
		// A human comment is an answer only if it replies to the ask directly, or
		// the gate is already down (the human's comment is what released it —
		// releaseHumanGate). While the gate is still up, an unrelated remark is
		// not an answer and the new marker just reaffirms the live ask.
		isReply := c.ParentCommentID != nil && *c.ParentCommentID == lastMarkerID
		if !isReply && task.HumanGate {
			continue
		}
		consider(priorAnswer{at: c.CreatedAt, quote: c.Body})
	}
	if s.hgdRepo != nil {
		rows, err := s.hgdRepo.ListByTask(ctx, taskID)
		if err != nil {
			log.Printf("[human-gate] WARNING: decision ListByTask on task %s failed during repeat-ask check: %v", taskID, err)
		}
		for _, d := range rows {
			if !d.IsDecision() || d.RevokedAt != nil || !d.CreatedAt.After(lastMarkerAt) {
				continue
			}
			q := "решение записано"
			if d.Quote != nil && *d.Quote != "" {
				q = *d.Quote
			}
			consider(priorAnswer{at: d.CreatedAt, quote: q})
		}
	}
	return best
}

// postRepeatAskNotice is enforceBlockingTriage's response to a marker that comes
// after an answer to the previous ask and does not cite `supersedes`: the gate is
// NOT armed and the author is told why, with the answer quoted.
func (s *commentService) postRepeatAskNotice(ctx context.Context, task *domain.Task, ans *priorAnswer) {
	quote := strings.Join(strings.Fields(ans.quote), " ")
	if r := []rune(quote); len(r) > 300 {
		quote = string(r[:300]) + "…"
	}
	now := timeNow()
	sysComment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🔁 Auto: human_gate НЕ взведён — на прошлый вопрос уже есть ответ (%s): «%s».\n\n"+
				"Новый вопрос принимается только с новым предметом и явной ссылкой: строка "+
				"`supersedes: <что именно из ответа не покрывает вопрос>` в комментарии или "+
				"`metadata.supersedes`. Иначе работай по полученному ответу.",
			ans.at.Format("2006-01-02 15:04"), quote,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sysComment); err != nil {
		log.Printf("[human-gate] WARNING: create repeat-ask notice on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}
