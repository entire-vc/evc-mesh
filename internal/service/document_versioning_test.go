package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Conditional writes on documents.
//
// The behaviour under test is not "a 409 comes back" — it is that the losing
// write DOES NOT HAPPEN. A conditional write that refuses the caller and stores
// the body anyway is indistinguishable from a working one by status code alone,
// and it reproduces the incident this exists to prevent while reporting that it
// did not. So every refusal here also asserts on what is in storage.
//
// The repository double is the in-memory one, not a mock of the SQL: it holds a
// per-document lock and computes version as stored+1, the same shape the real
// repository gets from `version = version + 1` under FOR UPDATE. The claim that
// PostgreSQL actually behaves that way under concurrency is not testable here
// and is asserted against a real database in document_versioning_db_test.go.

// updateBody is the common edit: change the body, built on baseVersion.
func (f *documentFixture) updateBody(doc *domain.Document, body string, baseVersion int64) (*domain.Document, error) {
	return f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
		Body:          &body,
		BaseVersion:   &baseVersion,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeAgent,
	})
}

// storedBody is what object storage actually holds for this document, which is
// the only account of the body that matters.
func (f *documentFixture) storedBody(doc *domain.Document) string {
	f.storage.mu.RLock()
	defer f.storage.mu.RUnlock()
	return string(f.storage.objects[doc.StorageKey])
}

// AC1. Two writers read the same document; the first one wins. The second is
// refused AND its text must be nowhere — the stored body is byte-identical to
// what the first write left.
//
// This is the 2026-08-19 incident in miniature: both agents held version 1, both
// wrote, and the second silently won.
func TestDocumentService_Update_StaleBaseVersionIsRefusedAndWritesNothing(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	// Both writers read the document at the same version.
	base := doc.Version

	first, err := f.updateBody(doc, "first writer's text\n", base)
	require.NoError(t, err)
	require.Equal(t, base+1, first.Version)

	second, err := f.updateBody(doc, "second writer's text\n", base)

	require.Error(t, err)
	assert.Nil(t, second)

	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict, "a stale write is a version conflict, not a generic failure")
	assert.Equal(t, base, conflict.BaseVersion)
	assert.Equal(t, base+1, conflict.CurrentVersion,
		"the refusal names the version the document is at, so the caller need not re-read to find out")

	assert.Equal(t, "first writer's text\n", f.storedBody(doc),
		"the refused write reached object storage — the 409 is cosmetic and the incident reproduces")

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, base+1, stored.Version, "a refused write must not bump the version either")
}

// AC2, the positive control. Without this the suite is satisfied by a service
// that refuses everything.
func TestDocumentService_Update_FreshBaseVersionSucceedsAndIncrements(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "v1\n")
	require.Equal(t, int64(1), doc.Version, "a new document starts at version 1")

	second, err := f.updateBody(doc, "v2\n", doc.Version)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second.Version)
	assert.Equal(t, "v2\n", f.storedBody(doc))

	// And again, chaining off the version the previous write returned — the
	// counter has to keep moving, not toggle between two values.
	third, err := f.updateBody(doc, "v3\n", second.Version)
	require.NoError(t, err)
	assert.Equal(t, int64(3), third.Version)
	assert.Equal(t, "v3\n", f.storedBody(doc))
}

// Every write bumps the version, not only body writes. A rename that left the
// version alone would make a concurrent body edit built on the pre-rename read
// look current when the document had in fact changed underneath it.
func TestDocumentService_Update_MetadataOnlyWriteAlsoBumpsVersion(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "body\n")

	title := "Renamed"
	renamed, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{
		Title:       &title,
		BaseVersion: &doc.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, doc.Version+1, renamed.Version, "a title change is a change")

	_, err = f.updateBody(doc, "written against the pre-rename read\n", doc.Version)
	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "body\n", f.storedBody(doc))
}

// AC5. An update with no base_version at all is refused. This is the rule that
// makes the guard a guard: were it treated as an unconditional write, every
// caller would be one omitted field away from the old behaviour, and the callers
// most likely to omit it are the ones that never read the document.
func TestDocumentService_Update_WithoutBaseVersionIsRejected(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	body := "written with no base version\n"
	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{Body: &body})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation, "base_version", "the refusal names the field that is missing")

	assert.Equal(t, "original\n", f.storedBody(doc), "rejected, not silently written")

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.Version, stored.Version)
}

// A zero base_version is a sent value, not an absent one, so it reaches the
// version check and fails it — documents start at 1. This pins that the pointer
// is doing its job: were BaseVersion a plain int64, an omitted field would
// arrive here as 0 and this is the error the caller would see instead of the
// clear "you did not send one".
func TestDocumentService_Update_ZeroBaseVersionIsAConflictNotAnOmission(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	_, err := f.updateBody(doc, "nope\n", 0)

	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, int64(0), conflict.BaseVersion)
	assert.Equal(t, int64(1), conflict.CurrentVersion)
	assert.Equal(t, "original\n", f.storedBody(doc))
}

// AC3. Append takes no base version and adds to what is there.
func TestDocumentService_AppendBody_NeedsNoBaseVersionAndKeepsExistingText(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Run log", "# Run log\n")

	editor := uuid.New()
	appended, err := f.svc.AppendBody(context.Background(), doc.ID, f.wsID, AppendDocumentInput{
		Text:          "- 12:01 deploy started\n",
		UpdatedBy:     editor,
		UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	assert.Equal(t, "# Run log\n- 12:01 deploy started\n", f.storedBody(doc),
		"the existing text survives and the addition lands after it")
	assert.Equal(t, "# Run log\n- 12:01 deploy started\n", appended.Body,
		"the response carries the body as it now stands")
	assert.Equal(t, doc.Version+1, appended.Version, "an append is a write, so the version moves")
	require.NotNil(t, appended.UpdatedBy)
	assert.Equal(t, editor, *appended.UpdatedBy)
}

// An append built on a stale view of the document is still fine — that is the
// whole point of not asking for a base version. Here the document is edited out
// from under an appender that read it long ago, and the append still lands on
// top of the current text rather than the text it once saw.
func TestDocumentService_AppendBody_LandsOnCurrentTextNotTheCallersView(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Run log", "first\n")

	_, err := f.updateBody(doc, "somebody else rewrote this\n", doc.Version)
	require.NoError(t, err)

	_, err = f.svc.AppendBody(context.Background(), doc.ID, f.wsID, AppendDocumentInput{Text: "appended\n"})
	require.NoError(t, err)

	assert.Equal(t, "somebody else rewrote this\nappended\n", f.storedBody(doc))
}

// The separator rule, byte for byte. Markdown is line-sensitive: text run onto
// the end of the previous line joins that paragraph or list item instead of
// starting a new block. One newline, and never one the caller did not ask for.
func TestDocumentService_AppendBody_SeparatorRule(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		addition string
		want     string
	}{
		{"body already ends in a newline", "line\n", "next\n", "line\nnext\n"},
		{"body does not end in a newline", "line", "next", "line\nnext"},
		{"empty body takes the addition verbatim", "", "next\n", "next\n"},
		{"a blank line the caller asked for is kept", "line\n\n", "next", "line\n\nnext"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupDocumentService(t)
			doc := f.create(t, "Doc", tc.existing)

			_, err := f.svc.AppendBody(context.Background(), doc.ID, f.wsID, AppendDocumentInput{Text: tc.addition})
			require.NoError(t, err)

			assert.Equal(t, tc.want, f.storedBody(doc))
		})
	}
}

func TestDocumentService_AppendBody_EmptyTextIsRejected(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Doc", "body\n")

	_, err := f.svc.AppendBody(context.Background(), doc.ID, f.wsID, AppendDocumentInput{Text: ""})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, "body\n", f.storedBody(doc))

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, doc.Version, stored.Version, "a rejected append does not bump the version")
}

// The size ceiling applies to the RESULT, not to the addition. An append that
// takes a document past the cap has to be refused, or the limit is enforced only
// against callers who send the whole body at once.
func TestDocumentService_AppendBody_RefusesToGrowPastTheBodyLimit(t *testing.T) {
	f := setupDocumentService(t)
	nearlyFull := strings.Repeat("x", maxDocumentBodyBytes-8)
	doc := f.create(t, "Big", nearlyFull)

	_, err := f.svc.AppendBody(context.Background(), doc.ID, f.wsID,
		AppendDocumentInput{Text: strings.Repeat("y", 64)})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, nearlyFull, f.storedBody(doc), "the oversized result was not stored")
}

func TestDocumentService_AppendBody_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Confidential", "secret\n")

	_, err := f.svc.AppendBody(context.Background(), doc.ID, uuid.New(), AppendDocumentInput{Text: "leak\n"})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Equal(t, "secret\n", f.storedBody(doc))
}

func TestDocumentService_Update_UnknownDocumentIsNotFound(t *testing.T) {
	f := setupDocumentService(t)

	body, base := "text", int64(1)
	_, err := f.svc.Update(context.Background(), uuid.New(), f.wsID,
		UpdateDocumentInput{Body: &body, BaseVersion: &base})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}
