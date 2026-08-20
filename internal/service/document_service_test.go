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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentFixture is one project inside one workspace, with the service under
// test wired to fresh mocks.
type documentFixture struct {
	svc       *documentService
	repo      *MockDocumentRepository
	storage   *MockStorageClient
	comments  *MockDocumentCommentRepository
	projectID uuid.UUID
	wsID      uuid.UUID
}

func setupDocumentService(t *testing.T) *documentFixture {
	t.Helper()

	projectID := uuid.New()
	wsID := uuid.New()

	repo := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	storage := NewMockStorageClient()
	projectRepo := NewMockProjectRepository()
	comments := NewMockDocumentCommentRepository()
	require.NoError(t, projectRepo.Create(context.Background(), &domain.Project{ID: projectID, WorkspaceID: wsID}))

	timeNow = func() time.Time { return frozenTime }

	return &documentFixture{
		svc:       NewDocumentService(repo, storage, projectRepo, comments).(*documentService),
		repo:      repo,
		storage:   storage,
		comments:  comments,
		projectID: projectID,
		wsID:      wsID,
	}
}

// create is the happy-path create most tests start from.
func (f *documentFixture) create(t *testing.T, title, body string) *domain.Document {
	t.Helper()
	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID:     f.projectID,
		Title:         title,
		Body:          body,
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	require.NotNil(t, doc)
	return doc
}

func TestDocumentService_Create(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	createdBy := uuid.New()
	doc, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID:     f.projectID,
		Title:         "  Release Notes  ",
		Body:          "# Release Notes\n",
		Position:      3,
		CreatedBy:     createdBy,
		CreatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	assert.NotEqual(t, uuid.Nil, doc.ID)
	assert.Equal(t, "Release Notes", doc.Title, "the title is trimmed")
	assert.Equal(t, "release-notes", doc.Slug, "an absent slug is derived from the title")
	assert.Equal(t, 3, doc.Position)
	assert.Equal(t, createdBy, doc.CreatedBy)
	assert.Equal(t, domain.ActorTypeAgent, doc.CreatedByType)
	assert.Equal(t, frozenTime, doc.CreatedAt)
	assert.Equal(t, frozenTime, doc.UpdatedAt)
	assert.Nil(t, doc.DeletedAt)

	// The key is project-scoped and keyed on the immutable document id, without
	// the artifact key's repeated name segment.
	assert.Equal(t, "documents/"+f.projectID.String()+"/"+doc.ID.String()+".md", doc.StorageKey)
	assert.Equal(t, "# Release Notes\n", string(f.storage.objects[doc.StorageKey]),
		"the markdown body is what went to object storage")

	stored, err := f.repo.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, doc.StorageKey, stored.StorageKey)
}

func TestDocumentService_Create_RejectsEmptyTitle(t *testing.T) {
	f := setupDocumentService(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "   ",
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentService_Create_UnknownProject(t *testing.T) {
	f := setupDocumentService(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: uuid.New(),
		Title:     "Orphan",
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// The slug generator is unicode-aware (mdoc.Slugify — the same rule as heading
// anchors), not ASCII-only: a Cyrillic title gets a slug built from its own
// letters, not a fallback to the id. Before this, every non-Latin title
// slugified to a bare run of hyphens ("-", "--"), so two Russian-titled
// documents in the same folder collided on the (project_id, parent_id, slug)
// uniqueness constraint and the second was refused outright.
func TestDocumentService_Create_CyrillicTitleGetsAMeaningfulSlug(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "Регламент выката", "")

	assert.Equal(t, "регламент-выката", doc.Slug)
}

// Two differently-worded Cyrillic titles in one folder must not collapse onto
// the same degenerate slug — that's the exact defect this task reports.
func TestDocumentService_Create_TwoCyrillicTitlesInSameFolderGetDistinctSlugs(t *testing.T) {
	f := setupDocumentService(t)

	first := f.create(t, "Регламент выката", "")
	second := f.create(t, "Правила дежурства", "")

	assert.NotEqual(t, first.Slug, second.Slug)
	assert.Equal(t, "регламент-выката", first.Slug)
	assert.Equal(t, "правила-дежурства", second.Slug)
}

// A mixed-script title keeps both scripts rather than dropping the non-Latin
// half, which the old ASCII-only rule did ("Смешанный Mixed 42" -> "-mixed-42").
func TestDocumentService_Create_MixedScriptTitleKeepsBothScripts(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "Смешанный Mixed 42", "")

	assert.Equal(t, "смешанный-mixed-42", doc.Slug)
}

// Latin with diacritics is also unicode.IsLetter, so it now survives instead of
// being dropped by the old ASCII range.
func TestDocumentService_Create_LatinWithDiacriticsIsPreserved(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "Ünïcode Ïssue", "")

	assert.Equal(t, "ünïcode-ïssue", doc.Slug)
}

// Plain latin is the regression check: unchanged from before this fix.
func TestDocumentService_Create_PlainLatinTitleUnaffected(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "Latin Test Page", "")

	assert.Equal(t, "latin-test-page", doc.Slug)
}

// A title made only of emoji has no letters or digits, and Slugify's trim only
// strips leading/trailing '-' — so a pure-emoji title collapses to "" (all
// hyphens, entirely trimmed) and hits the fallback the same way it always did.
func TestDocumentService_Create_EmojiOnlyTitleFallsBackToID(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "!!! 🎉🎉🎉 ---", "")

	assert.Equal(t, "doc-"+doc.ID.String()[:8], doc.Slug)
}

// The sharper case: Slugify's trim strips only '-', not '_', so a title with an
// underscore among otherwise-dropped characters slugifies to a NON-empty string
// ("_") that still has no letter or digit. A bare `slug == ""` check misses this
// exactly the class of bug this task reports, just one layer further down — this
// is why hasLetterOrDigit checks for a letter/digit rather than for emptiness.
func TestDocumentService_Create_UnderscoreOnlyTitleFallsBackToID(t *testing.T) {
	f := setupDocumentService(t)

	doc := f.create(t, "🎉 _ 🎉", "")

	assert.Equal(t, "doc-"+doc.ID.String()[:8], doc.Slug)
}

// An explicit non-ASCII slug (input.Slug) gets the same unicode-aware treatment
// as one derived from the title — the old ASCII-only rule applied to whichever
// one an agent thought to pass.
func TestDocumentService_Create_ExplicitNonLatinSlugIsUnicodeAware(t *testing.T) {
	f := setupDocumentService(t)

	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID, Title: "Notes", Slug: "Заметки",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	assert.Equal(t, "заметки", doc.Slug)
}

// The row is what makes the object reachable, so a failed insert must not leave
// the body behind as unreferenced garbage.
func TestDocumentService_Create_RepoFailureRemovesTheUploadedBody(t *testing.T) {
	f := setupDocumentService(t)
	f.repo.createErr = errors.New("insert failed")

	_, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Doomed",
		Body:      "orphan body",
	})

	require.Error(t, err)
	assert.Empty(t, f.storage.objects, "the uploaded body outlived the failed insert")
}

func TestDocumentService_Create_RejectsForeignParent(t *testing.T) {
	f := setupDocumentService(t)

	// A document of another project, seeded directly.
	foreignParent := uuid.New()
	f.repo.Seed(&domain.Document{ID: foreignParent, ProjectID: uuid.New(), Title: "Someone else's"})

	_, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Child",
		ParentID:  &foreignParent,
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode(),
		"a document may not hang off another project's document")
}

func TestDocumentService_GetByIDInWorkspace_ReturnsBody(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Runbook", "step one")

	got, err := f.svc.GetByIDInWorkspace(context.Background(), created.ID, f.wsID)
	require.NoError(t, err)

	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "step one", got.Body, "the single-document read fetches the markdown")
}

// The negative control at the service layer: the same id, asked for by another
// tenant, is a 404 and not the document.
func TestDocumentService_GetByIDInWorkspace_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Confidential", "secret")

	got, err := f.svc.GetByIDInWorkspace(context.Background(), created.ID, uuid.New())

	require.Nil(t, got)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// An unreadable body must not be served as an empty one: an editor that renders
// "" and then saves would overwrite the real document with nothing.
func TestDocumentService_GetByIDInWorkspace_StorageFailureIsAnError(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Runbook", "step one")
	f.storage.errToReturn = errors.New("s3 down")

	got, err := f.svc.GetByIDInWorkspace(context.Background(), created.ID, f.wsID)

	require.Nil(t, got)
	require.Error(t, err)
}

func TestDocumentService_Update(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Draft", "old body")

	newTitle := "Final"
	newBody := "new body"
	newPos := 7
	updated, err := f.svc.Update(context.Background(), created.ID, f.wsID, UpdateDocumentInput{
		Title:    &newTitle,
		Body:     &newBody,
		Position: &newPos,
	})
	require.NoError(t, err)

	assert.Equal(t, "Final", updated.Title)
	assert.Equal(t, 7, updated.Position)
	assert.Equal(t, created.Slug, updated.Slug, "the slug is not rewritten by a rename")
	assert.Equal(t, created.StorageKey, updated.StorageKey, "the body stays at the same key")
	assert.Equal(t, "new body", string(f.storage.objects[created.StorageKey]))
}

func TestDocumentService_Update_ClearParentMovesToRoot(t *testing.T) {
	f := setupDocumentService(t)
	parent := f.create(t, "Parent", "")
	child, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Child",
		ParentID:  &parent.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, child.ParentID)

	updated, err := f.svc.Update(context.Background(), child.ID, f.wsID, UpdateDocumentInput{ClearParent: true})
	require.NoError(t, err)
	assert.Nil(t, updated.ParentID)
}

func TestDocumentService_Update_RejectsSelfParent(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Self", "")

	_, err := f.svc.Update(context.Background(), doc.ID, f.wsID, UpdateDocumentInput{ParentID: &doc.ID})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// A document moved under its own descendant takes the whole subtree out of the
// tree — the cycle is unreachable from the roots, so it vanishes from every
// listing that walks down.
func TestDocumentService_Update_RejectsCycle(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	parent := f.create(t, "Parent", "")
	child, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Child",
		ParentID:  &parent.ID,
	})
	require.NoError(t, err)

	_, err = f.svc.Update(ctx, parent.ID, f.wsID, UpdateDocumentInput{ParentID: &child.ID})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentService_Update_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Confidential", "secret")

	newTitle := "hijacked"
	_, err := f.svc.Update(context.Background(), created.ID, uuid.New(), UpdateDocumentInput{Title: &newTitle})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	stored, err := f.repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Confidential", stored.Title, "the cross-tenant rename reached the row")
}

func TestDocumentService_Update_BodyTooLarge(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Draft", "small")

	huge := strings.Repeat("x", maxDocumentBodyBytes+1)
	_, err := f.svc.Update(context.Background(), created.ID, f.wsID, UpdateDocumentInput{Body: &huge})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, "small", string(f.storage.objects[created.StorageKey]), "the oversized body was not stored")
}

// The delete is reversible by design, so the body has to survive it — a restored
// document whose row still claims content but whose object is gone is silent
// data loss.
func TestDocumentService_Delete_SoftDeletesAndKeepsTheBody(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()
	created := f.create(t, "Doomed", "still here")

	require.NoError(t, f.svc.Delete(ctx, created.ID, f.wsID, uuid.New(), domain.ActorTypeUser))

	gone, err := f.repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, gone, "the document is no longer readable")
	assert.Equal(t, "still here", string(f.storage.objects[created.StorageKey]),
		"the stored body was dropped, so a restore would return an empty document")
}

func TestDocumentService_Delete_TakesTheSubtreeWithIt(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	parent := f.create(t, "Parent", "")
	child, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Child",
		ParentID:  &parent.ID,
	})
	require.NoError(t, err)

	require.NoError(t, f.svc.Delete(ctx, parent.ID, f.wsID, uuid.New(), domain.ActorTypeUser))

	orphan, err := f.repo.GetByID(ctx, child.ID)
	require.NoError(t, err)
	assert.Nil(t, orphan, "the child outlived its deleted parent")
}

func TestDocumentService_Delete_OtherWorkspaceIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Confidential", "secret")

	err := f.svc.Delete(context.Background(), created.ID, uuid.New(), uuid.New(), domain.ActorTypeUser)

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	stored, err := f.repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	assert.NotNil(t, stored, "the cross-tenant delete reached the row")
}

func TestDocumentService_ListByProject(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	first, err := f.svc.Create(ctx, CreateDocumentInput{ProjectID: f.projectID, Title: "Second", Position: 2})
	require.NoError(t, err)
	second, err := f.svc.Create(ctx, CreateDocumentInput{ProjectID: f.projectID, Title: "First", Position: 1})
	require.NoError(t, err)

	page, err := f.svc.ListByProject(ctx, f.projectID, pagination.Params{})
	require.NoError(t, err)

	require.Len(t, page.Items, 2)
	assert.Equal(t, second.ID, page.Items[0].ID, "documents come back in sibling order")
	assert.Equal(t, first.ID, page.Items[1].ID)
	assert.Empty(t, page.Items[0].Body, "a list does not go to object storage for every body")
}

// Without object storage the API still serves everything else, and the routes
// that need a body say so instead of failing as a 500.
func TestDocumentService_NoStorageConfigured(t *testing.T) {
	projectID := uuid.New()
	repo := NewMockDocumentRepository().WithProjectWorkspace(projectID, uuid.New())
	projectRepo := NewMockProjectRepository()
	require.NoError(t, projectRepo.Create(context.Background(), &domain.Project{ID: projectID, WorkspaceID: uuid.New()}))

	svc := NewDocumentService(repo, nil, projectRepo, NewMockDocumentCommentRepository())

	_, err := svc.Create(context.Background(), CreateDocumentInput{ProjectID: projectID, Title: "No storage"})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 503, apiErr.StatusCode())
}

func TestDocumentService_Create_BodyTooLarge(t *testing.T) {
	f := setupDocumentService(t)

	_, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Huge",
		Body:      strings.Repeat("x", maxDocumentBodyBytes+1),
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Empty(t, f.storage.objects, "an oversized body was streamed to storage before being refused")
}

// An upload that fails must not leave a row pointing at an object that is not
// there: the document would read as an empty one forever.
func TestDocumentService_Create_UploadFailureCreatesNoRow(t *testing.T) {
	f := setupDocumentService(t)
	f.storage.errToReturn = errors.New("s3 down")

	_, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID,
		Title:     "Doomed",
		Body:      "content",
	})

	require.Error(t, err)
	page, listErr := f.svc.ListByProject(context.Background(), f.projectID, pagination.Params{})
	require.NoError(t, listErr)
	assert.Empty(t, page.Items, "a document row survived a failed upload")
}

func TestDocumentService_Update_RejectsEmptyTitle(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Draft", "body")

	blank := "   "
	_, err := f.svc.Update(context.Background(), created.ID, f.wsID, UpdateDocumentInput{Title: &blank})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentService_Update_UploadFailureIsAnError(t *testing.T) {
	f := setupDocumentService(t)
	created := f.create(t, "Draft", "old")
	f.storage.errToReturn = errors.New("s3 down")

	newBody := "new"
	_, err := f.svc.Update(context.Background(), created.ID, f.wsID, UpdateDocumentInput{Body: &newBody})

	require.Error(t, err)
	stored, getErr := f.repo.GetByID(context.Background(), created.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "Draft", stored.Title, "the row was updated even though the body never landed")
}

// The three read/write paths that need object storage each say "unavailable"
// rather than failing as an internal error when it is not configured. These only
// fire when S3 is down, which is exactly when nobody is watching them.
func TestDocumentService_NoStorageConfigured_ReadAndUpdate(t *testing.T) {
	ctx := context.Background()
	projectID, wsID, docID := uuid.New(), uuid.New(), uuid.New()

	repo := NewMockDocumentRepository().WithProjectWorkspace(projectID, wsID)
	repo.Seed(&domain.Document{ID: docID, ProjectID: projectID, Title: "Runbook", StorageKey: "documents/x/y.md"})
	projectRepo := NewMockProjectRepository()
	require.NoError(t, projectRepo.Create(ctx, &domain.Project{ID: projectID, WorkspaceID: wsID}))

	svc := NewDocumentService(repo, nil, projectRepo, NewMockDocumentCommentRepository())

	t.Run("read", func(t *testing.T) {
		_, err := svc.GetByIDInWorkspace(ctx, docID, wsID)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 503, apiErr.StatusCode())
	})

	t.Run("update with a body", func(t *testing.T) {
		body := "new"
		_, err := svc.Update(ctx, docID, wsID, UpdateDocumentInput{Body: &body})
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 503, apiErr.StatusCode())
	})

	// A metadata-only update needs no storage at all, so it still works.
	t.Run("update without a body", func(t *testing.T) {
		title := "Renamed"
		doc, err := svc.Update(ctx, docID, wsID, UpdateDocumentInput{Title: &title})
		require.NoError(t, err)
		assert.Equal(t, "Renamed", doc.Title)
	})
}
