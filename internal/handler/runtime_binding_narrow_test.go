package handler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestNarrowBindingForReceiver_KeepsOnlyAdmittedProfiles(t *testing.T) {
	view := &postgres.RuntimeBindingView{
		Admission: postgres.RuntimeAdmission{Enabled: true, PermittedProfiles: []string{"p1"}},
		Profiles:  map[string]domain.RuntimeProfile{"p1": {AccountRef: "acct-1"}},
		Binding: domain.RuntimeBinding{
			PermittedProfiles: []string{"p1", "p2"},
			Policy: domain.RuntimePolicy{
				PrimaryProfiles:   []string{"p2", "p1"},
				QuotaEdges:        map[string][]string{"p1": {"p2", "p1"}, "p2": {"p1"}},
				PreferredAccounts: map[string]string{"anthropic": "acct-1", "openai": "acct-2"},
				MaxAttempts:       3,
			},
		},
	}
	got := narrowBindingForReceiver(view)
	require.Equal(t, []string{"p1"}, got.Binding.PermittedProfiles)
	require.Equal(t, []string{"p1"}, got.Binding.Policy.PrimaryProfiles)
	require.Equal(t, map[string][]string{"p1": {"p1"}}, got.Binding.Policy.QuotaEdges)
	require.Equal(t, map[string]string{"anthropic": "acct-1"}, got.Binding.Policy.PreferredAccounts)
	require.Equal(t, 3, got.Binding.Policy.MaxAttempts)
	// the owner's own view is a different value and is untouched
	require.Equal(t, []string{"p1", "p2"}, view.Binding.PermittedProfiles)
	require.Len(t, view.Binding.Policy.PreferredAccounts, 2)
}
