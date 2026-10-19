package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVCSLink_ShippedEvidence(t *testing.T) {
	green, red := "success", "failed"
	mk := func(status VCSLinkStatus, meta string, p *string) VCSLink {
		return VCSLink{LinkType: VCSLinkTypePR, Status: status, Metadata: []byte(meta), PipelineStatus: p}
	}
	cases := []struct {
		name string
		l    VCSLink
		want bool
	}{
		{"merged, matching shas, green", mk(VCSLinkStatusMerged, `{"head_sha":"a","ship_sha":"a","pipeline_sha":"a"}`, &green), true},
		{"sha with spaces is trimmed", mk(VCSLinkStatusMerged, `{"head_sha":" a ","ship_sha":"a","pipeline_sha":"a"}`, &green), true},
		{"open", mk(VCSLinkStatusOpen, `{"head_sha":"a","ship_sha":"a","pipeline_sha":"a"}`, &green), false},
		{"closed", mk(VCSLinkStatusClosed, `{"head_sha":"a","ship_sha":"a","pipeline_sha":"a"}`, &green), false},
		{"stale ship", mk(VCSLinkStatusMerged, `{"head_sha":"b","ship_sha":"a","pipeline_sha":"b"}`, &green), false},
		{"no ship", mk(VCSLinkStatusMerged, `{"head_sha":"a"}`, &green), false},
		{"cleared head", mk(VCSLinkStatusMerged, `{"head_sha":"","ship_sha":"a","pipeline_sha":""}`, &green), false},
		{"green pipeline ran on an older head", mk(VCSLinkStatusMerged, `{"head_sha":"a","ship_sha":"a","pipeline_sha":"old"}`, &green), false},
		{"green pipeline sha unknown", mk(VCSLinkStatusMerged, `{"head_sha":"a","ship_sha":"a"}`, &green), false},
		{"red pipeline", mk(VCSLinkStatusMerged, `{"head_sha":"a","ship_sha":"a","pipeline_sha":"a"}`, &red), false},
		{"no pipeline", mk(VCSLinkStatusMerged, `{"head_sha":"a","ship_sha":"a","pipeline_sha":"a"}`, nil), false},
		{"empty metadata", mk(VCSLinkStatusMerged, ``, &green), false},
		{"malformed metadata", mk(VCSLinkStatusMerged, `{`, &green), false},
		{"non-string sha", mk(VCSLinkStatusMerged, `{"head_sha":1,"ship_sha":1}`, &green), false},
		{"commit link", VCSLink{LinkType: VCSLinkTypeCommit, Status: VCSLinkStatusMerged, Metadata: []byte(`{"head_sha":"a","ship_sha":"a","pipeline_sha":"a"}`), PipelineStatus: &green}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, c.l.IsMergedShippedGreen()) })
	}
	l := mk(VCSLinkStatusMerged, `{"head_sha":"h","ship_sha":"s"}`, nil)
	assert.Equal(t, "h", l.HeadSHA())
	assert.Equal(t, "s", l.ShipSHA())
}
