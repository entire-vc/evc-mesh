package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is the AC-required "test failing on current code" (#b052cdda): before
// chunkText existed, embedAndStore/BatchEmbed called s.embedder.Embed(ctx,
// text) exactly once with the FULL, unchunked text — the prod embedder then
// silently truncates at 512 tokens (~2000 chars, measured live in #e8063a65)
// and only the first ~15% of a long memory ever entered the vector. This test
// pins the fix at the unit level: a document far longer than the truncation
// window must be split into more than one embeddable piece, and — the part
// truncation would fail — text from PAST the old truncation point must
// actually appear in one of those pieces.
func TestChunkText_LongDocumentIsNotSilentlyTruncated(t *testing.T) {
	const truncationWindowChars = 2000 // ~512 tokens, per the live TEI measurement in #e8063a65

	filler := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200) // ~9200 chars
	needle := "COMMUTE_TAKES_45_MINUTES_EACH_WAY"
	// Place the needle well past where a single 512-token embed call would
	// have cut off, mirroring the live incident (fact at ~75% into a 13163-char doc).
	doc := filler[:truncationWindowChars+500] + " " + needle + " " + filler[truncationWindowChars+500:]

	chunks := chunkText(doc, defaultChunkSize, defaultChunkOverlap)

	require.Greater(t, len(chunks), 1, "a document longer than the embedder's window must produce more than one chunk")

	found := false
	for _, c := range chunks {
		if strings.Contains(c.Text, needle) {
			found = true
			break
		}
	}
	assert.True(t, found, "the fact placed past the old single-embed truncation point must survive into some chunk's own text")
}

func TestChunkText_ShortTextIsSingleChunk(t *testing.T) {
	text := "short memory content"
	chunks := chunkText(text, defaultChunkSize, defaultChunkOverlap)

	require.Len(t, chunks, 1)
	assert.Equal(t, text, chunks[0].Text)
	assert.Equal(t, 0, chunks[0].Start)
	assert.Equal(t, len([]rune(text)), chunks[0].End)
}

func TestChunkText_Empty(t *testing.T) {
	assert.Nil(t, chunkText("", defaultChunkSize, defaultChunkOverlap))
}

func TestChunkText_SlidingWindow_NoSpeakerMarkers(t *testing.T) {
	text := strings.Repeat("abcdefghij", 500) // 5000 chars, no "User:"/"Assistant:" anywhere

	chunks := chunkText(text, 1500, 300)
	require.Greater(t, len(chunks), 1)

	for i, c := range chunks {
		assert.LessOrEqual(t, len([]rune(c.Text)), 1500, "chunk %d exceeds chunkSize", i)
		assert.Equal(t, c.Text, string([]rune(text)[c.Start:c.End]), "chunk %d Start/End must round-trip to its own Text", i)
	}

	// Reassembling with overlap removed must reproduce the source exactly —
	// proves no content was skipped between chunks.
	var rebuilt strings.Builder
	rebuilt.WriteString(chunks[0].Text)
	for i := 1; i < len(chunks); i++ {
		prevEnd := chunks[i-1].End
		newStart := chunks[i].Start
		require.LessOrEqual(t, newStart, prevEnd, "chunk %d must overlap or abut the previous chunk, not skip ahead", i)
		skip := prevEnd - newStart
		runes := []rune(chunks[i].Text)
		rebuilt.WriteString(string(runes[skip:]))
	}
	assert.Equal(t, text, rebuilt.String())
}

func TestChunkText_ConsecutiveChunksActuallyOverlap(t *testing.T) {
	text := strings.Repeat("x", 5000)
	chunks := chunkText(text, 1500, 300)
	require.Greater(t, len(chunks), 1)

	for i := 1; i < len(chunks); i++ {
		overlapLen := chunks[i-1].End - chunks[i].Start
		assert.Equal(t, 300, overlapLen, "chunk %d should start exactly `overlap` runes before the previous chunk ended", i)
	}
}

func TestChunkText_SpeakerTurns_NoTurnIsEverTruncated(t *testing.T) {
	var wantLines []string
	var b strings.Builder
	for i := 0; i < 30; i++ {
		u := fmt.Sprintf("User: question number %d, padded so this line has some real length to it.", i)
		a := fmt.Sprintf("Assistant: answer number %d, also padded with enough text to matter for chunk sizing here.", i)
		wantLines = append(wantLines, u, a)
		b.WriteString(u + "\n")
		b.WriteString(a + "\n")
	}
	text := b.String()
	require.Greater(t, len([]rune(text)), 1500, "fixture must exceed chunkSize to exercise multi-chunk packing")

	chunks := chunkText(text, 500, 100)
	require.Greater(t, len(chunks), 1)

	// The primary invariant from the task spec ("cut on turn boundaries, not
	// blindly by character"): every whole turn from the source text must
	// appear intact, as an exact line, in at least one chunk. The overlap
	// seed at the START of a chunk is allowed to be a partial tail from the
	// previous chunk (that's what overlap means) — what must never happen is
	// a turn being torn apart at its OWN primary chunk boundary.
	for _, want := range wantLines {
		found := false
		for _, c := range chunks {
			for _, line := range strings.Split(c.Text, "\n") {
				if line == want {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		assert.True(t, found, "turn line was never found intact in any chunk: %q", want)
	}
}

func TestChunkText_OneOversizedTurnFallsBackToSlidingWindow(t *testing.T) {
	huge := "User: " + strings.Repeat("word ", 1000) // one turn, ~5000 chars, alone bigger than chunkSize
	text := huge + "\nAssistant: short reply\n"

	chunks := chunkText(text, 1500, 300)
	require.Greater(t, len(chunks), 2, "the oversized turn alone must split into multiple chunks via sliding window")

	for i, c := range chunks {
		assert.LessOrEqual(t, len([]rune(c.Text)), 1500, "chunk %d exceeds chunkSize even for the oversized-turn fallback", i)
	}
}

func TestChunkText_DefaultsAppliedOnInvalidParams(t *testing.T) {
	text := strings.Repeat("y", 5000)

	viaZero := chunkText(text, 0, 0)
	viaDefaults := chunkText(text, defaultChunkSize, defaultChunkOverlap)
	assert.Equal(t, len(viaDefaults), len(viaZero))

	// overlap >= chunkSize is invalid and must fall back to the default too.
	viaBadOverlap := chunkText(text, 1500, 1500)
	assert.Equal(t, len(viaDefaults), len(viaBadOverlap))
}
