package service

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chatCapture struct {
	mu     sync.Mutex
	byChan map[string][]map[string]any
}

func (c *chatCapture) Publish(_ context.Context, channel string, event any) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChan == nil {
		c.byChan = map[string][]map[string]any{}
	}
	c.byChan[channel] = append(c.byChan[channel], m)
	return nil
}

func (c *chatCapture) on(ch string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.byChan[ch]...)
}

type countingSeq struct{ n atomic.Int64 }

func (s *countingSeq) NextSeq(context.Context, uuid.UUID) (int64, error) { return s.n.Add(1), nil }

func chatFixture() (*ChatEventPublisher, *chatCapture, ChatThreadRef, uuid.UUID) {
	ws := &chatCapture{}
	owner, other := uuid.New(), uuid.New()
	t := ChatThreadRef{WorkspaceID: uuid.New(), ProjectID: uuid.New(), ThreadID: uuid.New(), OwnerID: owner}
	return NewChatEventPublisher(ws, &countingSeq{}), ws, t, other
}

func TestChatEvents_OnlyOwnerChannelReceives(t *testing.T) {
	p, ws, th, userB := chatFixture()
	ctx := context.Background()
	msg := ChatMessagePayload{ID: uuid.New(), ThreadID: th.ThreadID, Role: "user", Body: "hi", Status: "done"}
	turn := ChatTurnRef{ID: uuid.New(), State: "queued"}

	require.NoError(t, p.MessageCreated(ctx, th, msg))
	require.NoError(t, p.TurnState(ctx, th, turn, "working", nil))
	require.NoError(t, p.TurnTool(ctx, th, turn.ID, "read", "reading file"))
	require.NoError(t, p.MessageCompleted(ctx, th, msg))
	require.NoError(t, p.ThreadChanged(ctx, th, "renamed", nil))

	got := ws.on("ws:user:" + th.OwnerID.String())
	require.Len(t, got, 5)
	// red control: user B's channel must stay empty, and the check must be
	// able to fail — publishing to B's channel makes this very assertion fire.
	assert.Empty(t, ws.on("ws:user:"+userB.String()))
	assert.Len(t, ws.byChan, 1)

	for i, ev := range got {
		assert.EqualValues(t, i+1, ev["seq"], "seq is monotonic per thread")
		assert.Equal(t, th.ThreadID.String(), ev["thread_id"])
	}
	assert.Equal(t, ChatEventMessageCreated, got[0]["event"])
	assert.Equal(t, ChatEventTurnState, got[1]["event"])
}

func TestChatEvents_RedControlWrongChannelIsDetected(t *testing.T) {
	_, ws, th, userB := chatFixture()
	// A misrouted publish (the bug the guard test exists for) is visible.
	require.NoError(t, ws.Publish(context.Background(), "ws:user:"+userB.String(), map[string]any{"event": "chat.message.created"}))
	assert.NotEmpty(t, ws.on("ws:user:"+userB.String()))
	assert.Empty(t, ws.on("ws:user:"+th.OwnerID.String()))
}

func TestChatEvents_RejectsMissingOwner(t *testing.T) {
	p, ws, th, _ := chatFixture()
	th.OwnerID = uuid.Nil
	assert.Error(t, p.TurnTool(context.Background(), th, uuid.New(), "read", "x"))
	assert.Empty(t, ws.byChan)
}

func TestChatDeltaStream_BatchesAndCountsCodePoints(t *testing.T) {
	p, ws, th, _ := chatFixture()
	ctx := context.Background()
	s := p.NewDeltaStream(th, uuid.New(), uuid.New())
	s.interval = 20 * time.Millisecond

	s.Append("пр")
	s.Append("ив")
	s.Append("ет 👋")
	require.Eventually(t, func() bool { return len(ws.on("ws:user:"+th.OwnerID.String())) == 1 }, time.Second, 5*time.Millisecond)
	s.Append("!!")
	require.NoError(t, s.Close(ctx))

	got := ws.on("ws:user:" + th.OwnerID.String())
	require.Len(t, got, 2, "three fragments inside the window leave as one delta")
	assert.Equal(t, "привет 👋", got[0]["text"])
	assert.EqualValues(t, 0, got[0]["offset"])
	assert.EqualValues(t, 8, got[1]["offset"], "offset counts code points, not bytes or UTF-16 units")
	assert.Equal(t, "!!", got[1]["text"])
	s.Append("late")
	require.NoError(t, s.Flush(ctx))
	assert.Len(t, ws.on("ws:user:"+th.OwnerID.String()), 2, "nothing after Close")
}

// slowWS makes publication take long enough for Close to overtake a timer flush.
type slowWS struct {
	chatCapture
	delay time.Duration
}

func (w *slowWS) Publish(ctx context.Context, ch string, ev any) error {
	time.Sleep(w.delay)
	return w.chatCapture.Publish(ctx, ch, ev)
}

func TestChatDeltaStream_CloseWaitsForInFlightFlush(t *testing.T) {
	ws := &slowWS{delay: 100 * time.Millisecond}
	owner := uuid.New()
	th := ChatThreadRef{WorkspaceID: uuid.New(), ProjectID: uuid.New(), ThreadID: uuid.New(), OwnerID: owner}
	p := NewChatEventPublisher(ws, &countingSeq{})
	s := p.NewDeltaStream(th, uuid.New(), uuid.New())
	s.interval = 10 * time.Millisecond

	s.Append("text")
	time.Sleep(40 * time.Millisecond) // timer has taken the text and is publishing
	require.NoError(t, s.Close(context.Background()))
	assert.Len(t, ws.on("ws:user:"+owner.String()), 1, "delta is on the wire before Close returns")
}

type flakyWS struct {
	chatCapture
	failures atomic.Int32
}

func (w *flakyWS) Publish(ctx context.Context, ch string, ev any) error {
	if w.failures.Add(-1) >= 0 {
		return context.DeadlineExceeded
	}
	return w.chatCapture.Publish(ctx, ch, ev)
}

func TestChatDeltaStream_FailedPublishIsResentWithSameOffset(t *testing.T) {
	ws := &flakyWS{}
	ws.failures.Store(1)
	owner := uuid.New()
	th := ChatThreadRef{WorkspaceID: uuid.New(), ProjectID: uuid.New(), ThreadID: uuid.New(), OwnerID: owner}
	s := NewChatEventPublisher(ws, &countingSeq{}).NewDeltaStream(th, uuid.New(), uuid.New())
	s.interval = time.Hour

	s.Append("abc")
	require.Error(t, s.Flush(context.Background()))
	s.Append("de")
	require.NoError(t, s.Close(context.Background()))

	got := ws.on("ws:user:" + owner.String())
	require.Len(t, got, 1)
	assert.Equal(t, "abcde", got[0]["text"])
	assert.EqualValues(t, 0, got[0]["offset"], "no gap after a failed publish")
}

func TestChatDeltaStream_AppendAfterCloseIsDropped(t *testing.T) {
	ws := &chatCapture{}
	owner := uuid.New()
	th := ChatThreadRef{WorkspaceID: uuid.New(), ProjectID: uuid.New(), ThreadID: uuid.New(), OwnerID: owner}
	s := NewChatEventPublisher(ws, &countingSeq{}).NewDeltaStream(th, uuid.New(), uuid.New())
	s.interval = 5 * time.Millisecond
	require.NoError(t, s.Close(context.Background()))
	s.Append("late")
	time.Sleep(30 * time.Millisecond)
	assert.Empty(t, ws.on("ws:user:"+owner.String()))
}

func TestChatEvents_NilPublisherIsNoop(t *testing.T) {
	var p *ChatEventPublisher
	th := ChatThreadRef{ThreadID: uuid.New(), OwnerID: uuid.New()}
	assert.NoError(t, p.ThreadChanged(context.Background(), th, "deleted", nil))
}
