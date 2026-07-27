package service

import "strings"

// Chunk is a slice of a larger text. Start and End are RUNE offsets (not byte
// offsets) into the original string — chunking always operates on runes so a
// multi-byte character (e.g. Cyrillic) is never split across a chunk boundary.
type Chunk struct {
	Text  string
	Start int
	End   int
}

const (
	// defaultChunkSize and defaultChunkOverlap are sized from the live
	// measurement in #e8063a65: the prod embedder (TEI, multilingual-e5-small)
	// silently truncates at 512 tokens (~2000 chars). ~1500 chars stays safely
	// under that window; ~300 chars of overlap is what the measurement used to
	// keep a fact sitting on a chunk boundary from being dropped out of both
	// chunks.
	defaultChunkSize    = 1500
	defaultChunkOverlap = 300
)

// turnMarkers are the speaker-turn prefixes chunkText looks for at the start
// of a line (after leading whitespace) to decide whether text is a
// conversation transcript.
var turnMarkers = []string{"User:", "Assistant:"}

// chunkText splits text into overlapping chunks sized to fit under an
// embedding model's input window.
//
// When text looks like a conversation transcript (a line starts with a
// turnMarker), chunkText packs whole turns into each chunk so a turn is never
// split mid-sentence — unless that single turn alone exceeds chunkSize, in
// which case only that turn falls back to a plain sliding window. Text with
// no turn markers at all is chunked by a plain sliding character window.
//
// chunkSize and overlap are in runes. chunkSize <= 0, or overlap outside
// [0, chunkSize), falls back to the package defaults above.
//
// Chunks always overlap by exactly `overlap` runes (except the first) so a
// fact sitting on a boundary in the original text is never dropped entirely
// — see the rank-35-vs-rank-1 measurement in #e8063a65.
func chunkText(text string, chunkSize, overlap int) []Chunk {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if overlap < 0 || overlap >= chunkSize {
		overlap = defaultChunkOverlap
	}

	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= chunkSize {
		return []Chunk{{Text: text, Start: 0, End: len(runes)}}
	}

	if starts := turnStarts(runes); starts != nil {
		return packTurns(runes, starts, chunkSize, overlap)
	}
	return slidingWindow(runes, chunkSize, overlap)
}

// turnStarts returns the rune offsets where a new speaker turn begins — a
// line that, after leading whitespace, starts with a turnMarker — or nil if
// no marker occurs anywhere in runes.
func turnStarts(runes []rune) []int {
	var starts []int
	lineStart := 0
	for i := 0; i <= len(runes); i++ {
		if i != len(runes) && runes[i] != '\n' {
			continue
		}
		line := runes[lineStart:i]
		trimmed := strings.TrimLeft(string(line), " \t")
		for _, marker := range turnMarkers {
			if strings.HasPrefix(trimmed, marker) {
				leading := len(line) - len([]rune(trimmed))
				starts = append(starts, lineStart+leading)
				break
			}
		}
		lineStart = i + 1
	}
	return starts
}

// turnSpan is one speaker turn as a rune range [start, end) into the original
// text.
type turnSpan struct{ start, end int }

// turnSpans converts turn-start offsets into contiguous spans covering the
// whole text: any text before the first marker becomes a leading span (so it
// is never silently dropped), then one span per marker to the next marker
// (or end of text).
func turnSpans(totalRunes int, starts []int) []turnSpan {
	spans := make([]turnSpan, 0, len(starts)+1)
	if starts[0] > 0 {
		spans = append(spans, turnSpan{0, starts[0]})
	}
	for i, s := range starts {
		end := totalRunes
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		spans = append(spans, turnSpan{s, end})
	}
	return spans
}

// packTurns greedily packs whole speaker turns into chunks of at most
// chunkSize runes each, seeding every chunk after the first with up to
// `overlap` trailing runes of the previous chunk. A turn longer than
// chunkSize on its own is split via slidingWindow instead of being packed.
func packTurns(runes []rune, starts []int, chunkSize, overlap int) []Chunk {
	spans := turnSpans(len(runes), starts)

	var chunks []Chunk
	cur := make([]rune, 0, chunkSize)
	curStart := spans[0].start

	flush := func() {
		if len(cur) == 0 {
			return
		}
		chunks = append(chunks, Chunk{Text: string(cur), Start: curStart, End: curStart + len(cur)})
	}

	for _, sp := range spans {
		spanLen := sp.end - sp.start
		if spanLen == 0 {
			continue
		}

		if spanLen > chunkSize {
			flush()
			cur = cur[:0]
			for _, c := range slidingWindow(runes[sp.start:sp.end], chunkSize, overlap) {
				chunks = append(chunks, Chunk{Text: c.Text, Start: sp.start + c.Start, End: sp.start + c.End})
			}
			curStart = sp.end
			continue
		}

		if len(cur) > 0 && len(cur)+spanLen > chunkSize {
			flush()
			tail := cur
			if len(tail) > overlap {
				tail = tail[len(tail)-overlap:]
			}
			curStart = curStart + len(cur) - len(tail)
			cur = append(cur[:0], tail...)
		}
		cur = append(cur, runes[sp.start:sp.end]...)
	}
	flush()

	return chunks
}

// slidingWindow chunks runes by a plain sliding character window: chunks of
// chunkSize runes, each subsequent chunk starting chunkSize-overlap runes
// after the previous one, so consecutive chunks share `overlap` runes.
func slidingWindow(runes []rune, chunkSize, overlap int) []Chunk {
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= chunkSize {
		return []Chunk{{Text: string(runes), Start: 0, End: len(runes)}}
	}

	step := chunkSize - overlap
	var chunks []Chunk
	for start := 0; start < len(runes); start += step {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, Chunk{Text: string(runes[start:end]), Start: start, End: end})
		if end == len(runes) {
			break
		}
	}
	return chunks
}
