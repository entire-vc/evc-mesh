package domain

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func runtimeRouteFixture(t *testing.T) (*RuntimeCatalog, RuntimeBinding, map[string]RuntimeCandidateEvidence, time.Time) {
	t.Helper()
	c, err := ParseRuntimeCatalog(runtimeExample(t))
	require.NoError(t, err)
	now := time.Now()
	pools := map[string]RuntimePoolObservation{}
	for ref := range c.Pools {
		pools[ref] = RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	}
	facts := map[string]RuntimeCandidateEvidence{}
	for ref, p := range c.Profiles {
		facts[ref] = RuntimeCandidateEvidence{ControllerCurrent: true, Pools: pools, ActualModelDeveloper: p.ModelDeveloper, ActualModelFamily: p.ModelFamily}
	}
	return c, c.Bindings["worker-b"], facts, now
}

func TestRuntimePreviewPreferredQuotaAndReset(t *testing.T) {
	c, b, facts, now := runtimeRouteFixture(t)
	request := RuntimePreviewInput{Purpose: "new_launch", RequiredCapabilities: []string{"code"}}
	decide := func() RuntimePreview {
		return PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, request, now)
	}
	first := decide()
	require.Equal(t, "preferred", first.SelectedProfileID)
	require.False(t, first.LaunchAuthorized)
	facts["preferred"].Pools["long-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	require.Equal(t, "reserve", decide().SelectedProfileID, "all applicable windows must be read")
	facts["preferred"].Pools["long-window"] = RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	require.Equal(t, "preferred", decide().SelectedProfileID, "reset only affects a new selection")
	request.Purpose = "quota_reserve"
	request.SourceProfileID = "reserve"
	result := decide()
	require.Empty(t, result.SelectedProfileID)
	require.Equal(t, "source_has_no_sole_quota_trigger", result.Reason, "an available running backup cannot be moved back")
}

func TestRuntimePreviewUnknownAndDisabledCannotOpenReserve(t *testing.T) {
	for _, state := range []string{"unknown", "auth_error", "network_error", "threshold"} {
		t.Run(state, func(t *testing.T) {
			c, b, facts, now := runtimeRouteFixture(t)
			facts["preferred"].Pools["long-window"] = RuntimePoolObservation{State: state, Verified: true, ObservedAt: now.Add(-time.Second)}
			result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
			require.Empty(t, result.SelectedProfileID)
			require.Equal(t, "primary_has_no_sole_quota_trigger", result.Reason)
		})
	}
	c, b, facts, now := runtimeRouteFixture(t)
	result := PreviewRuntime(c, b, 1, false, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "integration_or_admission_disabled", result.Reason)
}

func TestRuntimePreviewSharedPoolAliasAndExplicitAPI(t *testing.T) {
	c, b, facts, now := runtimeRouteFixture(t)
	account := c.Accounts["reserve"]
	account.QuotaPoolsByMode["subscription"] = []string{"another-credential-view"}
	c.Accounts["reserve"] = account
	facts["preferred"].Pools["shared-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	request := RuntimePreviewInput{Purpose: "new_launch"}
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, request, now)
	require.Empty(t, result.SelectedProfileID, "changing credentials cannot multiply or evade a shared pool")
	account.QuotaPoolsByMode["api"] = []string{"reserve-window"}
	c.Accounts["reserve"] = account
	profile := c.Profiles["reserve"]
	profile.ExecutionMode = "api"
	c.Profiles["reserve"] = profile
	result = PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, request, now)
	require.Empty(t, result.SelectedProfileID)
	require.Equal(t, "api_not_opted_in", result.Trace[len(result.Trace)-1].Reason)
	b.Policy.APIReserveProviders = []string{"provider-a"}
	result = PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, request, now)
	require.Equal(t, "reserve", result.SelectedProfileID, "no mandatory local budget")
}

func TestRuntimePreviewExactSHAQAQuotaOnlyFallback(t *testing.T) {
	c, b, facts, now := runtimeRouteFixture(t)
	artifact := uuid.New()
	provenance := &RuntimeProvenance{ArtifactID: artifact, Revision: "exact-sha", Complete: true, Authors: []RuntimeAuthor{{AgentID: uuid.New(), ModelDeveloper: "developer-a", ModelFamily: "family-a"}}}
	request := RuntimePreviewInput{Purpose: "new_launch", ArtifactID: artifact, ArtifactRevision: "exact-sha"}
	independent := facts["reserve"]
	independent.ActualModelDeveloper = "developer-b"
	independent.ActualModelFamily = "family-b"
	facts["reserve"] = independent
	decide := func() RuntimePreview {
		return PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	}
	require.Equal(t, "reserve", decide().SelectedProfileID)
	facts["reserve"].Pools["reserve-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	require.Empty(t, decide().SelectedProfileID, "fallback defaults off")
	b.Policy.QA.QuotaFallback = true
	result := decide()
	require.Equal(t, "preferred", result.SelectedProfileID)
	require.True(t, result.QACompromise)
	require.Equal(t, "prefer", result.QAMode)
	facts["reserve"].Pools["reserve-window"] = RuntimePoolObservation{State: "network_error", Verified: true, ObservedAt: now.Add(-time.Second)}
	require.Empty(t, decide().SelectedProfileID, "network failure cannot relax strict")
	facts["reserve"].Pools["reserve-window"] = RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	result = decide()
	require.Equal(t, "reserve", result.SelectedProfileID)
	require.False(t, result.QACompromise)
	require.Equal(t, "strict", result.QAMode)
	request.ArtifactRevision = "new-sha"
	require.Equal(t, "exact_sha_provenance_unavailable", decide().Reason)
	request.ArtifactRevision = "exact-sha"
	provenance.Authors[0].AgentID = b.Binding.AgentID
	require.Equal(t, "reviewer_is_author_or_identity_unknown", decide().Reason)
}

func TestRuntimePreviewLaterPrimaryGroupBeforeQuotaEdge(t *testing.T) {
	c, b, facts, now := runtimeRouteFixture(t)
	c.Accounts["other"] = RuntimeAccount{Provider: "provider-b", CredentialRef: "cred:prepared-other", QuotaPoolsByMode: map[string][]string{"subscription": {"other-window"}}}
	other := c.Profiles["preferred"]
	other.AccountRef = "other"
	c.Profiles["other"] = other
	c.Pools["other-window"] = RuntimePool{Provider: "provider-b", ResourceRef: "other-window", Aliases: []string{}, MaxConcurrency: 1}
	for _, f := range facts {
		f.Pools["other-window"] = RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	}
	facts["other"] = RuntimeCandidateEvidence{ControllerCurrent: true, Pools: facts["preferred"].Pools, ActualModelDeveloper: other.ModelDeveloper, ActualModelFamily: other.ModelFamily}
	b.PermittedProfiles = append(slices.Clone(b.PermittedProfiles), "other")
	b.Policy.PrimaryProfiles = []string{"preferred", "other"}
	facts["preferred"].Pools["long-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "other", result.SelectedProfileID, "a later primary group precedes quota edges")
}

func TestRuntimeCatalogRedactedHidesCredentialRefs(t *testing.T) {
	c, _, _, _ := runtimeRouteFixture(t)
	out := c.Redacted()
	body, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(body), "cred:prepared")
	require.Equal(t, "cred:prepared-preferred", c.Accounts["preferred"].CredentialRef, "source catalog is not mutated")
	var none *RuntimeCatalog
	require.Nil(t, none.Redacted())
}

func TestRuntimePreviewReserveEdgeNeverPreemptsLaterPrimary(t *testing.T) {
	c, b, facts, now := runtimeRouteFixture(t)
	exhausted := RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	available := RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	for _, name := range []string{"other", "third"} {
		provider := "provider-" + name
		c.Accounts[name] = RuntimeAccount{Provider: provider, CredentialRef: "cred:" + name, QuotaPoolsByMode: map[string][]string{"subscription": {name + "-window"}}}
		profile := c.Profiles["preferred"]
		profile.AccountRef = name
		c.Profiles[name] = profile
		c.Pools[name+"-window"] = RuntimePool{Provider: provider, ResourceRef: name + "-window", Aliases: []string{}, MaxConcurrency: 1}
		b.PermittedProfiles = append(slices.Clone(b.PermittedProfiles), name)
	}
	for _, f := range facts {
		f.Pools["other-window"], f.Pools["third-window"] = exhausted, available
	}
	for _, name := range []string{"other", "third"} {
		facts[name] = RuntimeCandidateEvidence{ControllerCurrent: true, Pools: facts["preferred"].Pools, ActualModelDeveloper: "developer-a", ActualModelFamily: "family-a"}
	}
	facts["preferred"].Pools["long-window"] = exhausted
	b.Policy.PrimaryProfiles = []string{"preferred", "other", "third"}
	b.Policy.QuotaEdges = map[string][]string{"other": {"reserve"}}
	b.Policy.MaxAttempts = 4
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "third", result.SelectedProfileID, "a reserve edge never precedes an available later primary")
}

func reviewFixture(t *testing.T) (*RuntimeCatalog, RuntimeBinding, map[string]RuntimeCandidateEvidence, time.Time) {
	c, b, facts, now := runtimeRouteFixture(t)
	independent := facts["reserve"]
	independent.ActualModelDeveloper, independent.ActualModelFamily = "developer-b", "family-b"
	facts["reserve"] = independent
	return c, b, facts, now
}

func reviewRequest() (*RuntimeProvenance, RuntimePreviewInput) {
	artifact := uuid.New()
	provenance := &RuntimeProvenance{ArtifactID: artifact, Revision: "exact-sha", Complete: true, Authors: []RuntimeAuthor{{AgentID: uuid.New(), ModelDeveloper: "developer-a", ModelFamily: "family-a"}}}
	return provenance, RuntimePreviewInput{Purpose: "new_launch", ArtifactID: artifact, ArtifactRevision: "exact-sha"}
}

// A configured soft threshold is a routing trigger, not proof that the independent
// reviewer's quota is spent: it must not let strict QA fall back to a non-independent one.
func TestRuntimePreviewQAThresholdIsNotQuotaProof(t *testing.T) {
	c, b, facts, now := reviewFixture(t)
	provenance, request := reviewRequest()
	b.Policy.QA.QuotaFallback = true
	pool := c.Pools["reserve-window"]
	pool.ThresholdRef = "soft-1"
	c.Pools["reserve-window"] = pool
	facts["reserve"].Pools["reserve-window"] = RuntimePoolObservation{State: "threshold", ThresholdRef: "soft-1", EvidenceKind: "configured_threshold", Verified: true, ObservedAt: now.Add(-time.Second)}
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	require.Empty(t, result.SelectedProfileID)
	require.False(t, result.QACompromise)
	require.Equal(t, "strict_independent_review_unavailable", result.Reason)
	// control: the same shape with confirmed exhaustion still relaxes.
	facts["reserve"].Pools["reserve-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	result = PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	require.Equal(t, "preferred", result.SelectedProfileID)
	require.True(t, result.QACompromise)
}

// The independent reviewer is found by the normal primary/edge walk, not by the
// first available entry of the permitted list.
func TestRuntimePreviewIndependentReviewerFollowsRouteWalk(t *testing.T) {
	c, b, facts, now := reviewFixture(t)
	provenance, request := reviewRequest()
	c.Accounts["stray"] = RuntimeAccount{Provider: "provider-stray", CredentialRef: "cred:stray", QuotaPoolsByMode: map[string][]string{"subscription": {"stray-window"}}}
	stray := c.Profiles["reserve"]
	stray.AccountRef = "stray"
	c.Profiles["stray"] = stray
	c.Pools["stray-window"] = RuntimePool{Provider: "provider-stray", ResourceRef: "stray-window", Aliases: []string{}, MaxConcurrency: 1}
	for _, f := range facts {
		f.Pools["stray-window"] = RuntimePoolObservation{State: "available", Verified: true, ObservedAt: now.Add(-time.Second)}
	}
	facts["stray"] = RuntimeCandidateEvidence{ControllerCurrent: true, Pools: facts["preferred"].Pools, ActualModelDeveloper: "developer-b", ActualModelFamily: "family-b"}
	b.PermittedProfiles = append(slices.Clone(b.PermittedProfiles), "stray")
	facts["reserve"].Pools["reserve-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, provenance, request, now)
	require.NotEqual(t, "stray", result.SelectedProfileID, "a profile outside primary_profiles/quota_edges is not routable")
	require.Empty(t, result.SelectedProfileID)
	require.Equal(t, "strict_independent_review_unavailable", result.Reason)
}

// A sibling whose evidence is unknown stops the walk: reserve/API is not reached
// past evidence the controller could not provide.
func TestRuntimePreviewUnknownSiblingStopsWalk(t *testing.T) {
	for _, state := range []string{"unknown", "auth_error", "network_error"} {
		t.Run(state, func(t *testing.T) {
			c, b, facts, now := runtimeRouteFixture(t)
			c.Accounts["other"] = RuntimeAccount{Provider: "provider-b", CredentialRef: "cred:other", QuotaPoolsByMode: map[string][]string{"subscription": {"other-window"}}}
			other := c.Profiles["preferred"]
			other.AccountRef = "other"
			c.Profiles["other"] = other
			c.Pools["other-window"] = RuntimePool{Provider: "provider-b", ResourceRef: "other-window", Aliases: []string{}, MaxConcurrency: 1}
			for _, f := range facts {
				f.Pools["other-window"] = RuntimePoolObservation{State: state, Verified: true, ObservedAt: now.Add(-time.Second)}
			}
			facts["other"] = RuntimeCandidateEvidence{ControllerCurrent: true, Pools: facts["preferred"].Pools, ActualModelDeveloper: other.ModelDeveloper, ActualModelFamily: other.ModelFamily}
			b.PermittedProfiles = append(slices.Clone(b.PermittedProfiles), "other")
			b.Policy.PrimaryProfiles = []string{"preferred", "other"}
			facts["preferred"].Pools["long-window"] = RuntimePoolObservation{State: "exhausted", Verified: true, ObservedAt: now.Add(-time.Second)}
			result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
			require.Empty(t, result.SelectedProfileID)
			require.Equal(t, "sibling_evidence_unknown", result.Reason)
		})
	}
}

// A configured threshold on the primary still opens the reserve edge.
func TestRuntimePreviewConfiguredThresholdStillTriggersReserve(t *testing.T) {
	c, b, facts, now := runtimeRouteFixture(t)
	pool := c.Pools["long-window"]
	pool.ThresholdRef = "soft-1"
	c.Pools["long-window"] = pool
	facts["preferred"].Pools["long-window"] = RuntimePoolObservation{State: "threshold", ThresholdRef: "soft-1", EvidenceKind: "configured_threshold", Verified: true, ObservedAt: now.Add(-time.Second)}
	result := PreviewRuntime(c, b, 1, true, b.PermittedProfiles, facts, nil, RuntimePreviewInput{Purpose: "new_launch"}, now)
	require.Equal(t, "reserve", result.SelectedProfileID)
	require.Equal(t, "threshold_blocked", result.Trace[0].Reason)
}
