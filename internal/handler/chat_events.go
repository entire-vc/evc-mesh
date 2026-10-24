package handler

import (
	"context"
	"log"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// ChatEvents is what the chat handlers need from service.ChatEventPublisher.
// Every event goes to the thread owner's channel only.
type ChatEvents interface {
	MessageCreated(ctx context.Context, t service.ChatThreadRef, m service.ChatMessagePayload) error
	TurnState(ctx context.Context, t service.ChatThreadRef, turn service.ChatTurnRef, sessionState string, errorCode *string) error
	ThreadChanged(ctx context.Context, t service.ChatThreadRef, change string, thread any) error
}

// chatEmit runs one publish; a failed publish is logged and never fails the
// request the state change already committed.
func chatEmit(what string, err error) {
	if err != nil {
		log.Printf("[chat-events] %s: %v", what, err)
	}
}

func chatThreadRef(workspace, project, thread, owner uuid.UUID) service.ChatThreadRef {
	return service.ChatThreadRef{WorkspaceID: workspace, ProjectID: project, ThreadID: thread, OwnerID: owner}
}

func chatTurnRef(t postgres.ChatTurnRef) service.ChatTurnRef {
	return service.ChatTurnRef{ID: t.ID, State: t.State, QueuePosition: t.QueuePosition, AgentOffline: t.AgentOffline}
}

func chatMessagePayload(m postgres.ChatMessage) service.ChatMessagePayload {
	atts := make([]any, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		atts = append(atts, a)
	}
	return service.ChatMessagePayload{ID: m.ID, ThreadID: m.ThreadID, Role: m.Role, Body: m.Body, Status: m.Status,
		Truncated: m.Truncated, TurnID: m.TurnID, ErrorCode: m.ErrorCode, ClientMessageID: m.ClientMessageID,
		Attachments: atts, CreatedAt: m.CreatedAt}
}
