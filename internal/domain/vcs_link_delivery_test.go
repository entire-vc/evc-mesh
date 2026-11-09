package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prLink(meta string) VCSLink {
	return VCSLink{LinkType: VCSLinkTypePR, Status: VCSLinkStatusMerged, Metadata: json.RawMessage(meta)}
}

func TestVCSLink_DeliveryAccessors(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	l := prLink(`{"implementation_sha":"` + a + `","merged_sha":"` + b + `",
		"ci_verdict":{"status":"success","pipeline_id":7,"sha":"` + b + `"},
		"deployed":{"sha":"` + b + `","environment":"prod"},
		"acceptance":{"result":"passed","by":"qa","evidence_url":"https://x"}}`)

	d := l.Delivery()
	assert.Equal(t, a, d.ImplementationSHA)
	assert.Equal(t, b, d.MergedSHA)
	require.NotNil(t, d.CIVerdict)
	assert.EqualValues(t, 7, d.CIVerdict.PipelineID)
	require.NotNil(t, d.Deployed)
	assert.Equal(t, "prod", d.Deployed.Environment)
	require.NotNil(t, d.Acceptance)
	assert.Equal(t, "qa", d.Acceptance.By)
	assert.True(t, d.IsShipped)
	assert.True(t, l.IsShipped())
}

func TestVCSLink_DeliveryAbsentAndMalformed(t *testing.T) {
	for _, meta := range []string{``, `{}`, `not json`, `{"deployed":"x","acceptance":1,"ci_verdict":[]}`, `{"deployed":{},"acceptance":{},"ci_verdict":{}}`} {
		l := prLink(meta)
		assert.Nil(t, l.Deployed(), meta)
		assert.Nil(t, l.Acceptance(), meta)
		assert.Nil(t, l.CIVerdict(), meta)
		assert.False(t, l.IsShipped(), meta)
	}
}

func TestVCSLink_IsShippedMergedWithoutDeploy(t *testing.T) {
	m := strings.Repeat("c", 40)
	assert.False(t, prLink(`{"merged_sha":"`+m+`"}`).IsShipped())
	assert.False(t, prLink(`{"merged_sha":"`+m+`","deployed":{"sha":"`+m+`","environment":"staging"}}`).IsShipped())
}

func TestVCSLink_MarshalJSONAddsDeliveryKeepsStoredFields(t *testing.T) {
	l := prLink(`{"head_sha":"h"}`)
	l.URL = "https://git.example/x/-/merge_requests/1"
	b, err := json.Marshal(l)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, l.URL, out["url"])
	assert.Equal(t, map[string]any{"head_sha": "h"}, out["metadata"])
	assert.Equal(t, false, out["is_shipped"])
	delivery, ok := out["delivery"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, delivery["is_shipped"])
}

func TestVCSLink_DeliveryStatesMatrix(t *testing.T) {
	h, m, o := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	green := "success"
	mk := func(status VCSLinkStatus, meta string) VCSLink {
		l := prLink(meta)
		l.Status = status
		l.PipelineStatus = &green
		return l
	}
	cases := []struct {
		name    string
		l       VCSLink
		want    VCSDeliveryStates
		shipped bool
	}{
		{"merged + CODE SHIP + green, never installed (merged-only example)",
			mk(VCSLinkStatusMerged, `{"head_sha":"`+h+`","ship_sha":"`+h+`","pipeline_sha":"`+h+`"}`),
			VCSDeliveryStates{"yes", "yes", "unknown", "unknown"}, false},
		{"installed, no product acceptance",
			mk(VCSLinkStatusMerged, `{"head_sha":"`+h+`","ship_sha":"`+h+`","merged_sha":"`+m+`","deployed":{"sha":"`+m+`","environment":"prod"}}`),
			VCSDeliveryStates{"yes", "yes", "yes", "unknown"}, true},
		{"accepted with immutable receipt",
			mk(VCSLinkStatusMerged, `{"head_sha":"`+h+`","ship_sha":"`+h+`","merged_sha":"`+m+`","deployed":{"sha":"`+m+`","environment":"prod"},"acceptance":{"result":"passed","sha":"`+m+`","evidence_url":"https://x"}}`),
			VCSDeliveryStates{"yes", "yes", "yes", "passed"}, true},
		{"new HEAD after SHIP: review stale, nothing inherited",
			mk(VCSLinkStatusMerged, `{"head_sha":"`+o+`","ship_sha":"`+h+`"}`),
			VCSDeliveryStates{"yes", "stale", "unknown", "unknown"}, false},
		{"stale receipt: deployed and accepted on an older sha",
			mk(VCSLinkStatusMerged, `{"head_sha":"`+h+`","ship_sha":"`+h+`","merged_sha":"`+m+`","deployed":{"sha":"`+o+`","environment":"prod"},"acceptance":{"result":"passed","sha":"`+o+`"}}`),
			VCSDeliveryStates{"yes", "yes", "stale", "stale"}, false},
		{"open MR, no evidence at all",
			mk(VCSLinkStatusOpen, `{}`),
			VCSDeliveryStates{"no", "unknown", "unknown", "unknown"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.l.DeliveryStates())
			assert.Equal(t, c.shipped, c.l.IsShipped())
			assert.Equal(t, c.shipped, c.l.Delivery().IsShipped)
			assert.Equal(t, c.want, c.l.Delivery().States)
		})
	}
	// acceptance bound to a sha that is not the current commit, or to none.
	unmerged := mk(VCSLinkStatusOpen, `{"head_sha":"`+o+`","acceptance":{"result":"passed","sha":"`+h+`"}}`)
	assert.Equal(t, DeliveryStateStale, unmerged.DeliveryStates().Accepted, "accepted an older head")
	noSHA := mk(VCSLinkStatusMerged, `{"merged_sha":"`+m+`","acceptance":{"result":"passed"}}`)
	assert.Equal(t, DeliveryStateUnknown, noSHA.DeliveryStates().Accepted, "unbound acceptance is not passed")
	// no recorded status is unknown, a closed MR is no.
	assert.Equal(t, DeliveryStateUnknown, mk(VCSLinkStatus(""), `{}`).DeliveryStates().Merged)
	assert.Equal(t, DeliveryStateNo, mk(VCSLinkStatusClosed, `{}`).DeliveryStates().Merged)
	// same-sha staging deploy is "no" (not in production), never stale.
	staging := mk(VCSLinkStatusMerged, `{"merged_sha":"`+m+`","deployed":{"sha":"`+m+`","environment":"staging"}}`)
	assert.Equal(t, DeliveryStateNo, staging.DeliveryStates().Installed)
	assert.False(t, staging.IsShipped())
	// prod deploy but no merged sha to compare: unknown.
	noMerged := mk(VCSLinkStatusMerged, `{"deployed":{"sha":"`+m+`","environment":"prod"}}`)
	assert.Equal(t, DeliveryStateUnknown, noMerged.DeliveryStates().Installed)
	// ship_sha without a known head is unknown, not stale.
	noHead := mk(VCSLinkStatusMerged, `{"ship_sha":"`+h+`"}`)
	assert.Equal(t, DeliveryStateUnknown, noHead.DeliveryStates().Reviewed)
	// unmerged link with a stale merged_sha: acceptance binds to the head.
	staleMerged := mk(VCSLinkStatusOpen, `{"head_sha":"`+o+`","merged_sha":"`+m+`","acceptance":{"result":"passed","sha":"`+m+`"}}`)
	assert.Equal(t, DeliveryStateStale, staleMerged.DeliveryStates().Accepted)
	// research / source-only: a commit link carries no merge or review fact.
	commit := VCSLink{LinkType: VCSLinkTypeCommit}
	assert.Equal(t, VCSDeliveryStates{"na", "na", "unknown", "unknown"}, commit.DeliveryStates())
	assert.False(t, commit.IsShipped())
}
