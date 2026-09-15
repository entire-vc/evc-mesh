package handler

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/service"
)

// findCSVRow returns the first row of the parsed CSV whose first two columns
// (Metric, Category) match category/field, or nil if none does.
func findCSVRow(t *testing.T, body, category, field string) []string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(body))
	rows, err := r.ReadAll()
	require.NoError(t, err)
	for _, row := range rows {
		if len(row) >= 2 && row[0] == category && row[1] == field {
			return row
		}
	}
	return nil
}

func doExportMetrics(t *testing.T, mockSvc *MockAnalyticsService) *httptest.ResponseRecorder {
	t.Helper()
	h := NewAnalyticsHandler(mockSvc)
	req := httptest.NewRequest(http.MethodGet, "/?format=csv&from=2026-08-01&to=2026-08-02", http.NoBody)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/analytics/export")
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.ExportMetrics(c))
	return rec
}

// TestAnalyticsHandler_ExportMetrics_RetainedSincePresent covers the branch
// added for task #9b803a9b: when the event queue's TTL-floor is known, the
// CSV must carry a `retained_since` row alongside `total_events` and
// `period_fully_covered` — the exact condition the audit found silently
// missing (a closed August report reading the live queue with no coverage
// caveat at all).
func TestAnalyticsHandler_ExportMetrics_RetainedSincePresent(t *testing.T) {
	retainedSince := time.Date(2026, 9, 14, 0, 0, 52, 0, time.UTC)
	mockSvc := &MockAnalyticsService{
		GetMetricsFunc: func(_ context.Context, _ service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
			return &service.AnalyticsMetrics{
				EventMetrics: service.EventMetrics{
					TotalEvents:        0,
					ByType:             map[string]int{},
					RetainedSince:      &retainedSince,
					PeriodFullyCovered: false,
				},
			}, nil
		},
	}

	rec := doExportMetrics(t, mockSvc)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	totalRow := findCSVRow(t, body, "Event Coverage", "total_events")
	require.NotNil(t, totalRow, "total_events row must always be present")
	require.Equal(t, "0", totalRow[2])

	coveredRow := findCSVRow(t, body, "Event Coverage", "period_fully_covered")
	require.NotNil(t, coveredRow, "period_fully_covered row must always be present")
	require.Equal(t, "false", coveredRow[2])

	retainedRow := findCSVRow(t, body, "Event Coverage", "retained_since")
	require.NotNil(t, retainedRow, "retained_since row must appear when RetainedSince is non-nil")
	require.Equal(t, retainedSince.Format(time.RFC3339), retainedRow[2])
}

// TestAnalyticsHandler_ExportMetrics_RetainedSinceNil is the negative
// control: an empty scope (queue holds nothing at all) must NOT print a
// `retained_since` row — there is no floor to report.
func TestAnalyticsHandler_ExportMetrics_RetainedSinceNil(t *testing.T) {
	mockSvc := &MockAnalyticsService{
		GetMetricsFunc: func(_ context.Context, _ service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
			return &service.AnalyticsMetrics{
				EventMetrics: service.EventMetrics{
					TotalEvents:        0,
					ByType:             map[string]int{},
					RetainedSince:      nil,
					PeriodFullyCovered: false,
				},
			}, nil
		},
	}

	rec := doExportMetrics(t, mockSvc)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Nil(t, findCSVRow(t, body, "Event Coverage", "retained_since"),
		"retained_since row must be absent when RetainedSince is nil")
}
