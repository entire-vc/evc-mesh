package domain

import (
	"slices"
	"time"

	"github.com/google/uuid"
)

type RuntimePreviewInput struct {
	Purpose              string    `json:"purpose"` // new_launch or quota_reserve
	SourceProfileID      string    `json:"source_profile_id,omitempty"`
	RequiredCapabilities []string  `json:"required_capabilities"`
	ArtifactID           uuid.UUID `json:"artifact_id,omitempty"`
	ArtifactRevision     string    `json:"artifact_revision,omitempty"`
}

type RuntimeTrace struct {
	ProfileID string `json:"profile_id"`
	Reason    string `json:"reason"`
}

type RuntimePreview struct {
	SelectedProfileID   string         `json:"selected_profile_id,omitempty"`
	SelectedAccountRef  string         `json:"selected_account_ref,omitempty"`
	PreferredAccountRef string         `json:"preferred_account_ref,omitempty"`
	Reason              string         `json:"reason"`
	Trace               []RuntimeTrace `json:"trace"`
	Revision            int64          `json:"revision"`
	LaunchAuthorized    bool           `json:"launch_authorized"`
	ArtifactRevision    string         `json:"artifact_revision,omitempty"`
	QAMode              string         `json:"qa_mode,omitempty"`
	QACompromise        bool           `json:"qa_compromise"`
}

// RuntimeCandidateEvidence is assembled from fresh authorized controller reports
// and persisted exact-SHA attestations. It is never decoded from preview JSON.
type RuntimeCandidateEvidence struct {
	ControllerCurrent    bool
	Pools                map[string]RuntimePoolObservation
	ActualModelDeveloper string
	ActualModelFamily    string
}

type RuntimeAuthor struct {
	AgentID        uuid.UUID `json:"agent_id"`
	ModelDeveloper string    `json:"model_developer"`
	ModelFamily    string    `json:"model_family"`
}

// RuntimeProvenanceReport is what a controller attests about one exact artifact
// revision. Complete=false means authorship may be larger than Authors.
type RuntimeProvenanceReport struct {
	ControllerRef    string          `json:"controller_ref"`
	ArtifactID       uuid.UUID       `json:"-"` // from the route, never the body
	ArtifactRevision string          `json:"artifact_revision"`
	Complete         bool            `json:"complete"`
	Authors          []RuntimeAuthor `json:"authors"`
}

type RuntimeProvenance struct {
	ArtifactID uuid.UUID
	Revision   string
	Authors    []RuntimeAuthor
	Complete   bool
}

func runtimeIndependent(candidate RuntimeCandidateEvidence, provenance *RuntimeProvenance) bool {
	if candidate.ActualModelDeveloper == "" || candidate.ActualModelFamily == "" {
		return false
	}
	for _, author := range provenance.Authors {
		if author.ModelDeveloper == "" || author.ModelFamily == "" || author.ModelDeveloper == candidate.ActualModelDeveloper || author.ModelFamily == candidate.ActualModelFamily {
			return false
		}
	}
	return true
}

// PreviewRuntime is read-only. A successful selection never grants launch or
// spend authority; the controller must obtain its fenced execution reservation.
func PreviewRuntime(c *RuntimeCatalog, binding RuntimeBinding, revision int64, enabled bool, admitted []string, evidence map[string]RuntimeCandidateEvidence, provenance *RuntimeProvenance, request RuntimePreviewInput, now time.Time) RuntimePreview {
	result := RuntimePreview{Reason: "no_available_route", Trace: []RuntimeTrace{}, Revision: revision, ArtifactRevision: request.ArtifactRevision}
	if !enabled || !binding.Enabled {
		result.Reason = "integration_or_admission_disabled"
		return result
	}
	if request.Purpose != "new_launch" && request.Purpose != "quota_reserve" {
		result.Reason = "request_state_invalid"
		return result
	}
	if (request.Purpose == "new_launch" && request.SourceProfileID != "") || (request.Purpose == "quota_reserve" && request.SourceProfileID == "") {
		result.Reason = "request_state_invalid"
		return result
	}
	review := request.ArtifactID != uuid.Nil || request.ArtifactRevision != ""
	if review {
		result.QAMode = binding.Policy.QA.Mode
		if provenance == nil || !provenance.Complete || len(provenance.Authors) == 0 || provenance.ArtifactID != request.ArtifactID || provenance.Revision != request.ArtifactRevision {
			result.Reason = "exact_sha_provenance_unavailable"
			return result
		}
		for _, author := range provenance.Authors {
			if author.AgentID == uuid.Nil || author.AgentID == binding.Binding.AgentID {
				result.Reason = "reviewer_is_author_or_identity_unknown"
				return result
			}
		}
	}
	assess := func(ref string) string {
		profile, ok := c.Profiles[ref]
		if !ok || !slices.Contains(binding.PermittedProfiles, ref) || !slices.Contains(admitted, ref) {
			return "profile_not_permitted"
		}
		if !profile.Enabled || !c.Controllers[profile.ControllerRef].Enabled {
			return "profile_disabled"
		}
		account := c.Accounts[profile.AccountRef]
		if profile.ExecutionMode == "api" && !slices.Contains(binding.Policy.PrimaryProfiles, ref) && !slices.Contains(binding.Policy.APIReserveProviders, account.Provider) {
			return "api_not_opted_in"
		}
		observation, ok := evidence[ref]
		if !ok || !observation.ControllerCurrent {
			return "controller_unavailable"
		}
		for _, cap := range request.RequiredCapabilities {
			if !slices.Contains(profile.Capabilities, cap) {
				return "capability_missing"
			}
		}
		quota := false
		for _, ref := range account.QuotaPoolsByMode[profile.ExecutionMode] {
			pool, _ := c.ResolvePool(ref)
			fact, found := observation.Pools[pool]
			if !found || !fact.Verified || fact.ObservedAt.IsZero() || fact.ObservedAt.After(now) || now.Sub(fact.ObservedAt) > time.Duration(binding.Policy.EvidenceMaxAgeSeconds)*time.Second {
				return "pool_evidence_unavailable"
			}
			switch fact.State {
			case "available":
			case "exhausted":
				quota = true
			case "threshold":
				if c.Pools[pool].ThresholdRef == "" || c.Pools[pool].ThresholdRef != fact.ThresholdRef || fact.EvidenceKind != "configured_threshold" {
					return "pool_evidence_unknown"
				}
				quota = true
			default:
				return "pool_evidence_unknown"
			}
		}
		if quota {
			return "quota_blocked"
		}
		return "available"
	}
	// Strict QA is evaluated over ALL permitted alternatives, outside the route
	// attempt limit. A limit cannot hide one independent usable/unknown option
	// and manufacture permission to relax strict.
	if review && binding.Policy.QA.Mode != "off" {
		independent, allQuota, available := 0, true, ""
		unknownModel := false
		for _, ref := range binding.PermittedProfiles {
			if !slices.Contains(admitted, ref) {
				continue
			}
			observation := evidence[ref]
			if observation.ActualModelDeveloper == "" || observation.ActualModelFamily == "" {
				unknownModel = true
				continue
			}
			if !runtimeIndependent(observation, provenance) {
				continue
			}
			independent++
			reason := assess(ref)
			result.Trace = append(result.Trace, RuntimeTrace{ProfileID: ref, Reason: reason})
			if reason != "quota_blocked" {
				allQuota = false
			}
			if reason == "available" && available == "" {
				available = ref
			}
		}
		if available != "" {
			result.SelectedProfileID = available
			result.SelectedAccountRef = c.Profiles[available].AccountRef
			result.Reason = "independent_reviewer_available"
			return result
		}
		if binding.Policy.QA.Mode == "strict" {
			if !binding.Policy.QA.QuotaFallback || independent == 0 || !allQuota || unknownModel {
				result.Reason = "strict_independent_review_unavailable"
				return result
			}
			result.QAMode = "prefer"
			result.QACompromise = true
		}
	}
	// Preference changes the account within an explicitly ordered provider
	// group, never the order of unrelated providers or downstream quota edges.
	groups := func(refs []string) [][]string {
		var groups [][]string
		for _, ref := range refs {
			profile := c.Profiles[ref]
			provider := c.Accounts[profile.AccountRef].Provider
			if len(groups) == 0 {
				groups = append(groups, []string{ref})
				continue
			}
			last := groups[len(groups)-1]
			previous := c.Profiles[last[0]]
			if c.Accounts[previous.AccountRef].Provider != provider || previous.ExecutionMode != profile.ExecutionMode {
				groups = append(groups, []string{ref})
			} else {
				groups[len(groups)-1] = append(last, ref)
			}
		}
		for _, group := range groups {
			provider := c.Accounts[c.Profiles[group[0]].AccountRef].Provider
			preferred := binding.Policy.PreferredAccounts[provider]
			if preferred != "" {
				slices.SortStableFunc(group, func(a, b string) int {
					aPreferred, bPreferred := c.Profiles[a].AccountRef == preferred, c.Profiles[b].AccountRef == preferred
					if aPreferred == bPreferred {
						return 0
					}
					if aPreferred {
						return -1
					}
					return 1
				})
			}
		}
		return groups
	}
	visited := map[string]bool{}
	attempts := 0
	inspect := func(ref string) string {
		if visited[ref] {
			return "already_visited"
		}
		visited[ref] = true
		if attempts >= binding.Policy.MaxAttempts {
			return "attempt_limit"
		}
		attempts++
		reason := assess(ref)
		result.Trace = append(result.Trace, RuntimeTrace{ProfileID: ref, Reason: reason})
		return reason
	}
	walk := func(pending [][]string, edgesLast bool) string {
		for len(pending) > 0 {
			group := pending[0]
			pending = pending[1:]
			var children [][]string
			for _, ref := range group {
				reason := inspect(ref)
				if reason == "available" {
					return ref
				}
				if reason == "attempt_limit" {
					result.Reason = reason
					return ""
				}
				if reason == "quota_blocked" {
					children = append(children, groups(binding.Policy.QuotaEdges[ref])...)
				}
			}
			if edgesLast {
				pending = append(pending, children...)
			} else {
				pending = append(children, pending...)
			}
		}
		return ""
	}
	var selected string
	if request.Purpose == "quota_reserve" {
		if !slices.Contains(binding.PermittedProfiles, request.SourceProfileID) {
			result.Reason = "source_not_permitted"
			return result
		}
		reason := inspect(request.SourceProfileID)
		if reason != "quota_blocked" {
			result.Reason = "source_has_no_sole_quota_trigger"
			return result
		}
		selected = walk(groups(binding.Policy.QuotaEdges[request.SourceProfileID]), false)
	} else {
		primaryGroups := groups(binding.Policy.PrimaryProfiles)
		first := primaryGroups[0][0]
		reason := inspect(first)
		switch reason {
		case "available":
			selected = first
		case "quota_blocked":
			var pending [][]string
			if len(primaryGroups[0]) > 1 {
				pending = append(pending, primaryGroups[0][1:])
			}
			pending = append(pending, primaryGroups[1:]...)
			// Every remaining primary group is tried before any reserve edge.
			pending = append(pending, groups(binding.Policy.QuotaEdges[first])...)
			selected = walk(pending, true)
		default:
			result.Reason = "primary_has_no_sole_quota_trigger"
			return result
		}
	}
	if selected != "" {
		result.SelectedProfileID = selected
		result.SelectedAccountRef = c.Profiles[selected].AccountRef
		provider := c.Accounts[result.SelectedAccountRef].Provider
		result.PreferredAccountRef = binding.Policy.PreferredAccounts[provider]
		result.Reason = "profile_available"
	}
	return result
}
