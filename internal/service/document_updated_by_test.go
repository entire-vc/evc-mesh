package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// "Created by X, last updated by Y on DATE" is the byline the Docs UI wants, and
// updated_by is the half that did not exist. These pin the rule the migration
// states: every mutation stamps it, nothing invents it, and a delete counts as a
// mutation.

func TestDocumentService_Create_StampsTheCreatorAsTheLastEditor(t *testing.T) {
	f := setupDocumentService(t)
	author := uuid.New()

	doc, err := f.svc.Create(context.Background(), CreateDocumentInput{
		ProjectID:     f.projectID,
		Title:         "Runbook",
		CreatedBy:     author,
		CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	// Writing the document IS its most recent change. Leaving the pair NULL until
	// somebody edits would make "never edited" and "predates the column" the same
	// value, and the read model has to tell those apart.
	require.NotNil(t, doc.UpdatedBy)
	assert.Equal(t, author, *doc.UpdatedBy)
	require.NotNil(t, doc.UpdatedByType)
	assert.Equal(t, domain.ActorTypeUser, *doc.UpdatedByType)

	stored, err := f.repo.GetByID(context.Background(), doc.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.UpdatedBy)
	assert.Equal(t, author, *stored.UpdatedBy)
}

func TestDocumentService_Update_StampsTheEditor(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	author := uuid.New()
	created, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID, Title: "Draft", Body: "v1",
		CreatedBy: author, CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	editor := uuid.New()
	newBody := "v2"
	updated, err := f.svc.Update(ctx, created.ID, f.wsID, UpdateDocumentInput{
		Body:          &newBody,
		UpdatedBy:     editor,
		UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	assert.Equal(t, author, updated.CreatedBy, "an edit does not change who created it")
	assert.Equal(t, domain.ActorTypeUser, updated.CreatedByType)
	require.NotNil(t, updated.UpdatedBy)
	assert.Equal(t, editor, *updated.UpdatedBy)
	require.NotNil(t, updated.UpdatedByType)
	assert.Equal(t, domain.ActorTypeAgent, *updated.UpdatedByType,
		"the editor can be an agent, so the type travels with the id")
}

// A caller who only moved the document in the tree still changed it. A stamp that
// fired on some kinds of change and not others would report the wrong person the
// rest of the time.
func TestDocumentService_Update_StampsEvenWhenOnlyThePositionMoved(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	created, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID, Title: "Draft",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	editor := uuid.New()
	pos := 9
	updated, err := f.svc.Update(ctx, created.ID, f.wsID, UpdateDocumentInput{
		Position:      &pos,
		UpdatedBy:     editor,
		UpdatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	require.NotNil(t, updated.UpdatedBy)
	assert.Equal(t, editor, *updated.UpdatedBy)
}

// A delete is a change to every row it touches, and the restore path needs to be
// able to say who made it.
func TestDocumentService_Delete_StampsTheDeleterOnTheWholeSubtree(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	parent, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID, Title: "Parent",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)
	child, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID, Title: "Child", ParentID: &parent.ID,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	deleter := uuid.New()
	require.NoError(t, f.svc.Delete(ctx, parent.ID, f.wsID, deleter, domain.ActorTypeAgent))

	// Both rows are soft-deleted now, so they are read out of the mock's store
	// directly — the repository's own reads hide them, which is the point.
	for name, id := range map[string]uuid.UUID{"parent": parent.ID, "child": child.ID} {
		row := f.repo.items[id]
		require.NotNil(t, row, "%s row vanished", name)
		require.NotNil(t, row.DeletedAt, "%s was not soft-deleted", name)
		require.NotNil(t, row.UpdatedBy, "%s lost its last editor to the delete", name)
		assert.Equal(t, deleter, *row.UpdatedBy, "%s", name)
		require.NotNil(t, row.UpdatedByType)
		assert.Equal(t, domain.ActorTypeAgent, *row.UpdatedByType, "%s", name)
	}
}

// The response to an edit carries the same resolved names the read endpoints do,
// so the client does not have to refetch to render the byline it just changed.
func TestDocumentService_Update_ReturnsTheResolvedNames(t *testing.T) {
	f := setupDocumentService(t)
	ctx := context.Background()

	created, err := f.svc.Create(ctx, CreateDocumentInput{
		ProjectID: f.projectID, Title: "Draft", Body: "v1",
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	// The mock repository is the read side here, so the names are seeded onto the
	// stored row the way the enriched SELECT would compute them.
	creatorName, editorName := "Ada", "howard"
	f.repo.items[created.ID].CreatedByName = &creatorName
	f.repo.items[created.ID].UpdatedByName = &editorName

	newBody := "v2"
	updated, err := f.svc.Update(ctx, created.ID, f.wsID, UpdateDocumentInput{
		Body:          &newBody,
		UpdatedBy:     uuid.New(),
		UpdatedByType: domain.ActorTypeAgent,
	})
	require.NoError(t, err)

	require.NotNil(t, updated.CreatedByName)
	assert.Equal(t, "Ada", *updated.CreatedByName)
	require.NotNil(t, updated.UpdatedByName)
	assert.Equal(t, "howard", *updated.UpdatedByName)
	assert.Equal(t, "v2", updated.Body, "the re-read must not drop the body the caller just wrote")
}
