package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// End-to-end proof (task #bb1aaa09, tail of #e8388213) that a "❓ Blocking @pavel"
// marker posted on a secret-leak/rotation card does NOT arm human_gate — closing the
// gap the agent-lane PreToolUse hook (secret-rotation-gate-guard.py) cannot cover: a
// marker posted through the Mesh web UI has no PreToolUse to pass through at all.

// TestEnforceBlockingTriage_SecretLeakCard_DoesNotArm is AC #1: a comment carrying the
// marker on a `secret-leak`-labelled card must still be CREATED (the ask itself is
// never lost), but must arm nothing.
func TestEnforceBlockingTriage_SecretLeakCard_DoesNotArm(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	env.taskRepo.items[taskID].Labels = []string{"secret-leak"}
	author := uuid.New()

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: ротировать сейчас?",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	// The original comment was persisted — the ask is not lost, only unarmed.
	persisted, ok := env.commentRepo.items[comment.ID]
	require.True(t, ok, "the marker comment itself must still be created")
	assert.Contains(t, persisted.Body, "ротировать сейчас")

	assert.Empty(t, env.taskMover.humanGateArmCalls(),
		"a secret-leak/rotation card must not arm human_gate, however the marker is worded")
	assert.Empty(t, env.taskMover.calls(),
		"must not occupy the triage column either — Pavel already decided this class")

	sys := env.systemComments()
	require.Len(t, sys, 1, "an explanation must replace the arm, not vanish silently")
	assert.Contains(t, sys[0].Body, "НЕ взведён")
	assert.Contains(t, sys[0].Body, "e8388213")
	assert.Contains(t, sys[0].Body, secretRotationNotSecretRotationLabel,
		"the one-label undo must be discoverable in the refusal itself")
}

// TestEnforceBlockingTriage_OrdinaryCard_StillArms is AC #2, the positive control:
// without it, a version of enforceBlockingTriage that suppressed EVERY marker would
// pass AC #1 for the wrong reason.
func TestEnforceBlockingTriage_OrdinaryCard_StillArms(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	author := uuid.New()

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: какой хост для деплоя?",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	arms := env.taskMover.humanGateArmCalls()
	require.Len(t, arms, 1, "an ordinary card's marker must arm exactly once")
	assert.Equal(t, taskID, arms[0].TaskID)
	assert.Equal(t, author, arms[0].Author)
}

// TestEnforceBlockingTriage_SeverityCriticalException_StillArms is AC #3: confirmed
// ACTIVE exploitation is an incident, not a rotation ask — Pavel's decision defers
// rotating a credential that merely leaked, not one someone is actively using, and
// `severity:critical` is the machine-readable escape hatch (EXCLUDE_LABELS in
// fleet_secret_rotation.py, mirrored in secretRotationExcludeLabels).
func TestEnforceBlockingTriage_SeverityCriticalException_StillArms(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	env.taskRepo.items[taskID].Labels = []string{"secret-leak", "severity:critical"}
	author := uuid.New()

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: подтверждённая эксплуатация, ротировать немедленно?",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	arms := env.taskMover.humanGateArmCalls()
	require.Len(t, arms, 1, "severity:critical is the active-exploitation escape hatch — it must still arm")
	assert.Equal(t, taskID, arms[0].TaskID)
}

// TestEnforceBlockingTriage_SecretLeakCard_OptOutLabelStillArms proves the one-label
// undo actually works end to end, not just inside the predicate unit tests: a false
// positive must be reversible by the person who spots it, without touching code.
func TestEnforceBlockingTriage_SecretLeakCard_OptOutLabelStillArms(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	env.taskRepo.items[taskID].Labels = []string{"secret-leak", secretRotationNotSecretRotationLabel}
	author := uuid.New()

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		Body:       "❓ **Blocking @pavel**: это не ротация, реальный вопрос",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	arms := env.taskMover.humanGateArmCalls()
	require.Len(t, arms, 1, "the not-secret-rotation label must undo the suppression")
	assert.Equal(t, taskID, arms[0].TaskID)
}
