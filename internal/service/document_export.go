package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// maxExportDocuments bounds how many documents a single tree export may touch,
// checked before any body leaves object storage. The largest document tree
// measured in this workspace today is under 120 pages; 500 stays generous above
// that while still bounding worst-case request cost — at a rough ~50ms DB+S3
// round-trip per document, 500 is a ~25s ceiling rather than the minutes an
// unbounded walk on a pathological tree could take.
const maxExportDocuments = 500

// maxExportTotalBytes bounds the combined body size a tree export may
// download. Each document body is already capped at maxDocumentBodyBytes
// (5 MiB) by Create/Update, so maxExportDocuments documents at that ceiling
// could in principle total 2.5 GiB — far past what a PDF/DOCX renderer or a
// user's browser download should have to hold at once. 50 MiB keeps a
// worst-case export inside what comfortably fits in memory during rendering
// without being so tight that a legitimate large handbook (a few dozen
// image-free pages) trips it.
const maxExportTotalBytes = 50 << 20 // 50 MiB

// ExportTreeTooLargeError is returned by WalkExportTree when the subtree
// rooted at the requested document exceeds an export ceiling. It names the
// actual measurement against the limit — never a silent, partial export the
// caller has no way to detect (see #480f59c2 §3.5, the requirement this type
// exists to satisfy).
type ExportTreeTooLargeError struct {
	// Kind is which ceiling was hit: "documents" or "bytes".
	Kind string
	// Actual is the measurement that triggered the refusal.
	//
	// For Kind "documents" this is the exact live document count in the
	// subtree, known before any body is downloaded.
	//
	// For Kind "bytes" this is the total accumulated up to the point the
	// ceiling was crossed, not necessarily the full subtree's total: once the
	// export is going to be refused anyway, downloading the remainder just to
	// report an exact number would spend precisely the resource this limit
	// exists to bound.
	Actual int64
	Limit  int64
}

func (e *ExportTreeTooLargeError) Error() string {
	return fmt.Sprintf("export tree exceeds the %s limit: %d > %d", e.Kind, e.Actual, e.Limit)
}

// WalkExportTree returns rootID and its live descendants, authorized and
// ordered for export.
//
// # Authorization
//
// rootID is fetched through GetByIDInWorkspace first — the same path an
// ordinary single-document read uses — so a caller with no access to the root
// gets exactly the 404 a direct read would give, and the walk never starts.
//
// Every candidate descendant is then required to share the root's project_id,
// the same tenancy boundary requireParentInProject enforces on write. That
// filter runs in SQL, on every row DocumentRepository.SubtreeInProject's
// recursion considers — not as a check applied after the fact — so a document
// whose parent_id merely points into this tree, but whose own project_id does
// not match, cannot enter the export by riding along on that pointer. A
// project belongs to exactly one workspace (GetByIDInWorkspace's own join
// proves that for the root's project), so filtering by project_id is also
// filtering by workspace; there is no second, weaker check duplicating what
// the route's wsAccess middleware already decided.
//
// # Order
//
// Depth-first, siblings ordered by Position with the same (position,
// created_at, id) tiebreak ListByProject uses. Two exports of an unchanged
// tree return the same order every time — see SubtreeInProject for where that
// guarantee actually comes from.
//
// # Soft deletes
//
// Never appear, at any depth: both the root fetch and the subtree query
// filter deleted_at IS NULL.
//
// # Size ceiling
//
// Returns *ExportTreeTooLargeError (retrievable via errors.As) when the
// subtree's document count or combined body size exceeds the limit, instead
// of silently truncating. The count is checked first, cheaply, right after
// the subtree's shape is known — before any body OTHER THAN the root's is
// downloaded (the root's body is already in hand at this point, as a side
// effect of authorizing it through GetByIDInWorkspace above); the byte total
// is then checked incrementally as the remaining bodies are downloaded, so a
// tree that blows the byte ceiling stops downloading as soon as it does
// rather than paying for the rest of a rejected export.
func (s *documentService) WalkExportTree(ctx context.Context, rootID, workspaceID uuid.UUID) ([]domain.Document, error) {
	root, err := s.GetByIDInWorkspace(ctx, rootID, workspaceID)
	if err != nil {
		return nil, err
	}
	// GetByIDInWorkspace never returns (nil, nil) — a miss comes back as
	// apierror.NotFound — so root is non-nil whenever err is nil.

	docs, err := s.documentRepo.SubtreeInProject(ctx, root.ID, root.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		// root was live and in-project a moment ago — the GetByIDInWorkspace
		// call above just proved it — but a concurrent delete between the two
		// calls is possible. A caller that lost that race sees the same 404 a
		// re-read would give, not an empty export.
		return nil, apierror.NotFound("Document")
	}
	if len(docs) > maxExportDocuments {
		return nil, &ExportTreeTooLargeError{
			Kind:   "documents",
			Actual: int64(len(docs)),
			Limit:  maxExportDocuments,
		}
	}

	var totalBytes int64
	for i := range docs {
		// root's body is already in hand — GetByIDInWorkspace fetched it to
		// authorize the walk — so it is reused here rather than downloaded a
		// second time. Compared by ID, not position: SubtreeInProject's own
		// ordering guarantee is about export order, not an API contract this
		// function should lean on for which row happens to be root.
		var body string
		if docs[i].ID == root.ID {
			body = root.Body
		} else {
			body, err = s.downloadBody(ctx, docs[i].StorageKey)
			if err != nil {
				return nil, err
			}
		}
		docs[i].Body = body
		totalBytes += int64(len(body))
		if totalBytes > maxExportTotalBytes {
			return nil, &ExportTreeTooLargeError{
				Kind:   "bytes",
				Actual: totalBytes,
				Limit:  maxExportTotalBytes,
			}
		}
	}

	return docs, nil
}
