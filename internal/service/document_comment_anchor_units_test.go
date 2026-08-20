package service

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The anchor columns are documented as UTF-8 byte offsets into the markdown, and
// until the guard these tests cover, nothing enforced that: the service read the
// sign and the order of the offsets and never opened the document, so the
// coordinate system was whatever the writing client used.
//
// Every test here is on Cyrillic, deliberately. A character index and a byte
// offset are the same number for ASCII, so the ASCII version of this file would
// pass in full against a service with no check in it at all.
const cyrillicDocumentBody = "# Регламент выката\n\n" +
	"Если прод отдаёт 500 — **откатите образ** первым делом, потом миграция.\n\n" +
	"Второй абзац существует, чтобы промах было куда деть.\n"

// seedCyrillicBody replaces the fixture document's markdown with Russian prose.
func seedCyrillicBody(t *testing.T, f *documentCommentFixture) {
	t.Helper()
	f.docs.Seed(&domain.Document{
		ID: f.documentID, ProjectID: f.projectID, Title: "Регламент", Body: cyrillicDocumentBody,
	})
}

// anchoredInput is the create the guard judges: one quote, one span, nothing else
// varying between the positive and the negative case.
func anchoredInput(f *documentCommentFixture, quote string, start, end int) CreateDocumentCommentInput {
	return CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        "это предложение противоречит абзацу выше",
		Anchor: &domain.DocumentCommentAnchor{
			Exact: quote,
			Start: intPointer(start),
			End:   intPointer(end),
		},
		AuthorID:   f.author,
		AuthorType: domain.ActorTypeUser,
	}
}

// AC1 — the model PR #619 writes: UTF-8 byte offsets into the markdown.
func TestDocumentCommentService_Create_AcceptsByteOffsetsOnACyrillicDocument(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedCyrillicBody(t, f)

	quote := "первым делом"
	at := strings.Index(cyrillicDocumentBody, quote)
	require.GreaterOrEqual(t, at, 0)

	c, err := f.svc.Create(context.Background(), anchoredInput(f, quote, at, at+len(quote)))

	require.NoError(t, err)
	require.NotNil(t, c.Anchor)
	require.NotNil(t, c.Anchor.Start)
	assert.Equal(t, at, *c.Anchor.Start, "the anchor must be stored exactly as sent, not repaired")
}

// AC2 — the load-bearing negative control: the model PR #621 writes, character
// indices rather than byte offsets. This is the case that used to be accepted in
// silence, and the row it produced pointed at a different sentence.
func TestDocumentCommentService_Create_RejectsCharacterOffsetsOnACyrillicDocument(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedCyrillicBody(t, f)

	// This quote is chosen so that its CHARACTER offsets still land on valid UTF-8
	// boundaries. A character index usually does — it just lands on the wrong
	// character — and a guard that only checked the encoding would pass this
	// anchor while it points at another word entirely. Picking a quote whose
	// character offsets happened to split a rune would let this test go green
	// against exactly the weak check it exists to rule out.
	quote := "потом миграция"
	byteStart := strings.Index(cyrillicDocumentBody, quote)
	require.GreaterOrEqual(t, byteStart, 0)
	charStart := utf8.RuneCountInString(cyrillicDocumentBody[:byteStart])
	charEnd := charStart + utf8.RuneCountInString(quote)

	// Without this the assertion could pass on a body where the two units happen
	// to agree — which is every ASCII body, and is why this test is in Russian.
	require.NotEqual(t, byteStart, charStart,
		"fixture is degenerate: character index equals byte offset, nothing is being tested")
	require.True(t, utf8.RuneStart(cyrillicDocumentBody[charStart]),
		"fixture is degenerate: the wrong offsets split a character, so an encoding check "+
			"alone would reject them and this test would prove nothing")
	require.True(t, utf8.RuneStart(cyrillicDocumentBody[charEnd]),
		"fixture is degenerate: see above, for the end offset")
	require.NotContains(t, cyrillicDocumentBody[charStart:charEnd], quote,
		"fixture is degenerate: the wrong span happens to contain the quote")

	_, err := f.svc.Create(context.Background(), anchoredInput(f, quote, charStart, charEnd))

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())

	// The message has to name the units, or the client cannot tell this apart
	// from "your quote is wrong" and will retry with the same offsets.
	assert.Contains(t, strings.ToUpper(apiErr.Validation["anchor.start"]), "BYTE")

	// And nothing may have been written: a rejected anchor that still lands is
	// the failure this whole card is about.
	assert.Empty(t, f.comments.items, "a refused anchor must not leave a row behind")
}

// AC3 — the legitimate case a naive body[start:end] == exact guard would break:
// the selection crossed inline markup, so the raw slice carries `**` and the
// quote, taken from the rendered text, does not.
func TestDocumentCommentService_Create_AcceptsASelectionAcrossMarkup(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedCyrillicBody(t, f)

	quote := "откатите образ первым делом"
	start := strings.Index(cyrillicDocumentBody, "откатите")
	require.GreaterOrEqual(t, start, 0)
	end := start + len("откатите образ** первым делом")

	require.NotEqual(t, quote, cyrillicDocumentBody[start:end],
		"fixture is degenerate: the raw slice already equals the quote")

	_, err := f.svc.Create(context.Background(), anchoredInput(f, quote, start, end))
	require.NoError(t, err, "the guard must not reject an anchor whose selection crossed markup")
}

// AC4 — a comment on the document as a whole, and an orphaned one. Neither
// carries offsets, so there is nothing for the guard to judge, and refusing them
// would lose comments the API is documented to accept.
func TestDocumentCommentService_Create_LeavesUnpositionedAnchorsAlone(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedCyrillicBody(t, f)

	t.Run("no anchor at all", func(t *testing.T) {
		in := anchoredInput(f, "", 0, 0)
		in.Anchor = nil
		c, err := f.svc.Create(context.Background(), in)
		require.NoError(t, err)
		assert.Nil(t, c.Anchor)
	})

	t.Run("quote with no offsets is orphaned, not refused", func(t *testing.T) {
		in := anchoredInput(f, "", 0, 0)
		in.Anchor = &domain.DocumentCommentAnchor{Exact: "текста больше нет в документе"}
		c, err := f.svc.Create(context.Background(), in)
		require.NoError(t, err)
		require.NotNil(t, c.Anchor)
		assert.True(t, c.Anchor.IsOrphaned())
	})
}

// A quote that is simply not in the document is refused for the same reason: the
// offsets cannot be pointing at it.
func TestDocumentCommentService_Create_RejectsAQuoteThatIsNotInTheDocument(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedCyrillicBody(t, f)

	_, err := f.svc.Create(context.Background(), anchoredInput(f, "этой фразы здесь нет", 20, 40))

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// The guard fails closed. A body it cannot read is not evidence that the anchor
// is fine — and the whole premise of this check is that a wrong anchor is
// indistinguishable from a right one once it is in the table.
func TestDocumentCommentService_Create_RefusesAnAnchorWhenTheBodyCannotBeRead(t *testing.T) {
	f := setupDocumentCommentService(t)

	svc := &documentCommentService{
		commentRepo:  f.comments,
		documentRepo: f.docs,
		documentBody: nil,
	}

	_, err := svc.Create(context.Background(), f.createInput())
	require.Error(t, err)
	assert.Empty(t, f.comments.items)

	// The unanchored comment is unaffected: it never needed the body.
	in := f.createInput()
	in.Anchor = nil
	_, err = svc.Create(context.Background(), in)
	require.NoError(t, err)
}

// The guard runs after the reply check, and this pins that order. A reply that
// carries an anchor at all is refused whatever its offsets say, and "a reply
// inherits its parent's anchor" is the answer that tells the caller what to
// change; leading with "your offsets are wrong" would send them to fix the one
// thing that is not the problem.
func TestDocumentCommentService_Create_ReplyWithAnAnchorFailsOnBeingAReply(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedCyrillicBody(t, f)

	quote := "первым делом"
	at := strings.Index(cyrillicDocumentBody, quote)
	root, err := f.svc.Create(context.Background(), anchoredInput(f, quote, at, at+len(quote)))
	require.NoError(t, err)

	// Offsets that are wrong AND on a reply: the reply rule has to be the one
	// that answers.
	reply := anchoredInput(f, quote, 0, 4)
	reply.ParentCommentID = &root.ID

	_, err = f.svc.Create(context.Background(), reply)

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation["anchor"], "inherits its parent's anchor")
}
