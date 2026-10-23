package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Chat WS event names (contract: agent-chat openapi.yaml, x-websocket-events).
const (
	ChatEventMessageCreated   = "chat.message.created"
	ChatEventMessageDelta     = "chat.message.delta"
	ChatEventMessageCompleted = "chat.message.completed"
	ChatEventTurnState        = "chat.turn.state"
	ChatEventTurnTool         = "chat.turn.tool"
	ChatEventThreadChanged    = "chat.thread.changed"
)

// ChatDeltaBatchInterval is the longest a streamed text fragment waits on the
// server before it is sent to the browser.
const ChatDeltaBatchInterval = 300 * time.Millisecond

// ChatThreadRef identifies the thread an event belongs to. OwnerID is the
// thread's author: it is the only user whose channel ever receives the event.
type ChatThreadRef struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	ThreadID    uuid.UUID
	OwnerID     uuid.UUID
}

// ChatWSEnvelope is the part shared by every chat.* event.
type ChatWSEnvelope struct {
	Event       string    `json:"event"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	ProjectID   uuid.UUID `json:"project_id"`
	ThreadID    uuid.UUID `json:"thread_id"`
	Seq         int64     `json:"seq"`
}

// ChatTurnRef is the TurnRef schema.
type ChatTurnRef struct {
	ID            uuid.UUID `json:"id"`
	State         string    `json:"state"`
	QueuePosition *int      `json:"queue_position,omitempty"`
	AgentOffline  bool      `json:"agent_offline,omitempty"`
}

// ChatMessagePayload is the Message schema as it travels over WS.
type ChatMessagePayload struct {
	ID              uuid.UUID  `json:"id"`
	ThreadID        uuid.UUID  `json:"thread_id"`
	Role            string     `json:"role"`
	Body            string     `json:"body"`
	Status          string     `json:"status"`
	Truncated       bool       `json:"truncated"`
	TurnID          *uuid.UUID `json:"turn_id"`
	ErrorCode       *string    `json:"error_code"`
	ClientMessageID *string    `json:"client_message_id"`
	Attachments     []any      `json:"attachments"`
	CreatedAt       time.Time  `json:"created_at"`
}

type chatMessageEvent struct {
	ChatWSEnvelope
	Message ChatMessagePayload `json:"message"`
}

type chatDeltaEvent struct {
	ChatWSEnvelope
	MessageID uuid.UUID `json:"message_id"`
	TurnID    uuid.UUID `json:"turn_id"`
	Offset    int       `json:"offset"`
	Text      string    `json:"text"`
}

type chatTurnStateEvent struct {
	ChatWSEnvelope
	Turn         ChatTurnRef `json:"turn"`
	SessionState string      `json:"session_state"`
	ErrorCode    *string     `json:"error_code"`
}

type chatTurnToolEvent struct {
	ChatWSEnvelope
	TurnID uuid.UUID `json:"turn_id"`
	Kind   string    `json:"kind,omitempty"`
	Label  string    `json:"label"`
}

type chatThreadChangedEvent struct {
	ChatWSEnvelope
	Change string `json:"change"`
	Thread any    `json:"thread,omitempty"`
}

// ChatSeqSource hands out the per-thread monotonic counter shared by all
// chat.* events of that thread.
type ChatSeqSource interface {
	NextSeq(ctx context.Context, threadID uuid.UUID) (int64, error)
}

type redisChatSeq struct{ rdb *redis.Client }

// NewRedisChatSeq counts in Redis so every API replica agrees on the order.
func NewRedisChatSeq(rdb *redis.Client) ChatSeqSource { return &redisChatSeq{rdb: rdb} }

func (r *redisChatSeq) NextSeq(ctx context.Context, threadID uuid.UUID) (int64, error) {
	return r.rdb.Incr(ctx, "chat:seq:"+threadID.String()).Result()
}

// ChatEventPublisher turns chat state changes into WS events on the owner's
// personal channel and on no other.
type ChatEventPublisher struct {
	ws  WSPublisher
	seq ChatSeqSource

	mu    sync.Mutex
	locks map[uuid.UUID]*sync.Mutex
}

func NewChatEventPublisher(ws WSPublisher, seq ChatSeqSource) *ChatEventPublisher {
	return &ChatEventPublisher{ws: ws, seq: seq, locks: map[uuid.UUID]*sync.Mutex{}}
}

func chatChannel(owner uuid.UUID) string { return "ws:user:" + owner.String() }

func (p *ChatEventPublisher) threadLock(id uuid.UUID) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.locks[id]
	if !ok {
		l = &sync.Mutex{}
		p.locks[id] = l
	}
	return l
}

// emit allocates seq and publishes under one per-thread lock, so the order on
// the wire matches seq order.
func (p *ChatEventPublisher) emit(ctx context.Context, t ChatThreadRef, event string, build func(ChatWSEnvelope) any) error {
	if p == nil || p.ws == nil {
		return nil
	}
	if t.OwnerID == uuid.Nil || t.ThreadID == uuid.Nil {
		return errors.New("chat event: thread owner and id are required")
	}
	l := p.threadLock(t.ThreadID)
	l.Lock()
	defer l.Unlock()
	seq, err := p.seq.NextSeq(ctx, t.ThreadID)
	if err != nil {
		return fmt.Errorf("chat event seq: %w", err)
	}
	env := ChatWSEnvelope{Event: event, WorkspaceID: t.WorkspaceID, ProjectID: t.ProjectID, ThreadID: t.ThreadID, Seq: seq}
	return p.ws.Publish(ctx, chatChannel(t.OwnerID), build(env))
}

func (p *ChatEventPublisher) MessageCreated(ctx context.Context, t ChatThreadRef, m ChatMessagePayload) error {
	return p.emit(ctx, t, ChatEventMessageCreated, func(e ChatWSEnvelope) any {
		return chatMessageEvent{ChatWSEnvelope: e, Message: m}
	})
}

func (p *ChatEventPublisher) MessageCompleted(ctx context.Context, t ChatThreadRef, m ChatMessagePayload) error {
	return p.emit(ctx, t, ChatEventMessageCompleted, func(e ChatWSEnvelope) any {
		return chatMessageEvent{ChatWSEnvelope: e, Message: m}
	})
}

func (p *ChatEventPublisher) TurnState(ctx context.Context, t ChatThreadRef, turn ChatTurnRef, sessionState string, errorCode *string) error {
	return p.emit(ctx, t, ChatEventTurnState, func(e ChatWSEnvelope) any {
		return chatTurnStateEvent{ChatWSEnvelope: e, Turn: turn, SessionState: sessionState, ErrorCode: errorCode}
	})
}

func (p *ChatEventPublisher) TurnTool(ctx context.Context, t ChatThreadRef, turnID uuid.UUID, kind, label string) error {
	if utf8.RuneCountInString(label) > 200 {
		label = string([]rune(label)[:200])
	}
	return p.emit(ctx, t, ChatEventTurnTool, func(e ChatWSEnvelope) any {
		return chatTurnToolEvent{ChatWSEnvelope: e, TurnID: turnID, Kind: kind, Label: label}
	})
}

func (p *ChatEventPublisher) ThreadChanged(ctx context.Context, t ChatThreadRef, change string, thread any) error {
	return p.emit(ctx, t, ChatEventThreadChanged, func(e ChatWSEnvelope) any {
		return chatThreadChangedEvent{ChatWSEnvelope: e, Change: change, Thread: thread}
	})
}

// ChatDeltaStream batches the streamed text of one agent message: fragments
// collect for at most ChatDeltaBatchInterval, then go out as one
// chat.message.delta whose offset counts Unicode code points already sent.
type ChatDeltaStream struct {
	p         *ChatEventPublisher
	t         ChatThreadRef
	messageID uuid.UUID
	turnID    uuid.UUID
	interval  time.Duration

	// flushMu spans take-and-publish, so a Close that finds nothing pending
	// still waits for a timer flush that has already taken the text.
	flushMu sync.Mutex
	mu      sync.Mutex
	pending []byte
	sent    int // code points already published
	timer   *time.Timer
	closed  bool
}

func (p *ChatEventPublisher) NewDeltaStream(t ChatThreadRef, messageID, turnID uuid.UUID) *ChatDeltaStream {
	return &ChatDeltaStream{p: p, t: t, messageID: messageID, turnID: turnID, interval: ChatDeltaBatchInterval}
}

// Append queues text; the first fragment of a batch arms the flush timer.
func (s *ChatDeltaStream) Append(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.pending = append(s.pending, text...)
	if s.timer == nil {
		s.timer = time.AfterFunc(s.interval, func() { _ = s.Flush(context.Background()) })
	}
}

// Flush publishes whatever is pending. Close calls it, and the caller sends
// chat.message.completed after Close so no delta trails the final body.
func (s *ChatDeltaStream) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	text := string(s.pending)
	s.pending = nil
	offset := s.sent
	s.sent += utf8.RuneCountInString(text)
	s.mu.Unlock()
	err := s.p.emit(ctx, s.t, ChatEventMessageDelta, func(e ChatWSEnvelope) any {
		return chatDeltaEvent{ChatWSEnvelope: e, MessageID: s.messageID, TurnID: s.turnID, Offset: offset, Text: text}
	})
	if err != nil {
		// Keep the batch and the offset where they were, so the next flush
		// resends it instead of leaving a gap in the client's text.
		s.mu.Lock()
		s.pending = append([]byte(text), s.pending...)
		s.sent = offset
		if s.timer == nil && !s.closed {
			s.timer = time.AfterFunc(s.interval, func() { _ = s.Flush(context.Background()) })
		}
		s.mu.Unlock()
	}
	return err
}

// Close stops accepting text, then publishes what is left. Appends racing with
// it are dropped rather than left pending behind the final flush.
func (s *ChatDeltaStream) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.Flush(ctx)
}
