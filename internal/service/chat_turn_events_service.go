package service

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatEventsRatePerSecond is the batch rate one turn may sustain (protocol §6).
const ChatEventsRatePerSecond = 20

// chatExpiryBatch bounds one expiry pass.
const chatExpiryBatch = 200

// ChatTurnEventsService applies runner batches and tells the thread owner, and
// only the owner, what happened.
type ChatTurnEventsService struct {
	repo *postgres.ChatTurnEventsRepo
	pub  *ChatEventPublisher
	now  func() time.Time
	// limiter is the fleet-wide per-turn rate window; nil means in-process only.
	limiter ChatRateLimiter

	mu    sync.Mutex
	turns map[uuid.UUID]*chatTurnState
}

// chatTurnState is the per-turn serialisation point inside this process, plus
// the rate window. Nothing about a turn's stream survives a call: deltas are
// flushed before the call returns, so a later batch (or the final) handled by
// another replica can never be overtaken by a timer flush left behind here.
// Across replicas the order rests on the runner sending one batch at a time and
// waiting for each answer, which the protocol's cadence already requires.
type chatTurnState struct {
	mu sync.Mutex
	// rate window
	windowStart time.Time
	count       int
	lastUsed    time.Time
}

func NewChatTurnEventsService(repo *postgres.ChatTurnEventsRepo, pub *ChatEventPublisher) *ChatTurnEventsService {
	return &ChatTurnEventsService{repo: repo, pub: pub, now: time.Now, turns: map[uuid.UUID]*chatTurnState{}}
}

// WithRateLimiter makes the per-turn limit hold across API replicas.
func (s *ChatTurnEventsService) WithRateLimiter(l ChatRateLimiter) *ChatTurnEventsService {
	s.limiter = l
	return s
}

// admit applies the per-turn limit: the shared window when there is one, else
// (or when it cannot answer) this replica's own window.
func (s *ChatTurnEventsService) admit(ctx context.Context, ts *chatTurnState, turn uuid.UUID) bool {
	if s.limiter != nil {
		ok, err := s.limiter.Allow(ctx, turn, s.now())
		if err == nil {
			return ok
		}
		log.Printf("chat rate limiter: %v (falling back to the local window)", err)
	}
	return ts.allow(s.now())
}

func (s *ChatTurnEventsService) turn(id uuid.UUID) *chatTurnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// Forget turns nobody touched for an hour: a turn that never reached a
	// terminal event must not pin its stream forever.
	for k, v := range s.turns {
		if now.Sub(v.lastUsed) > time.Hour {
			delete(s.turns, k)
		}
	}
	t, ok := s.turns[id]
	if !ok {
		t = &chatTurnState{}
		s.turns[id] = t
	}
	t.lastUsed = now
	return t
}

func (s *ChatTurnEventsService) forget(id uuid.UUID) {
	s.mu.Lock()
	delete(s.turns, id)
	s.mu.Unlock()
}

// allow is the in-process fallback of the 20 batches per second per turn limit,
// used when no shared limiter is wired or the shared one is unreachable.
func (t *chatTurnState) allow(now time.Time) bool {
	if now.Sub(t.windowStart) >= time.Second {
		t.windowStart, t.count = now, 0
	}
	t.count++
	return t.count <= ChatEventsRatePerSecond
}

// ChatRateLimitedError carries Retry-After for the handler.
type ChatRateLimitedError struct{ RetryAfter time.Duration }

func (e *ChatRateLimitedError) Error() string { return "chat_rate_limited" }

// ApplyEvents commits the batch, then publishes in the strict order the owner's
// view needs: deltas before the final flush, the completed message after it.
func (s *ChatTurnEventsService) ApplyEvents(ctx context.Context, in postgres.ChatEventsInput) (*postgres.ChatEventsResult, error) {
	if len(in.Events) > postgres.ChatMaxBatchEvents {
		return nil, apierror.ChatRunner(http.StatusRequestEntityTooLarge, "chat_batch_too_large", "too many events in one batch",
			map[string]any{"max_events": postgres.ChatMaxBatchEvents})
	}
	ts := s.turn(in.TurnID)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	// Only the claim holder spends the turn's budget: a caller with the controller
	// credential but not the claim must not be able to starve the real runner. Its
	// batch still goes on to be refused by the repository.
	if s.repo.HoldsClaim(ctx, in.TurnID, in.RunnerID.String(), in.ClaimToken) && !s.admit(ctx, ts, in.TurnID) {
		return nil, &ChatRateLimitedError{RetryAfter: time.Second}
	}
	// Apply and publish under one cross-replica lock for the turn.
	unlock, err := s.repo.LockTurn(ctx, in.TurnID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	res, fx, err := s.repo.ApplyEvents(ctx, in)
	if err != nil || fx == nil {
		return res, err
	}
	s.publish(ctx, fx)
	if fx.Terminal != nil {
		s.forget(in.TurnID)
	}
	return res, nil
}

func chatMessagePayload(m *postgres.ChatMessageRow) ChatMessagePayload {
	return ChatMessagePayload{ID: m.ID, ThreadID: m.ThreadID, Role: m.Role, Body: m.Body, Status: m.Status,
		Truncated: m.Truncated, TurnID: m.TurnID, ErrorCode: m.ErrorCode, Attachments: []any{}, CreatedAt: m.CreatedAt}
}

func chatThreadRef(fx *postgres.ChatEventEffects) ChatThreadRef {
	return ChatThreadRef{WorkspaceID: fx.WorkspaceID, ProjectID: fx.ProjectID, ThreadID: fx.ThreadID, OwnerID: fx.OwnerID}
}

// publish never fails the call: the batch is committed and the owner's view is
// repaired by the final message, so a WS hiccup is logged, not returned.
func (s *ChatTurnEventsService) publish(ctx context.Context, fx *postgres.ChatEventEffects) {
	if s.pub == nil {
		return
	}
	ref := chatThreadRef(fx)
	if fx.Created != nil {
		if err := s.pub.MessageCreated(ctx, ref, chatMessagePayload(fx.Created)); err != nil {
			log.Printf("chat events: message created: %v", err)
		}
	}
	if len(fx.Deltas) > 0 && fx.Message != nil {
		stream := s.pub.NewDeltaStreamAt(ref, fx.Message.ID, fx.TurnID, fx.DeltaOffset)
		for _, d := range fx.Deltas {
			stream.Append(d)
		}
		if err := stream.Close(ctx); err != nil {
			log.Printf("chat events: deltas: %v", err)
		}
	}
	for _, tl := range fx.Tools {
		if err := s.pub.TurnTool(ctx, ref, fx.TurnID, tl.Kind, tl.Label); err != nil {
			log.Printf("chat events: tool: %v", err)
		}
	}
	if fx.Terminal == nil {
		return
	}
	// Strict order (final): the deltas above are already out; completed follows.
	if fx.Message != nil {
		if err := s.pub.MessageCompleted(ctx, ref, chatMessagePayload(fx.Message)); err != nil {
			log.Printf("chat events: message completed: %v", err)
		}
	}
	if err := s.pub.TurnState(ctx, ref, ChatTurnRef{ID: fx.TurnID, State: fx.Terminal.State}, fx.Terminal.State, fx.Terminal.ErrorCode); err != nil {
		log.Printf("chat events: turn state: %v", err)
	}
}

// publishOutcome announces a Mesh-side transition (expiry, Stop of a queued turn).
func (s *ChatTurnEventsService) publishOutcome(ctx context.Context, o postgres.ChatTurnOutcome) {
	if s.pub == nil || o.State == "" {
		return
	}
	ref := chatThreadRef(&o.ChatEventEffects)
	if o.UserMessage != nil {
		if err := s.pub.MessageCompleted(ctx, ref, chatMessagePayload(o.UserMessage)); err != nil {
			log.Printf("chat events: user message: %v", err)
		}
	}
	if err := s.pub.TurnState(ctx, ref, ChatTurnRef{ID: o.TurnID, State: o.State}, o.State, o.ErrorCode); err != nil {
		log.Printf("chat events: turn state: %v", err)
	}
}

// RequestCancel is the user's Stop; see ChatTurnEventsRepo.RequestCancel.
func (s *ChatTurnEventsService) RequestCancel(ctx context.Context, userID, project, thread uuid.UUID) (string, error) {
	o, err := s.repo.RequestCancel(ctx, userID, project, thread)
	if err != nil {
		return "", err
	}
	if o.State == postgres.ChatTurnCancelled {
		s.publishOutcome(ctx, *o)
	}
	return o.State, nil
}

// ExpireOnce is one T13 pass; it returns how many offline turns expired.
func (s *ChatTurnEventsService) ExpireOnce(ctx context.Context) int {
	out, err := s.repo.ExpireQueued(ctx, chatExpiryBatch)
	if err != nil {
		log.Printf("chat expiry: %v", err)
	}
	for _, o := range out {
		s.publishOutcome(ctx, o)
	}
	return len(out)
}

// RunExpiry sweeps expired offline turns every interval until ctx is done.
func (s *ChatTurnEventsService) RunExpiry(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := s.ExpireOnce(ctx); n > 0 {
				log.Printf("chat expiry: %d offline turn(s) expired, not delivered", n)
			}
		}
	}
}
