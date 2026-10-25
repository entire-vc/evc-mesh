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
