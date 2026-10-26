package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Released is the one Go definition of "this edge no longer holds the
// dependent"; DependencyReleasedSQL is checked against it in the postgres DB test.
func TestTaskDependencyReleased(t *testing.T) {
	url, met := "https://mesh.example/e", time.Now()
	open := BlockerState{Category: StatusCategoryInProgress}
	done := BlockerState{Category: StatusCategoryDone}
	cancelled := BlockerState{Category: StatusCategoryCancelled}
	shippedOpen := BlockerState{Category: StatusCategoryInProgress, IsShipped: true}
	deployedOpen := BlockerState{Category: StatusCategoryInProgress, HasDeployment: true}
	deployedDone := BlockerState{Category: StatusCategoryDone, HasDeployment: true}

	edge := func(c DependencyCondition) TaskDependency {
		return TaskDependency{DependencyType: DependencyTypeBlocks, Condition: c}
	}
	evidenced := func(c DependencyCondition) TaskDependency {
		d := edge(c)
		d.EvidenceURL, d.ConditionMetAt = &url, &met
		return d
	}

	cases := []struct {
		name string
		dep  TaskDependency
		b    BlockerState
		want bool
	}{
		// closed (task_done, also the empty legacy value): done or cancelled, as before.
		{"task_done/open", edge(DependencyConditionTaskDone), open, false},
		{"task_done/done", edge(DependencyConditionTaskDone), done, true},
		{"task_done/cancelled", edge(DependencyConditionTaskDone), cancelled, true},
		{"empty condition behaves as task_done/cancelled", edge(""), cancelled, true},
		{"task_done/shipped but open still holds", edge(DependencyConditionTaskDone), shippedOpen, false},
		// evidence-only conditions keep the terminal-status release they had.
		{"access_ready/open", edge(DependencyConditionAccessReady), open, false},
		{"access_ready/done", edge(DependencyConditionAccessReady), done, true},
		{"contract_ready/cancelled", edge(DependencyConditionContractReady), cancelled, true},
		{"artifact_published/open+evidence", evidenced(DependencyConditionArtifactPublished), open, true},
		// shipped: is_shipped or done; cancelled never.
		{"shipped/open", edge(DependencyConditionShipped), open, false},
		{"shipped/done", edge(DependencyConditionShipped), done, true},
		{"shipped/is_shipped", edge(DependencyConditionShipped), shippedOpen, true},
		{"shipped/cancelled", edge(DependencyConditionShipped), cancelled, false},
		{"shipped/deployed but not shipped or done", edge(DependencyConditionShipped), deployedOpen, false},
		// deployed: is_shipped or a recorded deploy; done alone is merged, not deployed.
		{"deployed/open", edge(DependencyConditionDeployed), open, false},
		{"deployed/done only", edge(DependencyConditionDeployed), done, false},
		{"deployed/cancelled", edge(DependencyConditionDeployed), cancelled, false},
		{"deployed/is_shipped", edge(DependencyConditionDeployed), shippedOpen, true},
		{"deployed/deploy recorded", edge(DependencyConditionDeployed), deployedOpen, true},
		{"deployed/done and deploy recorded", edge(DependencyConditionDeployed), deployedDone, true},
		// accepted: done only; cancelled is not acceptance.
		{"accepted/open", edge(DependencyConditionAccepted), open, false},
		{"accepted/done", edge(DependencyConditionAccepted), done, true},
		{"accepted/cancelled", edge(DependencyConditionAccepted), cancelled, false},
		{"accepted/is_shipped alone", edge(DependencyConditionAccepted), shippedOpen, false},
		{"accepted/deployed alone", edge(DependencyConditionAccepted), deployedOpen, false},
		// evidence releases any typed edge whatever the blocker is.
		{"accepted/cancelled+evidence", evidenced(DependencyConditionAccepted), cancelled, true},
		{"deployed/open+evidence", evidenced(DependencyConditionDeployed), open, true},
		{"shipped/open+evidence", evidenced(DependencyConditionShipped), open, true},
		// a met condition without evidence_url does not release.
		{"deployed/met without evidence", TaskDependency{DependencyType: DependencyTypeBlocks, Condition: DependencyConditionDeployed, ConditionMetAt: &met}, open, false},
		// a non-blocks edge never holds anything.
		{"relates_to", TaskDependency{DependencyType: DependencyTypeRelatesTo}, open, true},
		{"is_child_of with accepted-looking state", TaskDependency{DependencyType: DependencyTypeIsChildOf}, cancelled, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.dep.Released(tc.b), tc.name)
	}
}

func TestDependencyConditionShippedIsValid(t *testing.T) {
	assert.True(t, DependencyConditionShipped.Valid())
	for _, c := range []DependencyCondition{DependencyConditionTaskDone, DependencyConditionAccessReady,
		DependencyConditionContractReady, DependencyConditionArtifactPublished, DependencyConditionDeployed, DependencyConditionAccepted} {
		assert.True(t, c.Valid(), string(c))
	}
	assert.False(t, DependencyCondition("closed").Valid(), "the earlier draft name is not a stored value")
}

func TestTaskDependencyNeedsDeploymentLookup(t *testing.T) {
	url, met := "https://mesh.example/e", time.Now()
	assert.True(t, TaskDependency{DependencyType: DependencyTypeBlocks, Condition: DependencyConditionDeployed}.NeedsDeploymentLookup())
	assert.False(t, TaskDependency{DependencyType: DependencyTypeBlocks, Condition: DependencyConditionAccepted}.NeedsDeploymentLookup())
	assert.False(t, TaskDependency{DependencyType: DependencyTypeBlocks, Condition: DependencyConditionDeployed, EvidenceURL: &url, ConditionMetAt: &met}.NeedsDeploymentLookup(),
		"evidence already releases it: no vcs_links query")
}

func TestDependencyReleasedSQLShape(t *testing.T) {
	q := DependencyReleasedSQL("td", "bt", "bts.category")
	for _, want := range []string{
		"td.dependency_type <> 'blocks'", "td.condition <> 'task_done'", // non-blocks + evidence half
		"WHEN 'shipped' THEN (bt.is_shipped OR bts.category = 'done')",
		"WHEN 'deployed' THEN (bt.is_shipped OR EXISTS (SELECT 1 FROM vcs_links dv WHERE dv.task_id = bt.id",
		"WHEN 'accepted' THEN bts.category = 'done'",
		"ELSE bts.category IN ('done', 'cancelled')",
	} {
		assert.Contains(t, q, want)
	}
	assert.True(t, strings.HasPrefix(q, "COALESCE("), "a NULL verdict (missing blocker) must read as not released")
}

func TestVCSLinkJSONExposesFlatDeploymentFields(t *testing.T) {
	sha40 := strings.Repeat("a", 40)
	none := VCSLink{Metadata: json.RawMessage(`{}`)}
	b, err := json.Marshal(none)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	for _, k := range []string{"deployed_sha", "deployed_env", "deployed_at"} {
		v, present := out[k]
		assert.True(t, present, k+" is always present")
		assert.Nil(t, v, k+" is null before a deploy")
	}
	assert.False(t, none.IsDeployed())

	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	l := VCSLink{Metadata: json.RawMessage(`{"deployed":{"sha":"` + sha40 + `","environment":"production","at":"` + at.Format(time.RFC3339) + `"}}`)}
	b, err = json.Marshal(l)
	require.NoError(t, err)
	out = map[string]any{}
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, sha40, out["deployed_sha"])
	assert.Equal(t, "production", out["deployed_env"])
	assert.Equal(t, "2026-10-25T10:00:00Z", out["deployed_at"])
	assert.True(t, l.IsDeployed())

	noAt := VCSLink{Metadata: json.RawMessage(`{"deployed":{"sha":"` + sha40 + `","environment":"production"}}`)}
	assert.False(t, noAt.IsDeployed(), "a deploy without a time is not a recorded deploy")
}
