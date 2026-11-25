package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// staleChainFixture builds A -> B -> C over quota edges, each profile on its
// own account and pool, so a fact about one never leaks into another.
func staleChainFixture(t *testing.T) (c *RuntimeCatalog, b RuntimeBinding, facts map[string]RuntimeCandidateEvidence, pools map[string]RuntimePoolObservation, now time.Time) {
	t.Helper()
	c, b, facts, now = runtimeRouteFixture(t)
	pools = map[string]RuntimePoolObservation{}
	for _, ref := range []string{"a", "b", "c"} {
		window := ref + "-window"
		c.Pools[window] = RuntimePool{Provider: "provider-a", ResourceRef: window, Aliases: []string{}, MaxConcurrency: 1}
		c.Accounts[ref] = RuntimeAccount{Provider: "provider-a", CredentialRef: "cred:" + ref, QuotaPoolsByMode: map[string][]string{"subscription": {window}}}
		p := c.Profiles["preferred"]
		p.AccountRef = ref
		c.Profiles[ref] = p
		pools[window] = RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	}
	for ref := range facts {
		delete(facts, ref)
	}
	for _, ref := range []string{"a", "b", "c"} {
		facts[ref] = RuntimeCandidateEvidence{ControllerCurrent: true, Pools: pools, ActualModelDeveloper: "developer-a", ActualModelFamily: "family-a"}
	}
	b.PermittedProfiles = []string{"a", "b", "c"}
	b.Policy.PrimaryProfiles = []string{"a"}
	b.Policy.QuotaEdges = map[string][]string{"a": {"b"}, "b": {"c"}}
	b.Policy.PreferredAccounts = map[string]string{}
	b.Policy.MaxAttempts = 5
	return c, b, facts, pools, now
}

func staleObservation(now time.Time) RuntimePoolObservation {
	return RuntimePoolObservation{State: "unknown", Verified: false, ErrorKind: RuntimePoolErrorCredentialStale, ObservedAt: now.Add(-time.Second)}
}

func exhaustedObservation(now time.Time) RuntimePoolObservation {
	return RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
}

func traceOf(p RuntimePreview) []RuntimeTrace { return p.Trace }

// A(quota_blocked) -> B(credential_stale) -> C(available) selects C, for both
// a new launch and a reserve request from A.
func TestRuntimePreviewCredentialStaleContinuesQuotaEdges(t *testing.T) {
	c, b, facts, pools, now := staleChainFixture(t)
	pools["a-window"] = exhaustedObservation(now)
	pools["b-window"] = staleObservation(now)
	want := []RuntimeTrace{{ProfileID: "a", Reason: "quota_blocked"}, {ProfileID: "b", Reason: "credential_stale"}, {ProfileID: "c", Reason: "available"}}

	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "c", result.SelectedProfileID, result.Reason)
	require.Equal(t, "profile_available", result.Reason)
	require.Equal(t, want, traceOf(result))
	require.False(t, result.LaunchAuthorized)

	reserve := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "quota_reserve", SourceProfileID: "a"}, now)
	require.Equal(t, "c", reserve.SelectedProfileID, reserve.Reason)
	require.Equal(t, want, traceOf(reserve))
}

// Only the exact fresh unknown/unverified/credential_stale fact is a stale
// carrier. Every other unresolved B stops the walk before C.
func TestRuntimePreviewOnlyConfirmedCredentialStaleOpensEdges(t *testing.T) {
	cases := map[string]func(now time.Time, pools map[string]RuntimePoolObservation){
		"auth_error": func(now time.Time, pools map[string]RuntimePoolObservation) {
			pools["b-window"] = RuntimePoolObservation{State: "auth_error", Verified: true, ObservedAt: now.Add(-time.Second)}
		},
		"auth_error_with_stale_kind": func(now time.Time, pools map[string]RuntimePoolObservation) {
			pools["b-window"] = RuntimePoolObservation{State: "auth_error", Verified: false, ErrorKind: RuntimePoolErrorCredentialStale, ObservedAt: now.Add(-time.Second)}
		},
		"plain_unknown": func(now time.Time, pools map[string]RuntimePoolObservation) {
			pools["b-window"] = RuntimePoolObservation{State: "unknown", Verified: false, ObservedAt: now.Add(-time.Second)}
		},
		"verified_unknown_with_stale_kind": func(now time.Time, pools map[string]RuntimePoolObservation) {
			pools["b-window"] = RuntimePoolObservation{State: "unknown", Verified: true, ErrorKind: RuntimePoolErrorCredentialStale, ObservedAt: now.Add(-time.Second)}
		},
		"old_stale_fact": func(now time.Time, pools map[string]RuntimePoolObservation) {
			pools["b-window"] = RuntimePoolObservation{State: "unknown", Verified: false, ErrorKind: RuntimePoolErrorCredentialStale, ObservedAt: now.Add(-time.Hour * 24)}
		},
		"future_stale_fact": func(now time.Time, pools map[string]RuntimePoolObservation) {
			pools["b-window"] = RuntimePoolObservation{State: "unknown", Verified: false, ErrorKind: RuntimePoolErrorCredentialStale, ObservedAt: now.Add(time.Minute)}
		},
		"missing_window": func(now time.Time, pools map[string]RuntimePoolObservation) {
			delete(pools, "b-window")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, b, facts, pools, now := staleChainFixture(t)
			pools["a-window"] = exhaustedObservation(now)
			mutate(now, pools)
			result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
			require.Empty(t, result.SelectedProfileID, "C must not be reached")
			require.Equal(t, "sibling_evidence_unknown", result.Reason)
			for _, step := range result.Trace {
				require.NotEqual(t, "c", step.ProfileID)
				require.NotEqual(t, "credential_stale", step.Reason)
			}
		})
	}
}

// B with several windows: credential_stale only counts when it is the cause of
// every unresolved window. A sibling auth_error or plain unknown blocks.
func TestRuntimePreviewMixedStaleAndAuthWindowsDoNotPass(t *testing.T) {
	for _, other := range []RuntimePoolObservation{
		{State: "auth_error", Verified: true},
		{State: "auth_error", Verified: false},
		{State: "unknown", Verified: false},
		{State: "network_error", Verified: true},
	} {
		t.Run(other.State, func(t *testing.T) {
			c, b, facts, pools, now := staleChainFixture(t)
			c.Pools["b-second"] = RuntimePool{Provider: "provider-a", ResourceRef: "b-second", Aliases: []string{}, MaxConcurrency: 1}
			account := c.Accounts["b"]
			account.QuotaPoolsByMode = map[string][]string{"subscription": {"b-window", "b-second"}}
			c.Accounts["b"] = account
			pools["a-window"] = exhaustedObservation(now)
			pools["b-window"] = staleObservation(now)
			other.ObservedAt = now.Add(-time.Second)
			pools["b-second"] = other
			result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
			require.Empty(t, result.SelectedProfileID)
			require.Equal(t, "sibling_evidence_unknown", result.Reason)
		})
	}
	// Positive control: two stale windows on B still pass.
	c, b, facts, pools, now := staleChainFixture(t)
	c.Pools["b-second"] = RuntimePool{Provider: "provider-a", ResourceRef: "b-second", Aliases: []string{}, MaxConcurrency: 1}
	account := c.Accounts["b"]
	account.QuotaPoolsByMode = map[string][]string{"subscription": {"b-window", "b-second"}}
	c.Accounts["b"] = account
	pools["a-window"] = exhaustedObservation(now)
	pools["b-window"] = staleObservation(now)
	pools["b-second"] = staleObservation(now)
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "c", result.SelectedProfileID, result.Reason)
}

// A confirmed quota block on any window still wins over a stale sibling window.
func TestRuntimePreviewQuotaWindowWinsOverStaleWindow(t *testing.T) {
	c, b, facts, pools, now := staleChainFixture(t)
	c.Pools["b-second"] = RuntimePool{Provider: "provider-a", ResourceRef: "b-second", Aliases: []string{}, MaxConcurrency: 1}
	account := c.Accounts["b"]
	account.QuotaPoolsByMode = map[string][]string{"subscription": {"b-window", "b-second"}}
	c.Accounts["b"] = account
	pools["a-window"] = exhaustedObservation(now)
	pools["b-window"] = staleObservation(now)
	pools["b-second"] = exhaustedObservation(now)
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "c", result.SelectedProfileID)
	require.Equal(t, RuntimeTrace{ProfileID: "b", Reason: "quota_blocked"}, result.Trace[1])
}

// A stale primary or a stale reserve source is no quota trigger: no fallback.
func TestRuntimePreviewStalePrimaryOrSourceOpensNoFallback(t *testing.T) {
	c, b, facts, pools, now := staleChainFixture(t)
	pools["a-window"] = staleObservation(now)
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Empty(t, result.SelectedProfileID)
	require.Equal(t, "primary_has_no_sole_quota_trigger", result.Reason)
	require.Equal(t, []RuntimeTrace{{ProfileID: "a", Reason: "credential_stale"}}, result.Trace)

	reserve := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "quota_reserve", SourceProfileID: "a"}, now)
	require.Empty(t, reserve.SelectedProfileID)
	require.Equal(t, "source_has_no_sole_quota_trigger", reserve.Reason)

	// A later primary reached only through the primary list is not inside a
	// quota edge: fail closed even after the first primary was quota-blocked.
	c, b, facts, pools, now = staleChainFixture(t)
	b.Policy.PrimaryProfiles = []string{"a", "b"}
	b.Policy.QuotaEdges = map[string][]string{"b": {"c"}}
	pools["a-window"] = exhaustedObservation(now)
	pools["b-window"] = staleObservation(now)
	later := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Empty(t, later.SelectedProfileID)
	require.Equal(t, "sibling_evidence_unknown", later.Reason)
}

// Review mode: a stale first primary behaves like unavailable evidence (later
// primaries are tried) and never opens its quota edges.
func TestRuntimePreviewQAStalePrimaryOpensNoEdge(t *testing.T) {
	c, b, facts, pools, now := staleChainFixture(t)
	provenance, request := reviewRequest()
	b.Policy.QA.Mode = "prefer"
	pools["a-window"] = staleObservation(now)
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	require.Empty(t, result.SelectedProfileID)
	for _, step := range result.Trace {
		require.Equal(t, "a", step.ProfileID, "no quota edge of a stale primary is walked")
	}

	// Inside an open fallback the stale reviewer node is traversed too.
	c, b, facts, pools, now = staleChainFixture(t)
	b.Policy.QA.Mode = "prefer"
	pools["a-window"] = exhaustedObservation(now)
	pools["b-window"] = staleObservation(now)
	result = PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	require.Equal(t, "c", result.SelectedProfileID, result.Reason)
}

// A traversed stale node spends an attempt, so the attempt cap still bounds the walk.
func TestRuntimePreviewStaleTraversalSpendsAttempt(t *testing.T) {
	for _, review := range []bool{false, true} {
		c, b, facts, pools, now := staleChainFixture(t)
		b.Policy.MaxAttempts = 2
		pools["a-window"] = exhaustedObservation(now)
		pools["b-window"] = staleObservation(now)
		request := RuntimePreviewInput{Purpose: "new_launch"}
		var provenance *RuntimeProvenance
		if review {
			provenance, request = reviewRequest()
			b.Policy.QA.Mode = "off"
		}
		result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
		require.Empty(t, result.SelectedProfileID, "review=%v", review)
		require.Equal(t, "attempt_limit", result.Reason, "review=%v", review)
		require.Len(t, result.Trace, 2)
	}
}

// A later primary that is also a quota-edge target of the first primary is
// inspected as a primary (primaries go before edges): its stale credential
// opens nothing. The edge context belongs to the queue item, not the profile.
func TestRuntimePreviewStaleLaterPrimaryAlsoEdgeTargetStaysClosed(t *testing.T) {
	c, b, facts, pools, now := staleChainFixture(t)
	b.Policy.PrimaryProfiles = []string{"a", "b"}
	pools["a-window"] = exhaustedObservation(now)
	pools["b-window"] = staleObservation(now)
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Empty(t, result.SelectedProfileID)
	require.Equal(t, "sibling_evidence_unknown", result.Reason)

	// Review mode: the stale primary is skipped as unknown, its edge stays shut.
	provenance, request := reviewRequest()
	b.Policy.QA.Mode = "prefer"
	result = PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	require.Empty(t, result.SelectedProfileID)
	for _, step := range result.Trace {
		require.NotEqual(t, "c", step.ProfileID)
	}

	// Positive control: with b only an edge target the walk reaches c.
	b.Policy.PrimaryProfiles = []string{"a"}
	b.Policy.QA.Mode = ""
	result = PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "c", result.SelectedProfileID, result.Reason)
}
