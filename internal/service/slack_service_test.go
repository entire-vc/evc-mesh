package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestSendMessage_Success verifies that SendMessage POSTs valid JSON to the webhook URL.
func TestSendMessage_Success(t *testing.T) {
	var received SlackMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc := &slackService{client: srv.Client()}

	msg := SlackMessage{
		Blocks: []slackBlock{
			{Type: "header", Text: &slackTextObj{Type: "plain_text", Text: "Test Header"}},
		},
	}

	if err := svc.SendMessage(context.Background(), srv.URL, msg); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	if len(received.Blocks) != 1 || received.Blocks[0].Type != "header" {
		t.Errorf("unexpected message received: %+v", received)
	}
}

// TestSendMessage_Non2xxError verifies that a non-2xx Slack response returns an error.
func TestSendMessage_Non2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	svc := &slackService{client: srv.Client()}
	msg := SlackMessage{Text: "test"}

	err := svc.SendMessage(context.Background(), srv.URL, msg)
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

// TestSlackEventSubscribed_DefaultEvents verifies default event subscription behavior.
func TestSlackEventSubscribed_DefaultEvents(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{"task.created", true},
		{"task.status_changed", true},
		{"task.assigned", true},
		{"comment.created", true},
		{"webhook.test", false},
		{"task.deleted", false},
	}

	for _, tt := range tests {
		got := slackEventSubscribed(nil, tt.eventType)
		if got != tt.want {
			t.Errorf("slackEventSubscribed(nil, %q) = %v, want %v", tt.eventType, got, tt.want)
		}
	}
}

// TestSlackEventSubscribed_CustomList verifies that a custom notify_events list is respected.
func TestSlackEventSubscribed_CustomList(t *testing.T) {
	list := []string{"task.created", "comment.created"}

	if !slackEventSubscribed(list, "task.created") {
		t.Error("expected task.created to be subscribed")
	}
	if slackEventSubscribed(list, "task.status_changed") {
		t.Error("expected task.status_changed NOT to be subscribed")
	}
}

// TestBuildSlackMessage_TaskCreated verifies the Block Kit structure for task.created.
func TestBuildSlackMessage_TaskCreated(t *testing.T) {
	event := TaskEvent{
		EventType: "task.created",
		TaskID:    uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		TaskTitle: "Fix the bug",
		Priority:  "high",
		BaseURL:   "https://example.com",
	}

	msg := buildSlackMessage(event)

	if len(msg.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(msg.Blocks))
	}

	header := msg.Blocks[0]
	if header.Type != "header" {
		t.Errorf("first block should be header, got %s", header.Type)
	}
	if header.Text == nil || header.Text.Text != "New Task Created" {
		t.Errorf("unexpected header text: %+v", header.Text)
	}

	section := msg.Blocks[1]
	if section.Type != "section" {
		t.Errorf("second block should be section, got %s", section.Type)
	}
	// Should have task link field and priority field.
	if len(section.Fields) < 2 {
		t.Errorf("expected at least 2 fields in section, got %d", len(section.Fields))
	}
	// Task field should contain the task link.
	taskField := section.Fields[0]
	if !strings.Contains(taskField.Text, "Fix the bug") {
		t.Errorf("task field should contain task title, got: %s", taskField.Text)
	}
	if !strings.Contains(taskField.Text, "https://example.com/tasks/00000000-0000-0000-0000-000000000001") {
		t.Errorf("task field should contain task link, got: %s", taskField.Text)
	}
}

// TestBuildSlackMessage_StatusChanged verifies status transition is shown.
func TestBuildSlackMessage_StatusChanged(t *testing.T) {
	event := TaskEvent{
		EventType: "task.status_changed",
		TaskID:    uuid.New(),
		TaskTitle: "Deploy feature",
		OldStatus: "todo",
		NewStatus: "in_progress",
	}

	msg := buildSlackMessage(event)

	section := msg.Blocks[1]
	found := false
	for _, f := range section.Fields {
		if strings.Contains(f.Text, "todo → in_progress") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected status transition 'todo → in_progress' in fields: %+v", section.Fields)
	}
}

// TestSlackEventFromPayload verifies field extraction from a map payload.
func TestSlackEventFromPayload(t *testing.T) {
	taskID := uuid.New()
	projID := uuid.New()

	payload := map[string]interface{}{
		"task_id":    taskID.String(),
		"project_id": projID.String(),
		"title":      "My Task",
		"priority":   "medium",
	}

	event := slackEventFromPayload("task.created", payload)

	if event.EventType != "task.created" {
		t.Errorf("expected event type task.created, got %s", event.EventType)
	}
	if event.TaskID != taskID {
		t.Errorf("expected task_id %s, got %s", taskID, event.TaskID)
	}
	if event.ProjectID != projID {
		t.Errorf("expected project_id %s, got %s", projID, event.ProjectID)
	}
	if event.TaskTitle != "My Task" {
		t.Errorf("expected title 'My Task', got %s", event.TaskTitle)
	}
	if event.Priority != "medium" {
		t.Errorf("expected priority 'medium', got %s", event.Priority)
	}
}

// TestToStringMap verifies conversion of various payload types.
func TestToStringMap(t *testing.T) {
	// Already a map.
	m1 := map[string]interface{}{"key": "val"}
	got1, ok1 := toStringMap(m1)
	if !ok1 || got1["key"] != "val" {
		t.Errorf("toStringMap with map: got %v, ok=%v", got1, ok1)
	}

	// Struct that marshals to JSON.
	type S struct {
		Name string `json:"name"`
	}
	got2, ok2 := toStringMap(S{Name: "mesh"})
	if !ok2 || got2["name"] != "mesh" {
		t.Errorf("toStringMap with struct: got %v, ok=%v", got2, ok2)
	}

	// Non-marshalable (channel).
	_, ok3 := toStringMap(make(chan int))
	if ok3 {
		t.Error("toStringMap with channel should return ok=false")
	}
}
