package postgres

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// vectorCache keeps decoded chunk embeddings in process memory so the dense
// recall arms stop hauling every vector of a workspace out of Postgres on each
// call.
//
// Without pgvector (ADR-0002) the cosine runs in Go, and the old read path
// selected the `embedding` text of every candidate chunk per recall — tens of
// megabytes of TOASTed base64 for the largest workspace, 1–1.4s per query and
// slower still when a burst of recalls ran it concurrently. The
// vectors themselves almost never change, so the database now only answers
// "which rows, at which version" and the text is read once per row version.
//
// A row version is (id, xmin). Postgres stamps a new xmin on every UPDATE and
// freezing keeps the visible value, so a cached vector can never be served for
// content it was not decoded from. The chunk tables are rewritten by
// delete+insert, which gives fresh ids as well; xmin covers any in-place write.
type vectorCache struct {
	mu        sync.Mutex
	entries   map[vectorKey]*vectorEntry
	floats    int
	maxFloats int
	gen       uint64
}

type vectorKey struct {
	id   uuid.UUID
	xmin int64
}

type vectorEntry struct {
	vec []float32 // nil = stored text did not decode; cached so it is not re-read each call
	gen uint64
}

// vectorRow is one stored embedding as the loader reads it.
type vectorRow struct {
	ID        uuid.UUID `db:"id"`
	Xmin      int64     `db:"xmin"`
	Embedding string    `db:"embedding"`
}

// vectorLoader reads the embedding text of the given row ids.
type vectorLoader func(ctx context.Context, ids []uuid.UUID) ([]vectorRow, error)

// vectorLoadBatch bounds one loader query. A cold cache (first recall after a
// restart) loads a whole workspace; split into batches, no single statement
// lands in the 1s slow log or holds a large result in flight.
const vectorLoadBatch = 1000

// defaultVectorCacheFloats caps one cache at ~48MB of float32. Prod holds ~6.5M
// floats per chunk table today (384-dim), so the cap leaves room to grow.
const defaultVectorCacheFloats = 12 << 20

// vectorEntryCost is what one entry is charged against the cap on top of its
// floats, in float32 units: the key, the entry and its map slot. Without it an
// entry that failed to decode would cost nothing, and a corpus of corrupt rows
// would grow the map without ever reaching the cap.
const vectorEntryCost = 16

func entryCost(vec []float32) int { return len(vec) + vectorEntryCost }

func newVectorCache(maxFloats int) *vectorCache {
	return &vectorCache{entries: make(map[vectorKey]*vectorEntry), maxFloats: maxFloats}
}

// vectors returns the decoded vector of every requested row version, keyed by
// row id. Rows whose text does not decode are left out, as are rows deleted or
// rewritten between the key query and the load. Only versions missing from the
// cache are read through load.
func (c *vectorCache) vectors(ctx context.Context, keys []vectorKey, load vectorLoader) (map[uuid.UUID][]float32, error) {
	out := make(map[uuid.UUID][]float32, len(keys))
	var misses []uuid.UUID
	wantXmin := make(map[uuid.UUID]int64)

	c.mu.Lock()
	c.gen++
	gen := c.gen
	for _, k := range keys {
		if e, ok := c.entries[k]; ok {
			e.gen = gen
			if e.vec != nil {
				out[k.id] = e.vec
			}
			continue
		}
		misses = append(misses, k.id)
		wantXmin[k.id] = k.xmin
	}
	// Checked here too, not only after a load: a call that is all hits must
	// still shed what an earlier, larger working set left above the cap.
	if c.floats > c.maxFloats {
		c.evictUnusedLocked(gen)
	}
	c.mu.Unlock()

	for start := 0; start < len(misses); start += vectorLoadBatch {
		end := min(start+vectorLoadBatch, len(misses))
		rows, err := load(ctx, misses[start:end])
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		for _, row := range rows {
			vec, err := domain.DecodeEmbedding(row.Embedding)
			if err != nil {
				vec = nil
			}
			// The loaded xmin can be newer than the one the key query saw: the
			// row was rewritten in between. That vector is cached under the
			// version it was decoded from, but not served for the older key the
			// caller holds; the row sits out this one call and is a hit on the next.
			c.put(vectorKey{id: row.ID, xmin: row.Xmin}, vec, gen)
			if vec != nil && row.Xmin == wantXmin[row.ID] {
				out[row.ID] = vec
			}
		}
		// The cap is enforced per batch, not once at the end: a later batch
		// that fails returns early, and what earlier batches added must not
		// stay over the cap until some future call happens to succeed.
		if c.floats > c.maxFloats {
			c.evictUnusedLocked(gen)
		}
		c.mu.Unlock()
	}
	return out, nil
}

func (c *vectorCache) put(k vectorKey, vec []float32, gen uint64) {
	if old, ok := c.entries[k]; ok {
		c.floats -= entryCost(old.vec)
	}
	c.entries[k] = &vectorEntry{vec: vec, gen: gen}
	c.floats += entryCost(vec)
}

// evictUnusedLocked drops every entry last touched before the current call:
// versions of deleted or rewritten rows, and other workspaces' vectors. What the
// current call (or a later, concurrent one) used stays even above the cap, so a
// working set larger than the cap does not thrash by reloading itself.
func (c *vectorCache) evictUnusedLocked(gen uint64) {
	for k, e := range c.entries {
		if e.gen < gen {
			c.floats -= entryCost(e.vec)
			delete(c.entries, k)
		}
	}
}
