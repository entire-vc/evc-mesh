package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DB-backed, no build tag: skips without DATABASE_URL like the other chat runner
// tests here, so the default job and its diff-coverage gate run it.

// attach inserts an attachment of msg in the workspace; uploaded=false leaves it pending.
func (f *chatDeleteFixture) attach(t *testing.T, ws, msg uuid.UUID, name string, uploaded bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(`INSERT INTO ext_agent_chat_attachments (id, workspace_id, thread_id, message_id, s3_key, file_name, mime, size, uploaded_at)
		SELECT $1, $2, thread_id, $3, $4, $5, 'image/png', 42, CASE WHEN $6::boolean THEN now() END
		  FROM ext_agent_chat_messages WHERE id = $3`, id, ws, msg, "chat/"+ws.String()+"/"+id.String(), name, uploaded)
	require.NoError(t, err)
	return id
}

func TestChatRunnerHeldTurnAttachment_OnlyTheHolderOfALiveTurnGetsItsOwnFiles(t *testing.T) {
	f := newChatDeleteFixture(t)
	repo := NewChatRunnerRepo(f.db)
	ctx := context.Background()
	runner := uuid.New()
	agents := []uuid.UUID{f.agentA}

	turn, msg := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	att := f.attach(t, f.wsA, msg, "photo.png", true)
	pending := f.attach(t, f.wsA, msg, "pending.png", false)
	_, otherMsg := f.queueTurn(t, f.wsA, f.projA, f.agentA, f.userA)
	foreign := f.attach(t, f.wsA, otherMsg, "other.png", true)

	turnNotFound := func(t *testing.T, err error, why string) {
		t.Helper()
		pe := protoErr(t, err)
		assert.Equal(t, 404, pe.Code, why)
		assert.Equal(t, ChatErrTurnNotFound, pe.ErrorCode, why)
	}
	attNotFound := func(t *testing.T, err error, why string) {
		t.Helper()
		pe := protoErr(t, err)
		assert.Equal(t, 404, pe.Code, why)
		assert.Equal(t, "attachment_not_found", pe.Details, why)
	}

	_, err := repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, turn, att)
	turnNotFound(t, err, "a queued turn is held by nobody")

	claimed, err := repo.ClaimTurns(ctx, ChatClaimParams{WorkspaceID: f.wsA, AgentIDs: agents, RunnerID: runner, Max: 1,
		LeaseSeconds: ChatLeaseSeconds, MaxAttempts: ChatMaxAttempts})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, turn, claimed[0].TurnID)

	t.Run("the holder gets the attachment, claimed and streaming", func(t *testing.T) {
		for _, state := range []string{"claimed", "streaming"} {
			_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET state = $2 WHERE id = $1`, turn, state)
			require.NoError(t, err)
			got, gerr := repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, turn, att)
			require.NoError(t, gerr, state)
			assert.Equal(t, &ChatTurnAttachment{ID: att, WorkspaceID: f.wsA, FileName: "photo.png", Mime: "image/png", Size: 42}, got, state)
		}
	})

	t.Run("anyone but the holder is told the turn does not exist", func(t *testing.T) {
		_, err = repo.HeldTurnAttachment(ctx, f.wsA, agents, uuid.New(), turn, att)
		turnNotFound(t, err, "another runner id")
		_, err = repo.HeldTurnAttachment(ctx, f.wsA, []uuid.UUID{f.agentB}, runner, turn, att)
		turnNotFound(t, err, "another agent")
		_, err = repo.HeldTurnAttachment(ctx, f.wsB, agents, runner, turn, att)
		turnNotFound(t, err, "another workspace")
		_, err = repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, uuid.New(), att)
		turnNotFound(t, err, "an unknown turn")
	})

	t.Run("only an uploaded file of the turn's own message", func(t *testing.T) {
		_, err = repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, turn, foreign)
		attNotFound(t, err, "an attachment of another message")
		_, err = repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, turn, pending)
		attNotFound(t, err, "an attachment that was never uploaded")
		_, err = repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, turn, uuid.New())
		attNotFound(t, err, "an unknown attachment")
	})

	t.Run("a released or finished turn is held by nobody", func(t *testing.T) {
		for _, state := range []string{"queued", "done", "failed", "cancelled", "expired"} {
			_, err = f.db.Exec(`UPDATE ext_agent_chat_turns SET state = $2 WHERE id = $1`, turn, state)
			require.NoError(t, err, state)
			_, err = repo.HeldTurnAttachment(ctx, f.wsA, agents, runner, turn, att)
			turnNotFound(t, err, state)
		}
	})
}
