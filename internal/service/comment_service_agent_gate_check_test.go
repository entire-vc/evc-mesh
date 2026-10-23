package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	var r *GateArmRefusedError
	require.True(t, errors.As(err, &r), "expected a *GateArmRefusedError, got %v", err)
	return r.Code
}

// an internal ticket: set_human_gate with an agent key gets the same two checks as the marker.
func TestCheckAgentGateArm(t *testing.T) {
	ctx := context.Background()
	// Other tests freeze timeNow; the answer must be strictly newer than the ask.
	prev := timeNow
	tick := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { tick = tick.Add(time.Minute); return tick }
	t.Cleanup(func() { timeNow = prev })
	env, hgdRepo := setupHGDTriageEnv(t)
	taskID := env.seedTask(env.inProgressID)

	// No addressee, and an agent / unknown name as addressee — refused with a code.
	assert.Equal(t, GateRefusalAddresseeMissing, refusalCode(t, env.svc.CheckAgentGateArm(ctx, taskID, "", "", "")))
	assert.Equal(t, GateRefusalAddresseeNotUser, refusalCode(t, env.svc.CheckAgentGateArm(ctx, taskID, "@beth", "", "")))
	assert.Equal(t, GateRefusalAddresseeNotUser, refusalCode(t, env.svc.CheckAgentGateArm(ctx, taskID, "nobody-here", "", "")))

	// POSITIVE CONTROL: a human addressee on a thread with no earlier ask passes.
	require.NoError(t, env.svc.CheckAgentGateArm(ctx, taskID, "@Alex", "", ""))

	// Earlier ask, answered by a human: a new ask is a repeat.
	marker := &domain.Comment{TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @alex**: merge now?"}
	require.NoError(t, env.svc.Create(ctx, marker))
	answer := &domain.Comment{TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeUser,
		ParentCommentID: &marker.ID, Body: "merge it"}
	require.NoError(t, env.svc.Create(ctx, answer))

	err := env.svc.CheckAgentGateArm(ctx, taskID, "alex", "", "")
	assert.Equal(t, GateRefusalRepeatAsk, refusalCode(t, err))
	assert.Contains(t, err.Error(), "merge it", "the refusal quotes the answer")
	var notice bool
	for _, c := range env.systemComments() {
		if c.SystemCode != nil && *c.SystemCode == SysCodeRepeatAskRefused {
			notice = true
		}
	}
	assert.True(t, notice, "the thread records why nothing was armed")

	// Explicit supersedes is the opt-out, as on the marker path.
	require.NoError(t, env.svc.CheckAgentGateArm(ctx, taskID, "alex", "the answer does not cover rollback", ""))

	// A live recorded decision under the same canonical_key refuses the ask, as on the marker path.
	key := "merge-gateway"
	require.NoError(t, hgdRepo.Create(ctx, &domain.HumanGateDecision{
		TaskID: taskID, CanonicalKey: &key, DecidedBy: uuid.New(),
		Provenance: ptr(domain.HumanGateProvenanceDirect), Channel: ptr(domain.HumanGateChannelMesh),
	}))
	assert.Equal(t, GateRefusalRepeatAsk, refusalCode(t, env.svc.CheckAgentGateArm(ctx, taskID, "alex", "new subject", key)))
	// POSITIVE CONTROL: a different key is not blocked by that decision.
	require.NoError(t, env.svc.CheckAgentGateArm(ctx, taskID, "alex", "new subject", "other-key"))
}
