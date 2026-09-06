package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The "episodic 17%" trap (#a9752575, audit §4.6): the recall default threshold
// and the score this service assigns to session-checkpoints were set
// independently, and the checkpoint score sat BELOW the threshold. The result
// was a default under which the fleet's own session hand-offs — the single class
// written specifically to be read by the next session — could never be returned.
//
// The assertion below is deliberately a RELATIONSHIP, not the literal 0.3.
// Pinning the number would pass just as happily if someone moved the checkpoint
// score down to 0.2 tomorrow, which is the exact defect re-created. What must
// hold is that the floor never rises above the lowest score we ourselves mint.
func TestDefaultMinImportanceDoesNotHideAnyKindWeMint(t *testing.T) {
	// computeImportanceScore is the single writer of importance_score; session-checkpoint
	// is its documented minimum ("a downgrade: overrides positive kind: tags").
	checkpoint := computeImportanceScore([]string{"kind:session-checkpoint"}, "")

	require.LessOrEqual(t, float64(checkpoint), float64(defaultMinImportance)+1e-6,
		"sanity: session-checkpoint is expected to be the LOWEST score we assign")

	assert.LessOrEqual(t, float64(defaultMinImportance), float64(checkpoint)+1e-6,
		"the default recall threshold must not exclude session-checkpoints: they are "+
			"written for the next session to read, so a default that hides them makes "+
			"every caller override it (which is what the spawn prompts used to do)")
}

// A checkpoint carrying a "positive" kind: tag must still be visible by default.
// computeImportanceScore treats session-checkpoint as a downgrade that overrides the other
// kind: tags, so this is the lowest-scoring realistic entry a lane can produce.
func TestSessionCheckpointVisibleUnderDefaultThreshold(t *testing.T) {
	for _, tags := range [][]string{
		{"kind:session-checkpoint"},
		{"kind:session-checkpoint", "kind:decision"},
		{"kind:decision", "kind:session-checkpoint"},
	} {
		got := computeImportanceScore(tags, "")
		assert.GreaterOrEqual(t, float64(got), float64(defaultMinImportance)-1e-6,
			"tags %v scored %.2f, below the %.2f default — it would be invisible to a "+
				"plain recall()", tags, got, defaultMinImportance)
	}
}
