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
	"github.com/entire-vc/evc-mesh/pkg/markdown"
)

// fence is written out of band because the fixtures contain backtick fences and
// Go raw strings cannot.
const fence = "```"

// runbookBody is a Russian document with the shapes that break naive parsers: a
// fenced block holding heading-looking lines, a nested subsection, and the same
// heading text under two different parents.
var runbookBody = strings.Join([]string{
	"# Раннбук",
	"",
	"Вступление.",
	"",
	"## Установка",
	"",
	"### Linux",
	"",
	fence,
	"# не заголовок",
	fence,
	"",
	"Ставим через apt.",
	"",
	"#### Зависимости",
	"",
	"libpq и goose.",
	"",
	"### macOS",
	"",
	"Ставим через brew.",
	"",
	"## Настройка",
	"",
	"### Linux",
	"",
	"Правим /etc/mesh.conf.",
	"",
}, "\n")

// AC1 — the table of contents the service returns reflects the document's real
// heading structure, and nothing from inside a code fence.
func TestDocumentService_TOC(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Раннбук", runbookBody)

	toc, err := f.svc.TOC(context.Background(), doc.ID, f.wsID)
	require.NoError(t, err)

	assert.Equal(t, doc.ID, toc.DocumentID)
	assert.Equal(t, "Раннбук", toc.Title)

	type entry struct {
		level  int
		text   string
		anchor string
	}
	got := make([]entry, 0, len(toc.Headings))
	for _, h := range toc.Headings {
		got = append(got, entry{h.Level, h.Text, h.Anchor})
	}
	assert.Equal(t, []entry{
		{1, "Раннбук", "раннбук"},
		{2, "Установка", "установка"},
		{3, "Linux", "linux-1"},
		{4, "Зависимости", "зависимости"},
		{3, "macOS", "macos"},
		{2, "Настройка", "настройка"},
		{3, "Linux", "linux-2"},
	}, got)
}

// AC6 — the load-bearing test for "computed on read, never stored".
//
// Edit the body through the ordinary Update path, ask for the outline again, and
// the new heading is there. If the outline were persisted or memoised anywhere,
// this returns the pre-edit structure and the failure is silent in production:
// the entries still look like a table of contents, they just describe a document
// that no longer exists.
func TestDocumentService_TOCIsRecomputedAfterTheBodyChanges(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()
	doc := f.create(t, "Раннбук", runbookBody)

	before, err := f.svc.TOC(ctx, doc.ID, f.wsID)
	require.NoError(t, err)
	require.Len(t, before.Headings, 7)
	for _, h := range before.Headings {
		require.NotEqual(t, "Диагностика", h.Text, "the section does not exist yet")
	}
	_, err = f.svc.Section(ctx, doc.ID, f.wsID, "диагностика")
	require.Error(t, err, "a section that has not been written must not resolve")

	edited := runbookBody + "\n## Диагностика\n\n### Логи\n\nСмотрим journalctl.\n"
	_, err = f.svc.Update(ctx, doc.ID, f.wsID, UpdateDocumentInput{
		Body:          &edited,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	after, err := f.svc.TOC(ctx, doc.ID, f.wsID)
	require.NoError(t, err)
	require.Len(t, after.Headings, 9, "the two new headings are in the outline")
	assert.Equal(t, "Диагностика", after.Headings[7].Text)
	assert.Equal(t, "Логи", after.Headings[8].Text)

	// And the new section is now addressable with its content.
	section, err := f.svc.Section(ctx, doc.ID, f.wsID, "диагностика/логи")
	require.NoError(t, err)
	assert.Contains(t, section.Content, "Смотрим journalctl.")

	// The reverse direction too: a heading REMOVED from the body leaves the
	// outline. A stored table of contents typically keeps growing and never
	// shrinks, so deletion is the half that catches it.
	shrunk := "# Раннбук\n\nОсталось только вступление.\n"
	_, err = f.svc.Update(ctx, doc.ID, f.wsID, UpdateDocumentInput{
		Body:          &shrunk,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	final, err := f.svc.TOC(ctx, doc.ID, f.wsID)
	require.NoError(t, err)
	require.Len(t, final.Headings, 1)
	assert.Equal(t, "Раннбук", final.Headings[0].Text)

	_, err = f.svc.Section(ctx, doc.ID, f.wsID, "установка")
	require.Error(t, err, "a section deleted from the body must stop resolving")
}

// AC2 + AC3 through the service: only that section, subsections included.
func TestDocumentService_Section(t *testing.T) {
	f := setupDocumentService(t)
	doc := f.create(t, "Раннбук", runbookBody)

	section, err := f.svc.Section(context.Background(), doc.ID, f.wsID, "установка/linux")
	require.NoError(t, err)

	assert.Equal(t, doc.ID, section.DocumentID)
	assert.Equal(t, "Linux", section.Heading.Text)
	assert.True(t, strings.HasPrefix(section.Content, "### Linux\n"))

	assert.Contains(t, section.Content, "Ставим через apt.")
	assert.Contains(t, section.Content, "#### Зависимости", "the subsection travels with its parent")
	assert.Contains(t, section.Content, "libpq и goose.")

	assert.NotContains(t, section.Content, "macOS", "the next sibling heading leaked in")
	assert.NotContains(t, section.Content, "Ставим через brew.")
	assert.NotContains(t, section.Content, "Настройка")
}

// AC4 through the service — the repeated heading is addressable per parent, and
// the bare name is a 400 that says how to disambiguate rather than a 200 holding
// the wrong section.
func TestDocumentService_Section_DuplicateHeadings(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()
	doc := f.create(t, "Раннбук", runbookBody)

	install, err := f.svc.Section(ctx, doc.ID, f.wsID, "установка/linux")
	require.NoError(t, err)
	assert.Contains(t, install.Content, "Ставим через apt.")
	assert.NotContains(t, install.Content, "Правим /etc/mesh.conf.")

	configure, err := f.svc.Section(ctx, doc.ID, f.wsID, "настройка/linux")
	require.NoError(t, err)
	assert.Contains(t, configure.Content, "Правим /etc/mesh.conf.")
	assert.NotContains(t, configure.Content, "Ставим через apt.")

	_, err = f.svc.Section(ctx, doc.ID, f.wsID, "linux")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode(),
		"an ambiguous reference is the caller being imprecise, not a missing section")
	assert.Contains(t, err.Error(), "linux-1")
	assert.Contains(t, err.Error(), "linux-2")

	// The anchors the refusal offers must actually work.
	byAnchor, err := f.svc.Section(ctx, doc.ID, f.wsID, "linux-2")
	require.NoError(t, err)
	assert.Contains(t, byAnchor.Content, "Правим /etc/mesh.conf.")
}

func TestDocumentService_Section_UnknownRefAndEmptyRef(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()
	doc := f.create(t, "Раннбук", runbookBody)

	_, err := f.svc.Section(ctx, doc.ID, f.wsID, "нет-такого")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	_, err = f.svc.Section(ctx, doc.ID, f.wsID, "  ")
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

// A document in another tenant is not readable through the new routes either.
// The outline and a section are still document reads, and a new read path is a
// new chance to forget the workspace check.
func TestDocumentService_TOCAndSection_RefuseAnotherWorkspace(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()
	doc := f.create(t, "Раннбук", runbookBody)
	stranger := uuid.New()

	_, err := f.svc.TOC(ctx, doc.ID, stranger)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	_, err = f.svc.Section(ctx, doc.ID, stranger, "установка")
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// ---------------------------------------------------------------------------
// Path addressing
// ---------------------------------------------------------------------------

// tree builds architecture/adr/adr-004 and returns the leaf.
func (f *documentFixture) tree(t *testing.T) *domain.Document {
	t.Helper()
	ctx := context.Background()
	mk := func(title, slug string, parent *uuid.UUID, body string) *domain.Document {
		doc, err := f.svc.Create(ctx, CreateDocumentInput{
			ProjectID:     f.projectID,
			ParentID:      parent,
			Slug:          slug,
			Title:         title,
			Body:          body,
			CreatedBy:     uuid.New(),
			CreatedByType: domain.ActorTypeUser,
		})
		require.NoError(t, err)
		return doc
	}
	arch := mk("Architecture", "architecture", nil, "# Architecture\n")
	adr := mk("ADR", "adr", &arch.ID, "# ADR\n")
	return mk("ADR-004", "adr-004", &adr.ID,
		"# ADR-004\n\n## Контекст\n\nПочему.\n\n## Решение\n\nЧто решили.\n")
}

func TestDocumentService_GetByPath(t *testing.T) {
	f := setupDocumentService(t)
	leaf := f.tree(t)

	doc, err := f.svc.GetByPathInWorkspace(context.Background(), f.projectID, f.wsID, "architecture/adr/adr-004")
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, doc.ID)
	assert.Contains(t, doc.Body, "## Решение", "the body comes with the document")

	// Stray separators are noise, not a lookup for an empty slug.
	same, err := f.svc.GetByPathInWorkspace(context.Background(), f.projectID, f.wsID, "/architecture//adr/adr-004/")
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, same.ID)
}

// AC4 (Mesh) — a path that does not resolve says WHERE it broke rather than
// answering with nothing.
func TestDocumentService_GetByPath_MissingSegmentIsNamed(t *testing.T) {
	f := setupDocumentService(t)
	f.tree(t)
	ctx := context.Background()

	_, err := f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, "architecture/adr/adr-999")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
	assert.Contains(t, err.Error(), "adr-999", "the error names the segment that failed")
	assert.Contains(t, err.Error(), "architecture/adr", "and the prefix that did resolve")

	_, err = f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, "nope/adr/adr-004")
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, err.Error(), "nope")
	assert.Contains(t, err.Error(), "root")

	// A real document named out of order is still a miss: the path is the
	// chain, not a set of slugs that appear somewhere in the project.
	_, err = f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, "adr/architecture/adr-004")
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())

	// A prefix of a real path resolves to the ancestor, not to the leaf.
	mid, err := f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, "architecture/adr")
	require.NoError(t, err)
	assert.Equal(t, "adr", mid.Slug)
}

func TestDocumentService_GetByPath_EmptyAndOverlongPaths(t *testing.T) {
	f := setupDocumentService(t)
	f.tree(t)
	ctx := context.Background()

	var apiErr *apierror.Error
	for _, p := range []string{"", "/", "   ", "///"} {
		_, err := f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, p)
		require.ErrorAs(t, err, &apiErr, "path %q", p)
		assert.Equal(t, 400, apiErr.StatusCode(), "path %q", p)
	}

	deep := strings.Repeat("a/", maxDocumentPathDepth+1)
	_, err := f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, deep)
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
}

func TestDocumentService_GetByPath_RefusesAnotherWorkspace(t *testing.T) {
	f := setupDocumentService(t)
	f.tree(t)

	_, err := f.svc.GetByPathInWorkspace(context.Background(), f.projectID, uuid.New(), "architecture/adr/adr-004")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

// A path address is bound to where the document sits in the tree, so moving it
// breaks the old path. Asserted rather than merely documented: it is the known
// cost of path addressing, and a test is what stops it being re-discovered as a
// bug later.
func TestDocumentService_GetByPath_BreaksAfterAMove(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()
	leaf := f.tree(t)

	root, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID:     f.projectID,
		Slug:          "decisions",
		Title:         "Decisions",
		Body:          "# Decisions\n",
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	_, err = f.svc.Update(ctx, leaf.ID, f.wsID, UpdateDocumentInput{
		ParentID:      &root.ID,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	_, err = f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, "architecture/adr/adr-004")
	require.Error(t, err, "the old path must stop resolving rather than resolve to something else")

	moved, err := f.svc.GetByPathInWorkspace(ctx, f.projectID, f.wsID, "decisions/adr-004")
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, moved.ID)

	// The id is the address that survives a move — which is why the id route
	// is not going anywhere.
	byID, err := f.svc.GetByIDInWorkspace(ctx, leaf.ID, f.wsID)
	require.NoError(t, err)
	assert.Equal(t, leaf.ID, byID.ID)
}

// The outline type is what the API serialises, so the JSON tags have to be the
// ones a client is told to read.
func TestDocumentTOC_HeadingShape(t *testing.T) {
	h := markdown.Heading{Level: 2, Text: "Установка", Anchor: "установка", Path: []string{"раннбук", "установка"}}
	assert.Equal(t, "раннбук/установка", h.PathString())
}
