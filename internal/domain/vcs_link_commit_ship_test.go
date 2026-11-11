package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// commitShipLink builds a commit link; shipBy uuid.Nil leaves ship_set out.
func commitShipLink(sha, ship string, shipBy uuid.UUID) VCSLink {
	meta := map[string]any{}
	if ship != "" {
		meta[VCSLinkMetaShipSHA] = ship
	}
	if shipBy != uuid.Nil {
		meta[VCSLinkMetaShipSet] = map[string]string{"by_id": shipBy.String(), "by_type": "agent", "at": "2026-11-10T20:00:00Z"}
	}
	b, _ := json.Marshal(meta)
	return VCSLink{LinkType: VCSLinkTypeCommit, ExternalID: sha, Metadata: b}
}

func TestCommitShipGaps(t *testing.T) {
	author, reviewer := uuid.New(), uuid.New()
	sha, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	noStampID := commitShipLink(sha, sha, uuid.Nil)
	noStampID.Metadata = json.RawMessage(`{"ship_sha":"` + sha + `","ship_set":{"by_type":"agent"}}`)
	pr := commitShipLink(sha, sha, reviewer)
	pr.LinkType = VCSLinkTypePR

	cases := []struct {
		name   string
		link   VCSLink
		author *uuid.UUID
		want   []string
	}{
		{"ship on the commit by another agent", commitShipLink(sha, sha, reviewer), &author, nil},
		{"ship written by the author", commitShipLink(sha, sha, author), &author, []string{ShipGapByAuthor}},
		{"ship names another commit", commitShipLink(sha, other, reviewer), &author, []string{ShipGapStaleShipSHA}},
		{"no ship_sha", commitShipLink(sha, "", uuid.Nil), &author, []string{ShipGapMissingShipSHA}},
		{"ship_sha without a stamp", commitShipLink(sha, sha, uuid.Nil), &author, []string{ShipGapWriterUnknown}},
		{"stamp without by_id", noStampID, &author, []string{ShipGapWriterUnknown}},
		{"short external_id", commitShipLink(sha[:12], sha, reviewer), &author, []string{ShipGapNotFullSHA}},
		{"unknown author fails closed", commitShipLink(sha, sha, reviewer), nil, []string{ShipGapByAuthor}},
		{"pr link is not judged here", pr, &author, []string{ShipGapNotCommit}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.link.CommitShipGaps(c.author))
		})
	}
}

func TestShipSet_ReadsStamp(t *testing.T) {
	by := uuid.New()
	l := commitShipLink(strings.Repeat("a", 40), strings.Repeat("a", 40), by)
	st := l.ShipSet()
	if assert.NotNil(t, st) {
		assert.Equal(t, by.String(), st.ByID)
		assert.Equal(t, "agent", st.ByType)
	}
	assert.Nil(t, commitShipLink(strings.Repeat("a", 40), "", uuid.Nil).ShipSet())
}

// ship_set is written only by Mesh; the agent PATCH whitelist must not name it.
func TestAgentWritableVCSMetadataKeys_ExcludeShipSet(t *testing.T) {
	assert.False(t, AgentWritableVCSMetadataKeys[VCSLinkMetaShipSet])
	assert.False(t, AgentWritableVCSMetadataKeys[VCSLinkMetaRoleSet])
}
