package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentAttachmentFixture is one document, inside one project, inside one
// workspace, with the service under test wired to fresh mocks.
type documentAttachmentFixture struct {
	svc        *documentAttachmentService
	repo       *MockDocumentAttachmentRepository
	docRepo    *MockDocumentRepository
	storage    *MockStorageClient
	documentID uuid.UUID
	projectID  uuid.UUID
	wsID       uuid.UUID
}

func setupDocumentAttachmentService(t *testing.T) *documentAttachmentFixture {
	t.Helper()

	projectID := uuid.New()
	wsID := uuid.New()
	documentID := uuid.New()

	docRepo := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	docRepo.Seed(&domain.Document{
		ID:         documentID,
		ProjectID:  projectID,
		Slug:       "runbook",
		Title:      "Runbook",
		StorageKey: "documents/" + projectID.String() + "/" + documentID.String() + ".md",
	})

	repo := NewMockDocumentAttachmentRepository().WithDocumentWorkspace(documentID, wsID)
	storage := NewMockStorageClient()

	timeNow = func() time.Time { return frozenTime }

	return &documentAttachmentFixture{
		svc:        NewDocumentAttachmentService(repo, docRepo, storage).(*documentAttachmentService),
		repo:       repo,
		docRepo:    docRepo,
		storage:    storage,
		documentID: documentID,
		projectID:  projectID,
		wsID:       wsID,
	}
}

// upload is the happy-path upload most tests start from.
func (f *documentAttachmentFixture) upload(t *testing.T, name, content string) *domain.DocumentAttachment {
	t.Helper()
	att, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:     f.documentID,
		WorkspaceID:    f.wsID,
		Name:           name,
		MimeType:       "image/png",
		Size:           int64(len(content)),
		Reader:         strings.NewReader(content),
		UploadedBy:     uuid.New(),
		UploadedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	require.NotNil(t, att)
	return att
}

func TestDocumentAttachmentService_Upload(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	ctx := context.Background()

	uploader := uuid.New()
	att, err := f.svc.Upload(ctx, UploadDocumentAttachmentInput{
		DocumentID:     f.documentID,
		WorkspaceID:    f.wsID,
		Name:           "  Screenshot.PNG  ",
		MimeType:       "image/png",
		Size:           4,
		Reader:         strings.NewReader("data"),
		UploadedBy:     uploader,
		UploadedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	assert.NotEqual(t, uuid.Nil, att.ID)
	assert.Equal(t, "Screenshot.PNG", att.Name, "the name is trimmed but not otherwise rewritten")
	assert.Equal(t, f.documentID, att.DocumentID)
	assert.Equal(t, "image/png", att.MimeType)
	assert.Equal(t, int64(4), att.SizeBytes)
	assert.Equal(t, uploader, att.UploadedBy)
	assert.Equal(t, domain.ActorTypeAgent, att.UploadedByType)
	assert.Equal(t, frozenTime, att.CreatedAt)
	assert.Nil(t, att.DeletedAt)

	// The key nests under the document's own prefix, is keyed on the immutable
	// attachment id, and carries a lowercased extension — not the artifact key's
	// repeated name segment.
	assert.Equal(t,
		fmt.Sprintf("documents/%s/%s/attachments/%s.png", f.projectID, f.documentID, att.ID),
		att.StorageKey)
	assert.Equal(t, "data", string(f.storage.objects[att.StorageKey]),
		"the file bytes are what went to object storage")

	stored, err := f.repo.GetByIDInWorkspace(ctx, att.ID, f.wsID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, att.StorageKey, stored.StorageKey)
}

// A name with no extension is legitimate — the extension is decoration on an
// id-keyed path, not part of the address.
func TestDocumentAttachmentService_Upload_NameWithoutExtension(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	att := f.upload(t, "Makefile", "all:")

	assert.Equal(t,
		fmt.Sprintf("documents/%s/%s/attachments/%s", f.projectID, f.documentID, att.ID),
		att.StorageKey, "an absent extension leaves the key with none, not a trailing dot")
}

func TestDocumentAttachmentService_Upload_RejectsEmptyName(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "   ",
		Reader:      strings.NewReader("x"),
		Size:        1,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// The declared-size check is what refuses an oversize upload BEFORE a byte
// reaches storage.
func TestDocumentAttachmentService_Upload_RejectsOversizeDeclaredSize(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "huge.bin",
		Size:        maxAttachmentBytes + 1,
		Reader:      strings.NewReader("x"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Empty(t, f.storage.objects, "nothing was streamed to storage")
}

// And this is the check that makes the cap true rather than advisory: the
// declared size is under the cap and the body is not. Without the limited reader
// the row would be created and the object kept, sized by a number the client
// made up.
func TestDocumentAttachmentService_Upload_RejectsOversizeStreamWithLyingContentLength(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	oversize := bytes.Repeat([]byte("a"), maxAttachmentBytes+1)
	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "liar.bin",
		Size:        10, // the claim
		Reader:      bytes.NewReader(oversize),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Empty(t, f.storage.objects,
		"the object written before the length was known was cleaned up")
	assert.Empty(t, f.repo.items, "no row survived the refusal")
}

// A body exactly at the cap is accepted: the limited reader is set to cap+1 so
// that the boundary itself is not off by one.
func TestDocumentAttachmentService_Upload_AcceptsExactlyTheCap(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	atCap := bytes.Repeat([]byte("a"), maxAttachmentBytes)
	att, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "exact.bin",
		Size:        maxAttachmentBytes,
		Reader:      bytes.NewReader(atCap),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(maxAttachmentBytes), att.SizeBytes)
}

// The recorded size is the counted one, not the declared one — a wrong
// size_bytes would be believed by everything downstream that reads it.
func TestDocumentAttachmentService_Upload_RecordsTheCountedSizeNotTheClaim(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	att, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "short.txt",
		Size:        9999, // under the cap, and wrong
		Reader:      strings.NewReader("hello"),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), att.SizeBytes)
}

// The ownership check: a document in another workspace is a 404, not an
// attachment hung off a stranger's page.
func TestDocumentAttachmentService_Upload_DocumentInAnotherWorkspace(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: uuid.New(), // an ordinary member of some other tenant
		Name:        "steal.png",
		Size:        1,
		Reader:      strings.NewReader("x"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Empty(t, f.storage.objects, "nothing was written for a document that is not the caller's")
	assert.Empty(t, f.repo.items)
}

func TestDocumentAttachmentService_Upload_UnknownDocument(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  uuid.New(),
		WorkspaceID: f.wsID,
		Name:        "x.png",
		Size:        1,
		Reader:      strings.NewReader("x"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A soft-deleted document takes its upload path with it: attaching to a deleted
// page would resurrect content in something its owner believes is gone.
func TestDocumentAttachmentService_Upload_SoftDeletedDocument(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	require.NoError(t, f.docRepo.SoftDelete(context.Background(), f.documentID, frozenTime))

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "x.png",
		Size:        1,
		Reader:      strings.NewReader("x"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentAttachmentService_Upload_StorageFailureLeavesNoRow(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	f.storage.errToReturn = errors.New("s3 is down")

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "x.png",
		Size:        1,
		Reader:      strings.NewReader("x"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.StatusCode())
	assert.Empty(t, f.repo.items,
		"a row would advertise an object that was never written")
}

// The other direction: the object landed and the row did not. The object is
// unreferenced garbage, so it is cleaned up.
func TestDocumentAttachmentService_Upload_RowFailureCleansUpTheObject(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	f.repo.createErr = errors.New("insert failed")

	_, err := f.svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Name:        "x.png",
		Size:        1,
		Reader:      strings.NewReader("x"),
	})

	require.Error(t, err)
	assert.Empty(t, f.storage.objects,
		"the object with no row pointing at it was deleted")
}

func TestDocumentAttachmentService_Upload_NoStorageConfigured(t *testing.T) {
	repo := NewMockDocumentAttachmentRepository()
	docRepo := NewMockDocumentRepository()
	svc := NewDocumentAttachmentService(repo, docRepo, nil)

	_, err := svc.Upload(context.Background(), UploadDocumentAttachmentInput{
		DocumentID:  uuid.New(),
		WorkspaceID: uuid.New(),
		Name:        "x.png",
		Size:        1,
		Reader:      strings.NewReader("x"),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 503, apiErr.StatusCode())
}

// --- GetDownloadURL ---

func TestDocumentAttachmentService_GetDownloadURL_InlinePassesNoFilename(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	att := f.upload(t, "screenshot.png", "data")

	url, err := f.svc.GetDownloadURL(context.Background(), att.ID, f.wsID, true)
	require.NoError(t, err)
	assert.Contains(t, url, att.StorageKey)

	// An empty filename is what omits Content-Disposition: attachment, and that
	// omission is the whole difference between an <img> that renders and one that
	// triggers a download.
	assert.Empty(t, f.storage.lastFilename)
}

func TestDocumentAttachmentService_GetDownloadURL_AttachmentPassesTheFilename(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	att := f.upload(t, "spec.pdf", "%PDF")

	_, err := f.svc.GetDownloadURL(context.Background(), att.ID, f.wsID, false)
	require.NoError(t, err)
	assert.Equal(t, "spec.pdf", f.storage.lastFilename,
		"a non-inline download names the file so the browser saves it under that name")
}

func TestDocumentAttachmentService_GetDownloadURL_UsesTheOneHourExpiry(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	att := f.upload(t, "screenshot.png", "data")

	url, err := f.svc.GetDownloadURL(context.Background(), att.ID, f.wsID, true)
	require.NoError(t, err)
	// MockStorageClient echoes the expiry it was handed, which is the only way to
	// see from here that the presign is short-lived rather than permanent.
	assert.Contains(t, url, "expiry="+presignedURLExpiry.String())
}

func TestDocumentAttachmentService_GetDownloadURL_AnotherWorkspaceIs404(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	att := f.upload(t, "screenshot.png", "data")

	_, err := f.svc.GetDownloadURL(context.Background(), att.ID, uuid.New(), true)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDocumentAttachmentService_GetDownloadURL_UnknownIs404(t *testing.T) {
	f := setupDocumentAttachmentService(t)

	_, err := f.svc.GetDownloadURL(context.Background(), uuid.New(), f.wsID, true)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// --- ListByDocument ---

func TestDocumentAttachmentService_ListByDocument(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	f.upload(t, "a.png", "a")
	f.upload(t, "b.png", "b")

	page, err := f.svc.ListByDocument(context.Background(), f.documentID, f.wsID, pagination.Params{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

// A listing for another tenant's document is a 404, not an empty page: an empty
// page is an answer, and answering at all confirms which ids exist.
func TestDocumentAttachmentService_ListByDocument_AnotherWorkspaceIs404(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	f.upload(t, "a.png", "a")

	_, err := f.svc.ListByDocument(context.Background(), f.documentID, uuid.New(), pagination.Params{})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// --- Delete ---

// The delete is reversible by design, so the bytes stay: dropping them would make
// a restored attachment a permanently broken image the row still claims to have.
func TestDocumentAttachmentService_Delete_KeepsTheObject(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	att := f.upload(t, "screenshot.png", "data")

	require.NoError(t, f.svc.Delete(context.Background(), att.ID, f.wsID))

	assert.Equal(t, "data", string(f.storage.objects[att.StorageKey]),
		"the stored object outlives the soft delete")

	gone, err := f.repo.GetByIDInWorkspace(context.Background(), att.ID, f.wsID)
	require.NoError(t, err)
	assert.Nil(t, gone, "the row is hidden from every read")
}

func TestDocumentAttachmentService_Delete_AnotherWorkspaceIs404(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	att := f.upload(t, "screenshot.png", "data")

	err := f.svc.Delete(context.Background(), att.ID, uuid.New())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	still, getErr := f.repo.GetByIDInWorkspace(context.Background(), att.ID, f.wsID)
	require.NoError(t, getErr)
	assert.NotNil(t, still, "the cross-tenant delete did not reach the row")
}

// --- The expiry property ---

// countingSigner is a StorageClient that records how many times a URL was signed
// and stamps each one with the call number, standing in for the X-Amz-Date that a
// real signer varies. MockStorageClient returns a fixed URL and so cannot tell a
// fresh signature from a cached one.
type countingSigner struct {
	*MockStorageClient
	calls atomic.Int64
}

func (s *countingSigner) GetPresignedURL(ctx context.Context, key string, expiry time.Duration, contentType, filename string) (string, error) {
	base, err := s.MockStorageClient.GetPresignedURL(ctx, key, expiry, contentType, filename)
	if err != nil {
		return "", err
	}
	n := s.calls.Add(1)
	return fmt.Sprintf("%s&X-Amz-Date=2026081916%02d00&X-Amz-Signature=sig-%d&X-Amz-Expires=3600", base, n, n), nil
}

// TestDocumentAttachmentService_EveryResolveIsFreshlySigned is the mechanical half
// of the acceptance criterion "the image still opens an hour later".
//
// Waiting an hour is not a test. What actually has to hold is that the reference
// stored in the markdown body contains no signature material — so it has no expiry
// to reach — and that resolving it goes to the signer EVERY time rather than
// handing back something minted earlier.
//
// The call-count assertion is the load-bearing one, and it is deliberately not
// "the two URLs differ": a real SigV4 presign is a pure function of
// (key, expiry, credentials, X-Amz-Date), and X-Amz-Date has one-second
// granularity, so two same-second resolves are byte-identical against real
// storage and both are equally valid. Asserting inequality would be asserting a
// property the real signer does not have — it passes here only because the fake
// varies. What distinguishes a live signer from a cached URL is that the signer is
// consulted at all, and that is what is checked.
//
// The regression it catches: a service that memoised the presigned URL on the row
// or on the domain object would pass every other test in this file and start
// serving 403s exactly one expiry after deploy.
func TestDocumentAttachmentService_EveryResolveIsFreshlySigned(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	signer := &countingSigner{MockStorageClient: f.storage}
	f.svc.storage = signer

	att := f.upload(t, "screenshot.png", "data")

	// (a) Nothing that gets written into a document body carries a credential.
	// The stored reference is the API path built from the id, and the id is all of
	// it — the row itself has no URL field at all.
	assert.NotContains(t, att.StorageKey, "X-Amz-",
		"the stored key is an object key, not a signed URL")
	storedRef := "/api/v1/document-attachments/" + att.ID.String() + "/download?disposition=inline"
	assert.NotContains(t, storedRef, "X-Amz-")
	assert.NotContains(t, storedRef, "Signature")

	// (b) Every resolve of that reference consults the signer.
	first, err := f.svc.GetDownloadURL(context.Background(), att.ID, f.wsID, true)
	require.NoError(t, err)
	assert.Equal(t, int64(1), signer.calls.Load())

	second, err := f.svc.GetDownloadURL(context.Background(), att.ID, f.wsID, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), signer.calls.Load(),
		"the second resolve did not reach the signer — the URL is being cached, and a "+
			"cached presign stops working one expiry after it was minted")

	// (c) The URLs it hands out really are time-limited; otherwise "it expires" is
	// not a property of this system and (b) is about nothing.
	for _, url := range []string{first, second} {
		assert.Contains(t, url, "X-Amz-Signature=")
		assert.Contains(t, url, "X-Amz-Expires=")
	}
}

// requireStorageClient accepts only the full port. It is a compile-time
// statement dressed as a call: nothing is checked at runtime.
func requireStorageClient(_ StorageClient) {}

// A guard against the storage port narrowing by accident: an attachment needs
// GetPresignedURL, which DocumentStore deliberately omits, so the service must
// keep taking the full StorageClient.
//
// The failure this catches is a compile error, not a red assertion — which is the
// right shape for it, because at runtime a narrowed port would degrade silently
// into "serve the bytes through the API" and every <img> in every document would
// break at once.
func TestDocumentAttachmentService_TakesTheFullStorageClient(t *testing.T) {
	f := setupDocumentAttachmentService(t)
	requireStorageClient(f.svc.storage)

	// And it really is the presign path that is exercised, not a Download.
	att := f.upload(t, "x.png", "x")
	url, err := f.svc.GetDownloadURL(context.Background(), att.ID, f.wsID, true)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "https://"), "got %q", url)
}

// countingReader is what makes the streamed-length check possible; a wrong count
// would silently mis-size every row.
func TestCountingReader_CountsWhatWasRead(t *testing.T) {
	c := &countingReader{r: strings.NewReader("hello world")}
	n, err := io.Copy(io.Discard, c)
	require.NoError(t, err)
	assert.Equal(t, int64(11), n)
	assert.Equal(t, int64(11), c.n)
}
