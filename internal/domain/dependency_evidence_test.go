package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func evPRLink(meta map[string]any) VCSLink {
	raw, _ := json.Marshal(meta)
	return VCSLink{ID: uuid.New(), LinkType: VCSLinkTypePR, Status: VCSLinkStatusOpen, Metadata: raw}
}

func TestVerifyDependencyEvidence(t *testing.T) {
	const head, other = "aaaa111", "bbbb222"
	const u = "https://git.example/mr/1"
	edge := func(c DependencyCondition) TaskDependency {
		return TaskDependency{DependencyType: DependencyTypeBlocks, Condition: c}
	}
	shipLink := evPRLink(map[string]any{"head_sha": head, "ship_sha": head})
	noShip := evPRLink(map[string]any{"head_sha": head})
	staleShip := evPRLink(map[string]any{"head_sha": other, "ship_sha": head})
	deployed := evPRLink(map[string]any{"head_sha": head, "deployed": map[string]any{"sha": head, "environment": "prod"}})
	accepted := evPRLink(map[string]any{"head_sha": head, "acceptance": map[string]any{"result": "passed", "sha": head}})
	advanced := evPRLink(map[string]any{"head_sha": other, "deployed": map[string]any{"sha": head, "environment": "prod"}})
	advancedAcc := evPRLink(map[string]any{"head_sha": other, "acceptance": map[string]any{"result": "passed", "sha": head}})
	rejected := evPRLink(map[string]any{"head_sha": head, "acceptance": map[string]any{"result": "failed", "sha": head}})

	tests := []struct {
		name  string
		dep   TaskDependency
		claim EvidenceClaim
		links []VCSLink
		actor bool
		want  EvidenceStatus
	}{
		{"shipped: SHIP on current HEAD", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped", HeadSHA: head}, []VCSLink{shipLink}, true, EvidenceTrusted},
		{"shipped: matching receipt from an actor without access to the blocker", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped", HeadSHA: head}, []VCSLink{shipLink}, false, EvidenceInvalid},
		{"deployed: matching receipt from an actor without access to the blocker", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head, Environment: "prod"}, []VCSLink{deployed}, false, EvidenceInvalid},
		{"shipped: old HEAD claim", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped", HeadSHA: other}, []VCSLink{shipLink}, true, EvidenceInvalid},
		{"shipped: no SHIP recorded is pending", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped", HeadSHA: head}, []VCSLink{noShip}, true, EvidencePending},
		{"shipped: SHIP for another HEAD is not trusted", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped", HeadSHA: head}, []VCSLink{staleShip}, true, EvidenceInvalid},
		{"shipped: another task's receipt (no links)", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped", HeadSHA: head}, nil, true, EvidencePending},
		{"shipped: no head_sha", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "shipped"}, []VCSLink{shipLink}, true, EvidenceInvalid},
		{"criterion mismatch", edge(DependencyConditionShipped), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head}, []VCSLink{shipLink}, true, EvidenceInvalid},
		{"custom criterion matches", TaskDependency{DependencyType: DependencyTypeBlocks, Condition: DependencyConditionShipped, Criterion: "S3 shipped"}, EvidenceClaim{URL: u, Criterion: "S3 shipped", HeadSHA: head}, []VCSLink{shipLink}, true, EvidenceTrusted},
		{"no criterion: link only", edge(DependencyConditionShipped), EvidenceClaim{URL: u, HeadSHA: head}, []VCSLink{shipLink}, true, EvidenceUnverified},
		{"deployed: same sha+env", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head, Environment: "prod"}, []VCSLink{deployed}, true, EvidenceTrusted},
		{"deployed: production claim matches a prod deploy", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head, Environment: " Production "}, []VCSLink{deployed}, true, EvidenceTrusted},
		{"deployed: other env", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head, Environment: "staging"}, []VCSLink{deployed}, true, EvidenceInvalid},
		{"deployed: other sha", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: other, Environment: "prod"}, []VCSLink{deployed}, true, EvidenceInvalid},
		{"deployed: nothing deployed yet", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head, Environment: "prod"}, []VCSLink{noShip}, true, EvidencePending},
		{"deployed: receipt of an old HEAD on an advanced link", edge(DependencyConditionDeployed), EvidenceClaim{URL: u, Criterion: "deployed", HeadSHA: head, Environment: "prod"}, []VCSLink{advanced}, true, EvidenceInvalid},
		{"accepted: receipt of an old HEAD on an advanced link", edge(DependencyConditionAccepted), EvidenceClaim{URL: u, Criterion: "accepted", HeadSHA: head}, []VCSLink{advancedAcc}, true, EvidenceInvalid},
		{"accepted: passed", edge(DependencyConditionAccepted), EvidenceClaim{URL: u, Criterion: "accepted", HeadSHA: head}, []VCSLink{accepted}, true, EvidenceTrusted},
		{"accepted: negative verdict", edge(DependencyConditionAccepted), EvidenceClaim{URL: u, Criterion: "accepted", HeadSHA: head}, []VCSLink{rejected}, true, EvidenceInvalid},
		{"manual: attested by authorized", edge(DependencyConditionAccessReady), EvidenceClaim{URL: u, Criterion: "access_ready", Attest: true}, nil, true, EvidenceTrusted},
		{"manual: unauthorized actor", edge(DependencyConditionAccessReady), EvidenceClaim{URL: u, Criterion: "access_ready", Attest: true}, nil, false, EvidenceInvalid},
		{"manual: bare URL (plan / HTTP 200 shell)", edge(DependencyConditionAccessReady), EvidenceClaim{URL: u, Criterion: "access_ready"}, nil, true, EvidenceUnverified},
		{"non-http URL", edge(DependencyConditionAccessReady), EvidenceClaim{URL: "file:///etc/passwd", Criterion: "access_ready", Attest: true}, nil, true, EvidenceInvalid},
		{"task_done takes no evidence", edge(DependencyConditionTaskDone), EvidenceClaim{URL: u, Criterion: "task_done"}, nil, true, EvidenceInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VerifyDependencyEvidence(tt.dep, tt.claim, tt.links, tt.actor)
			assert.Equal(t, tt.want, got.Status, got.Reason)
			if got.Status == EvidenceTrusted && tt.dep.Condition != DependencyConditionAccessReady {
				assert.NotNil(t, got.LinkID, "delivery evidence is bound to a pr link")
			}
		})
	}
}

// The stored claim environment is the canonical one, so the drift
// trigger compares it with the link's stored "prod" and does not revoke it.
func TestVerifyDependencyEvidence_ProductionClaimStoredAsProd(t *testing.T) {
	const head = "aaaa111"
	dep := TaskDependency{DependencyType: DependencyTypeBlocks, Condition: DependencyConditionDeployed}
	link := evPRLink(map[string]any{"head_sha": head, "deployed": map[string]any{"sha": head, "environment": "prod"}})
	ev := VerifyDependencyEvidence(dep, EvidenceClaim{URL: "https://git.example/mr/1", Criterion: "deployed", HeadSHA: head, Environment: "production"}, []VCSLink{link}, true)
	assert.Equal(t, EvidenceTrusted, ev.Status, ev.Reason)
	assert.Equal(t, VCSEnvProd, ev.Environment)

	staging := VerifyDependencyEvidence(dep, EvidenceClaim{URL: "https://git.example/mr/1", Criterion: "deployed", HeadSHA: head, Environment: "staging"}, []VCSLink{link}, true)
	assert.NotEqual(t, EvidenceTrusted, staging.Status, "staging claim never matches a prod deploy")
}

func TestNormalizeVCSEnvironment(t *testing.T) {
	for in, want := range map[string]string{
		"prod": "prod", "production": "prod", " Production ": "prod", "PRODUCTION": "prod",
		"staging": "staging", " Staging": "staging", "": "", "prod-eu": "prod-eu", "productions": "productions",
	} {
		assert.Equal(t, want, NormalizeVCSEnvironment(in), "input %q", in)
	}
}
