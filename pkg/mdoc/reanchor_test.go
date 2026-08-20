package mdoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The invariant every one of these asserts, and the one the defect broke: after
// an edit, an anchor either sits on its own words or has no position at all.
// Asserting on the returned ok flag alone would restate what the function
// returned; SpanMatchesQuote asks the document.
func requireOnItsOwnWords(t *testing.T, body string, a *Anchor) {
	t.Helper()
	require.NotNil(t, a)
	require.True(t, SpanMatchesQuote(body, a.Start, a.End, a.Exact),
		"anchor [%d,%d) reads %q, not its own quote %q", a.Start, a.End, safeSlice(body, a.Start, a.End), a.Exact)
}

func safeSlice(body string, start, end int) string {
	if start < 0 || end > len(body) || end <= start {
		return "<out of range>"
	}
	return body[start:end]
}

func TestReanchor_TextInsertedAbove_FollowsItsQuote(t *testing.T) {
	// CASE 1 from the prod measurement: a paragraph appears before the quote, so
	// every offset below it moves. This is the case that produced an anchor
	// reading somebody else's sentence while reporting orphaned:false.
	before := "# Заголовок\n\nКомментарий цепляется вот за эту фразу целиком, и она уникальна.\n"
	after := "# Заголовок\n\nНовый абзац существует только чтобы дать якорю окружение слева.\n\nКомментарий цепляется вот за эту фразу целиком, и она уникальна.\n"

	original, err := ResolveQuote(before, "вот за эту фразу целиком", "", "")
	require.NoError(t, err)
	requireOnItsOwnWords(t, before, original)

	moved, ok := Reanchor(after, *original)
	require.True(t, ok, "the quote is still in the document word for word")
	requireOnItsOwnWords(t, after, moved)
	require.Greater(t, moved.Start, original.Start, "an insertion above the quote must push it down")
}

func TestReanchor_QuoteRewritten_Orphans(t *testing.T) {
	// CASE 2: the quoted sentence itself is rewritten. There is nothing to point
	// at any more, and saying so is the whole point — the old behaviour kept the
	// offsets and kept claiming they were fine.
	before := "Абзац один.\n\nКомментарий цепляется вот за эту фразу целиком.\n"
	after := "Абзац один.\n\nЭту фразу переписали до неузнаваемости.\n"

	original, err := ResolveQuote(before, "цепляется вот за эту фразу целиком", "", "")
	require.NoError(t, err)

	_, ok := Reanchor(after, *original)
	require.False(t, ok, "the quoted text is gone; the anchor has to admit it")
}

func TestReanchor_FragmentDeleted_Orphans(t *testing.T) {
	// CASE 3: the whole fragment is deleted.
	before := "Абзац один.\n\nКомментарий цепляется вот за эту фразу целиком.\n\nАбзац три.\n"
	after := "Абзац один.\n\n\n\nАбзац три.\n"

	original, err := ResolveQuote(before, "Комментарий цепляется вот за эту фразу целиком.", "", "")
	require.NoError(t, err)

	_, ok := Reanchor(after, *original)
	require.False(t, ok)
}

func TestReanchor_RepeatedQuote_TakesTheOneNearestItsOldPosition(t *testing.T) {
	// The reason Reanchor exists beside ResolveQuote. This quote occurs three
	// times with identical neighbourhoods, so context settles nothing and
	// ResolveQuote refuses — correctly, on the create path, where the caller can
	// be asked for more. Refusing here would orphan a comment whose text is still
	// there, three times over, the first time anybody edits the page.
	body := "раз the API два\n\nтри the API четыре\n\nпять the API шесть\n"
	_, err := ResolveQuote(body, "the API", "", "")
	require.Error(t, err, "guard: if this stops being ambiguous the test below proves nothing")
	var ambiguous *AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
	require.Equal(t, 3, ambiguous.Matches)

	second := strings.Index(body, "три the API") + len("три ")
	third := strings.Index(body, "пять the API") + len("пять ")

	for _, tc := range []struct {
		name string
		hint int
		want int
	}{
		{"anchored on the second, edit shifted it by a few bytes", second + 3, second},
		{"anchored on the third", third - 2, third},
		{"anchored on the first", 0, strings.Index(body, "the API")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Reanchor(body, Anchor{Exact: "the API", Start: tc.hint})
			require.True(t, ok)
			require.Equal(t, tc.want, got.Start)
			requireOnItsOwnWords(t, body, got)
		})
	}
}

func TestReanchor_ContextBeatsProximity(t *testing.T) {
	// Proximity is the TIE-BREAK, not the rule. A quote whose remembered
	// neighbourhood survived belongs where that neighbourhood is, even when a
	// different copy of it sits closer to the stale offset — otherwise an edit
	// that moves a paragraph would hand the anchor to the nearest lookalike.
	body := "alpha the API omega\n\nbravo the API charlie\n"
	nearest := strings.Index(body, "the API")
	contextual := strings.LastIndex(body, "the API")

	got, ok := Reanchor(body, Anchor{
		Exact:  "the API",
		Prefix: "bravo ",
		Suffix: " charlie",
		Start:  nearest, // deliberately pointing at the wrong one
	})
	require.True(t, ok)
	require.Equal(t, contextual, got.Start, "context must outrank a nearer copy")
}

func TestReanchor_RetakesNeighbourhoodFromTheNewDocument(t *testing.T) {
	// The neighbourhood describes where the quote sits NOW. Carrying the old one
	// forward would make each edit's context a little more wrong than the last,
	// and the context is what disambiguates the next re-anchor.
	after := "совершенно новое окружение слева, quoted text, и справа тоже новое\n"

	got, ok := Reanchor(after, Anchor{Exact: "quoted text", Prefix: "старое окружение", Suffix: "старый хвост", Start: 0})
	require.True(t, ok)
	require.Contains(t, got.Prefix, "окружение слева")
	require.Contains(t, got.Suffix, "и справа")
	require.NotContains(t, got.Prefix, "старое")
}

func TestReanchor_OrphanWhoseTextComesBack_IsReadopted(t *testing.T) {
	// An orphan keeps its quote, so an edit that restores the sentence restores
	// the anchor. This is why orphaning nulls the position rather than dropping
	// the row, and it is the reason the pass runs over already-orphaned anchors
	// too instead of skipping them as settled.
	restored := "Абзац один.\n\nКомментарий цепляется вот за эту фразу целиком.\n"

	got, ok := Reanchor(restored, Anchor{Exact: "цепляется вот за эту фразу целиком"})
	require.True(t, ok)
	requireOnItsOwnWords(t, restored, got)
}

func TestReanchor_CyrillicOffsetsAreBytes(t *testing.T) {
	// The unit the columns are documented in, and the one #633 had to enforce
	// because two clients disagreed about it. A re-anchored offset that reverted
	// to character indices would be a silent regression of that fix — and, on
	// ASCII, an invisible one, hence a Cyrillic body.
	body := "Первый абзац на кириллице, он длинный.\n\nЦелевая фраза здесь.\n"
	got, ok := Reanchor(body, Anchor{Exact: "Целевая фраза здесь."})
	require.True(t, ok)
	require.Equal(t, strings.Index(body, "Целевая фраза здесь."), got.Start)
	require.Equal(t, "Целевая фраза здесь.", body[got.Start:got.End])
	require.Greater(t, got.Start, len([]rune(body[:got.Start])), "bytes, not runes")
}

func TestReanchor_QuoteAcrossInlineMarkup_StillFound(t *testing.T) {
	// The quote comes from the rendered text and the offsets index the markdown,
	// so a selection that crossed `**bold**` has a quote the raw slice does not
	// equal. ResolveQuote's tolerant scan handles that on create; the re-anchor
	// has to use the same scan or every emphasised sentence would orphan on the
	// first edit.
	after := "вступление\n\nоткатите **образ** первым делом, потом миграция\n"
	got, ok := Reanchor(after, Anchor{Exact: "откатите образ первым делом"})
	require.True(t, ok)
	requireOnItsOwnWords(t, after, got)
	require.NotEqual(t, got.Exact, after[got.Start:got.End], "guard: this fixture is only interesting while the slice carries markup")
}

func TestReanchor_NoQuote_IsNotAnAnchorToMove(t *testing.T) {
	// A comment on the document as a whole, or a reply inheriting its parent's
	// anchor. Nothing to find, and inventing a position for it would be worse
	// than leaving it alone.
	_, ok := Reanchor("любой текст", Anchor{Exact: ""})
	require.False(t, ok)
	_, ok = Reanchor("любой текст", Anchor{Exact: "   "})
	require.False(t, ok)
}
