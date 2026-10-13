package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// resolveFaultRepo overrides the four reads resolveMovedPath makes so each
// error and race branch can be driven without a database.
type resolveFaultRepo struct {
	*MockDocumentRepository
	aliasDoc *domain.Document
	aliasErr error
	pathErr  error
	hits     []repository.DocumentPathHit
	hitsErr  error
	byIDDoc  *domain.Document
	byIDErr  error
}

func (r *resolveFaultRepo) GetByPathAlias(context.Context, uuid.UUID, string) (*domain.Document, error) {
	return r.aliasDoc, r.aliasErr
}

func (r *resolveFaultRepo) PathOf(context.Context, uuid.UUID) (string, error) {
	return "new/home", r.pathErr
}

func (r *resolveFaultRepo) FindLiveBySlug(context.Context, uuid.UUID, string, int) ([]repository.DocumentPathHit, error) {
	return r.hits, r.hitsErr
}

func (r *resolveFaultRepo) GetByID(context.Context, uuid.UUID) (*domain.Document, error) {
	return r.byIDDoc, r.byIDErr
}

func newResolveSvc(repo *resolveFaultRepo) *documentService {
	repo.MockDocumentRepository = NewMockDocumentRepository()
	return NewDocumentService(repo, NewMockStorageClient(), NewMockProjectRepository(),
		NewMockDocumentCommentRepository()).(*documentService)
}

func TestResolveMovedPath_AliasHitSetsMovedTo(t *testing.T) {
	repo := &resolveFaultRepo{aliasDoc: &domain.Document{ID: uuid.New()}}
	got, err := newResolveSvc(repo).resolveMovedPath(context.Background(), uuid.New(), []string{"old", "home"})
	require.NoError(t, err)
	assert.Equal(t, "new/home", got.MovedTo)
}

func TestResolveMovedPath_RepoErrorsSurface(t *testing.T) {
	boom := errors.New("boom")
	one := []string{"slug"}
	cases := map[string]struct {
		repo *resolveFaultRepo
		segs []string
	}{
		"alias lookup":  {&resolveFaultRepo{aliasErr: boom}, one},
		"alias path-of": {&resolveFaultRepo{aliasDoc: &domain.Document{ID: uuid.New()}, pathErr: boom}, one},
		"slug lookup":   {&resolveFaultRepo{hitsErr: boom}, one},
		"hit by id": {&resolveFaultRepo{
			hits: []repository.DocumentPathHit{{ID: uuid.New(), Path: "a/slug"}}, byIDErr: boom}, one},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newResolveSvc(c.repo).resolveMovedPath(context.Background(), uuid.New(), c.segs)
			assert.Nil(t, got)
			assert.ErrorIs(t, err, boom)
		})
	}
}

func TestResolveMovedPath_MultiSegmentMissIsPlainMiss(t *testing.T) {
	got, err := newResolveSvc(&resolveFaultRepo{}).resolveMovedPath(context.Background(), uuid.New(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestResolveMovedPath_HitDeletedBetweenReadsIsPlainMiss(t *testing.T) {
	repo := &resolveFaultRepo{hits: []repository.DocumentPathHit{{ID: uuid.New(), Path: "a/slug"}}}
	got, err := newResolveSvc(repo).resolveMovedPath(context.Background(), uuid.New(), []string{"slug"})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestResolveMovedPath_UniqueBareSlugSetsMovedTo(t *testing.T) {
	id := uuid.New()
	repo := &resolveFaultRepo{
		hits:    []repository.DocumentPathHit{{ID: id, Path: "a/slug"}},
		byIDDoc: &domain.Document{ID: id},
	}
	got, err := newResolveSvc(repo).resolveMovedPath(context.Background(), uuid.New(), []string{"slug"})
	require.NoError(t, err)
	assert.Equal(t, "a/slug", got.MovedTo)
}

func TestResolveMovedPath_AmbiguousListsCandidatesAndCapsThem(t *testing.T) {
	var hits []repository.DocumentPathHit
	for i := 0; i <= maxAmbiguousSlugCandidates; i++ { // one past the cap
		hits = append(hits, repository.DocumentPathHit{ID: uuid.New(), Path: fmt.Sprintf("f%02d/slug", i)})
	}
	got, err := newResolveSvc(&resolveFaultRepo{hits: hits}).resolveMovedPath(context.Background(), uuid.New(), []string{"slug"})
	assert.Nil(t, got)
	require.Error(t, err)
	msg := fmt.Sprint(err)
	assert.Contains(t, msg, "Ambiguous")
	assert.Contains(t, msg, "f00/slug")
	assert.NotContains(t, msg, fmt.Sprintf("f%02d/slug", maxAmbiguousSlugCandidates), "the candidate past the cap must not be listed")
	assert.Contains(t, msg, "first 10 shown")
}
