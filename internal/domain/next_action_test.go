package domain

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func naID(n byte) uuid.UUID { return uuid.UUID{15: n} }

func naEdge(n, from, to byte, c DependencyCondition) TaskDependency {
	return TaskDependency{ID: naID(n), DependsOnTaskID: naID(from), TaskID: naID(to), DependencyType: DependencyTypeBlocks, Condition: c}
}

var inProgress = BlockerState{Category: StatusCategoryInProgress}

// AC1: code/prep ready and final acceptance blocked at the same time.
func TestNextActionPrepReadyFinalBlocked(t *testing.T) {
	in := []NextActionEdge{{Edge: naEdge(1, 2, 3, DependencyConditionDeployed), Blocker: inProgress, BlockerName: "Developer One"}}
	p := BuildNextAction(naID(3), in, nil)
	require.Len(t, p.Stages, 2)
	assert.True(t, p.Stages[0].Ready, "prep must proceed")
	assert.False(t, p.Stages[1].Ready, "final acceptance must stay blocked")
	assert.Equal(t, "continue_prep", p.NextAction)

	// legacy task_done edge still gates everything
	in[0].Edge.Condition = DependencyConditionTaskDone
	p = BuildNextAction(naID(3), in, nil)
	assert.False(t, p.Stages[0].Ready)
	assert.Equal(t, "wait_requirement", p.NextAction)
	assert.Equal(t, "Developer One", p.NextActor)
}

// AC4: DONE with DO-NOT-SHIP or another SHA is not positive evidence.
func TestNextActionVerdictNotPositive(t *testing.T) {
	done := BlockerState{Category: StatusCategoryDone}
	mk := func(v *ReviewVerdict) NextActionProjection {
		return BuildNextAction(naID(3), []NextActionEdge{{Edge: naEdge(1, 2, 3, DependencyConditionAccepted), Blocker: done, BlockerHead: "aaa", Verdict: v}}, nil)
	}
	assert.True(t, mk(nil).Stages[1].Ready)
	assert.True(t, mk(&ReviewVerdict{"SHIP", "aaa"}).Stages[1].Ready)
	assert.False(t, mk(&ReviewVerdict{"DO-NOT-SHIP", "aaa"}).Stages[1].Ready)
	assert.False(t, mk(&ReviewVerdict{"SHIP", "bbb"}).Stages[1].Ready)
}

func TestNextActionGenerationAndWake(t *testing.T) {
	e := NextActionEdge{Edge: naEdge(1, 2, 3, DependencyConditionDeployed), Blocker: inProgress}
	g1 := BuildNextAction(naID(3), []NextActionEdge{e}, nil).Generation
	assert.Equal(t, g1, BuildNextAction(naID(3), []NextActionEdge{e}, nil).Generation, "stable")
	e.Blocker.HasDeployment = true
	g2 := BuildNextAction(naID(3), []NextActionEdge{e}, nil).Generation
	assert.NotEqual(t, g1, g2)
	assert.True(t, ShouldWake(g1, g2))
	// owner change alone changes generation (requirement owner/next_actor change)
	o := NextActionEdge{Edge: naEdge(1, 2, 3, DependencyConditionTaskDone), BlockerName: "A"}
	go1 := BuildNextAction(naID(3), []NextActionEdge{o}, nil).Generation
	o.BlockerName = "B"
	assert.NotEqual(t, go1, BuildNextAction(naID(3), []NextActionEdge{o}, nil).Generation)
	// stored verdict flipping (blocker reached the state) changes generation too
	f := NextActionEdge{Edge: naEdge(1, 2, 3, DependencyConditionTaskDone)}
	gf := BuildNextAction(naID(3), []NextActionEdge{f}, nil).Generation
	f.Edge.ConditionSatisfied = true
	assert.NotEqual(t, gf, BuildNextAction(naID(3), []NextActionEdge{f}, nil).Generation)
	assert.False(t, ShouldWake(g2, g2))
}

func TestGraphFindings(t *testing.T) {
	never := func(TaskDependency) bool { return false }
	owners := map[uuid.UUID]string{naID(1): "A", naID(2): "B"}
	// e0/release loop: only final-stage conditions -> semantic loop, not hard.
	loop := []TaskDependency{naEdge(1, 1, 2, DependencyConditionDeployed), naEdge(2, 2, 1, DependencyConditionAccepted)}
	f := GraphFindings(loop, never, owners)
	require.Len(t, f, 1)
	assert.Equal(t, FindingSemanticLoop, f[0].Kind)
	// a released edge is not part of any cycle
	assert.Empty(t, GraphFindings(loop, func(d TaskDependency) bool { return d.ID == naID(2) }, owners))
	// real loop through prep-gating conditions.
	hard := []TaskDependency{naEdge(1, 1, 2, DependencyConditionContractReady), naEdge(2, 2, 1, DependencyConditionTaskDone)}
	f = GraphFindings(hard, never, owners)
	require.Len(t, f, 1)
	assert.Equal(t, FindingHardCycle, f[0].Kind)
	assert.Len(t, f[0].Edges, 2, "edges reported, none removed")
	// the owner is the dependent side of the newest edge, not component order
	hard[0].CreatedAt = time.Unix(100, 0)
	hard[1].CreatedAt = time.Unix(200, 0) // edge 2: task 1 depends on... dependent = naID(1)
	assert.Equal(t, "A", GraphFindings(hard, never, owners)[0].Owner)
	hard[0].CreatedAt, hard[1].CreatedAt = time.Unix(300, 0), time.Unix(200, 0) // edge 1 newest: dependent = naID(2)
	assert.Equal(t, "B", GraphFindings(hard, never, owners)[0].Owner)
	// AND of two blockers + diamond (1 -> 2, 1 -> 3, 2 -> 4, 3 -> 4).
	d := []TaskDependency{naEdge(1, 1, 2, "task_done"), naEdge(2, 1, 3, "task_done"), naEdge(3, 2, 4, "task_done"), naEdge(4, 3, 4, "task_done")}
	kinds := map[string]int{}
	for _, x := range GraphFindings(d, never, owners) {
		kinds[x.Kind]++
	}
	assert.Equal(t, 1, kinds[FindingAndBlockers])
	assert.Equal(t, 1, kinds[FindingDiamond])
	assert.Zero(t, kinds[FindingHardCycle])
}

func TestEvaluateWait(t *testing.T) {
	owner, other := naID(1), naID(2)
	armed := time.Now()
	at := func(s int) time.Time { return armed.Add(time.Duration(s) * time.Second) }
	long := "Вариант A принят, реализую контракт и фикстуры сейчас."
	cases := []struct {
		name    string
		r       WaitReply
		resolve bool
	}{
		{"ack", WaitReply{ID: "c1", AuthorID: owner, Body: "ок", CreatedAt: at(1)}, false},
		{"quote only", WaitReply{ID: "c2", AuthorID: owner, Body: "> " + long, CreatedAt: at(1)}, false},
		{"stranger", WaitReply{ID: "c3", AuthorID: other, Body: long, CreatedAt: at(1)}, false},
		{"before arm", WaitReply{ID: "c4", AuthorID: owner, Body: long, CreatedAt: at(-5)}, false},
		{"fenced code only", WaitReply{ID: "c7", AuthorID: owner, Body: "```\n" + long + "\n```", CreatedAt: at(1)}, false},
		{"inline code only", WaitReply{ID: "c8", AuthorID: owner, Body: "`" + long + "`", CreatedAt: at(1)}, false},
		{"authorized", WaitReply{ID: "c5", AuthorID: owner, Body: long, CreatedAt: at(1)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.resolve, EvaluateWait(owner, armed, "", []WaitReply{c.r}).Resolved)
		})
	}
	for _, bad := range []WaitReply{
		{ID: "h1", AuthorID: other, Body: long, CreatedAt: at(1)},
		{ID: "h2", AuthorID: owner, Body: "ок", CreatedAt: at(1)},
		{ID: "h3", AuthorID: owner, Body: long, CreatedAt: at(-5)},
	} {
		assert.False(t, EvaluateWait(owner, armed, bad.ID, []WaitReply{bad}).Resolved, "handled id must not bypass qualification: "+bad.ID)
	}
	older := WaitReply{ID: "o1", AuthorID: owner, Body: long, CreatedAt: at(1)}
	newer := WaitReply{ID: "o2", AuthorID: owner, Body: long + " Итог: готово.", CreatedAt: at(2)}
	dn := EvaluateWait(owner, armed, "o1", []WaitReply{older, newer})
	assert.Equal(t, "o2", dn.ByComment, "handled older reply must not mask a newer one")
	assert.True(t, dn.OwnerOutcome)
	d := EvaluateWait(owner, armed, "", []WaitReply{{ID: "c5", AuthorID: owner, Body: long, CreatedAt: at(1)}})
	assert.True(t, d.OwnerOutcome, "no WAIT marker -> owner outcome")
	d = EvaluateWait(owner, armed, "", []WaitReply{{ID: "c6", AuthorID: owner, Body: long, CreatedAt: at(1), HasWaitMarker: true}})
	assert.False(t, d.OwnerOutcome)
}

func TestBindingProposalCAS(t *testing.T) {
	p := func() *BindingProposal {
		return &BindingProposal{ID: "p", EdgeID: naID(1), Old: DependencyConditionTaskDone, New: DependencyConditionContractReady,
			Sources: []string{"comment:x"}, Scope: "edge", Generation: "g1"}
	}
	_, err := p().Confirm(true, true, false, "g1")
	assert.ErrorIs(t, err, ErrAutomatedActor)
	_, err = p().Confirm(false, false, false, "g1")
	assert.ErrorIs(t, err, ErrNotOwner)
	_, err = p().Confirm(true, false, true, "g1")
	assert.ErrorIs(t, err, ErrHeldGate)
	_, err = p().Confirm(true, false, false, "g2")
	assert.ErrorIs(t, err, ErrStaleGeneration)
	q := p()
	c, err := q.Confirm(true, false, false, "g1")
	require.NoError(t, err)
	assert.Equal(t, DependencyConditionContractReady, c)
	_, err = q.Confirm(true, false, false, "g1")
	assert.ErrorIs(t, err, ErrReplay)
	assert.True(t, Revoked("g1", "g2"))
	noGen := p()
	noGen.Generation = ""
	_, err = noGen.Confirm(true, false, false, "")
	assert.Error(t, err, "empty generation must not pass CAS")
	bad := p()
	bad.Sources = nil
	assert.Error(t, bad.Validate())
}

// Published fixture for the rank consumer; regenerate with UPDATE_FIXTURES=1.
func TestNextActionFixtureV1(t *testing.T) {
	in := []NextActionEdge{
		{Edge: naEdge(1, 2, 3, DependencyConditionContractReady), Blocker: BlockerState{Category: StatusCategoryDone}, BlockerName: "Lead"},
		{Edge: naEdge(2, 4, 3, DependencyConditionDeployed), Blocker: inProgress, BlockerName: "Developer One"},
	}
	p := BuildNextAction(naID(3), in, []TaskDependency{naEdge(3, 3, 5, DependencyConditionAccepted)})
	got, err := json.MarshalIndent(p, "", "  ")
	require.NoError(t, err)
	const path = "testdata/next_action_fixture_v1.json"
	if os.Getenv("UPDATE_FIXTURES") != "" {
		require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(got))
	assert.True(t, p.Stages[0].Ready)
	assert.False(t, p.Stages[1].Ready)
}
