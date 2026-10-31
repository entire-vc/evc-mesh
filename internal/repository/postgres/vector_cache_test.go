package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// fakeVectorStore is the loader side of the cache: a table of row versions.
// It records every id it was asked for, so a test can tell a hit from a load.
type fakeVectorStore struct {
	rows    map[uuid.UUID]vectorRow
	loaded  []uuid.UUID
	batches int
	err     error
}

func (s *fakeVectorStore) load(_ context.Context, ids []uuid.UUID) ([]vectorRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.batches++
	s.loaded = append(s.loaded, ids...)
	out := make([]vectorRow, 0, len(ids))
	for _, id := range ids {
		if r, ok := s.rows[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeVectorStore) set(id uuid.UUID, xmin int64, vec []float32) vectorKey {
	s.rows[id] = vectorRow{ID: id, Xmin: xmin, Embedding: domain.EncodeEmbedding(vec)}
	return vectorKey{id: id, xmin: xmin}
}

func (k vectorKey) vecOf(s *fakeVectorStore) []float32 {
	vec, _ := domain.DecodeEmbedding(s.rows[k.id].Embedding)
	return vec
}

func newFakeVectorStore() *fakeVectorStore {
	return &fakeVectorStore{rows: make(map[uuid.UUID]vectorRow)}
}

func TestVectorCache_SecondCallAtSameVersionDoesNotLoad(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(1<<20)
	a := store.set(uuid.New(), 1, []float32{1, 0})
	b := store.set(uuid.New(), 1, []float32{0, 1})

	got, err := c.vectors(context.Background(), []vectorKey{a, b}, store.load)
	require.NoError(t, err)
	assert.Equal(t, []float32{1, 0}, got[a.id])
	assert.Len(t, store.loaded, 2)

	got, err = c.vectors(context.Background(), []vectorKey{a, b}, store.load)
	require.NoError(t, err)
	assert.Equal(t, []float32{0, 1}, got[b.id])
	assert.Len(t, store.loaded, 2, "an unchanged row version must be served from the cache")
}

// The whole point of keying on xmin: an in-place rewrite of a row must never be
// answered with the vector decoded from its previous content.
func TestVectorCache_NewXminReloadsTheRow(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(1<<20)
	id := uuid.New()
	old := store.set(id, 1, []float32{1, 0})
	_, err := c.vectors(context.Background(), []vectorKey{old}, store.load)
	require.NoError(t, err)

	cur := store.set(id, 2, []float32{0, 1})
	got, err := c.vectors(context.Background(), []vectorKey{cur}, store.load)
	require.NoError(t, err)
	assert.Equal(t, []float32{0, 1}, got[id])
	assert.Equal(t, []uuid.UUID{id, id}, store.loaded)
}

// A row rewritten between the key query and the load: the loaded vector belongs
// to a version the caller did not ask for, so it is not served for the old key.
// It is cached under the version it came from, so the next call is a hit.
func TestVectorCache_RowRewrittenMidCallIsNotServedForTheOldVersion(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(1<<20)
	id := uuid.New()
	seen := vectorKey{id: id, xmin: 1}
	loaded := store.set(id, 2, []float32{0, 1})

	got, err := c.vectors(context.Background(), []vectorKey{seen}, store.load)
	require.NoError(t, err)
	assert.NotContains(t, got, id, "a vector decoded at xmin 2 must not answer a key at xmin 1")

	got, err = c.vectors(context.Background(), []vectorKey{loaded}, store.load)
	require.NoError(t, err)
	assert.Equal(t, []float32{0, 1}, got[id])
	assert.Len(t, store.loaded, 1, "the version actually loaded must be the one cached")
}

func TestVectorCache_UndecodableAndDeletedRowsAreLeftOut(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(1<<20)
	good := store.set(uuid.New(), 1, []float32{1})
	bad := vectorKey{id: uuid.New(), xmin: 1}
	store.rows[bad.id] = vectorRow{ID: bad.id, Xmin: 1, Embedding: "not-base64!!"}
	gone := vectorKey{id: uuid.New(), xmin: 1}

	got, err := c.vectors(context.Background(), []vectorKey{good, bad, gone}, store.load)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Contains(t, got, good.id)

	// The corrupt row is remembered as corrupt; only the deleted one is asked for again.
	store.loaded = nil
	_, err = c.vectors(context.Background(), []vectorKey{good, bad, gone}, store.load)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{gone.id}, store.loaded)
}

func TestVectorCache_ColdLoadIsBatched(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(1<<30)
	keys := make([]vectorKey, vectorLoadBatch*2+1)
	for i := range keys {
		keys[i] = store.set(uuid.New(), 1, []float32{1})
	}
	got, err := c.vectors(context.Background(), keys, store.load)
	require.NoError(t, err)
	assert.Len(t, got, len(keys))
	assert.Equal(t, 3, store.batches)
}

func TestVectorCache_LoadErrorIsReturned(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(1<<20)
	k := store.set(uuid.New(), 1, []float32{1})
	store.err = errors.New("db down")
	_, err := c.vectors(context.Background(), []vectorKey{k}, store.load)
	require.ErrorContains(t, err, "db down")
}

// Over the cap, what the current call did not use goes; what it used stays,
// even when that alone exceeds the cap.
func TestVectorCache_OverCapEvictsOnlyUnusedEntries(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(2*vectorEntryCost+3)
	a := store.set(uuid.New(), 1, []float32{1, 1})
	b := store.set(uuid.New(), 1, []float32{1, 1})
	_, err := c.vectors(context.Background(), []vectorKey{a}, store.load)
	require.NoError(t, err)
	_, err = c.vectors(context.Background(), []vectorKey{b}, store.load)
	require.NoError(t, err)

	assert.NotContains(t, c.entries, a, "a was not used by the call that overflowed")
	assert.Contains(t, c.entries, b)
	assert.Equal(t, entryCost(b.vecOf(store)), c.floats)

	c2 := newVectorCache(1)
	_, err = c2.vectors(context.Background(), []vectorKey{a, b}, store.load)
	require.NoError(t, err)
	assert.Len(t, c2.entries, 2, "the current working set is kept above the cap")
}

// Entries that failed to decode hold no floats, yet they must still count
// toward the cap, or a corpus of corrupt rows grows the map without bound.
func TestVectorCache_UndecodableEntriesCountTowardTheCap(t *testing.T) {
	store, c := newFakeVectorStore(), newVectorCache(vectorEntryCost)
	bad := func() vectorKey {
		k := vectorKey{id: uuid.New(), xmin: 1}
		store.rows[k.id] = vectorRow{ID: k.id, Xmin: 1, Embedding: "not-base64!!"}
		return k
	}
	first := bad()
	_, err := c.vectors(context.Background(), []vectorKey{first}, store.load)
	require.NoError(t, err)
	_, err = c.vectors(context.Background(), []vectorKey{bad()}, store.load)
	require.NoError(t, err)
	assert.Len(t, c.entries, 1, "the older corrupt entry must be evicted once the cap is crossed")
	assert.NotContains(t, c.entries, first)
}

// A load that fails on a later batch has already cached the earlier ones; the
// cap must hold for them too, not only on calls that load everything.
func TestVectorCache_CapHoldsWhenALaterBatchFails(t *testing.T) {
	store := newFakeVectorStore()
	c := newVectorCache(entryCost([]float32{1}))
	old := store.set(uuid.New(), 1, []float32{1})
	_, err := c.vectors(context.Background(), []vectorKey{old}, store.load)
	require.NoError(t, err)

	keys := make([]vectorKey, vectorLoadBatch+1)
	for i := range keys {
		keys[i] = store.set(uuid.New(), 1, []float32{1})
	}
	calls := 0
	failSecond := func(ctx context.Context, ids []uuid.UUID) ([]vectorRow, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("db down")
		}
		return store.load(ctx, ids)
	}
	_, err = c.vectors(context.Background(), keys, failSecond)
	require.ErrorContains(t, err, "db down")
	assert.NotContains(t, c.entries, old, "entries unused by the failing call must be evicted once its first batch crossed the cap")
}

// A call served entirely from the cache must still bring it back under the cap
// when an earlier, larger working set was kept above it.
func TestVectorCache_AllHitCallEnforcesTheCap(t *testing.T) {
	store := newFakeVectorStore()
	c := newVectorCache(entryCost([]float32{1}))
	a := store.set(uuid.New(), 1, []float32{1})
	b := store.set(uuid.New(), 1, []float32{1})
	_, err := c.vectors(context.Background(), []vectorKey{a, b}, store.load)
	require.NoError(t, err)
	require.Len(t, c.entries, 2, "premise: the working set was kept above the cap")

	_, err = c.vectors(context.Background(), []vectorKey{a}, store.load)
	require.NoError(t, err)
	assert.Len(t, store.loaded, 2, "premise: the second call was all hits")
	assert.NotContains(t, c.entries, b)
	assert.Equal(t, entryCost([]float32{1}), c.floats)
}
