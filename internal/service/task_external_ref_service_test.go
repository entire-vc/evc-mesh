package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// memExtRefRepo reproduces the write-once-hash contract of the Postgres repo.
type memExtRefRepo struct {
	m map[uuid.UUID]*domain.TaskExternalRef
}

func (r *memExtRefRepo) Get(_ context.Context, id uuid.UUID) (*domain.TaskExternalRef, error) {
	return r.m[id], nil
}

func (r *memExtRefRepo) Put(_ context.Context, in *domain.TaskExternalRef) (*domain.TaskExternalRef, bool, error) {
	if cur, ok := r.m[in.TaskID]; ok && cur.Hash != in.Hash {
		return cur, false, nil
	}
	cp := *in
	r.m[in.TaskID] = &cp
	return &cp, true, nil
}

func TestTaskExternalRef_HashIsWriteOnce(t *testing.T) {
	svc := NewTaskExternalRefService(&memExtRefRepo{m: map[uuid.UUID]*domain.TaskExternalRef{}})
	id := uuid.New()
	ctx := context.Background()

	_, err := svc.Put(ctx, id, "keep:req-1", "h1", nil)
	require.NoError(t, err)

	got, err := svc.Put(ctx, id, "keep:req-1b", "h1", nil)
	require.NoError(t, err, "same hash is idempotent")
	assert.Equal(t, "keep:req-1b", got.Ref)

	got, err = svc.Put(ctx, id, "keep:req-1", "h2", nil)
	assert.ErrorIs(t, err, ErrExternalHashConflict)
	assert.Equal(t, "h1", got.Hash, "a conflicting write must not replace the pinned hash")
}

func TestTaskExternalRef_Validation(t *testing.T) {
	svc := NewTaskExternalRefService(&memExtRefRepo{m: map[uuid.UUID]*domain.TaskExternalRef{}})
	for name, c := range map[string][2]string{
		"empty ref":  {"", "h"},
		"empty hash": {"r", " "},
		"long ref":   {strings.Repeat("a", domain.MaxExternalRefLen+1), "h"},
		"long hash":  {"r", strings.Repeat("a", domain.MaxExternalHashLen+1)},
	} {
		_, err := svc.Put(context.Background(), uuid.New(), c[0], c[1], nil)
		assert.ErrorIs(t, err, ErrExternalRefInvalid, name)
	}
}
