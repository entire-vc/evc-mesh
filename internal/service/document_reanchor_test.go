package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// The three cases below are the ones measured on prod in #90dd31f9, in the order
// they were measured. Each asserts on the CONTENT the stored offsets address,
// never on the orphaned flag: a flag agreeing with itself while disagreeing with
// the document is precisely the defect, so a test reading the flag would have
// passed on the broken code.

// seedAnchoredComment puts a comment on the document anchored at the quote,
// resolving the anchor the way the API does — the server measures the offsets,
// so the fixture cannot smuggle in offsets the create path would have refused.
func seedAnchoredComment(t *testing.T, f *documentFixture, documentID uuid.UUID, body, quote string) uuid.UUID {
	t.Helper()

	anchor, err := mdoc.ResolveQuote(body, quote, "", "")
	require.NoError(t, err, "fixture quote must resolve, or the test is about something else")

	id := uuid.New()
	start, end := anchor.Start, anchor.End
	require.NoError(t, f.comments.Create(context.Background(), &domain.DocumentComment{
		ID:         id,
		DocumentID: documentID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       "комментарий",
		Anchor:     domain.NewDocumentCommentAnchor(anchor.Exact, anchor.Prefix, anchor.Suffix, &start, &end),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}))
	return id
}

// storedAnchor reads the anchor back out of the repository, which is what the
// API serves and what an agent reading JSON would get.
func storedAnchor(t *testing.T, f *documentFixture, commentID uuid.UUID) *domain.DocumentCommentAnchor {
	t.Helper()
	c, err := f.comments.GetByID(context.Background(), commentID)
	require.NoError(t, err)
	require.NotNil(t, c)
	return c.Anchor
}

// requireAnchorHonest is the invariant the defect broke, stated once: an anchor
// either addresses its own words or carries no position at all. Anything else —
// including the exact state prod was in — fails here.
func requireAnchorHonest(t *testing.T, body string, a *domain.DocumentCommentAnchor, _ ...string) {
	t.Helper()
	require.NotNil(t, a)
	if a.IsOrphaned() {
		return
	}
	require.NotNil(t, a.End)
	require.True(t, mdoc.SpanMatchesQuote(body, *a.Start, *a.End, a.Exact),
		"anchor [%d,%d) addresses %q while claiming to be about %q, and reports orphaned=false",
		*a.Start, *a.End, sliceOrOutOfRange(body, *a.Start, *a.End), a.Exact)
}

func sliceOrOutOfRange(body string, start, end int) string {
	if start < 0 || end > len(body) || end <= start {
		return "<out of range>"
	}
	return body[start:end]
}

func TestDocumentUpdate_ParagraphInsertedAboveTheQuote_AnchorFollowsIt(t *testing.T) {
	// CASE 1 on prod: start/end did not move, so the anchor came to address the
	// paragraph that had been inserted above it, and said orphaned:false.
	f := setupDocumentService(t)
	const v1 = "# Заголовок\n\nКомментарий цепляется вот за эту фразу целиком, и она уникальна.\n"
	doc := f.create(t, "Скратч", v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "вот за эту фразу целиком")

	before := storedAnchor(t, f, commentID)
	requireAnchorHonest(t, v1, before)

	v2 := "# Заголовок\n\nНовый абзац существует только чтобы дать якорю окружение слева.\n\n" +
		"Комментарий цепляется вот за эту фразу целиком, и она уникальна.\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v2})
	require.NoError(t, err)

	after := storedAnchor(t, f, commentID)
	requireAnchorHonest(t, v2, after)
	require.False(t, after.IsOrphaned(), "the quoted text is still there word for word")
	assert.Greater(t, *after.Start, *before.Start, "an insertion above the quote must push its offsets down")
}

func TestDocumentUpdate_QuoteRewritten_AnchorOrphans(t *testing.T) {
	// CASE 2 on prod: the sentence was rewritten and the anchor went on pointing
	// at the paragraph above it, still reporting orphaned:false.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nКомментарий цепляется вот за эту фразу целиком.\n"
	doc := f.create(t, "Скратч", v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "цепляется вот за эту фразу целиком")

	v3 := "Абзац один.\n\nЭту фразу переписали до неузнаваемости.\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v3})
	require.NoError(t, err)

	after := storedAnchor(t, f, commentID)
	requireAnchorHonest(t, v3, after)
	require.True(t, after.IsOrphaned(), "the text this comment was written about is gone")
	assert.Equal(t, "цепляется вот за эту фразу целиком", after.Exact,
		"an orphan keeps its quote — it is what the UI shows and what a later edit re-adopts it by")
}

func TestDocumentUpdate_FragmentDeleted_AnchorOrphans(t *testing.T) {
	// CASE 3 on prod.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nКомментарий цепляется вот за эту фразу целиком.\n\nТретий абзац.\n"
	doc := f.create(t, "Скратч", v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "Комментарий цепляется вот за эту фразу целиком.")

	v4 := "Абзац один.\n\n\n\nТретий абзац.\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v4})
	require.NoError(t, err)

	requireAnchorHonest(t, v4, storedAnchor(t, f, commentID))
	require.True(t, storedAnchor(t, f, commentID).IsOrphaned())
}

func TestDocumentUpdate_OrphanedAnchorIsReadoptedWhenItsTextReturns(t *testing.T) {
	// The consequence of orphaning by nulling the position rather than by
	// dropping the row, and the reason the pass runs over orphans rather than
	// treating them as settled. Undo is an ordinary thing for an editor to do.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nКомментарий цепляется вот за эту фразу целиком.\n"
	doc := f.create(t, "Скратч", v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "цепляется вот за эту фразу целиком")

	deleted := "Абзац один.\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &deleted})
	require.NoError(t, err)
	require.True(t, storedAnchor(t, f, commentID).IsOrphaned())

	restored := v1
	_, err = f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &restored})
	require.NoError(t, err)

	after := storedAnchor(t, f, commentID)
	require.False(t, after.IsOrphaned(), "the sentence is back, so the anchor is too")
	requireAnchorHonest(t, restored, after)
}

func TestDocumentUpdate_ResolvedThreadIsReanchoredToo(t *testing.T) {
	// Resolving a conversation says nothing about whether its offsets still
	// describe the text. Skipping resolved threads would leave exactly the rows
	// nobody re-reads pointing at whatever replaced them, which is the shape of
	// this defect with a smaller audience.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nРешённая ветка висит вот на этой фразе.\n"
	doc := f.create(t, "Скратч", v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе")

	c, err := f.comments.GetByID(context.Background(), commentID)
	require.NoError(t, err)
	at := time.Now()
	by := uuid.New()
	byType := domain.ActorTypeUser
	c.ResolvedAt, c.ResolvedBy, c.ResolvedByType = &at, &by, &byType
	require.NoError(t, f.comments.Update(context.Background(), c))

	v2 := "Ещё один абзац сверху, чтобы всё уехало вниз.\n\nАбзац один.\n\nРешённая ветка висит вот на этой фразе.\n"
	_, err = f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v2})
	require.NoError(t, err)

	requireAnchorHonest(t, v2, storedAnchor(t, f, commentID))
}

func TestDocumentUpdate_AppendReanchors(t *testing.T) {
	// An append moves nothing above it, so the offsets happen to stay right — but
	// the pass has to run anyway, because "nothing moved" is a property of this
	// append, not of appends. Asserted through the write counter: the rows alone
	// cannot tell "the pass ran and found nothing to move" from "the pass never
	// ran", and those have opposite meanings.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nЯкорь висит вот на этой фразе.\n"
	doc := f.create(t, "Скратч", v1)
	commentID := seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе")

	writesBefore := f.comments.AnchorWrites()
	tail := "\nдописанный хвост\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{AppendBody: &tail})
	require.NoError(t, err)

	assert.Greater(t, f.comments.AnchorWrites(), writesBefore, "an append rewrites the body, so the pass must run over it")
	requireAnchorHonest(t, v1+tail, storedAnchor(t, f, commentID))
}

func TestDocumentUpdate_TitleOnly_DoesNotTouchAnchors(t *testing.T) {
	// The pass is keyed on the body being rewritten, like the search reindex
	// beside it. A rename costs a document with a hundred comments nothing.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nЯкорь висит вот на этой фразе.\n"
	doc := f.create(t, "Скратч", v1)
	seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе")

	writesBefore := f.comments.AnchorWrites()
	title := "Переименовали"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Title: &title})
	require.NoError(t, err)

	assert.Equal(t, writesBefore, f.comments.AnchorWrites(), "a rename does not move a byte of the body")
}

func TestDocumentUpdate_UnanchoredCommentsAreNotGivenAPosition(t *testing.T) {
	// A comment on the document as a whole, and a reply inheriting its parent's
	// anchor, both have no quote. The pass must leave them alone rather than
	// invent somewhere for them to point.
	f := setupDocumentService(t)
	const v1 = "Абзац один.\n\nЯкорь висит вот на этой фразе.\n"
	doc := f.create(t, "Скратч", v1)

	plain := uuid.New()
	require.NoError(t, f.comments.Create(context.Background(), &domain.DocumentComment{
		ID: plain, DocumentID: doc.ID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "про документ целиком", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	v2 := "Совсем другой текст.\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v2})
	require.NoError(t, err)

	c, err := f.comments.GetByID(context.Background(), plain)
	require.NoError(t, err)
	assert.Nil(t, c.Anchor, "no quote, nothing to anchor")
}

func TestDocumentUpdate_ReanchorFailureDoesNotUnwindTheEdit(t *testing.T) {
	// The trade this pass makes, stated as a test rather than only as a comment.
	// The row and the body object are written before it runs and must stay
	// written: answering 500 would tell an author their edit was lost when it was
	// not. Both halves of the pass are broken separately, because "could not read
	// the anchors" and "could not write them" are different failures with the
	// same required outcome.
	for _, tc := range []struct {
		name   string
		break_ func(*MockDocumentCommentRepository)
	}{
		{"read fails", func(m *MockDocumentCommentRepository) { m.FailAnchorListWith(errors.New("boom")) }},
		{"write fails", func(m *MockDocumentCommentRepository) { m.FailAnchorWriteWith(errors.New("boom")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupDocumentService(t)
			const v1 = "Абзац один.\n\nЯкорь висит вот на этой фразе.\n"
			doc := f.create(t, "Скратч", v1)
			seedAnchoredComment(t, f, doc.ID, v1, "висит вот на этой фразе")
			tc.break_(f.comments)

			v2 := "Ещё абзац сверху.\n\nАбзац один.\n\nЯкорь висит вот на этой фразе.\n"
			updated, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v2})

			require.NoError(t, err, "the author's edit succeeded and must not be reported as failed")
			assert.Equal(t, v2, string(f.storage.objects[updated.StorageKey]), "the body is stored")
		})
	}
}

// v1Repeated is a checklist whose two identical lines have identical
// neighbourhoods, so no context can tell them apart — the case ResolveQuote is
// built to refuse and Reanchor has to answer anyway. The paragraph between them
// is load-bearing: it makes the two occurrences further apart than the edits
// below move them, which is what the position tie-break needs to be able to
// work at all. See TestDocumentUpdate_RepeatedQuote_LargeInsertionTakesTheWrongCopy
// for what happens when that stops being true.
const v1Repeated = "# Чек-лист\n\n- the API returns 200\n\n" +
	"Между двумя одинаковыми пунктами лежит абзац: он объясняет, чем первый случай отличается от второго, и занимает заметно больше места, чем обычная правка сверху.\n\n" +
	"- the API returns 200\n"

const duplicatedLine = "the API returns 200"

// seedRepeatedAnchor hangs a comment on the SECOND of the two identical lines.
func seedRepeatedAnchor(t *testing.T, f *documentFixture, documentID uuid.UUID) uuid.UUID {
	t.Helper()
	at := strings.LastIndex(v1Repeated, duplicatedLine)
	end := at + len(duplicatedLine)
	id := uuid.New()
	require.NoError(t, f.comments.Create(context.Background(), &domain.DocumentComment{
		ID: id, DocumentID: documentID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body:      "про второй пункт",
		Anchor:    domain.NewDocumentCommentAnchor(duplicatedLine, "- ", "\n", &at, &end),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	return id
}

// requireResolveQuoteRefuses is the guard that gives the two tests below their
// meaning: with the anchor's OWN context — not with empty context, which asks a
// different question — ResolveQuote cannot pick an occurrence and gives up. Give
// the two lines different surroundings and it succeeds, both tests pass either
// way, and neither is about anything any more.
func requireResolveQuoteRefuses(t *testing.T, body string) {
	t.Helper()
	_, err := mdoc.ResolveQuote(body, duplicatedLine, "- ", "\n")
	var ambiguous *mdoc.AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous, "guard: this only distinguishes Reanchor from ResolveQuote while ResolveQuote actually refuses")
	require.Equal(t, 2, ambiguous.Matches)
}

func TestDocumentUpdate_RepeatedQuoteIsNotOrphanedByAnUnrelatedEdit(t *testing.T) {
	// Why the service calls Reanchor and not ResolveQuote — which is the shape
	// option 1 was worded in on the card.
	//
	// ResolveQuote REFUSES an ambiguous quote. That is right on the create path,
	// where the caller can be asked for more context, and wrong here, where there
	// is nobody to ask: the refusal would orphan a comment whose sentence is
	// still on the page, word for word. A checklist that repeats a line is not
	// exotic, and under ResolveQuote a document like this would shed its comments
	// on the first edit of any other paragraph.
	f := setupDocumentService(t)
	doc := f.create(t, "Скратч", v1Repeated)
	id := seedRepeatedAnchor(t, f, doc.ID)

	v2 := strings.Replace(v1Repeated, "# Чек-лист\n\n", "# Чек-лист\n\nвводный абзац\n\n", 1)
	requireResolveQuoteRefuses(t, v2)

	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v2})
	require.NoError(t, err)

	after := storedAnchor(t, f, id)
	require.False(t, after.IsOrphaned(), "the sentence is still on the page, twice over — orphaning it would be a lie in the other direction")
	requireAnchorHonest(t, v2, after)
	assert.Equal(t, strings.LastIndex(v2, duplicatedLine), *after.Start,
		"the position it used to hold is the tie-break: it was on the second line and stays there")
}

func TestDocumentUpdate_RepeatedQuote_LargeInsertionTakesTheWrongCopy(t *testing.T) {
	// The honest limit of the position tie-break, pinned so that a green suite
	// is not read as "it always picks the right copy".
	//
	// Proximity is measured against the offset the anchor used to hold, so an
	// insertion LARGER than the gap between two identical lines carries the
	// nearest-copy title over to the earlier one. The frontend's tie-break has
	// the same property, by the same arithmetic, and matching it is deliberate:
	// two implementations disagreeing about which of two identical lines a
	// highlight belongs to is worse than one imperfect answer both give.
	//
	// It is the right trade because the two copies are IDENTICAL — text and
	// neighbourhood both. A comment shown against the other one reads the same;
	// orphaning it loses the thread. Wrong-but-equivalent beats gone.
	f := setupDocumentService(t)
	doc := f.create(t, "Скратч", v1Repeated)
	id := seedRepeatedAnchor(t, f, doc.ID)

	huge := strings.Repeat("вставленный сверху абзац, длиннее расстояния между пунктами.\n\n", 6)
	v2 := strings.Replace(v1Repeated, "# Чек-лист\n\n", "# Чек-лист\n\n"+huge, 1)
	requireResolveQuoteRefuses(t, v2)

	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &v2})
	require.NoError(t, err)

	after := storedAnchor(t, f, id)
	require.False(t, after.IsOrphaned())
	requireAnchorHonest(t, v2, after, "whichever copy it picked, it is on its own words — which is the invariant that matters")
	assert.Equal(t, strings.Index(v2, duplicatedLine), *after.Start,
		"documented limit: the insertion outgrew the gap, so the FIRST copy is now the nearest one")
}
