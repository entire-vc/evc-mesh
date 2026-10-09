package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func (h *parkHarness) say(t *testing.T, task *domain.Task, author uuid.UUID, kind domain.ActorType, body string, ago time.Duration, meta string) uuid.UUID {
	t.Helper()
	c := &domain.Comment{ID: uuid.New(), TaskID: task.ID, AuthorID: author, AuthorType: kind, Body: body, CreatedAt: timeNow().Add(-ago), UpdatedAt: timeNow().Add(-ago)}
	if meta != "" {
		c.Metadata = json.RawMessage(meta)
	}
	if err := h.commentRepo.Create(context.Background(), c); err != nil {
		t.Fatalf("comment: %v", err)
	}
	return c.ID
}

func (h *parkHarness) sweepOne(t *testing.T) {
	t.Helper()
	if _, err := h.reaper.SweepUnleasedInProgress(context.Background(), DefaultUnleasedGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}
}

func (h *parkHarness) category(t *testing.T, id uuid.UUID) domain.StatusCategory {
	t.Helper()
	st, err := h.statusRepo.GetByID(context.Background(), h.stored(t, id).StatusID)
	if err != nil || st == nil {
		t.Fatalf("task is in no known status (never moved?): %v", err)
	}
	return st.Category
}

func handoffHarness(t *testing.T) (*parkHarness, *domain.Task) {
	h := newParkHarness(t, &domain.MidPipelineConfig{AutoParkStalled: true})
	task := h.addUnleasedTask(t, h.projectID, 4*time.Hour)
	return h, task
}

// K1: the reviewer's DO-NOT-SHIP verdict is the last word, the executor must act.
func TestHandoff_ReviewerVerdictReturnsToTodoNotBacklog(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, *task.AssigneeID, domain.ActorTypeAgent, "готово, на ревью", 6*time.Hour, "")
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "VERDICT: DO-NOT-SHIP — C1 correctness FAIL", 5*time.Hour, "")
	h.sweepOne(t)

	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("handed-off card must return to todo, got %v", got)
	}
	stored := h.stored(t, task.ID)
	if stored.DueDate != nil || containsInStringArray(stored.Labels, parkMonitorLabel) {
		t.Fatalf("a handoff must not get the park alarm: due=%v labels=%v", stored.DueDate, stored.Labels)
	}
	if stored.AssigneeID == nil || *stored.AssigneeID != *task.AssigneeID {
		t.Fatalf("assignee must be unchanged")
	}
}

// R2b: another agent asks the assignee for a signature.
func TestHandoff_PeerRequestForSignatureReturnsToTodo(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "!1125 готов к merge, нужна одна подпись", 5*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("got %v, want todo", got)
	}
}

// Riker's third case: the assignee's WAIT, then the reviewer's verdict. The SQL candidate
// query lets the card through once the marker is no longer the newest word (see
// TestFindStaleUnleasedInProgress_LiveWaitMarker); here the reaper must send it to todo.
func TestHandoff_ReviewerCommentNewerThanAssigneeWaitReturnsToTodo(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, *task.AssigneeID, domain.ActorTypeAgent, "⏳ WAIT mr:entire-vc/evc-mesh!1125\nждём ревью", 8*time.Hour, "")
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "VERDICT: DO-NOT-SHIP — C1 correctness FAIL", 5*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("got %v, want todo", got)
	}
	if stored := h.stored(t, task.ID); stored.DueDate != nil {
		t.Fatalf("a handoff must not get the park alarm")
	}
}

// A human's message after the assignee's is a handoff too.
func TestHandoff_HumanReplyReturnsToTodo(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, *task.AssigneeID, domain.ActorTypeAgent, "вопрос", 8*time.Hour, "")
	h.say(t, task, uuid.New(), domain.ActorTypeUser, "ответ: делай вариант B", 5*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("got %v, want todo", got)
	}
}

// The assignee had the last word and left no outcome marker: abandoned, parked.
func TestHandoff_AssigneeLastWordStillParks(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "замечание ревьюера", 8*time.Hour, "")
	h.say(t, task, *task.AssigneeID, domain.ActorTypeAgent, "понял, берусь", 5*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryBacklog {
		t.Fatalf("got %v, want backlog", got)
	}
}

// System and informational comments do not carry the turn.
func TestHandoff_SystemAndInformationalCommentsAreSkipped(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, *task.AssigneeID, domain.ActorTypeAgent, "моё последнее слово", 9*time.Hour, "")
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "ок, принято", 6*time.Hour, `{"informational":true}`)
	h.say(t, task, uuid.Nil, domain.ActorTypeSystem, "системная заметка", 5*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryBacklog {
		t.Fatalf("got %v, want backlog", got)
	}
}

// A live WAIT/NEXT marker is excluded by the candidate query, so nothing moves.
func TestHandoff_NoHandoffWithoutAssigneeOrComments(t *testing.T) {
	h, task := handoffHarness(t)
	h.sweepOne(t) // no comments at all: abandoned
	if got := h.category(t, task.ID); got != domain.StatusCategoryBacklog {
		t.Fatalf("got %v, want backlog", got)
	}
}

// The same handoff comes back to todo once; if the card stalls again with the
// same last word it is parked like any abandoned card.
func TestHandoff_SameHandoffIsReturnedOnlyOnce(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "нужна подпись", 5*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("first pass: got %v, want todo", got)
	}
	marked := false
	for _, b := range h.commentBodies() {
		marked = marked || strings.Contains(b, "[handoff:")
	}
	if !marked {
		t.Fatalf("the return must leave the handoff marker note")
	}

	// Back in progress, still no lease and nobody answered.
	stored := h.stored(t, task.ID)
	inProgress := uuid.New()
	stored.StatusID, stored.UpdatedAt = inProgress, time.Now().Add(-4*time.Hour)
	if err := h.taskRepo.Update(context.Background(), stored); err != nil {
		t.Fatalf("reset task: %v", err)
	}
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryBacklog {
		t.Fatalf("second pass over the same handoff: got %v, want backlog", got)
	}
}

// A different, newer handoff is a new key and returns again.
func TestHandoff_NewHandoffAfterReturnIsReturnedAgain(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "нужна подпись", 6*time.Hour, "")
	h.sweepOne(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "ещё правка: поправь тест", 5*time.Hour, "")
	stored := h.stored(t, task.ID)
	stored.StatusID, stored.UpdatedAt = uuid.New(), time.Now().Add(-4*time.Hour)
	if err := h.taskRepo.Update(context.Background(), stored); err != nil {
		t.Fatalf("reset task: %v", err)
	}
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("got %v, want todo", got)
	}
}

// The comment list is oldest-first, so a long thread must be read from its last
// page, not its first.
func TestHandoff_LongThreadIsReadFromTheEnd(t *testing.T) {
	h, task := handoffHarness(t)
	for i := 0; i < handoffScanLimit+10; i++ {
		h.say(t, task, *task.AssigneeID, domain.ActorTypeAgent, "рабочая заметка", time.Duration(600-i)*time.Minute, "")
	}
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "DO-NOT-SHIP", 4*time.Hour, "")
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("got %v, want todo", got)
	}
}

// More than a page of later system notes must not hide the unanswered handoff
// behind them (a page-local scan would find no live comment and park the card).
func TestHandoff_SystemNotesBeyondOnePageDoNotHideTheHandoff(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "нужна подпись", 10*time.Hour, "")
	for i := 0; i < handoffScanLimit+5; i++ {
		h.say(t, task, uuid.Nil, domain.ActorTypeSystem, "авто-заметка", time.Duration(300-i)*time.Minute, "")
	}
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryTodo {
		t.Fatalf("got %v, want todo", got)
	}
}

// ...and the marker note still stops the loop when notes pile up after it.
func TestHandoff_MarkerBeyondOnePageStillStopsTheLoop(t *testing.T) {
	h, task := handoffHarness(t)
	h.say(t, task, uuid.New(), domain.ActorTypeAgent, "нужна подпись", 10*time.Hour, "")
	h.sweepOne(t)
	for i := 0; i < handoffScanLimit+5; i++ {
		h.say(t, task, uuid.Nil, domain.ActorTypeSystem, "авто-заметка", -time.Duration(i+1)*time.Second, "")
	}
	stored := h.stored(t, task.ID)
	stored.StatusID, stored.UpdatedAt = uuid.New(), time.Now().Add(-4*time.Hour)
	if err := h.taskRepo.Update(context.Background(), stored); err != nil {
		t.Fatalf("reset task: %v", err)
	}
	h.sweepOne(t)
	if got := h.category(t, task.ID); got != domain.StatusCategoryBacklog {
		t.Fatalf("got %v, want backlog", got)
	}
}
