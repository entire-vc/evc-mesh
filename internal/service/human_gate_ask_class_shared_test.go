package service

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// askClassFixture is testdata/ask_class_cases.json: the question set evc-mesh-runner's
// tests/test_ask_routing.py runs too (byte-identical copy in tests/fixtures/), so the
// server and the Runner cannot drift apart on the class of the same question.
type askClassFixture struct {
	HardPatterns    []string `json:"hard_patterns"`
	HardLabels      []string `json:"hard_labels"`
	FallbackDefault string   `json:"fallback_default"`
	Cases           []struct {
		Name    string   `json:"name"`
		Body    string   `json:"body"`
		Slug    string   `json:"slug"`
		Labels  []string `json:"labels"`
		Default string   `json:"default"`
		Class   string   `json:"class"`
		Rule    string   `json:"rule"`
	} `json:"cases"`
}

func loadAskClassFixture(t *testing.T) askClassFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/ask_class_cases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f askClassFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	return f
}

// The rule lists are the fixture's: a pattern added here and not there (or in
// asks.py and not here) fails one of the two repositories' tests.
func TestSharedAskFixture_RuleListsMatch(t *testing.T) {
	f := loadAskClassFixture(t)
	if !slices.Equal(hardAskPatterns, f.HardPatterns) {
		t.Errorf("hardAskPatterns differs from the shared fixture (%d vs %d entries); update testdata/ask_class_cases.json and the Runner's copy",
			len(hardAskPatterns), len(f.HardPatterns))
	}
	if !slices.Equal(hardAskLabels, f.HardLabels) {
		t.Errorf("hardAskLabels = %v, fixture %v", hardAskLabels, f.HardLabels)
	}
	if domain.DefaultMarkerRecommendedDefault != f.FallbackDefault {
		t.Errorf("fallback default = %q, fixture %q", domain.DefaultMarkerRecommendedDefault, f.FallbackDefault)
	}
}

func TestSharedAskFixture_Cases(t *testing.T) {
	f := loadAskClassFixture(t)
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := markerDefault(tc.Body, tc.Slug); got != tc.Default {
				t.Errorf("default = %q, want %q", got, tc.Default)
			}
			class, rule := markerGateClass(tc.Body, tc.Slug, tc.Labels)
			if string(class) != tc.Class || rule != tc.Rule {
				t.Errorf("markerGateClass = (%s, %q), want (%s, %q)", class, rule, tc.Class, tc.Rule)
			}
		})
	}
}
