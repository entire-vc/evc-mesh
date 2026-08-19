package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// update is the update most of these tests make: an edit by some user, with
// whatever the test is actually about layered on top.
func (f *documentFixture) update(t *testing.T, id uuid.UUID, in UpdateDocumentInput) (*domain.Document, error) {
	t.Helper()
	in.UpdatedBy = uuid.New()
	in.UpdatedByType = domain.ActorTypeUser
	return f.svc.Update(context.Background(), id, f.wsID, in)
}

// storedBody is what is actually in object storage — the assertion that matters
// for a refused write, because a status code says nothing about whether the
// bytes survived.
func (f *documentFixture) storedBody(t *testing.T, doc *domain.Document) string {
	t.Helper()
	f.storage.mu.RLock()
	defer f.storage.mu.RUnlock()
	body, ok := f.storage.objects[doc.StorageKey]
	require.True(t, ok, "the document has no body in storage")
	return string(body)
}

func ptr[T any](v T) *T { return &v }

func TestDocumentService_Create_StartsAtVersionOne(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "Runbook", "# Runbook\n")

	assert.Equal(t, 1, doc.Version, "a document that came back as version 0 could not be written to conditionally")

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.Version, "and the row agrees, without a re-read")
}

// --- the acceptance test for M1 -------------------------------------------

// The pair of writes the whole unit exists for: the second one is working from a
// version that has moved on, and it must be refused with the body left alone.
//
// The assertion is on the CONTENT, not on the status code. A 409 that had already
// overwritten the body would pass a status-code test and still be the data loss
// this is meant to prevent.
func TestDocumentService_Update_StaleBaseVersionIsRefusedAndNothingIsWritten(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original body\n")
	require.Equal(t, 1, doc.Version)

	// Writer A reads version 1 and writes.
	first, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:        ptr("body from writer A\n"),
		BaseVersion: ptr(1),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, first.Version)

	// Writer B also read version 1, and only gets here afterwards.
	second, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:        ptr("body from writer B\n"),
		BaseVersion: ptr(1),
	})

	require.Nil(t, second)
	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 2, conflict.CurrentVersion, "the caller is told the version to re-read")

	assert.Equal(t, "body from writer A\n", f.storedBody(t, doc),
		"the refused write must not have touched the stored markdown")

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, stored.Version, "and it must not have bumped the version either")
}

// The positive control for the test above: the same code path, with a version
// that is current, writes and bumps.
func TestDocumentService_Update_FreshBaseVersionWritesAndBumps(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original body\n")

	updated, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:        ptr("edited body\n"),
		BaseVersion: ptr(doc.Version),
	})
	require.NoError(t, err)

	assert.Equal(t, 2, updated.Version)
	assert.Equal(t, "edited body\n", f.storedBody(t, doc))

	// And again from the version it just returned, so the counter is usable as a
	// running handle rather than only once.
	twice, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:        ptr("edited twice\n"),
		BaseVersion: ptr(updated.Version),
	})
	require.NoError(t, err)
	assert.Equal(t, 3, twice.Version)
	assert.Equal(t, "edited twice\n", f.storedBody(t, doc))
}

// The compatibility decision, pinned: an absent base_version is an unconditional
// write. Changing this breaks every current client, so it should fail a test
// rather than be discovered in production.
func TestDocumentService_Update_AbsentBaseVersionWritesUnconditionally(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("first\n"), BaseVersion: ptr(1)})
	require.NoError(t, err)

	// A caller that never read version 2 and sends no base_version still wins.
	updated, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("second\n")})
	require.NoError(t, err)

	assert.Equal(t, 3, updated.Version)
	assert.Equal(t, "second\n", f.storedBody(t, doc))
}

// A stale base_version is refused even when the caller is only renaming the
// document: the title is content, and two agents renaming the same page is the
// same lost update as two agents rewriting it.
func TestDocumentService_Update_TitleWriteIsAlsoConditional(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "body\n")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Title: ptr("Renamed Once"), BaseVersion: ptr(1)})
	require.NoError(t, err)

	_, err = f.update(t, doc.ID, UpdateDocumentInput{Title: ptr("Renamed Twice"), BaseVersion: ptr(1)})

	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed Once", stored.Title)
}

func TestDocumentService_Update_TitleWriteBumpsTheVersion(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "body\n")

	updated, err := f.update(t, doc.ID, UpdateDocumentInput{Title: ptr("Renamed")})
	require.NoError(t, err)

	assert.Equal(t, 2, updated.Version, "the title is part of the document's content")
}

// A move is not a content change. Bumping there would 409 every editor in the
// project the moment somebody reorganises the tree, for a change that did not
// touch a word of any document.
func TestDocumentService_Update_MoveDoesNotBumpTheVersion(t *testing.T) {
	f := setupDocumentService(t)
	parent := f.create(t, "Parent", "parent body\n")
	doc := f.create(t, "Child", "child body\n")

	moved, err := f.update(t, doc.ID, UpdateDocumentInput{ParentID: &parent.ID, Position: ptr(7)})
	require.NoError(t, err)

	assert.Equal(t, 1, moved.Version, "re-filing a page does not change it")
	assert.Equal(t, 7, moved.Position, "and the move still happened")

	// So a writer holding version 1 is not refused by somebody else's move.
	after, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("edited\n"), BaseVersion: ptr(1)})
	require.NoError(t, err)
	assert.Equal(t, 2, after.Version)
}

// The race the pre-check alone cannot close: another writer lands between the
// service reading the document and the repository writing it. The compare is
// inside the UPDATE, so it still refuses.
func TestDocumentService_Update_ConflictLandingMidWriteIsStillRefused(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	f.repo.beforeUpdate = func() {
		// A competing write, after our read of version 1 and before our write.
		_, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("from the other writer\n")})
		require.NoError(t, err)
	}

	_, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:        ptr("from us\n"),
		BaseVersion: ptr(1),
	})

	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 2, conflict.CurrentVersion)
	assert.Equal(t, "from the other writer\n", f.storedBody(t, doc),
		"the loser's markdown never reached storage")
}

// --- append ----------------------------------------------------------------

func TestDocumentService_Update_AppendNeedsNoBaseVersion(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Run log", "# Run log\n")

	updated, err := f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("\n- first run: ok\n")})
	require.NoError(t, err)

	assert.Equal(t, "# Run log\n\n- first run: ok\n", f.storedBody(t, doc))
	assert.Equal(t, "# Run log\n\n- first run: ok\n", updated.Body, "the caller gets the whole document back")
	assert.Equal(t, 2, updated.Version, "an append is a content change")
}

// The property that makes append worth having: it does not clobber. Two
// appenders both end up in the document.
func TestDocumentService_Update_AppendsDoNotClobberEachOther(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Run log", "start\n")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("A\n")})
	require.NoError(t, err)
	_, err = f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("B\n")})
	require.NoError(t, err)

	assert.Equal(t, "start\nA\nB\n", f.storedBody(t, doc))
}

// An append whose version moved under it is retried, not reported: a version an
// append lost is a stale read to redo, not a conflict the caller has to resolve.
// This is the interleaving that a plain read-modify-write would silently lose.
func TestDocumentService_Update_AppendRetriesInsteadOfConflicting(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Run log", "start\n")

	f.repo.beforeUpdate = func() {
		_, err := f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("from the other writer\n")})
		require.NoError(t, err)
	}

	updated, err := f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("from us\n")})
	require.NoError(t, err)

	assert.Equal(t, "start\nfrom the other writer\nfrom us\n", f.storedBody(t, doc),
		"the retry re-read the body the winner wrote and appended to that")
	assert.Equal(t, 3, updated.Version)
}

// Unless the caller asked to be told. Sending base_version with an append means
// "I am also asserting what I read", and that assertion is answered rather than
// papered over with a retry.
func TestDocumentService_Update_AppendWithBaseVersionStillConflicts(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Run log", "start\n")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("A\n")})
	require.NoError(t, err)

	_, err = f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("B\n"), BaseVersion: ptr(1)})

	var conflict *DocumentVersionConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, 2, conflict.CurrentVersion)
	assert.Equal(t, "start\nA\n", f.storedBody(t, doc))
}

func TestDocumentService_Update_BodyAndAppendBodyTogetherIsRefused(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{
		Body:       ptr("replacement\n"),
		AppendBody: ptr("addition\n"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, "original\n", f.storedBody(t, doc))
}

func TestDocumentService_Update_AppendPastTheSizeCapIsRefused(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", strings.Repeat("x", 1024))

	_, err := f.update(t, doc.ID, UpdateDocumentInput{
		AppendBody: ptr(strings.Repeat("y", maxDocumentBodyBytes)),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation["append_body"], "already 1024")
	assert.Len(t, f.storedBody(t, doc), 1024, "nothing was written")
}

// A body that cannot be read must not be appended to: concatenating onto "" would
// replace the document with its own last paragraph.
func TestDocumentService_Update_AppendRefusesWhenTheBodyCannotBeRead(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")
	f.storage.errToReturn = errors.New("s3 is down")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{AppendBody: ptr("addition\n")})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.StatusCode())

	f.storage.errToReturn = nil
	assert.Equal(t, "original\n", f.storedBody(t, doc))

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.Version, "and the version did not move for a write that did not happen")
}

func TestDocumentService_Update_ConflictOnAMissingDocumentIsNotFound(t *testing.T) {
	f := setupDocumentService(t)

	_, err := f.update(t, uuid.New(), UpdateDocumentInput{Body: ptr("x"), BaseVersion: ptr(1)})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A repository failure that is not a version mismatch travels out unchanged,
// rather than being reported to the caller as a conflict they could retry.
func TestDocumentService_Update_RepositoryErrorIsNotAConflict(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	boom := errors.New("connection reset")
	f.repo.beforeUpdate = func() { f.repo.errToReturn = boom }

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("edited\n"), BaseVersion: ptr(1)})

	require.ErrorIs(t, err, boom)
	var conflict *DocumentVersionConflictError
	assert.False(t, errors.As(err, &conflict))

	f.repo.errToReturn = nil
	assert.Equal(t, "original\n", f.storedBody(t, doc), "the body write never ran")
}

// The failure the write order deliberately chooses to have.
//
// The row is written first so that a refused write cannot already have
// overwritten the markdown. The cost is this: an upload that fails afterwards
// leaves the version bumped with the old body. That is a 500 the caller sees and
// retries, and every conditional writer is forced to re-read — the safe
// direction. The other ordering's failure is a silently lost paragraph.
func TestDocumentService_Update_UploadFailingAfterTheRowLeavesTheOldBody(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "original\n")

	f.repo.beforeUpdate = func() { f.storage.errToReturn = errors.New("s3 is down") }

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("edited\n"), BaseVersion: ptr(1)})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.StatusCode())

	f.storage.errToReturn = nil
	assert.Equal(t, "original\n", f.storedBody(t, doc), "the markdown is untouched")

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, stored.Version,
		"the version moved without the body — which forces every conditional writer to re-read, "+
			"and is why this ordering is the safe one")

	// And the caller's retry, from the version it was told about, works.
	retried, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("edited\n"), BaseVersion: ptr(2)})
	require.NoError(t, err)
	assert.Equal(t, "edited\n", f.storedBody(t, doc))
	assert.Equal(t, 3, retried.Version)
}

func TestDocumentVersionConflictError_Message(t *testing.T) {
	err := &DocumentVersionConflictError{CurrentVersion: 12}
	assert.Contains(t, err.Error(), "12")
}
