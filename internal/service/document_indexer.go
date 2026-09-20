package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// maxDocChunks bounds one document's chunk set (5 MiB bodies exist in theory).
const maxDocChunks = 200

// DocumentIndexer maintains the recall index over Mesh Docs (#154450b1).
// Write path is gated by writeEnabled (DOC_INDEX_WRITE); the read path is gated
// separately in the memory service (DOC_INDEX_RECALL), so an index can be
// built and inspected before recall ever sees it.
type DocumentIndexer struct {
	repo         repository.DocumentChunkRepository
	embedder     embedding.Embedder
	store        DocumentStore
	writeEnabled bool
}

func NewDocumentIndexer(repo repository.DocumentChunkRepository, embedder embedding.Embedder, store DocumentStore, writeEnabled bool) *DocumentIndexer {
	return &DocumentIndexer{repo: repo, embedder: embedder, store: store, writeEnabled: writeEnabled}
}

// WriteEnabled reports the DOC_INDEX_WRITE flag. Nil-safe.
func (x *DocumentIndexer) WriteEnabled() bool { return x != nil && x.writeEnabled }

type docPiece struct {
	heading string
	text    string
}

// splitDocument chunks a markdown body by H2 section. The text before the first
// H2 is one section headed by the document title; a section longer than the
// embedder window is split further with the same sliding window memories use.
// A body with no text still yields one title-only chunk so every live document
// is represented (the backfill's completeness check depends on it).
func splitDocument(title, body string) []docPiece {
	var sections []docPiece
	pos := 0
	for _, h := range mdoc.Outline(body) {
		if h.Level != 2 {
			continue
		}
		if h.Start > pos {
			sections = append(sections, docPiece{title, body[pos:h.Start]})
		}
		sections = append(sections, docPiece{h.Text, body[h.Start:h.End]})
		pos = h.End
	}
	if pos < len(body) {
		heading := title
		if pos > 0 {
			heading = ""
		}
		sections = append(sections, docPiece{heading, body[pos:]})
	}

	var out []docPiece
	for _, s := range sections {
		text := strings.TrimSpace(s.text)
		if text == "" {
			continue
		}
		for _, c := range chunkText(text, defaultChunkSize, defaultChunkOverlap) {
			out = append(out, docPiece{s.heading, c.Text})
			if len(out) >= maxDocChunks {
				return out
			}
		}
	}
	if len(out) == 0 {
		out = append(out, docPiece{title, title})
	}
	return out
}

// Index (re)builds the chunk set of doc from body. Embedding failure is not
// fatal: chunks are stored without vectors and stay reachable by BM25.
func (x *DocumentIndexer) Index(ctx context.Context, doc *domain.Document, body string) error {
	pieces := splitDocument(doc.Title, body)
	chunks := make([]domain.DocumentChunk, len(pieces))
	for i, p := range pieces {
		chunks[i] = domain.DocumentChunk{ChunkIdx: i, Heading: p.heading, Content: p.text}
	}
	if x.embedder != nil && !embedding.IsNoop(x.embedder) {
		texts := make([]string, len(pieces))
		for i, p := range pieces {
			texts[i] = doc.Title + " / " + p.heading + "\n" + p.text
		}
		vecs, err := embedWithRetry(ctx, x.embedder, texts)
		if err != nil || len(vecs) != len(pieces) {
			log.Printf("[doc-index] embed failed for document %s, indexing BM25-only: %v", doc.ID, err)
		} else {
			model := x.embedder.Model()
			for i, v := range vecs {
				if len(v) == 0 {
					continue
				}
				enc, dim := domain.EncodeEmbedding(v), len(v)
				chunks[i].Embedding, chunks[i].EmbeddingModel, chunks[i].EmbeddingDim = &enc, &model, &dim
			}
		}
	}
	return x.repo.ReplaceChunks(ctx, doc.ID, doc.ProjectID, doc.Version, chunks)
}

// IndexAsync is the create/update hook: detached from the request so a slow
// embedder never delays a document write.
func (x *DocumentIndexer) IndexAsync(ctx context.Context, doc *domain.Document, body string) {
	if !x.WriteEnabled() {
		return
	}
	d := *doc
	bg := context.WithoutCancel(ctx)
	go func() {
		ictx, cancel := context.WithTimeout(bg, 2*time.Minute)
		defer cancel()
		if err := x.Index(ictx, &d, body); err != nil {
			log.Printf("[doc-index] index document %s failed: %v", d.ID, err)
		}
	}()
}

// Remove drops a deleted document's chunks. Search also joins on
// deleted_at IS NULL, so this is hygiene, not the correctness guard.
func (x *DocumentIndexer) Remove(ctx context.Context, documentID uuid.UUID) {
	if !x.WriteEnabled() {
		return
	}
	if err := x.repo.DeleteByDocument(ctx, documentID); err != nil {
		log.Printf("[doc-index] remove chunks of %s failed: %v", documentID, err)
	}
}

// BackfillResult is one backfill batch's outcome.
type BackfillResult struct {
	Indexed   int                   `json:"indexed"`
	Failed    int                   `json:"failed"`
	Purged    int64                 `json:"purged"`
	Status    domain.DocIndexStatus `json:"status"`
	WriteFlag bool                  `json:"write_enabled"`
}

// Backfill indexes up to limit live documents that have no chunk set at their
// current version. Idempotent and resumable: an indexed document is excluded
// from the next selection, so repeated calls converge and a re-run adds nothing.
// Refuses to run with DOC_INDEX_WRITE off.
func (x *DocumentIndexer) Backfill(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID, limit int) (*BackfillResult, error) {
	if !x.WriteEnabled() {
		return nil, fmt.Errorf("document index writes are disabled (DOC_INDEX_WRITE)")
	}
	if x.store == nil {
		return nil, fmt.Errorf("document storage is not configured")
	}
	res := &BackfillResult{WriteFlag: true}
	purged, err := x.repo.PurgeDeleted(ctx)
	if err != nil {
		return nil, err
	}
	res.Purged = purged
	docs, err := x.repo.ListStale(ctx, wsID, projID, limit)
	if err != nil {
		return nil, err
	}
	for i := range docs {
		d := &docs[i]
		rc, dlErr := x.store.Download(ctx, d.StorageKey)
		if dlErr != nil {
			log.Printf("[doc-index] backfill: download %s: %v", d.ID, dlErr)
			res.Failed++
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(rc, maxDocumentBodyBytes))
		_ = rc.Close()
		if rerr != nil {
			res.Failed++
			continue
		}
		if idxErr := x.Index(ctx, d, string(b)); idxErr != nil {
			log.Printf("[doc-index] backfill: index %s: %v", d.ID, idxErr)
			res.Failed++
			continue
		}
		res.Indexed++
	}
	if res.Status, err = x.repo.Status(ctx, wsID, projID); err != nil {
		return nil, err
	}
	return res, nil
}

// Status reports live vs indexed document counts.
func (x *DocumentIndexer) Status(ctx context.Context, wsID uuid.UUID, projID *uuid.UUID) (domain.DocIndexStatus, error) {
	return x.repo.Status(ctx, wsID, projID)
}
