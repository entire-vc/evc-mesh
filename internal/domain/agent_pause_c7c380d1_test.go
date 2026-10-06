package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFleetPauseOverridesPresence(t *testing.T) {
	now := time.Now()
	for _, caps := range []string{`["fleet-paused"]`, `{"fleet_paused":true}`} {
		a := Agent{Capabilities: json.RawMessage(caps), LastHeartbeat: &now}
		for _, sse := range []bool{true, false} {
			if got := a.ComputedStatus(sse); got != ComputedAgentStatus("paused") {
				t.Fatalf("paused capability %s, SSE=%t: got %s", caps, sse, got)
			}
		}
	}
	for _, caps := range []string{`[]`, `{"fleet_paused":false}`, `{"fleet_paused":"false"}`, `null`, `{"other":true}`, `invalid`} {
		a := Agent{Capabilities: json.RawMessage(caps), LastHeartbeat: &now}
		if got := a.ComputedStatus(true); got != ComputedStatusOnline {
			t.Fatalf("active control %s: got %s", caps, got)
		}
	}
}
