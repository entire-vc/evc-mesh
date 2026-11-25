package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuotaHistoryUsedOnlyMeasured(t *testing.T) {
	used := 0.99
	for _, status := range []string{"available", "exhausted", "unknown", "auth_error", "network_error"} {
		t.Run(status, func(t *testing.T) {
			o := RuntimeQuotaObservation{Status: status, UsedFraction: &used}
			if status == "available" || status == "exhausted" {
				require.Equal(t, &used, o.HistoryUsed(), "a share at the limit is a measurement")
			} else {
				require.Nil(t, o.HistoryUsed(), "an error or unknown status is not a measurement")
			}
		})
	}
	require.Nil(t, RuntimeQuotaObservation{Status: "exhausted"}.HistoryUsed(), "exhausted without a share stays NULL, never 0")
}
