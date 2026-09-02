package service

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentAttachmentLinkRE matches the one shape a document-attachment link is
// ever written in: documentAttachmentDownloadPath (web/src/lib/artifact-links.ts)
// always emits exactly "/api/v1/document-attachments/<id>/download?disposition=inline",
// and it is the only writer of that pattern in the frontend — so a literal match
// is safe, not a heuristic.
var documentAttachmentLinkRE = regexp.MustCompile(
	`/api/v1/document-attachments/([0-9a-fA-F-]{36})/download(?:\?disposition=inline)?`,
)

type documentExportService struct {
	documents      DocumentService
	attachmentRepo repository.DocumentAttachmentRepository
	storage        DocumentStore
}

// NewDocumentExportService returns a DocumentExportService. documents is the
// already-constructed DocumentService (its WalkExportTree does the
// authorization and ordering this builds on); attachmentRepo and storage read
// and fetch attachments for the tree scope — both are the same instances the
// document/attachment services themselves use, so an export reads through the
// identical tenancy-scoped rows everything else does.
func NewDocumentExportService(documents DocumentService, attachmentRepo repository.DocumentAttachmentRepository, storage DocumentStore) DocumentExportService {
	return &documentExportService{documents: documents, attachmentRepo: attachmentRepo, storage: storage}
}

func (s *documentExportService) ExportMarkdown(ctx context.Context, rootID, workspaceID uuid.UUID, scope ExportScope) (data []byte, filename, contentType string, err error) {
	switch scope {
	case ExportScopeSelf:
		return s.exportSelf(ctx, rootID, workspaceID)
	case ExportScopeTree:
		return s.exportTree(ctx, rootID, workspaceID)
	default:
		return nil, "", "", apierror.ValidationError(map[string]string{
			"scope": `must be "self" or "tree"`,
		})
	}
}

func (s *documentExportService) exportSelf(ctx context.Context, rootID, workspaceID uuid.UUID) (data []byte, filename, contentType string, err error) {
	doc, err := s.documents.GetByIDInWorkspace(ctx, rootID, workspaceID)
	if err != nil {
		return nil, "", "", err
	}
	filename = fmt.Sprintf("%s-%s.md", doc.Slug, exportDateTag())
	return []byte(doc.Body), filename, "text/markdown", nil
}

func (s *documentExportService) exportTree(ctx context.Context, rootID, workspaceID uuid.UUID) (data []byte, filename, contentType string, err error) {
	docs, err := s.documents.WalkExportTree(ctx, rootID, workspaceID)
	if err != nil {
		return nil, "", "", err
	}

	byID := make(map[uuid.UUID]domain.Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, doc := range docs {
		dirChain := documentZipDirChain(byID, doc)
		docPath := strings.Join(append(append([]string{}, dirChain...), doc.Slug+".md"), "/")

		body, referenced, rewriteErr := s.rewriteAttachmentLinks(ctx, doc, dirChain)
		if rewriteErr != nil {
			_ = zw.Close()
			return nil, "", "", rewriteErr
		}
		if writeErr := writeZipEntry(zw, docPath, []byte(body)); writeErr != nil {
			_ = zw.Close()
			return nil, "", "", writeErr
		}

		attDirPrefix := append(append([]string{"_attachments"}, dirChain...), doc.Slug)
		for _, ra := range referenced {
			rc, dlErr := s.storage.Download(ctx, ra.att.StorageKey)
			if dlErr != nil {
				_ = zw.Close()
				return nil, "", "", apierror.InternalError("failed to read attachment from storage")
			}
			data, readErr := io.ReadAll(io.LimitReader(rc, maxAttachmentBytes))
			_ = rc.Close()
			if readErr != nil {
				_ = zw.Close()
				return nil, "", "", apierror.InternalError("failed to read attachment from storage")
			}
			attPath := strings.Join(append(append([]string{}, attDirPrefix...), ra.zipName), "/")
			if writeErr := writeZipEntry(zw, attPath, data); writeErr != nil {
				_ = zw.Close()
				return nil, "", "", writeErr
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", "", apierror.InternalError("failed to build export archive")
	}

	root := byID[rootID]
	filename = fmt.Sprintf("%s-%s.zip", root.Slug, exportDateTag())
	return buf.Bytes(), filename, "application/zip", nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return apierror.InternalError("failed to build export archive")
	}
	if _, err := w.Write(data); err != nil {
		return apierror.InternalError("failed to build export archive")
	}
	return nil
}

// documentZipDirChain returns the slug of every ancestor of doc, root first,
// NOT including doc's own slug — the directory segments doc's own <slug>.md
// lives under inside the archive. Nil for the root document itself, which
// sits at the archive's top level; a document with children gets an
// implicit same-named directory for them (its own file is "guide.md", its
// children live under "guide/") — the same folder-note shape Obsidian users
// already have muscle memory for.
func documentZipDirChain(byID map[uuid.UUID]domain.Document, doc domain.Document) []string {
	var chain []string
	cur := doc
	for cur.ParentID != nil {
		parent, ok := byID[*cur.ParentID]
		if !ok {
			break
		}
		chain = append([]string{parent.Slug}, chain...)
		cur = parent
	}
	return chain
}

// referencedAttachment is one attachment a document's body actually linked to
// — see rewriteAttachmentLinks — paired with the (possibly deduplicated) name
// it will be written into the archive under.
type referencedAttachment struct {
	att     domain.DocumentAttachment
	zipName string
}

// rewriteAttachmentLinks returns doc's body with every document-attachment
// link it contains rewritten from the app-only API path
// (/api/v1/document-attachments/<id>/download — a JSON-returning endpoint
// that needs an authenticated session and a second hop to a presigned URL,
// meaningless outside the Mesh web app) to a path relative to doc's own
// location in the archive, plus the set of attachments that need bundling to
// make those rewritten links resolve.
//
// Only attachments doc's body actually links to are returned — an uploaded-
// but-unlinked attachment isn't a broken link if left out, and bundling it
// would just be dead weight in the archive. A link whose attachment id does
// not belong to doc (or was deleted) is left untouched: rewriting it to a
// path with nothing behind it would trade one kind of non-functional link
// for another, and this document's own set of attachments is the only one
// this function has any authorization to read.
func (s *documentExportService) rewriteAttachmentLinks(ctx context.Context, doc domain.Document, dirChain []string) (string, []referencedAttachment, error) {
	if !documentAttachmentLinkRE.MatchString(doc.Body) {
		return doc.Body, nil, nil
	}

	atts, err := s.listAllAttachments(ctx, doc.ID)
	if err != nil {
		return "", nil, err
	}
	if len(atts) == 0 {
		return doc.Body, nil, nil
	}
	byAttID := make(map[uuid.UUID]domain.DocumentAttachment, len(atts))
	for _, a := range atts {
		byAttID[a.ID] = a
	}

	// Pass 1: decide the archive name for every attachment actually
	// referenced, once each — a name computed per-occurrence would let the
	// SAME attachment linked twice pick up two different deduplicated names.
	nameFor := make(map[uuid.UUID]string)
	usedNames := make(map[string]int)
	for _, m := range documentAttachmentLinkRE.FindAllStringSubmatch(doc.Body, -1) {
		attID, parseErr := uuid.Parse(m[1])
		if parseErr != nil {
			continue
		}
		att, ok := byAttID[attID]
		if !ok {
			continue
		}
		if _, done := nameFor[attID]; done {
			continue
		}
		nameFor[attID] = dedupeAttachmentName(usedNames, att.Name)
	}
	if len(nameFor) == 0 {
		return doc.Body, nil, nil
	}

	attDirPrefix := append(append([]string{"_attachments"}, dirChain...), doc.Slug)
	relPrefix := strings.Repeat("../", len(dirChain))

	// Pass 2: substitute using the names pass 1 already committed to.
	body := documentAttachmentLinkRE.ReplaceAllStringFunc(doc.Body, func(match string) string {
		sub := documentAttachmentLinkRE.FindStringSubmatch(match)
		attID, parseErr := uuid.Parse(sub[1])
		if parseErr != nil {
			return match
		}
		name, ok := nameFor[attID]
		if !ok {
			return match
		}
		return relPrefix + strings.Join(append(append([]string{}, attDirPrefix...), name), "/")
	})

	referenced := make([]referencedAttachment, 0, len(nameFor))
	for id, name := range nameFor {
		referenced = append(referenced, referencedAttachment{att: byAttID[id], zipName: name})
	}
	return body, referenced, nil
}

// dedupeAttachmentName returns name, suffixed before its extension with a
// counter if name was already claimed in this call's directory — two
// attachments on the same document can legitimately share an
// operating-system-level filename (two screenshots both literally named
// "image.png"), and the archive needs distinct paths for both.
func dedupeAttachmentName(used map[string]int, name string) string {
	n := used[name]
	used[name]++
	if n == 0 {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s-%d%s", base, n+1, ext)
}

// listAllAttachments returns every live attachment on documentID, looping
// pages rather than reading only the first: an export is exactly the place a
// silently-truncated attachment list shows up as an inexplicable missing
// image, and a document can in principle carry more than one page.
func (s *documentExportService) listAllAttachments(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentAttachment, error) {
	var all []domain.DocumentAttachment
	params := pagination.Params{Page: 1, PageSize: pagination.MaxPageSize}
	for {
		p, err := s.attachmentRepo.ListByDocument(ctx, documentID, params)
		if err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if !p.HasMore {
			break
		}
		params.Page++
	}
	return all, nil
}

func exportDateTag() string {
	return time.Now().UTC().Format("2006-01-02")
}
