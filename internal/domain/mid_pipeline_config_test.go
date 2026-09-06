package domain

import (
	"encoding/json"
	"testing"
)

// The seam this test guards: the config is written as JSON by
// scripts/enable-mid-pipeline-guard.sql and read back through this struct. A
// mistyped json tag would leave every flag false, so both gates would stay off
// while the SQL, the service tests and the deploy all reported success — a
// feature that is inert and looks shipped. The literal below is copied from the
// SQL on purpose: if the two ever disagree, this fails.
func TestMidPipelineConfig_ParsesTheJSONTheEnableScriptWrites(t *testing.T) {
	const stored = `{"mid_pipeline":{"review_evidence_strict":true,"auto_park_stalled":true,"auto_park_due_hours":24}}`

	var cfg WorkflowRulesConfig
	if err := json.Unmarshal([]byte(stored), &cfg); err != nil {
		t.Fatalf("stored config does not parse: %v", err)
	}
	if cfg.MidPipeline == nil {
		t.Fatal("mid_pipeline block did not bind — the json tag on the field does not match what the enable script writes")
	}
	if !cfg.MidPipeline.ReviewStrict() {
		t.Error("review_evidence_strict did not bind; the gate would stay loose while the config claims otherwise")
	}
	if !cfg.MidPipeline.ParkStalled() {
		t.Error("auto_park_stalled did not bind; stalled cards would keep going back to todo")
	}
	if got := cfg.MidPipeline.AutoParkDue(); got != 24 {
		t.Errorf("auto_park_due_hours = %d, want 24", got)
	}
}

// A config written before this feature existed must still parse, and must read
// as all-off rather than erroring or defaulting to on.
func TestMidPipelineConfig_AbsentBlockIsAllOff(t *testing.T) {
	const legacy = `{"enforcement_mode":"advisory","transitions":{"todo":{"allowed":["done"]}}}`

	var cfg WorkflowRulesConfig
	if err := json.Unmarshal([]byte(legacy), &cfg); err != nil {
		t.Fatalf("a pre-existing config no longer parses: %v", err)
	}
	if cfg.MidPipeline != nil {
		t.Fatal("absent mid_pipeline block materialised out of nothing")
	}
	if cfg.MidPipeline.ReviewStrict() || cfg.MidPipeline.ParkStalled() {
		t.Error("a config with no mid_pipeline block reads as ON — every existing project would silently change behaviour")
	}
}

// Round-trip: what we marshal must be what the SQL stores, so a future writer
// that goes through Go rather than SQL produces the same shape.
func TestMidPipelineConfig_RoundTrip(t *testing.T) {
	in := WorkflowRulesConfig{MidPipeline: &MidPipelineConfig{
		ReviewEvidenceStrict: true,
		AutoParkStalled:      true,
		AutoParkDueHours:     24,
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out WorkflowRulesConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *out.MidPipeline != *in.MidPipeline {
		t.Errorf("round trip changed the config: %+v -> %+v", *in.MidPipeline, *out.MidPipeline)
	}
}
