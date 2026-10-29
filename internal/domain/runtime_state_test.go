package domain

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var rsNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestRuntimeState_Normalize_AllKinds(t *testing.T) {
	for _, k := range []RuntimeKind{RuntimeKindWorking, RuntimeKindWaiting, RuntimeKindQueued, RuntimeKindStopped} {
		s := &RuntimeState{Kind: k}
		require.NoError(t, s.Normalize(rsNow), k)
		assert.Equal(t, rsNow, s.Since, "since defaults to now")
		assert.Equal(t, rsNow, s.UpdatedAt)
	}
}

func TestRuntimeState_Normalize_Rejects(t *testing.T) {
	before := rsNow.Add(-time.Hour)
	long := string(make([]byte, 501))
	for name, tc := range map[string]struct {
		s     RuntimeState
		field string
	}{
		"unknown kind": {RuntimeState{Kind: "sleeping"}, "kind"},
		"empty kind":   {RuntimeState{}, "kind"},
		"until<since":  {RuntimeState{Kind: RuntimeKindWaiting, Since: rsNow, Until: &before}, "until"},
		"long ref":     {RuntimeState{Kind: RuntimeKindWaiting, Ref: &long}, "ref"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.s.Normalize(rsNow)
			var vErr *RuntimeStateValidationError
			require.True(t, errors.As(err, &vErr), "got %v", err)
			assert.Equal(t, tc.field, vErr.Field)
		})
	}
}

func TestRuntimeState_Marshal_DropsReadTimeFields(t *testing.T) {
	s := &RuntimeState{Kind: RuntimeKindQueued, Source: RuntimeStateSourceCheckout, Stale: true}
	require.NoError(t, s.Normalize(rsNow))
	raw, err := s.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "source")
	assert.NotContains(t, string(raw), "stale")
}

func storedRaw(t *testing.T, kind RuntimeKind, updated time.Time, until *time.Time) []byte {
	t.Helper()
	s := &RuntimeState{Kind: kind, Since: updated, Until: until}
	require.NoError(t, s.Normalize(updated))
	raw, err := s.Marshal()
	require.NoError(t, err)
	return raw
}

func TestResolveRuntimeState_StoredWinsAndRoundTrips(t *testing.T) {
	holder := uuid.New()
	task := &Task{CheckedOutBy: &holder}
	for _, k := range []RuntimeKind{RuntimeKindWorking, RuntimeKindWaiting, RuntimeKindQueued, RuntimeKindStopped} {
		got := ResolveRuntimeState(storedRaw(t, k, rsNow.Add(-time.Minute), nil), task, rsNow)
		require.NotNil(t, got, k)
		assert.Equal(t, k, got.Kind)
		assert.Equal(t, RuntimeStateSourceExplicit, got.Source, "stored beats the checkout fallback")
		assert.False(t, got.Stale)
	}
}

func TestResolveRuntimeState_Stale(t *testing.T) {
	task := &Task{}
	fresh := ResolveRuntimeState(storedRaw(t, RuntimeKindWorking, rsNow.Add(-RuntimeStateStaleAfter+time.Second), nil), task, rsNow)
	assert.False(t, fresh.Stale)
	old := ResolveRuntimeState(storedRaw(t, RuntimeKindWorking, rsNow.Add(-RuntimeStateStaleAfter-time.Second), nil), task, rsNow)
	assert.True(t, old.Stale, "not refreshed for longer than the window")
	assert.Equal(t, RuntimeKindWorking, old.Kind, "stale is a flag, the value stays")
	past := rsNow.Add(-time.Second)
	expired := ResolveRuntimeState(storedRaw(t, RuntimeKindWaiting, rsNow.Add(-time.Minute), &past), task, rsNow)
	assert.True(t, expired.Stale, "until has passed")
}

// A state written from a declared wait is not refreshed by a runner; its age
// alone must not mark it stale, only its until.
func TestResolveRuntimeState_WaitStateAgesOnlyByUntil(t *testing.T) {
	ref := WaitRuntimeRefPrefix + uuid.NewString()
	until := rsNow.Add(time.Hour)
	s := RuntimeState{Kind: RuntimeKindWaiting, Since: rsNow.Add(-2 * time.Hour), Until: &until, Ref: &ref, UpdatedAt: rsNow.Add(-2 * time.Hour)}
	raw, err := s.Marshal()
	require.NoError(t, err)
	assert.False(t, ResolveRuntimeState(raw, &Task{}, rsNow).Stale, "an old wait state with until ahead is current")
	assert.True(t, ResolveRuntimeState(raw, &Task{}, until.Add(time.Second)).Stale, "until has passed")

	other := "pipeline:g/p#1"
	s.Ref = &other
	raw, err = s.Marshal()
	require.NoError(t, err)
	assert.True(t, ResolveRuntimeState(raw, &Task{}, rsNow).Stale, "a runner-written state still ages")
}

func TestResolveRuntimeState_CheckoutFallback(t *testing.T) {
	holder := uuid.New()
	acquired := rsNow.Add(-10 * time.Minute)
	live := rsNow.Add(time.Hour)
	// todo status is irrelevant to the resolver: a live checkout alone is enough.
	task := &Task{CheckedOutBy: &holder, CheckoutAcquiredAt: &acquired, CheckoutExpires: &live}
	got := ResolveRuntimeState(nil, task, rsNow)
	require.NotNil(t, got)
	assert.Equal(t, RuntimeKindWorking, got.Kind)
	assert.Equal(t, RuntimeStateSourceCheckout, got.Source)
	assert.Equal(t, acquired, got.Since)
	require.NotNil(t, got.Lane)
	assert.Equal(t, holder.String(), *got.Lane)
	assert.False(t, got.Stale)

	dead := rsNow.Add(-time.Second)
	task.CheckoutExpires = &dead
	assert.Nil(t, ResolveRuntimeState(nil, task, rsNow), "expired checkout is not working")
	assert.Nil(t, ResolveRuntimeState(nil, &Task{}, rsNow), "no checkout, nothing stored")
	assert.Nil(t, ResolveRuntimeState([]byte("null"), &Task{}, rsNow), "JSON null column")
}

func TestResolveRuntimeState_CorruptStoredFallsBack(t *testing.T) {
	assert.Nil(t, ResolveRuntimeState([]byte(`{"kind":"bogus"}`), &Task{}, rsNow))
	assert.Nil(t, ResolveRuntimeState([]byte(`not json`), &Task{}, rsNow))
}

func TestRuntimeState_ReasonCode(t *testing.T) {
	code := "review_cooldown"
	s := &RuntimeState{Kind: RuntimeKindStopped, ReasonCode: &code, ReasonParams: map[string]string{"minutes": "4"}}
	require.NoError(t, s.Normalize(rsNow))
	raw, err := s.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"reason_code":"review_cooldown"`)
	assert.Contains(t, string(raw), `"reason_params":{"minutes":"4"}`)

	got := ResolveRuntimeState(raw, &Task{}, rsNow)
	require.NotNil(t, got)
	assert.Equal(t, "review_cooldown", *got.ReasonCode)
	assert.Equal(t, "4", got.ReasonParams["minutes"])
}

func TestRuntimeState_ReasonCode_Rejects(t *testing.T) {
	unknown, blank := "made_up", "  "
	for name, tc := range map[string]struct {
		s     RuntimeState
		field string
	}{
		"unknown code":         {RuntimeState{Kind: RuntimeKindStopped, ReasonCode: &unknown}, "reason_code"},
		"params without code":  {RuntimeState{Kind: RuntimeKindStopped, ReasonParams: map[string]string{"a": "b"}}, "reason_params"},
		"blank code+params":    {RuntimeState{Kind: RuntimeKindStopped, ReasonCode: &blank, ReasonParams: map[string]string{"a": "b"}}, "reason_params"},
		"empty params no code": {RuntimeState{Kind: RuntimeKindStopped, ReasonParams: map[string]string{}}, "reason_params"},
		"long param":           {RuntimeState{Kind: RuntimeKindStopped, ReasonCode: &[]string{"pause"}[0], ReasonParams: map[string]string{"a": string(make([]byte, 101))}}, "reason_params"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.s.Normalize(rsNow)
			var vErr *RuntimeStateValidationError
			require.True(t, errors.As(err, &vErr), "got %v", err)
			assert.Equal(t, tc.field, vErr.Field)
		})
	}
}

func TestRuntimeState_NoReasonCode_BackwardCompatible(t *testing.T) {
	reason := "ci"
	s := &RuntimeState{Kind: RuntimeKindWaiting, Reason: &reason}
	require.NoError(t, s.Normalize(rsNow))
	raw, err := s.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "reason_code")
	assert.NotContains(t, string(raw), "reason_params")
}

// Every code the API accepts must have a translation in both web locales,
// otherwise the badge silently falls back to a free-text reason that may be absent.
func TestRuntimeReasonCodes_HaveWebTranslations(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		raw, err := os.ReadFile("../../web/src/locales/" + lang + "/board.json")
		require.NoError(t, err)
		var board struct {
			RuntimeState struct {
				Reasons map[string]string `json:"reasons"`
			} `json:"runtimeState"`
		}
		require.NoError(t, json.Unmarshal(raw, &board))
		require.NotEmpty(t, board.RuntimeState.Reasons)
		for code := range validReasonCodes {
			assert.Contains(t, board.RuntimeState.Reasons, string(code), "%s locale lacks reason code", lang)
		}
		assert.Len(t, board.RuntimeState.Reasons, len(validReasonCodes), "%s locale has keys outside the closed list", lang)
	}
}

func TestValidRuntimeReasonCode_RunnerCodes(t *testing.T) {
	for _, c := range []RuntimeReasonCode{ReasonBlockerInBacklog, ReasonDependenciesUnreadable} {
		if !ValidRuntimeReasonCode(c) {
			t.Fatalf("%s must be accepted", c)
		}
	}
	if ValidRuntimeReasonCode("no_such_code") {
		t.Fatal("unknown code accepted")
	}
}
