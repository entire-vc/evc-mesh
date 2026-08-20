package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

const readingDoc = `# Runbook

Preamble.

## Deploy

Push the tag.

### Rollback

Revert it.

## Monitoring

Watch the error rate.
`

// --- outline ---------------------------------------------------------------

func TestDocumentService_Outline(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", readingDoc)

	got, err := f.svc.Outline(context.Background(), doc.ID, f.wsID)
	require.NoError(t, err)

	assert.Equal(t, doc.ID, got.DocumentID)
	assert.Equal(t, "Runbook", got.Title)
	assert.Equal(t, 1, got.Version, "the version the outline was computed from, so a caller can write back safely")
	require.Len(t, got.Outline, 4)
	assert.Equal(t, "deploy", got.Outline[1].Anchor)
}

// The outline is computed from the body every time. A stored one drifts from the
// document the first time somebody edits a heading, and a stale table of contents
// is worse than none because it looks authoritative.
func TestDocumentService_Outline_FollowsTheBody(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "# One\n")

	_, err := f.update(t, doc.ID, UpdateDocumentInput{Body: ptr("# One\n\n## Two\n")})
	require.NoError(t, err)

	got, err := f.svc.Outline(context.Background(), doc.ID, f.wsID)
	require.NoError(t, err)

	require.Len(t, got.Outline, 2)
	assert.Equal(t, 2, got.Version)
}

// The tenant check is the same one every other read does, and it has to be: an
// outline is the document's contents by another name.
func TestDocumentService_Outline_OtherTenantIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", readingDoc)

	_, err := f.svc.Outline(context.Background(), doc.ID, uuid.New())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// --- section ---------------------------------------------------------------

func TestDocumentService_Section(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", readingDoc)

	got, err := f.svc.Section(context.Background(), doc.ID, f.wsID, "deploy")
	require.NoError(t, err)

	assert.Equal(t, doc.ID, got.DocumentID)
	assert.Equal(t, 1, got.Version)
	assert.Equal(t, "Deploy", got.Heading.Text)
	assert.Contains(t, got.Content, "### Rollback", "subsections come with it")
	assert.NotContains(t, got.Content, "## Monitoring", "the next sibling does not")
}

func TestDocumentService_Section_UnknownHeadingIs404WithTheList(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", readingDoc)

	_, err := f.svc.Section(context.Background(), doc.ID, f.wsID, "deployment")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Contains(t, apiErr.Details, "monitoring", "the answer names what is there")
}

// 400 rather than 404: the heading exists, twice, and it is the request that is
// under-specified. "Not found" would send the caller looking for something that
// is right in front of them.
func TestDocumentService_Section_AmbiguousHeadingIs400(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "## Rollback, in detail\n\na\n\n## Rollback, in detail\n\nb\n")

	_, err := f.svc.Section(context.Background(), doc.ID, f.wsID, "Rollback, in detail")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Details, "rollback-in-detail-1")
}

func TestDocumentService_Section_OtherTenantIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", readingDoc)

	_, err := f.svc.Section(context.Background(), doc.ID, uuid.New(), "deploy")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// --- path addressing -------------------------------------------------------

// tree seeds architecture/adr/adr-004 and returns the leaf.
func (f *documentFixture) tree(t *testing.T) *domain.Document {
	t.Helper()
	arch, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID, Title: "Architecture", Slug: "architecture",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	adr, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID, ParentID: &arch.ID, Title: "ADR", Slug: "adr",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	leaf, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID: f.projectID, ParentID: &adr.ID, Title: "ADR-004", Slug: "adr-004",
		Body:      "# ADR-004\n\nWe chose Postgres.\n",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	return leaf
}

func TestDocumentService_GetByPath(t *testing.T) {
	f := setupDocumentService(t)
	leaf := f.tree(t)

	got, err := f.svc.GetByPath(context.Background(), f.projectID, "architecture/adr/adr-004")
	require.NoError(t, err)

	assert.Equal(t, leaf.ID, got.ID)
	assert.Equal(t, "# ADR-004\n\nWe chose Postgres.\n", got.Body, "the body comes with it, so one call is enough")
}

func TestDocumentService_GetByPath_ToleratesStraySlashes(t *testing.T) {
	f := setupDocumentService(t)
	leaf := f.tree(t)

	for _, path := range []string{
		"/architecture/adr/adr-004",
		"architecture/adr/adr-004/",
		"architecture//adr/adr-004",
	} {
		got, err := f.svc.GetByPath(context.Background(), f.projectID, path)
		require.NoError(t, err, path)
		assert.Equal(t, leaf.ID, got.ID, path)
	}
}

// A bad path is an error that names the segment that failed. "Not found" alone
// sends the caller to re-read their own code; naming the segment sends them to
// look at the tree.
func TestDocumentService_GetByPath_NamesTheFailingSegment(t *testing.T) {
	f := setupDocumentService(t)
	f.tree(t)

	_, err := f.svc.GetByPath(context.Background(), f.projectID, "architecture/adr/adr-999")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Contains(t, apiErr.Details, `"architecture/adr"`, "how far it got")
	assert.Contains(t, apiErr.Details, `"adr-999"`, "and what was missing")
}

func TestDocumentService_GetByPath_FirstSegmentMissing(t *testing.T) {
	f := setupDocumentService(t)
	f.tree(t)

	_, err := f.svc.GetByPath(context.Background(), f.projectID, "nowhere/adr")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Contains(t, apiErr.Details, `"nowhere"`)
}

// A document whose slug was derived (not explicitly set) from a Cyrillic title
// resolves by path the same as any other — the fix is in slug generation, not in
// how a slug, once assigned, gets looked up.
func TestDocumentService_GetByPath_ResolvesACyrillicDerivedSlug(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Регламент выката", "# Регламент\n")

	got, err := f.svc.GetByPath(context.Background(), f.projectID, "регламент-выката")
	require.NoError(t, err)

	assert.Equal(t, doc.ID, got.ID)
}

func TestDocumentService_GetByPath_EmptyPath(t *testing.T) {
	f := setupDocumentService(t)

	_, err := f.svc.GetByPath(context.Background(), f.projectID, "///")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// A path names nothing outside the project it is resolved in, which is what lets
// the route be guarded by :proj_id alone.
func TestDocumentService_GetByPath_OtherProjectDoesNotResolve(t *testing.T) {
	f := setupDocumentService(t)
	f.tree(t)

	_, err := f.svc.GetByPath(context.Background(), uuid.New(), "architecture/adr/adr-004")

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// The documented consequence of paths not being identifiers: move the document
// and the old path stops resolving. Pinned as behaviour so that anyone who later
// wants aliases has to change a test that says why there are none.
func TestDocumentService_GetByPath_BreaksWhenTheDocumentMoves(t *testing.T) {
	f := setupDocumentService(t)
	leaf := f.tree(t)

	_, err := f.update(t, leaf.ID, UpdateDocumentInput{ClearParent: true})
	require.NoError(t, err)

	_, err = f.svc.GetByPath(context.Background(), f.projectID, "architecture/adr/adr-004")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	// The id is what survives, which is why every path lookup returns one.
	byID, err := f.svc.GetByIDInWorkspace(context.Background(), leaf.ID, f.wsID)
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, byID.ID)

	// And it answers to its new path.
	moved, err := f.svc.GetByPath(context.Background(), f.projectID, "adr-004")
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, moved.ID)
}

// A deleted document is not a step you can walk through, at any level.
func TestDocumentService_GetByPath_DeletedAncestorBreaksThePath(t *testing.T) {
	f := setupDocumentService(t)
	leaf := f.tree(t)
	arch, err := f.svc.GetByPath(context.Background(), f.projectID, "architecture")
	require.NoError(t, err)

	require.NoError(t, f.svc.Delete(context.Background(), arch.ID, f.wsID, uuid.New(), domain.ActorTypeUser))

	_, err = f.svc.GetByPath(context.Background(), f.projectID, "architecture/adr/adr-004")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	_ = leaf
}

// --- anchor resolution -----------------------------------------------------

func TestDocumentService_ResolveAnchor(t *testing.T) {
	f := setupDocumentService(t)
	body := "Дежурный обязан немедленно поднять эскалацию и позвать владельца сервиса.\n"
	doc := f.create(t, "Регламент", body)

	got, err := f.svc.ResolveAnchor(context.Background(), doc.ID, f.wsID, ResolveAnchorInput{
		Quote: "поднять эскалацию",
	})
	require.NoError(t, err)

	assert.Equal(t, "поднять эскалацию", body[got.Start:got.End],
		"the offsets slice the body, in bytes")
}

func TestDocumentService_ResolveAnchor_MissingQuoteIs400(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "The deploy gate refuses a release.\n")

	_, err := f.svc.ResolveAnchor(context.Background(), doc.ID, f.wsID, ResolveAnchorInput{
		Quote: "the rollback gate",
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Message, "No such quote")
}

// The ambiguous error travels to the handler untouched, because the match count
// is the one thing the caller needs and flattening it into prose loses it.
func TestDocumentService_ResolveAnchor_AmbiguousKeepsTheCount(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "the API here. the API there. the API again.\n")

	_, err := f.svc.ResolveAnchor(context.Background(), doc.ID, f.wsID, ResolveAnchorInput{Quote: "the API"})

	var ambiguous *mdoc.AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, 3, ambiguous.Matches)
}

func TestDocumentService_ResolveAnchor_ContextDisambiguates(t *testing.T) {
	f := setupDocumentService(t)
	body := "alpha the API omega. beta the API omega.\n"
	doc := f.create(t, "Runbook", body)

	got, err := f.svc.ResolveAnchor(context.Background(), doc.ID, f.wsID, ResolveAnchorInput{
		Quote:  "the API",
		Prefix: "beta ",
	})
	require.NoError(t, err)

	assert.Equal(t, "the API", body[got.Start:got.End])
	assert.Greater(t, got.Start, 20, "the second occurrence")
}

func TestDocumentService_ResolveAnchor_EmptyQuoteIs400(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "body\n")

	_, err := f.svc.ResolveAnchor(context.Background(), doc.ID, f.wsID, ResolveAnchorInput{Quote: "  "})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, "quote is required", apiErr.Validation["quote"])
}

func TestDocumentService_ResolveAnchor_TooLongIs400(t *testing.T) {
	f := setupDocumentService(t)
	long := make([]byte, 2001)
	for i := range long {
		long[i] = 'a'
	}
	doc := f.create(t, "Runbook", string(long))

	_, err := f.svc.ResolveAnchor(context.Background(), doc.ID, f.wsID, ResolveAnchorInput{Quote: string(long)})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.NotEmpty(t, apiErr.Validation["quote"])
}

func TestDocumentService_ResolveAnchor_OtherTenantIsNotFound(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Runbook", "The deploy gate refuses a release.\n")

	_, err := f.svc.ResolveAnchor(context.Background(), doc.ID, uuid.New(), ResolveAnchorInput{
		Quote: "deploy gate",
	})

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}
