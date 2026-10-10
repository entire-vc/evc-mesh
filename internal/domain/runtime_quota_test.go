package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

func validQuotaObservation(now time.Time) RuntimeQuotaObservation {
	return RuntimeQuotaObservation{Pool: "shared-window", Provider: "provider-a", Window: "5h", Unit: "fraction",
		UsedFraction: f64(0.25), Remaining: nil, ObservedAt: now.Add(-time.Minute), Source: "provider_headers",
		Confidence: "high", MaxAgeSeconds: 1800, Status: "available"}
}

func TestRuntimeQuotaObservationValidate(t *testing.T) {
	now := time.Now()
	require.NoError(t, validQuotaObservation(now).Validate(now))

	cases := map[string]func(*RuntimeQuotaObservation){
		"future observed_at":          func(o *RuntimeQuotaObservation) { o.ObservedAt = now.Add(time.Second) },
		"zero observed_at":            func(o *RuntimeQuotaObservation) { o.ObservedAt = time.Time{} },
		"used_fraction above 1":       func(o *RuntimeQuotaObservation) { o.UsedFraction = f64(1.01) },
		"used_fraction negative":      func(o *RuntimeQuotaObservation) { o.UsedFraction = f64(-0.1) },
		"remaining negative":          func(o *RuntimeQuotaObservation) { o.Remaining = f64(-1) },
		"bad status":                  func(o *RuntimeQuotaObservation) { o.Status = "fine" },
		"bad confidence":              func(o *RuntimeQuotaObservation) { o.Confidence = "certain" },
		"bad window":                  func(o *RuntimeQuotaObservation) { o.Window = "5 hours" },
		"empty unit":                  func(o *RuntimeQuotaObservation) { o.Unit = "" },
		"empty pool":                  func(o *RuntimeQuotaObservation) { o.Pool = "" },
		"zero max age":                func(o *RuntimeQuotaObservation) { o.MaxAgeSeconds = 0 },
		"huge max age":                func(o *RuntimeQuotaObservation) { o.MaxAgeSeconds = runtimeQuotaMaxAgeCeiling + 1 },
		"bad error_kind":              func(o *RuntimeQuotaObservation) { e := "Has Space"; o.ErrorKind = &e },
		"unknown status with numbers": func(o *RuntimeQuotaObservation) { o.Status = "unknown" },
		"auth_error with remaining":   func(o *RuntimeQuotaObservation) { o.Status, o.UsedFraction, o.Remaining = "auth_error", nil, f64(5) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := validQuotaObservation(now)
			mutate(&o)
			assert.Error(t, o.Validate(now))
		})
	}
}

func TestRuntimeQuotaUnknownIsNotZero(t *testing.T) {
	now := time.Now()
	o := validQuotaObservation(now)
	o.Status, o.UsedFraction, o.Remaining = "unknown", nil, nil
	require.NoError(t, o.Validate(now))
	data, err := json.Marshal(o)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"used_fraction":null`)
	assert.Contains(t, string(data), `"remaining":null`)
	assert.NotContains(t, string(data), `"used_fraction":0`)
}

func TestRuntimeQuotaReportValidate(t *testing.T) {
	now := time.Now()
	one := []RuntimeQuotaObservation{validQuotaObservation(now)}
	require.NoError(t, RuntimeQuotaReport{SchemaVersion: 1, Observations: one}.Validate(now))
	assert.Error(t, RuntimeQuotaReport{SchemaVersion: 2, Observations: one}.Validate(now), "wrong schema")
	assert.Error(t, RuntimeQuotaReport{SchemaVersion: 1}.Validate(now), "empty batch")
	assert.Error(t, RuntimeQuotaReport{SchemaVersion: 1, Observations: make([]RuntimeQuotaObservation, 257)}.Validate(now), "too many")
	bad := validQuotaObservation(now)
	bad.UsedFraction = f64(2)
	assert.Error(t, RuntimeQuotaReport{SchemaVersion: 1, Observations: []RuntimeQuotaObservation{one[0], bad}}.Validate(now), "one bad observation rejects the batch")
}

func TestRuntimeQuotaStale(t *testing.T) {
	now := time.Now()
	o := validQuotaObservation(now)
	o.ObservedAt, o.MaxAgeSeconds = now.Add(-59*time.Second), 60
	assert.False(t, o.Stale(now))
	o.ObservedAt = now.Add(-61 * time.Second)
	assert.True(t, o.Stale(now))
}
