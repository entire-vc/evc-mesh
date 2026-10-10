package domain

import (
	"errors"
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
