package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCapacityDeriveControllerFreshness(t *testing.T) {
	current := RuntimeAgentController{ControllerRef: "a", State: RuntimeControllerCurrent}
	paused := RuntimeAgentController{ControllerRef: "b", State: RuntimeControllerPaused}
	stale := RuntimeAgentController{ControllerRef: "c", State: RuntimeControllerStale}

	cases := []struct {
		name        string
		c           RuntimeAgentCapacity
		reason      string
		ready       int
		occupied    int
		unknownSeen int
	}{
		{"direct mode has no controller to wait for", RuntimeAgentCapacity{Configured: 2}, RuntimeCapacityAvailable, 2, 0, 0},
		{"one current controller is enough", RuntimeAgentCapacity{Configured: 2, Controllers: []RuntimeAgentController{paused, current}}, RuntimeCapacityAvailable, 2, 0, 0},
		{"paused only is never ready", RuntimeAgentCapacity{Configured: 2, Controllers: []RuntimeAgentController{paused}}, RuntimeCapacityControllerDown, 0, 0, 0},
		{"stale only is never ready", RuntimeAgentCapacity{Configured: 2, Controllers: []RuntimeAgentController{stale}}, RuntimeCapacityControllerDown, 0, 0, 0},
		{"held reservations stay occupied while down", RuntimeAgentCapacity{Configured: 3, Reserved: 1, Controllers: []RuntimeAgentController{stale}}, RuntimeCapacityControllerDown, 0, 1, 0},
		{"full beats controller state", RuntimeAgentCapacity{Configured: 1, Reserved: 1, Controllers: []RuntimeAgentController{stale}}, RuntimeCapacityAtCapacity, 0, 1, 0},
		{"lapsed lease is unknown and occupied", RuntimeAgentCapacity{Configured: 2, Reconcile: 1, Controllers: []RuntimeAgentController{current}}, RuntimeCapacityAvailable, 1, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.c.Derive()
			require.Equal(t, tc.reason, tc.c.Reason)
			require.Equal(t, tc.ready, tc.c.Ready)
			require.Equal(t, tc.occupied, tc.c.Occupied)
			require.Equal(t, tc.unknownSeen, tc.c.Unknown)
		})
	}
}

func TestRuntimeRenewInputValid(t *testing.T) {
	request := uuid.New()
	nilID := uuid.Nil
	ok := RuntimeRenewInput{Fence: 1, CheckoutRequestID: &request, CheckoutGeneration: 1, RunLeaseSeconds: 600}
	require.True(t, ok.Valid())
	noRequest := ok
	noRequest.CheckoutRequestID = nil
	require.True(t, noRequest.Valid(), "writers without a request id renew by generation")

	for name, mutate := range map[string]func(*RuntimeRenewInput){
		"zero fence":      func(in *RuntimeRenewInput) { in.Fence = 0 },
		"zero generation": func(in *RuntimeRenewInput) { in.CheckoutGeneration = 0 },
		"nil request id":  func(in *RuntimeRenewInput) { in.CheckoutRequestID = &nilID },
		"lease too short": func(in *RuntimeRenewInput) { in.RunLeaseSeconds = 59 },
		"lease too long":  func(in *RuntimeRenewInput) { in.RunLeaseSeconds = 86401 },
	} {
		in := ok
		mutate(&in)
		require.False(t, in.Valid(), name)
	}
}
