package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// fiveSectionDoc is a canon-sized page: five sections, each long enough that the
// document is well past the shrink guard's floor.
func fiveSectionDoc() string {
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "## %d. Section %d\n\n%s\n\n", i, i, strings.Repeat("text of the section. ", 40))
	}
	return b.String()
}

// The incident: read one section, write it back as the whole body. Four of five
// sections used to disappear without a word.
func TestDocumentService_Update_SectionBodyAsWholeDocumentIsRefused(t *testing.T) {
	f := setupDocumentService(t)
	body := fiveSectionDoc()
	doc := f.create(t, "Canon", body)

	_, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:        ptr("## 4. Section 4\n\nedited\n"),
		BaseVersion: ptr(1),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode())
	for _, lost := range []string{"1. Section 1", "2. Section 2", "3. Section 3", "5. Section 5"} {
		assert.Contains(t, apiErr.Message, lost, "the refusal names what would vanish")
	}
	assert.Equal(t, body, f.storedBody(t, doc), "nothing was written")
}

func TestDocumentService_Update_AllowShrinkLetsAnIntendedCutThrough(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Canon", fiveSectionDoc())

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("## only\n"), AllowShrink: true})

	require.NoError(t, err)
	assert.Equal(t, "## only\n", f.storedBody(t, doc))
}

func TestDocumentService_Update_SectionReplacesInPlace(t *testing.T) {
	f := setupDocumentService(t)
	body := fiveSectionDoc()
	doc := f.create(t, "Canon", body)

	updated, err := f.update(t, doc.ID, UpdateDocumentInput{
		Section:     ptr("4. Section 4"),
		Body:        ptr("## 4. Section 4\n\nedited"),
		BaseVersion: ptr(1),
	})

	require.NoError(t, err)
	assert.Equal(t, 2, updated.Version)
	got := f.storedBody(t, doc)
	for _, keep := range []string{"## 1. Section 1", "## 2. Section 2", "## 3. Section 3", "## 5. Section 5"} {
		assert.Contains(t, got, keep)
	}
	assert.Contains(t, got, "edited\n## 5. Section 5", "the replacement stays separated from the next heading")
	assert.Equal(t, 1, strings.Count(got, "## 4. Section 4"))
	assert.NotContains(t, got[strings.Index(got, "## 4."):strings.Index(got, "## 5.")], "text of the section")
}

func TestDocumentService_Update_SectionUnknownHeadingIsRefusedWithTheList(t *testing.T) {
	f := setupDocumentService(t)
	body := fiveSectionDoc()
	doc := f.create(t, "Canon", body)

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Section: ptr("nope"), Body: ptr("x")})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Equal(t, body, f.storedBody(t, doc))
}

func TestDocumentService_Update_SectionWithoutBodyIsRefused(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Canon", fiveSectionDoc())

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Section: ptr("4. Section 4")})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentService_Update_SectionWithAppendIsRefused(t *testing.T) {
	f := setupDocumentService(t)
	body := fiveSectionDoc()
	doc := f.create(t, "Canon", body)

	_, err := f.update(t, doc.ID, UpdateDocumentInput{
		Section: ptr("4. Section 4"), Body: ptr("## 4. Section 4\n"), AppendBody: ptr("tail"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, body, f.storedBody(t, doc))
}

// The read-modify-write race: another writer edits a DIFFERENT section between
// our read of the body and our write. Without a version precondition the stale
// splice would erase their edit.
func TestDocumentService_Update_SectionRetriesAgainstConcurrentWrite(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Canon", fiveSectionDoc())

	fired := false
	f.repo.beforeUpdate = func() {
		if fired {
			return
		}
		fired = true
		_, err := f.update(t, doc.ID, UpdateDocumentInput{Section: ptr("1. Section 1"), Body: ptr("## 1. Section 1\n\nother writer\n")})
		require.NoError(t, err)
	}

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Section: ptr("4. Section 4"), Body: ptr("## 4. Section 4\n\nours\n")})
	require.NoError(t, err)

	got := f.storedBody(t, doc)
	assert.Contains(t, got, "other writer", "the concurrent edit to section 1 survived")
	assert.Contains(t, got, "ours", "and so did ours")
}
