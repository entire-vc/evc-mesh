package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// humanGateStub stands in for the comment service: it records the posted
// question and arms the gate the way the marker path would, unless told not to.
type humanGateStub struct {
	refuse    error
	createErr error
	getErr    error
	noArm     bool
	posted    []*domain.Comment
	tasks     map[uuid.UUID]*domain.Task
	checked   []string
}

func (g *humanGateStub) CheckAgentGateArm(_ context.Context, _ uuid.UUID, addressee, supersedes, key string) error {
	g.checked = append(g.checked, addressee+"|"+supersedes+"|"+key)
	return g.refuse
}

func (g *humanGateStub) Create(_ context.Context, c *domain.Comment) error {
	if g.createErr != nil {
		return g.createErr
	}
	g.posted = append(g.posted, c)
	if !g.noArm {
		author := c.AuthorID
		g.tasks[c.TaskID] = &domain.Task{ID: c.TaskID, HumanGate: true, GateAuthor: &author}
	}
	return nil
}

func (g *humanGateStub) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	if g.getErr != nil {
		return nil, g.getErr
	}
	if t, ok := g.tasks[id]; ok {
		return t, nil
	}
	return &domain.Task{ID: id}, nil
}

func newAskEnv() (*turnEnv, *humanGateStub) {
	e := newTurnEnv(nil)
	g := &humanGateStub{tasks: map[uuid.UUID]*domain.Task{}}
	e.svc.WithHumanGate(g, g)
	return e, g
}

func TestTurnCommandService_AskHuman(t *testing.T) {
	deadline := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	t.Run("posts the Blocking line and records the human wait on it", func(t *testing.T) {
		e, g := newAskEnv()
		res, err := e.svc.AskHuman(e.ctx, e.task, AskHumanInput{To: "@Alex", Question: "Ship the\n  migration today?",
			RecommendedDefault: "ship after the nightly backup", Deadline: &deadline, CanonicalKey: "ship-migration", Supersedes: "the rollout date"})
		require.NoError(t, err)
		assert.True(t, res.Released)
		require.Len(t, g.posted, 1)
		c := g.posted[0]
		assert.Equal(t, e.agent, c.AuthorID)
		assert.Equal(t, domain.ActorTypeAgent, c.AuthorType)
		assert.Equal(t, "❓ Blocking @alex: Ship the migration today?\n\nDefault: ship after the nightly backup\n"+
			"Deadline: "+deadline.Format(time.RFC3339)+"\nsupersedes: the rollout date", c.Body)
		assert.JSONEq(t, `{"command":"ask_human","canonical_key":"ship-migration","supersedes":"the rollout date"}`, string(c.Metadata))
		assert.Equal(t, []string{"alex|the rollout date|ship-migration"}, g.checked)

		require.Len(t, e.store.committed, 1)
		cmd := e.store.committed[0]
		assert.Equal(t, domain.TurnOutcomeWait, cmd.Kind)
		require.NotNil(t, cmd.CommentID)
		assert.Equal(t, c.ID, *cmd.CommentID, "the question is the turn's only comment")
		w := cmd.Wait
		assert.Equal(t, domain.TaskWaitHuman, w.Kind)
		assert.Equal(t, "comment:"+c.ID.String(), *w.HumanGateRef)
		assert.Equal(t, "alex", *w.Addressee)
		assert.Equal(t, "Ship the\n  migration today?", *w.Question)
		assert.Equal(t, "asked @alex", w.Note)
		assert.Equal(t, "@alex's answer", cmd.RuntimeReason)
		assert.Equal(t, []domain.TurnOutcomeKind{domain.TurnOutcomeWait}, e.hooks.after)
	})

	// The comment is read by the same helpers the marker path arms from, so a
	// typed ask and a hand-written line raise the same gate.
	t.Run("the marker path reads the generated comment", func(t *testing.T) {
		// The class is the server's verdict: a hard question stays hard
		// even with soft: true, a soft-looking one arms soft with or without it.
		cases := []struct {
			question, def string
			soft          bool
			want          domain.HumanGateClass
		}{
			{"Rotate the key now?", "rotate on Monday", false, domain.HumanGateClassHard},
			{"Rotate the key now?", "rotate on Monday", true, domain.HumanGateClassHard},
			{"Ship the plan as written?", "ship it on Monday", false, domain.HumanGateClassSoft},
			{"Ship the plan as written?", "ship it on Monday", true, domain.HumanGateClassSoft},
		}
		for _, tc := range cases {
			e, g := newAskEnv()
			_, err := e.svc.AskHuman(e.ctx, e.task, AskHumanInput{To: "alex", Question: tc.question,
				RecommendedDefault: tc.def, Deadline: &deadline, Supersedes: "the old ask", Soft: tc.soft})
			require.NoError(t, err)
			c := g.posted[0]
			assert.True(t, hasBlockingMarker(c.Body))
			assert.Equal(t, []string{"alex"}, blockingMarkerSlugs(c.Body))
			in := armInputFromMarker(c, &domain.Task{ID: e.task}, "alex")
			assert.True(t, strings.HasPrefix(in.Reason, tc.question), in.Reason)
			assert.Equal(t, tc.def, in.RecommendedDefault)
			require.NotNil(t, in.Deadline)
			assert.True(t, in.Deadline.Equal(deadline))
			assert.Equal(t, tc.want, in.Class, "%q soft=%v", tc.question, tc.soft)
			assert.True(t, citesSupersedes(c))
		}
	})

	refused := []struct {
		name   string
		in     AskHumanInput
		setup  func(e *turnEnv, g *humanGateStub)
		code   string
		status int
	}{
		{"no addressee", AskHumanInput{Question: "q?"}, nil, GateRefusalAddresseeMissing, 0},
		{"not a username", AskHumanInput{To: "Alex User", Question: "q?"}, nil, domain.CodeInvalidTarget, 0},
		{"no question", AskHumanInput{To: "alex", Question: "  "}, nil, "", 400},
		{"question too long", AskHumanInput{To: "alex", Question: string(make([]rune, 2001))}, nil, "", 400},
		{"soft tag smuggled in the question", AskHumanInput{To: "alex", Question: "ok [soft]?"}, nil, "", 400},
		{"deadline in the past", AskHumanInput{To: "alex", Question: "q?", Deadline: ptr(time.Now().Add(-time.Hour))}, nil, "", 400},
		{"multi-line default", AskHumanInput{To: "alex", Question: "q?", RecommendedDefault: "a\nb"}, nil, "", 400},
		{"checkout not held", AskHumanInput{To: "alex", Question: "q?"}, func(e *turnEnv, _ *humanGateStub) { e.store.notHeld = true }, "", 409},
		{"addressee is not a person", AskHumanInput{To: "developer", Question: "q?"}, func(_ *turnEnv, g *humanGateStub) {
			g.refuse = &GateArmRefusedError{Code: GateRefusalAddresseeNotUser, Message: "@developer is not a person"}
		}, GateRefusalAddresseeNotUser, 0},
		{"repeat ask", AskHumanInput{To: "alex", Question: "q?"}, func(_ *turnEnv, g *humanGateStub) {
			g.refuse = &GateArmRefusedError{Code: GateRefusalRepeatAsk, Message: "already answered"}
		}, GateRefusalRepeatAsk, 0},
		{"lookup fails", AskHumanInput{To: "alex", Question: "q?"}, func(_ *turnEnv, g *humanGateStub) { g.refuse = errors.New("db down") }, "", -1},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			e, g := newAskEnv()
			if tc.setup != nil {
				tc.setup(e, g)
			}
			_, err := e.svc.AskHuman(e.ctx, e.task, tc.in)
			require.Error(t, err)
			switch {
			case tc.code != "":
				assert.Equal(t, tc.code, outcomeCode(t, err))
			case tc.status > 0:
				assert.True(t, isAPI(tc.status, err), "want %d, got %v", tc.status, err)
			}
			assert.Empty(t, g.posted, "a refused ask posted the question")
			assert.Empty(t, e.store.committed)
		})
	}

	t.Run("gate not armed after the question", func(t *testing.T) {
		e, g := newAskEnv()
		g.noArm = true
		_, err := e.svc.AskHuman(e.ctx, e.task, AskHumanInput{To: "alex", Question: "q?"})
		assert.Equal(t, domain.CodeGateNotArmed, outcomeCode(t, err))
		assert.Len(t, g.posted, 1, "the posted question stays as the record")
		assert.Empty(t, e.store.committed, "no wait on a gate that is not armed")
	})

	t.Run("a person cannot end an agent's turn", func(t *testing.T) {
		e, _ := newAskEnv()
		_, err := e.svc.AskHuman(context.Background(), e.task, AskHumanInput{To: "alex", Question: "q?"})
		assert.True(t, isAPI(403, err))
	})

	t.Run("unavailable without the human gate", func(t *testing.T) {
		e := newTurnEnv(nil)
		_, err := e.svc.AskHuman(e.ctx, e.task, AskHumanInput{To: "alex", Question: "q?"})
		assert.True(t, isAPI(503, err))
	})

}

// Failures of the stores around the ask come back as they are, and none of
// them leaves a wait behind.
func TestTurnCommandService_AskHumanStoreFailures(t *testing.T) {
	boom := errors.New("db down")
	long := strings.Repeat("s", domain.MaxSessionIDLen+1)
	cases := []struct {
		name   string
		in     AskHumanInput
		setup  func(e *turnEnv, g *humanGateStub)
		want   error
		posted int
	}{
		{"session id too long", AskHumanInput{TurnCommon: TurnCommon{SessionID: &long}, To: "alex", Question: "q?"}, nil, nil, 0},
		{"card lookup fails", AskHumanInput{To: "alex", Question: "q?"}, func(e *turnEnv, _ *humanGateStub) { e.store.scopeErr = boom }, boom, 0},
		{"checkout read fails", AskHumanInput{To: "alex", Question: "q?"}, func(e *turnEnv, _ *humanGateStub) { e.store.holdErr = boom }, boom, 0},
		{"posting the question fails", AskHumanInput{To: "alex", Question: "q?"}, func(_ *turnEnv, g *humanGateStub) { g.createErr = boom }, boom, 0},
		{"reading the card back fails", AskHumanInput{To: "alex", Question: "q?"}, func(_ *turnEnv, g *humanGateStub) { g.getErr = boom }, boom, 1},
		{"the commit fails", AskHumanInput{To: "alex", Question: "q?"}, func(e *turnEnv, _ *humanGateStub) { e.store.commitErr = boom }, boom, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, g := newAskEnv()
			if tc.setup != nil {
				tc.setup(e, g)
			}
			_, err := e.svc.AskHuman(e.ctx, e.task, tc.in)
			require.Error(t, err)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			} else {
				assert.True(t, isAPI(400, err), "%v", err)
			}
			assert.Len(t, g.posted, tc.posted)
			assert.Empty(t, e.store.committed)
			assert.Empty(t, e.hooks.after)
		})
	}
}
