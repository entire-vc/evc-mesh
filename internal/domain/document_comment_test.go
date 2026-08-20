package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

// The three states an anchor can be in have to stay distinguishable: a comment
// with no anchor is not the same thing as one whose anchor lost its position, and
// collapsing them is how an orphan silently becomes a document-level comment.
func TestNewDocumentCommentAnchor(t *testing.T) {
	t.Run("no quote is no anchor", func(t *testing.T) {
		assert.Nil(t, NewDocumentCommentAnchor("", "", "", intPtr(1), intPtr(2)),
			"an anchor with no quote can never be re-found and must not be representable")
	})

	t.Run("anchored", func(t *testing.T) {
		a := NewDocumentCommentAnchor("the API", "authenticate ", " with a token", intPtr(10), intPtr(17))
		require.NotNil(t, a)
		assert.Equal(t, "the API", a.Exact)
		assert.Equal(t, "authenticate ", a.Prefix)
		assert.Equal(t, " with a token", a.Suffix)
		require.NotNil(t, a.Start)
		assert.Equal(t, 10, *a.Start)
		require.NotNil(t, a.End)
		assert.Equal(t, 17, *a.End)
		assert.False(t, a.IsOrphaned())
	})

	t.Run("orphaned keeps the quote", func(t *testing.T) {
		a := NewDocumentCommentAnchor("the API", "authenticate ", "", nil, nil)
		require.NotNil(t, a)
		assert.True(t, a.IsOrphaned())
		assert.Equal(t, "the API", a.Exact,
			"the quote is what a re-anchoring pass searches with; losing it loses the comment's subject")
	})
}

// IsOrphaned is called on a *DocumentCommentAnchor that is frequently nil (a
// reply, a document-level comment), so it has to answer rather than panic — and
// it must answer "no": nothing was anchored, so nothing came unanchored.
func TestDocumentCommentAnchor_IsOrphaned_NilIsNotOrphaned(t *testing.T) {
	var a *DocumentCommentAnchor
	assert.False(t, a.IsOrphaned())
}

// `orphaned` is emitted, never stored or bound. If it were a field a client could
// set, it could be set to something the offsets beside it contradict.
func TestDocumentCommentAnchor_MarshalJSON(t *testing.T) {
	t.Run("anchored", func(t *testing.T) {
		raw, err := json.Marshal(NewDocumentCommentAnchor("q", "p", "s", intPtr(3), intPtr(4)))
		require.NoError(t, err)

		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, false, got["orphaned"])
		assert.Equal(t, "q", got["exact"])
		assert.Equal(t, float64(3), got["start"])
		assert.Equal(t, float64(4), got["end"])
	})

	t.Run("orphaned", func(t *testing.T) {
		raw, err := json.Marshal(NewDocumentCommentAnchor("q", "", "", nil, nil))
		require.NoError(t, err)

		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, true, got["orphaned"])
		assert.Nil(t, got["start"], "an orphan reports no position rather than a stale one")
		assert.Nil(t, got["end"])
		assert.Equal(t, "q", got["exact"])
	})
}

// The anchor is nested inside the comment, so the custom marshaller has to
// survive being reached through the parent rather than only when marshalled
// directly.
func TestDocumentComment_MarshalJSON_CarriesTheAnchorFlag(t *testing.T) {
	c := DocumentComment{
		ID:     uuid.New(),
		Body:   "typo",
		Anchor: NewDocumentCommentAnchor("a quote whose text has since been rewritten", "", "", nil, nil),
	}

	raw, err := json.Marshal(c)
	require.NoError(t, err)

	var got struct {
		Anchor map[string]any `json:"anchor"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, true, got.Anchor["orphaned"])
}

func TestDocumentComment_IsResolvedAndIsReply(t *testing.T) {
	parent := uuid.New()
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	var nilComment *DocumentComment
	assert.False(t, nilComment.IsResolved())
	assert.False(t, nilComment.IsReply())

	root := &DocumentComment{ID: uuid.New()}
	assert.False(t, root.IsResolved())
	assert.False(t, root.IsReply())

	reply := &DocumentComment{ID: uuid.New(), ParentCommentID: &parent}
	assert.True(t, reply.IsReply())

	resolved := &DocumentComment{ID: uuid.New(), ResolvedAt: &at}
	assert.True(t, resolved.IsResolved())
}
