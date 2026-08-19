//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end for the quote-anchored comment: an agent sends the text it is
// commenting on and the server works out where that text sits.
//
// It runs against the live API, real Postgres and real object storage, because
// the thing under test is a path rather than a function: the body has to make the
// round trip through S3 and come back byte-identical for the offsets computed
// against it to mean anything. A fake store would return whatever it was handed
// and the round trip — the part that can actually corrupt a body — would go
// untested.
//
// The body is Russian throughout. On ASCII a byte offset and a character offset
// are the same number, so an ASCII version of this test passes against a server
// using either, which is how the defect it guards against reached production once
// already.
const quoteDocBody = "# Рунбук отката\n\n" +
	"Сначала верните образ, потом применяйте миграцию. Токен берётся из 1Password.\n\n" +
	"Порядок отката: **сначала верните образ**, потом миграцию. Токен обязателен.\n"

// createDocumentWithBody posts a document carrying a chosen body and returns its id.
func (tn *tenant) createDocumentWithBody(t *testing.T, title, body string) string {
	t.Helper()

	resp := tn.env.Post(t, "/api/v1/projects/"+tn.projectID+"/documents",
		map[string]any{"title": title, "body": body})
	raw := tn.env.ReadBody(t, resp)

	if resp.StatusCode == http.StatusServiceUnavailable ||
		(resp.StatusCode == http.StatusInternalServerError && strings.Contains(string(raw), "storage")) {
		t.Skipf("object storage not available in this environment, skipping: %s", string(raw))
	}
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"fixture setup failed: POST /projects/%s/documents returned %d: %s",
		tn.projectID, resp.StatusCode, string(raw))

	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode the created document: %s", string(raw))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, "the created document has no id: %s", string(raw))
	return id
}

// postQuoteComment leaves a comment named by quote rather than by offsets.
func (tn *tenant) postQuoteComment(t *testing.T, docID string, payload map[string]any) (*http.Response, []byte) {
	t.Helper()
	resp := tn.env.Post(t, "/api/v1/documents/"+docID+"/comments", payload)
	return resp, tn.env.ReadBody(t, resp)
}

// anchorOf pulls the anchor out of a created comment.
func anchorOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created), "cannot decode the comment: %s", string(raw))
	anchor, ok := created["anchor"].(map[string]any)
	require.True(t, ok, "the created comment carries no anchor: %s", string(raw))
	return anchor
}

// TestDocumentComments_QuoteResolvesToByteOffsetsOnCyrillic is the acceptance run
// for the whole path.
func TestDocumentComments_QuoteResolvesToByteOffsetsOnCyrillic(t *testing.T) {
	tn := newTenant(t, "dcq-quote")
	docID := tn.createDocumentWithBody(t, "dcq рунбук", quoteDocBody)

	quote := "Сначала верните образ, потом применяйте миграцию."

	resp, raw := tn.postQuoteComment(t, docID, map[string]any{
		"body":  "это правило про порядок деплоя, а не про откат",
		"quote": quote,
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(raw))

	anchor := anchorOf(t, raw)
	start, ok := anchor["start"].(float64)
	require.True(t, ok, "the anchor has no start: %s", string(raw))
	end, ok := anchor["end"].(float64)
	require.True(t, ok, "the anchor has no end: %s", string(raw))

	assert.Equal(t, quote, anchor["exact"])
	assert.Equal(t, false, anchor["orphaned"], "an anchor the server just located is not an orphan")

	// The criterion, stated the way it can actually be checked: slice the body
	// BYTES with the offsets that came back and see the quote.
	assert.Equal(t, quote, quoteDocBody[int(start):int(end)],
		"the stored offsets do not slice back to the quote")

	// And the discriminator, so this cannot pass on a server counting characters.
	characterOffset := utf8.RuneCountInString(quoteDocBody[:int(start)])
	require.NotEqual(t, int(start), characterOffset,
		"fixture is vacuous: byte and character offsets coincide at this position")
	assert.NotEqual(t, characterOffset, int(start),
		"the server returned a character offset (%d) where the byte offset is %d",
		characterOffset, int(start))
}

// A quote that crosses inline markup: the reader selected rendered text, the
// stored range covers the source that produced it, and the two are not equal.
func TestDocumentComments_QuoteSpanningMarkupResolves(t *testing.T) {
	tn := newTenant(t, "dcq-markup")
	docID := tn.createDocumentWithBody(t, "dcq разметка", quoteDocBody)

	quote := "сначала верните образ, потом миграцию."

	resp, raw := tn.postQuoteComment(t, docID, map[string]any{
		"body":  "здесь порядок верный",
		"quote": quote,
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(raw))

	anchor := anchorOf(t, raw)
	slice := quoteDocBody[int(anchor["start"].(float64)):int(anchor["end"].(float64))]
	assert.Contains(t, slice, "**", "the raw range carries the markup the quote does not")
	assert.Equal(t, quote, anchor["exact"])
}

// An ambiguous quote is refused, the count travels in the refusal, and nothing is
// written — a comment on the wrong occurrence of a repeated phrase is exactly as
// wrong as one on the wrong document, and much harder to spot.
func TestDocumentComments_AmbiguousQuoteIsRefusedWithTheCount(t *testing.T) {
	tn := newTenant(t, "dcq-ambig")
	docID := tn.createDocumentWithBody(t, "dcq неоднозначность", quoteDocBody)

	resp, raw := tn.postQuoteComment(t, docID, map[string]any{
		"body":  "какой именно токен?",
		"quote": "Токен",
	})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "response: %s", string(raw))
	assert.Contains(t, string(raw), "2 times", "the caller needs the count: %s", string(raw))
	assert.Contains(t, string(raw), "quote_context", "and what to send instead")

	listResp := tn.env.Get(t, "/api/v1/documents/"+docID+"/comments")
	listRaw := tn.env.ReadBody(t, listResp)
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	assert.NotContains(t, string(listRaw), "какой именно токен",
		"the refused comment must not have been written to the first occurrence")
}

// The same quote, narrowed, lands on the occurrence the context names.
func TestDocumentComments_QuoteContextNarrowsARepeatedQuote(t *testing.T) {
	tn := newTenant(t, "dcq-narrow")
	docID := tn.createDocumentWithBody(t, "dcq уточнение", quoteDocBody)

	resp, raw := tn.postQuoteComment(t, docID, map[string]any{
		"body":          "этот токен обязателен",
		"quote":         "Токен",
		"quote_context": "миграцию. Токен обязателен.",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "response: %s", string(raw))

	anchor := anchorOf(t, raw)
	start := int(anchor["start"].(float64))
	assert.Equal(t, "Токен", quoteDocBody[start:int(anchor["end"].(float64))])
	assert.Equal(t, strings.LastIndex(quoteDocBody, "Токен"), start,
		"the context named the second occurrence, and that is where it should have landed")
}

// A quote that is not in the document is said to be missing, in those words.
func TestDocumentComments_MissingQuoteIsRefusedClearly(t *testing.T) {
	tn := newTenant(t, "dcq-missing")
	docID := tn.createDocumentWithBody(t, "dcq отсутствует", quoteDocBody)

	resp, raw := tn.postQuoteComment(t, docID, map[string]any{
		"body":  "не могу найти",
		"quote": "этой фразы в документе нет",
	})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "response: %s", string(raw))
	assert.Contains(t, string(raw), "no such quote in the document", "response: %s", string(raw))
}

// Offsets and a quote together are refused: an anchor is a caller saying where the
// text is, a quote is the same caller saying it does not know.
func TestDocumentComments_QuoteAndAnchorTogetherIsRefused(t *testing.T) {
	tn := newTenant(t, "dcq-both")
	docID := tn.createDocumentWithBody(t, "dcq оба", quoteDocBody)

	resp, raw := tn.postQuoteComment(t, docID, map[string]any{
		"body":  "и то и другое",
		"quote": "Токен обязателен.",
		"anchor": map[string]any{
			"exact": "Токен обязателен.", "prefix": "", "suffix": "",
			"start": 10, "end": 27,
		},
	})

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "response: %s", string(raw))
	assert.Contains(t, string(raw), "not both", "response: %s", string(raw))
}
