package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #4545660b. enforceBlockingTriage is now a CALLER of the one arming
// implementation, not a second implementation of it. These tests assert the thing that
// makes that true: the input it builds carries a gate_author taken from the comment's
// AUTHENTICATED author, plus whatever the marker itself stated.
//
// Why authorship matters more than it looks: everything the 21 text-grepping clients
// read was a CLAIM in a body anyone could type. AuthorID/AuthorType come from the
// identity on the request (comment_handler.go derives them, never the body), so this is
// the first version of "who is waiting" that cannot be forged by writing prose.

func TestArmInputFromMarker_TakesAuthorFromCommentIdentity(t *testing.T) {
	author := uuid.New()
	taskID := uuid.New()
	comment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     taskID,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		// The body NAMES a different actor. A text-grepping reader would have had to
		// choose whom to believe; identity-based attribution has nothing to choose.
		Body: "Отчёт готов, cc @bill.\n\n❓ **Blocking @pavel**: мёржим сейчас или ждём?",
	}
	task := &domain.Task{ID: taskID}

	in := armInputFromMarker(comment, task, "pavel")

	assert.Equal(t, taskID, in.TaskID)
	assert.Equal(t, author, in.Author, "gate_author is the comment's authenticated author")
	assert.Equal(t, domain.ActorTypeAgent, in.AuthorType)
	assert.Equal(t, domain.ArmHumanGateSourceMarker, in.Source)
	assert.Contains(t, in.Reason, "мёржим сейчас или ждём",
		"the ask itself is carried on the task, so no reader has to reopen the thread")
	assert.NotContains(t, in.Reason, "Отчёт готов",
		"the reason is the ASK, not the whole comment")
	assert.Equal(t, domain.HumanGateClassHard, in.Class, "no [soft] tag → hard, fail-closed")
}

func TestArmInputFromMarker_ExtractsStatedDefaultAndDeadline(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantDefault string
		wantNoDate  bool
	}{
		{
			name: "russian",
			body: "❓ **Blocking @pavel**: мёржим?\n\n" +
				"По умолчанию: мёржу — шлюз неактивен, списать никому нельзя.\n" +
				"Дедлайн: 2026-09-09T12:00:00Z",
			wantDefault: "мёржу — шлюз неактивен, списать никому нельзя.",
		},
		{
			name: "english",
			body: "❓ **Blocking @pavel**: ship it?\n\n" +
				"**Recommended default:** ship; the flag is off in prod\n" +
				"Deadline: 2026-09-09",
			wantDefault: "ship; the flag is off in prod",
		},
		{
			name:        "no default stated at all",
			body:        "❓ **Blocking @pavel**: какой шлюз выбираем?",
			wantDefault: "",
			wantNoDate:  true,
		},
		{
			// NEGATIVE CONTROL. A post-mortem explaining the convention must not have
			// its own example harvested as a real default — the exact shape that made
			// phantom gates possible (#84ab54fd: a probe that emits the token it probes
			// for is a latch, not a gate).
			name: "quoted example is not a real default",
			body: "❓ **Blocking @pavel**: вопрос по существу.\n\n" +
				"Как писать дефолт, для справки:\n" +
				"```\nПо умолчанию: тут ваш вариант\n```",
			wantDefault: "",
			wantNoDate:  true,
		},
		{
			// A prose deadline is deliberately NOT parsed: guessing a timestamp would
			// put a wrong date on the field the timeout sweep acts on, and a wrong
			// deadline is worse than no deadline.
			name:        "prose deadline is not harvested",
			body:        "❓ **Blocking @pavel**: ok?\n\nDefault: proceed\nDeadline: к пятнице",
			wantDefault: "proceed",
			wantNoDate:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantDefault, extractRecommendedDefault(tc.body))
			got := extractGateDeadline(tc.body)
			if tc.wantNoDate {
				assert.Nil(t, got, "an unparseable or absent deadline is nil, never a guess")
			} else {
				require.NotNil(t, got)
				assert.Equal(t, 2026, got.Year())
				assert.Equal(t, time.September, got.Month())
				assert.Equal(t, 9, got.Day())
			}
		})
	}
}

// TestEnforceBlockingTriage_ArmsThroughTheSinglePath is the end-to-end half: a real
// "❓ Blocking @pavel" comment must reach ArmHumanGate — one call carrying the whole ask —
// rather than the old SetHumanGate + SetHumanGateClass pair that recorded no author.
func TestEnforceBlockingTriage_ArmsThroughTheSinglePath(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedTask(env.inProgressID)
	author := uuid.New()

	comment := &domain.Comment{
		TaskID:     taskID,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		Body: "Отчёт готов.\n\n❓ **Blocking @pavel**: мёржим сейчас или ждём?\n\n" +
			"По умолчанию: жду ответа до дедлайна.\nДедлайн: 2026-09-09T12:00:00Z",
	}
	require.NoError(t, env.svc.Create(context.Background(), comment))

	arms := env.taskMover.humanGateArmCalls()
	require.Len(t, arms, 1, "exactly one arming call — not a pair of partial writes")
	assert.Equal(t, taskID, arms[0].TaskID)
	assert.Equal(t, author, arms[0].Author, "gate_author is recorded, not left empty")
	assert.Equal(t, domain.ActorTypeAgent, arms[0].AuthorType)
	assert.Equal(t, domain.ArmHumanGateSourceMarker, arms[0].Source)
	assert.Contains(t, arms[0].Reason, "мёржим сейчас")
	assert.Contains(t, arms[0].RecommendedDefault, "жду ответа")
	require.NotNil(t, arms[0].Deadline)
	assert.Equal(t, 2026, arms[0].Deadline.Year())

	// NEGATIVE CONTROL: the same harness on a comment with NO marker must arm nothing.
	// Without it, a fake that recorded an arm unconditionally would pass everything above.
	env2 := setupTriageEnv(t, true)
	taskID2 := env2.seedTask(env2.inProgressID)
	require.NoError(t, env2.svc.Create(context.Background(), &domain.Comment{
		TaskID:     taskID2,
		AuthorID:   author,
		AuthorType: domain.ActorTypeAgent,
		Body:       "Отчёт готов. По умолчанию: жду ответа до дедлайна.",
	}))
	assert.Empty(t, env2.taskMover.humanGateArmCalls(),
		"no Blocking marker → no arm, however much gate-shaped prose the body carries")
}
