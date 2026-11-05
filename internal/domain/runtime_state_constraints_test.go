package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sp(s string) *string { return &s }

func okConstraint(id string, class ConstraintClass) OperationalConstraint {
	return OperationalConstraint{
		ID: id, Class: class, ReasonCode: "x_" + string(class),
		NextActor:  ConstraintActor{Kind: ConstraintActorAgent, Name: sp("developer-one")},
		Wake:       ConstraintWake{Predicate: "pipeline", Ref: sp("g/p#1")},
		Source:     ConstraintSource{Kind: "runner"},
		ObservedAt: rsNow.Add(-time.Minute),
	}
}

// Semantic fixture: all six classes survive Normalize -> Marshal -> Resolve.
func TestRuntimeState_Constraints_SixClassesRoundTrip(t *testing.T) {
	classes := []ConstraintClass{ConstraintMeshDependency, ConstraintLocalWait, ConstraintCICapacity,
		ConstraintReviewRejection, ConstraintExpiredAuth, ConstraintWriteACL}
	s := &RuntimeState{Kind: RuntimeKindStopped}
	for _, c := range classes {
		s.Constraints = append(s.Constraints, okConstraint("id-"+string(c), c))
	}
	require.NoError(t, s.Normalize(rsNow))
	raw, err := s.Marshal()
	require.NoError(t, err)
	got := ResolveRuntimeState(raw, &Task{}, rsNow)
	require.NotNil(t, got)
	require.Len(t, got.Constraints, 6)
	for i, c := range classes {
		assert.Equal(t, c, got.Constraints[i].Class)
		assert.False(t, got.Constraints[i].Stale)
	}
}

// Legacy: a record with no constraints reads as before and serialises no field.
func TestRuntimeState_Constraints_LegacyPayloadUnchanged(t *testing.T) {
	got := ResolveRuntimeState([]byte(`{"kind":"waiting","since":"2026-10-10T11:59:00Z","updated_at":"2026-10-10T11:59:00Z","reason":"old"}`), &Task{}, rsNow)
	require.NotNil(t, got)
	assert.Nil(t, got.Constraints, "absent = legacy producer")
	out, _ := json.Marshal(got)
	assert.NotContains(t, string(out), "constraints")
}

// Unknown fields and codes in stored JSON are read safely.
func TestRuntimeState_Constraints_UnknownFieldAndCodeTolerated(t *testing.T) {
	raw := `{"kind":"stopped","since":"2026-10-10T11:59:00Z","updated_at":"2026-10-10T11:59:00Z","future":1,
	"constraints":[{"id":"a","class":"brand_new","reason_code":"never_seen","wake":{"predicate":"unknown"},"observed_at":"2026-10-10T11:59:00Z","extra":true}]}`
	got := ResolveRuntimeState([]byte(raw), &Task{}, rsNow)
	require.NotNil(t, got)
	require.Len(t, got.Constraints, 1)
	assert.Equal(t, ConstraintClass("brand_new"), got.Constraints[0].Class)
}

func TestRuntimeState_Constraints_NormalizeRejects(t *testing.T) {
	future := rsNow.Add(time.Hour)
	mut := func(f func(c *OperationalConstraint)) []OperationalConstraint {
		c := okConstraint("a", ConstraintWriteACL)
		f(&c)
		return []OperationalConstraint{c}
	}
	pos := 3
	for name, tc := range map[string]struct {
		cs    []OperationalConstraint
		field string
	}{
		"unknown class":      {mut(func(c *OperationalConstraint) { c.Class = "bogus" }), "constraints[0].class"},
		"no id":              {mut(func(c *OperationalConstraint) { c.ID = " " }), "constraints[0].id"},
		"no code":            {mut(func(c *OperationalConstraint) { c.ReasonCode = "" }), "constraints[0].reason_code"},
		"no predicate":       {mut(func(c *OperationalConstraint) { c.Wake.Predicate = "" }), "constraints[0].wake.predicate"},
		"no source":          {mut(func(c *OperationalConstraint) { c.Source.Kind = "" }), "constraints[0].source.kind"},
		"zero observed_at":   {mut(func(c *OperationalConstraint) { c.ObservedAt = time.Time{} }), "constraints[0].observed_at"},
		"future observed_at": {mut(func(c *OperationalConstraint) { c.ObservedAt = future }), "constraints[0].observed_at"},
		"bad actor":          {mut(func(c *OperationalConstraint) { c.NextActor.Kind = "bot" }), "constraints[0].next_actor.kind"},
		"long reason":        {mut(func(c *OperationalConstraint) { c.Reason = sp(strings.Repeat("x", 501)) }), "constraints[0].reason"},
		"bad until_at": {mut(func(c *OperationalConstraint) {
			c.Wake.Target = &ConstraintWakeTarget{UntilAt: sp("tomorrow")}
		}), "constraints[0].wake.target.until_at"},
		"ci on wrong class": {mut(func(c *OperationalConstraint) { c.CI = &ConstraintCI{State: ConstraintCIManual} }), "constraints[0].ci"},
		"ci bad state": {mut(func(c *OperationalConstraint) {
			c.Class = ConstraintCICapacity
			c.CI = &ConstraintCI{State: "busy"}
		}), "constraints[0].ci.state"},
		"ci position without provider": {mut(func(c *OperationalConstraint) {
			c.Class = ConstraintCICapacity
			c.CI = &ConstraintCI{State: ConstraintCICapacityState, Position: &pos}
		}), "constraints[0].ci"},
		"ci unavailable with scope": {mut(func(c *OperationalConstraint) {
			c.Class = ConstraintCICapacity
			c.CI = &ConstraintCI{State: ConstraintCIUnavailable, Provider: sp("gitlab"), Scope: sp("shared")}
		}), "constraints[0].ci"},
		"duplicate id": {[]OperationalConstraint{okConstraint("a", ConstraintLocalWait), okConstraint("a", ConstraintWriteACL)}, "constraints[1].id"},
		"too many": {func() []OperationalConstraint {
			var out []OperationalConstraint
			for i := 0; i < maxConstraints+1; i++ {
				out = append(out, okConstraint(string(rune('a'+i)), ConstraintLocalWait))
			}
			return out
		}(), "constraints"},
	} {
		t.Run(name, func(t *testing.T) {
			s := RuntimeState{Kind: RuntimeKindStopped, Constraints: tc.cs}
			err := s.Normalize(rsNow)
			var vErr *RuntimeStateValidationError
			require.True(t, errors.As(err, &vErr), "must be a validation error, got %v", err)
			assert.Equal(t, tc.field, vErr.Field)
		})
	}
}

// CI states stay distinct and provider evidence is kept; a lane position is not CI.
func TestRuntimeState_Constraints_CIStatesDistinct(t *testing.T) {
	pos := 4
	var states []string
	for _, ci := range []*ConstraintCI{
		{State: ConstraintCICapacityState, Provider: sp("gitlab"), Scope: sp("shared-runners"), Position: &pos},
		{State: ConstraintCIManual, Provider: sp("gitlab"), Reason: sp("manual job deploy")},
		{State: ConstraintCIUnavailable, Reason: sp("provider 503")},
	} {
		c := okConstraint("ci-"+ci.State, ConstraintCICapacity)
		c.CI = ci
		s := RuntimeState{Kind: RuntimeKindWaiting, Constraints: []OperationalConstraint{c}}
		require.NoError(t, s.Normalize(rsNow))
		states = append(states, s.Constraints[0].CI.State)
	}
	assert.Equal(t, []string{"capacity", "manual_required", "unavailable"}, states)
	// Producer sends lane position in reason_params only: ci.position stays unset.
	c := okConstraint("lane", ConstraintCICapacity)
	c.CI = &ConstraintCI{State: ConstraintCIUnavailable}
	c.ReasonParams = map[string]string{"lane_position": "2"}
	s := RuntimeState{Kind: RuntimeKindWaiting, Constraints: []OperationalConstraint{c}}
	require.NoError(t, s.Normalize(rsNow))
	assert.Nil(t, s.Constraints[0].CI.Position)
	assert.Nil(t, s.Constraints[0].CI.Scope)
}

// Freshness is per observation: a fresh updated_at does not refresh an old one.
func TestRuntimeState_Constraints_StalePerObservation(t *testing.T) {
	old := okConstraint("old", ConstraintCICapacity)
	old.ObservedAt = rsNow.Add(-time.Hour)
	fresh := okConstraint("fresh", ConstraintCICapacity)
	wait := okConstraint("wait", ConstraintLocalWait)
	wait.ObservedAt = rsNow.Add(-24 * time.Hour)
	expired := okConstraint("expired", ConstraintLocalWait)
	expired.Wake.Target = &ConstraintWakeTarget{UntilAt: sp(rsNow.Add(-time.Minute).Format(time.RFC3339))}
	s := RuntimeState{Kind: RuntimeKindStopped, Constraints: []OperationalConstraint{old, fresh, wait, expired}}
	require.NoError(t, s.Normalize(rsNow))
	raw, err := s.Marshal()
	require.NoError(t, err)
	got := ResolveRuntimeState(raw, &Task{}, rsNow)
	stale := map[string]bool{}
	for _, c := range got.Constraints {
		stale[c.ID] = c.Stale
	}
	assert.Equal(t, map[string]bool{"old": true, "fresh": false, "wait": false, "expired": true}, stale)
	assert.NotContains(t, string(raw), `"stale":true`, "stale is computed on read, never persisted as true")
}

func edge(cond DependencyCondition, typ DependencyType, satisfied bool) TaskDependency {
	return TaskDependency{ID: uuid.New(), DependsOnTaskID: uuid.New(), DependencyType: typ, Condition: cond, ConditionSatisfied: satisfied}
}

// Fulfilled blocks and relates_to are excluded; a blocker blocker is kept; and an
// unrelated local WAIT is returned next to it, not absorbed.
func TestRuntimeState_Constraints_DependencyUnionKeepsLocalWait(t *testing.T) {
	blocker := edge(DependencyConditionTaskDone, DependencyTypeBlocks, false)
	derived := DependencyConstraints([]DependencyEdgeView{
		{Edge: blocker, Visible: true},
		{Edge: edge(DependencyConditionTaskDone, DependencyTypeBlocks, true), Visible: true},
		{Edge: edge(DependencyConditionTaskDone, DependencyTypeRelatesTo, false), Visible: true},
	}, rsNow)
	require.Len(t, derived, 1, "negative control: only the live blocks edge counts")
	assert.Equal(t, "dep:"+blocker.ID.String(), derived[0].ID)
	assert.Equal(t, "card:"+blocker.DependsOnTaskID.String(), *derived[0].Wake.Ref)

	local := okConstraint("wait:local", ConstraintLocalWait)
	other := okConstraint("acl:1", ConstraintWriteACL)
	merged := MergeConstraints([]OperationalConstraint{local, other}, derived)
	var ids []string
	for _, c := range merged {
		ids = append(ids, c.ID)
	}
	assert.Equal(t, []string{"dep:" + blocker.ID.String(), "wait:local", "acl:1"}, ids)
}

// Dedup is by id: two constraints of one class survive; a clash is won by the server.
func TestRuntimeState_Constraints_MergeDedupByIDNotClass(t *testing.T) {
	a, b := okConstraint("a", ConstraintLocalWait), okConstraint("b", ConstraintLocalWait)
	assert.Len(t, MergeConstraints([]OperationalConstraint{a, b}, nil), 2)
	srv := okConstraint("a", ConstraintMeshDependency)
	m := MergeConstraints([]OperationalConstraint{a}, []OperationalConstraint{srv})
	require.Len(t, m, 1)
	assert.Equal(t, ConstraintMeshDependency, m[0].Class)
	assert.Nil(t, MergeConstraints(nil, nil))
}

// A blocker the caller cannot open stays visible as unknown, with no ref.
func TestRuntimeState_Constraints_HiddenDependencyRedacted(t *testing.T) {
	e := edge(DependencyConditionShipped, DependencyTypeBlocks, false)
	cs := DependencyConstraints([]DependencyEdgeView{{Edge: e, Visible: false}}, rsNow)
	require.Len(t, cs, 1)
	out, _ := json.Marshal(cs[0])
	assert.NotContains(t, string(out), e.DependsOnTaskID.String(), "closed blocker id must not leak")
	assert.Equal(t, ConstraintPredicateUnknown, cs[0].Wake.Predicate)
	assert.Equal(t, ConstraintActorUnknown, cs[0].NextActor.Kind)
	assert.Nil(t, cs[0].ReasonParams)
	assert.NotEmpty(t, cs[0].ReasonCode)
}
