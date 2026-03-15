package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

const (
	slackHTTPTimeout    = 5 * time.Second
	slackDefaultBaseURL = "https://mesh.entire.vc"
)

// slackConfig is the Slack Incoming Webhook configuration stored as JSONB
// in integration_configs.config.
type slackConfig struct {
	WebhookURL   string   `json:"webhook_url"`
	Channel      string   `json:"channel"`
	NotifyEvents []string `json:"notify_events"`
}

// SlackMessage is the top-level Incoming Webhook payload sent to Slack.
type SlackMessage struct {
	Text   string       `json:"text,omitempty"`
	Blocks []slackBlock `json:"blocks,omitempty"`
}

// slackBlock is a Slack Block Kit element.
type slackBlock struct {
	Type   string         `json:"type"`
	Text   *slackTextObj  `json:"text,omitempty"`
	Fields []slackTextObj `json:"fields,omitempty"`
}

// slackTextObj holds a Slack text object (plain_text or mrkdwn).
type slackTextObj struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TaskEvent carries the data required to build a Slack notification for a task lifecycle event.
type TaskEvent struct {
	EventType    string
	TaskID       uuid.UUID
	TaskTitle    string
	ProjectID    uuid.UUID
	ProjectName  string
	OldStatus    string
	NewStatus    string
	Priority     string
	AssigneeName string
	// BaseURL is the web UI base URL used to build task deep-links (e.g. "https://mesh.entire.vc").
	BaseURL string
}

// slackService implements SlackService.
type slackService struct {
	integrationRepo repository.IntegrationRepository
	client          *http.Client
}

// NewSlackService creates a new SlackService backed by the integration repository.
func NewSlackService(integrationRepo repository.IntegrationRepository) SlackService {
	return &slackService{
		integrationRepo: integrationRepo,
		client: &http.Client{
			Timeout: slackHTTPTimeout,
		},
	}
}

// NotifyTaskEvent looks up the active Slack integration for the workspace and,
// if the event type is configured for notifications, sends a rich Slack message.
// The call is fire-and-forget: errors are logged but not returned.
func (s *slackService) NotifyTaskEvent(ctx context.Context, workspaceID uuid.UUID, event TaskEvent) {
	go func() {
		bgCtx := context.Background()

		cfg, err := s.integrationRepo.GetByProvider(bgCtx, workspaceID, domain.IntegrationProviderSlack)
		if err != nil {
			log.Printf("[slack] failed to load Slack integration for workspace %s: %v", workspaceID, err)
			return
		}
		if cfg == nil || !cfg.IsActive {
			return
		}

		var sc slackConfig
		if err := json.Unmarshal(cfg.Config, &sc); err != nil {
			log.Printf("[slack] invalid Slack config for workspace %s: %v", workspaceID, err)
			return
		}
		if sc.WebhookURL == "" {
			log.Printf("[slack] webhook_url is empty for workspace %s, skipping", workspaceID)
			return
		}

		// Check if the event type is subscribed.
		if !slackEventSubscribed(sc.NotifyEvents, event.EventType) {
			return
		}

		msg := buildSlackMessage(event)
		if err := s.SendMessage(bgCtx, sc.WebhookURL, msg); err != nil {
			log.Printf("[slack] failed to deliver notification for event %s (workspace %s): %v", event.EventType, workspaceID, err)
		}
	}()
}

// SendMessage POSTs a SlackMessage to the given Incoming Webhook URL.
// It returns an error if the HTTP call fails or Slack returns a non-2xx response.
func (s *slackService) SendMessage(ctx context.Context, webhookURL string, message SlackMessage) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal slack message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "evc-mesh-slack/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post to slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned non-2xx status: %d", resp.StatusCode)
	}
	return nil
}

// slackEventSubscribed reports whether eventType appears in the notify_events list.
// If the list is empty it defaults to allowing all four standard events.
func slackEventSubscribed(notifyEvents []string, eventType string) bool {
	if len(notifyEvents) == 0 {
		// Default: notify for all standard task events.
		switch eventType {
		case "task.created", "task.status_changed", "task.assigned", "comment.created":
			return true
		}
		return false
	}
	for _, e := range notifyEvents {
		if e == eventType {
			return true
		}
	}
	return false
}

// buildSlackMessage constructs a rich Block Kit message for the given TaskEvent.
func buildSlackMessage(event TaskEvent) SlackMessage {
	header := eventTypeLabel(event.EventType)

	baseURL := event.BaseURL
	if baseURL == "" {
		baseURL = slackDefaultBaseURL
	}
	taskLink := fmt.Sprintf("%s/tasks/%s", baseURL, event.TaskID)

	taskField := slackTextObj{
		Type: "mrkdwn",
		Text: fmt.Sprintf("*Task:*\n<%s|%s>", taskLink, event.TaskTitle),
	}

	fields := []slackTextObj{taskField}

	switch event.EventType {
	case "task.status_changed":
		status := event.NewStatus
		if event.OldStatus != "" && event.NewStatus != "" {
			status = event.OldStatus + " → " + event.NewStatus
		}
		fields = append(fields, slackTextObj{Type: "mrkdwn", Text: "*Status:*\n" + status})
	case "task.assigned":
		assignee := event.AssigneeName
		if assignee == "" {
			assignee = "unassigned"
		}
		fields = append(fields, slackTextObj{Type: "mrkdwn", Text: "*Assignee:*\n" + assignee})
	case "comment.created":
		if event.ProjectName != "" {
			fields = append(fields, slackTextObj{Type: "mrkdwn", Text: "*Project:*\n" + event.ProjectName})
		}
	}

	if event.Priority != "" {
		fields = append(fields, slackTextObj{Type: "mrkdwn", Text: "*Priority:*\n" + event.Priority})
	}

	return SlackMessage{
		Blocks: []slackBlock{
			{
				Type: "header",
				Text: &slackTextObj{Type: "plain_text", Text: header},
			},
			{
				Type:   "section",
				Fields: fields,
			},
		},
	}
}

// eventTypeLabel returns a human-readable header string for the Slack message.
func eventTypeLabel(eventType string) string {
	switch eventType {
	case "task.created":
		return "New Task Created"
	case "task.status_changed":
		return "Task Status Changed"
	case "task.assigned":
		return "Task Assigned"
	case "comment.created":
		return "New Comment"
	default:
		return "Task Event"
	}
}

// Ensure slackService satisfies the SlackService interface.
var _ SlackService = (*slackService)(nil)
