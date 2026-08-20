package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// The HTTP half of "a comment names its text, not its offsets".
//
// The service resolves the quote; these tests cover the two things only the
// handler can get wrong: carrying the new fields through at all, and rendering a
// resolver refusal as something an agent can act on.

// The quote fields have to reach the service. Bound but dropped, the request
// would succeed as an UNANCHORED comment — a silent downgrade, since a comment
// with no anchor is a perfectly valid thing to store.
func TestDocumentCommentHandler_Create_CarriesTheQuoteFieldsThrough(t *testing.T) {
	docID, wsID, userID := uuid.New(), uuid.New(), uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New(), DocumentID: in.DocumentID, Body: in.Body}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	body := `{"body":"это противоречит абзацу выше",` +
		`"quote":"первым делом","quote_prefix":"образ** ","quote_suffix":", потом"}`
	c, rec := docCommentListRequest(e, http.MethodPost, docID.String(), &wsID, "/", body)
	c.Set("user_id", userID)

	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "первым делом", got.Quote)
	assert.Equal(t, "образ** ", got.QuotePrefix)
	assert.Equal(t, ", потом", got.QuoteSuffix)
	assert.Nil(t, got.Anchor, "a quote request carries no anchor — that is the whole point of it")
}

// The existing client is untouched: a request with an anchor and no quote arrives
// exactly as it did before, with the quote fields empty rather than zero-valued
// into something the service would try to resolve.
func TestDocumentCommentHandler_Create_AnchorOnlyRequestGainsNoQuote(t *testing.T) {
	wsID := uuid.New()
	var got service.CreateDocumentCommentInput

	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, in service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			got = in
			return &domain.DocumentComment{ID: uuid.New()}, nil
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	body := `{"body":"needs a comma","anchor":{"exact":"the API","start":10,"end":17}}`
	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/", body)
	c.Set("user_id", uuid.New())

	require.NoError(t, h.Create(c))

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Empty(t, got.Quote)
	assert.Empty(t, got.QuotePrefix)
	assert.Empty(t, got.QuoteSuffix)
	require.NotNil(t, got.Anchor)
	require.NotNil(t, got.Anchor.Start)
	assert.Equal(t, 10, *got.Anchor.Start, "the measured-offsets path is unchanged")
}

// AC2's second half, and the reason it is written as an assertion rather than
// assumed: the match count has to survive the handler.
//
// The service passes *mdoc.AmbiguousQuoteError through untouched precisely so
// that handleError can render the number. A handler that flattened it into a
// generic 400 — or worse, let it fall through to a 500 — would leave an agent
// with "that did not work" and no way to tell that adding a few words of context
// is the fix. So this asserts on the rendered JSON, not on the status alone.
func TestDocumentCommentHandler_Create_AmbiguousQuoteRendersItsMatchCount(t *testing.T) {
	mockSvc := &MockDocumentCommentService{
		CreateFunc: func(_ context.Context, _ service.CreateDocumentCommentInput) (*domain.DocumentComment, error) {
			return nil, &mdoc.AmbiguousQuoteError{Quote: "потом миграция", Matches: 2}
		},
	}
	h, e := setupDocumentCommentTest(mockSvc)

	wsID := uuid.New()
	c, rec := docCommentListRequest(e, http.MethodPost, uuid.New().String(), &wsID, "/",
		`{"body":"это противоречит абзацу выше","quote":"потом миграция"}`)
	c.Set("user_id", uuid.New())

	require.NoError(t, h.Create(c))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "ambiguous_quote", resp["code"])
	assert.EqualValues(t, 2, resp["matches"],
		"the count is the actionable part of this refusal; without it the caller only knows it failed")
	assert.Contains(t, resp["message"], "2 times")
}
