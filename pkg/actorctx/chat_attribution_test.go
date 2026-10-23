package actorctx

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnnotateChanges_NoAttributionIsUntouched(t *testing.T) {
	in := map[string]any{"status": "done"}
	assert.Equal(t, in, AnnotateChanges(context.Background(), in))
	raw := json.RawMessage(`{"a":1}`)
	assert.JSONEq(t, `{"a":1}`, string(AnnotateRawChanges(context.Background(), raw)))
}

func TestAnnotateChanges_AttributionWinsOverHandlerValues(t *testing.T) {
	user, thread, sess := uuid.New(), uuid.New(), uuid.New()
	ctx := WithChatAttribution(context.Background(), ChatAttribution{InitiatedByUserID: user, ChatThreadID: thread, ChatSessionID: sess, ExecutionEpoch: 4})

	// A handler-supplied value must not be able to shadow the verified one.
	out := AnnotateChanges(ctx, map[string]any{"initiated_by_user_id": "someone-else", "x": 1})
	assert.Equal(t, user, out["initiated_by_user_id"])
	assert.Equal(t, thread, out["chat_thread_id"])
	assert.Equal(t, sess, out["chat_session_id"])
	assert.EqualValues(t, 4, out["execution_epoch"])
	assert.Equal(t, 1, out["x"])

	raw := AnnotateRawChanges(ctx, json.RawMessage(`{"x":1}`))
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, user.String(), m["initiated_by_user_id"])

	// Not an object: returned as is, never rewritten.
	assert.JSONEq(t, `[1]`, string(AnnotateRawChanges(ctx, json.RawMessage(`[1]`))))
}

func TestChatAttributionFromContext_ZeroUserIsNotAnAttribution(t *testing.T) {
	ctx := WithChatAttribution(context.Background(), ChatAttribution{})
	_, ok := ChatAttributionFromContext(ctx)
	assert.False(t, ok)
}
