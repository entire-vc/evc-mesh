import {
  type CSSProperties,
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useLocation, useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { FilePlus2, FileText, NotebookText, Plus, Search, X } from "lucide-react";
import { useAuthStore } from "@/stores/auth";
import { DocTree, buildDocPath, moveTargets } from "@/components/doc-tree";
import { DOC_VIEW_BLOCKS, DocPageProvider, type DocPageState } from "@/core/slots/doc-blocks";
import { resolveSlotView, useSlotRegistryVersion, useThemeSettings, withSlotFallback } from "@/core/slots/registry";
import { useBuildSlotContext } from "@/core/slots/use-slot-context";
import type {
  Async,
  DocViewActions,
  DocViewData,
  DocViewDoc,
  DocViewSettings,
  DocViewView,
} from "@/core/slots/types";
import { useWorkspaceStore } from "@/stores/workspace";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ResizableDivider } from "@/components/resizable-divider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { copyText } from "@/lib/clipboard";
import { useIsMobile } from "@/lib/use-is-mobile";
import {
  listDocumentAttachments,
  type DocumentAttachment,
} from "@/lib/document-attachments";
import { applyTrEmbedAttachments } from "@/lib/tr-embeds";
// Two unrelated anchor systems live on this page. `@/lib/docs/anchor` (D6) is
// the fragment link that points at a paragraph; `@/lib/doc-comments` (D7) is
// the range a comment is attached to. Same word, different features — keep the
// import paths distinct and do not fold one into the other.
import {
  type AnchorMatch,
  type DocAnchor,
  anchorFromHash,
  anchorToHash,
} from "@/lib/docs/anchor";
import { useDocComments } from "@/lib/doc-comments/use-doc-comments";
import { ApiRequestError, apiBlob } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import {
  type DocumentSearchHit,
  searchDocuments,
  splitSnippet,
} from "@/lib/docs/document-search";
import {
  DOC_TREE_DEFAULT_WIDTH,
  DOC_TREE_MAX_WIDTH,
  DOC_TREE_MIN_WIDTH,
  loadDocCommentsRailOpen,
  loadDocTreeWidth,
  saveDocCommentsRailOpen,
  saveDocTreeWidth,
} from "@/lib/docs-layout-storage";
import { useDocumentStore } from "@/stores/document";
import { useProjectStore } from "@/stores/project";
import type { ProjectDocument } from "@/types";

// The body is autosaved this long after the last keystroke — same figure as the
// task description editor, so the two do not feel like different applications.
const AUTOSAVE_DEBOUNCE_MS = 2000;

// Same figure as the `[[` link menu's search debounce (use-doc-link-picker.ts) —
// this is the same server call, wired to a second surface.
const DOC_SEARCH_DEBOUNCE_MS = 200;

type ExportFormat = "pdf" | "md" | "docx";

type SaveState =
  | { status: "idle" }
  | { status: "saving" }
  | { status: "saved" }
  | { status: "error"; message: string }
  // The write was refused because base_version no longer matched — nothing was
  // written, not the row and not the draft still sitting in the editor.
  | { status: "conflict" };

function errorMessage(err: unknown, fallback: string): string {
  return apiErrorMessage(err, fallback);
}

// The one 409 a document PATCH can return that means "refused, not failed":
// the row exists, the request was well-formed, and the caller's base_version
// is simply stale. Every other error — network, 500, validation — stays a
// plain "error" so it keeps the Retry affordance that a conflict does not get.
function isVersionConflict(err: unknown): boolean {
  return err instanceof ApiRequestError && err.code === "document_version_conflict";
}

// ---------------------------------------------------------------------------
// Create / rename dialogs
// ---------------------------------------------------------------------------

function TitleDialog({
  open,
  title,
  label,
  initialValue,
  confirmText,
  isLoading,
  onClose,
  onSubmit,
}: {
  open: boolean;
  title: string;
  label: string;
  initialValue: string;
  confirmText: string;
  isLoading: boolean;
  onClose: () => void;
  onSubmit: (value: string) => void;
}) {
  const { t } = useTranslation("docs");
  const [value, setValue] = useState(initialValue);

  // The dialog is mounted once per opening (see the `open &&` at the call site),
  // so this only has to seed the field when the caller changes what it is
  // editing without closing in between.
  useEffect(() => {
    setValue(initialValue);
  }, [initialValue]);

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) return;
    onSubmit(trimmed);
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent onClose={onClose}>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <div className="mt-4 space-y-2">
            <label className="text-sm font-medium" htmlFor="doc-title">
              {label}
            </label>
            <Input
              id="doc-title"
              autoFocus
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder={t("dialog.untitled")}
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={isLoading}
            >
              {t("actions.cancel")}
            </Button>
            <Button type="submit" disabled={!value.trim() || isLoading}>
              {isLoading ? t("dialog.working") : confirmText}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Move dialog
// ---------------------------------------------------------------------------

/**
 * Picks the document's new parent.
 *
 * A list rather than drag-and-drop: the tree is a scrolling 256px column, and
 * dragging a node onto a target that may be collapsed, off-screen or its own
 * descendant is the hard version of a problem that a list does not have. The
 * list also states the illegal moves by omission — the document's own subtree is
 * simply not in it.
 */
/**
 * A destination row, and the row that is currently picked.
 *
 * Same pairing the tree itself uses (`doc-tree.tsx`), and for the same reason:
 * `--accent` is a saturated teal, not a tint, so filling a row with it and
 * leaving the inherited body foreground on top measures 2.82:1 in light and
 * 1.73:1 in dark — under the 4.5:1 AA floor. The dialog shipped with exactly
 * that pairing on both the pick and the hover, which is what "the highlight is
 * dark, it was supposed to be the light green one" is reporting. `--secondary`
 * is the light teal-green of the palette and carries its own foreground.
 *
 * `--secondary` and `--muted` (the hover tint) sit close in lightness on
 * purpose, so the pick is not left to the fill alone: it also gets the brand
 * bar at the row edge and heavier text — again as in the tree, so a picked row
 * looks the same in both places.
 */
const MOVE_OPTION =
  "group relative flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-sm hover:bg-muted";
const MOVE_OPTION_SELECTED = "bg-secondary font-medium text-secondary-foreground";

/**
 * The second cue on a picked row. A real element rather than a border: the row
 * encodes tree depth in its left padding, and a border would shift every nested
 * title by 3px.
 */
function SelectedBar() {
  return (
    <span
      aria-hidden="true"
      data-selected-bar=""
      className="absolute inset-y-1 left-0 w-[3px] rounded-full bg-primary"
    />
  );
}

function MoveDialog({
  open,
  doc,
  targets,
  isLoading,
  onClose,
  onSubmit,
}: {
  open: boolean;
  doc: ProjectDocument;
  targets: { doc: ProjectDocument; depth: number }[];
  isLoading: boolean;
  onClose: () => void;
  onSubmit: (parentId: string | null) => void;
}) {
  // null is "top level", which is a real destination and not "nothing picked" —
  // hence a separate `touched` flag rather than treating null as unset.
  const { t } = useTranslation("docs");
  const [selected, setSelected] = useState<string | null>(doc.parent_id);

  const currentParent = doc.parent_id;

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>{t("move.title", { title: doc.title })}</DialogTitle>
        </DialogHeader>

        <div className="mt-4 max-h-72 overflow-y-auto rounded-md border border-border p-1">
          <ul role="listbox" aria-label={t("move.newParent")} className="space-y-0.5">
            <li>
              <button
                type="button"
                role="option"
                aria-selected={selected === null}
                onClick={() => setSelected(null)}
                className={cn(
                  MOVE_OPTION,
                  selected === null && MOVE_OPTION_SELECTED,
                )}
              >
                {selected === null && <SelectedBar />}
                <NotebookText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <span className="truncate">{t("move.topLevel")}</span>
                {currentParent === null && (
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                    {t("move.current")}
                  </span>
                )}
              </button>
            </li>
            {targets.map(({ doc: target, depth }) => (
              <li key={target.id}>
                <button
                  type="button"
                  role="option"
                  aria-selected={selected === target.id}
                  onClick={() => setSelected(target.id)}
                  className={cn(
                    MOVE_OPTION,
                    selected === target.id && MOVE_OPTION_SELECTED,
                  )}
                  style={{ paddingLeft: `${8 + depth * 12}px` }}
                >
                  {selected === target.id && <SelectedBar />}
                  <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{target.title}</span>
                  {currentParent === target.id && (
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      {t("move.current")}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={onClose}
            disabled={isLoading}
          >
            {t("actions.cancel")}
          </Button>
          <Button
            type="button"
            onClick={() => onSubmit(selected)}
            disabled={isLoading || selected === currentParent}
          >
            {isLoading ? t("move.moving") : t("move.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Export dialog — only shown when the document has children (see
// handleExportClick). The choice text below is DRAFT copy pending Alex's
// approval on an internal ticket (§1r.A copy gate) — the epic that
// decomposed this feature explicitly authorizes shipping with draft strings
// rather than blocking the feature on that answer (an internal ticket, Gary,
// 2026-09-02): "если ответа не будет — катим с черновыми строками".
// ---------------------------------------------------------------------------

function ExportDialog({
  doc,
  childCount,
  isLoading,
  onClose,
  onSubmit,
}: {
  doc: ProjectDocument;
  childCount: number;
  isLoading: boolean;
  onClose: () => void;
  onSubmit: (scope: "self" | "tree") => void;
}) {
  const { t } = useTranslation("docs");
  const [scope, setScope] = useState<"self" | "tree">("self");

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent onClose={onClose} data-testid="export-dialog">
        <DialogHeader>
          <DialogTitle>{t("export.title", { title: doc.title })}</DialogTitle>
        </DialogHeader>

        <fieldset className="mt-2 space-y-2">
          <legend className="sr-only">{t("export.whatToInclude")}</legend>
          <label className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-muted">
            <input
              type="radio"
              name="export-scope"
              value="self"
              checked={scope === "self"}
              onChange={() => setScope("self")}
              className="h-4 w-4"
            />
            {t("export.justThis")}
          </label>
          <label className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-muted">
            <input
              type="radio"
              name="export-scope"
              value="tree"
              checked={scope === "tree"}
              onChange={() => setScope("tree")}
              className="h-4 w-4"
            />
            {t("export.includeChildren", { count: childCount })}
          </label>
        </fieldset>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose} disabled={isLoading}>
            {t("actions.cancel")}
          </Button>
          <Button type="button" onClick={() => onSubmit(scope)} disabled={isLoading}>
            {isLoading ? t("export.exporting") : t("export.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function DocsPage() {
  const { wsSlug, projectSlug, docId } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const { t } = useTranslation("docs");
  const { currentProject } = useProjectStore();
  const {
    documents,
    isLoading,
    error,
    fetchDocuments,
    getDocument,
    createDocument,
    updateDocument,
    deleteDocument,
  } = useDocumentStore();

  const projectId = currentProject?.id;
  const authUserId = useAuthStore((s) => s.user?.id);
  const docTreeStorageKey =
    authUserId && projectId ? `mesh.docs.tree.expanded.${authUserId}.${projectId}` : null;
  const docsPath = `/w/${wsSlug}/p/${projectSlug}/docs`;

  const [openDoc, setOpenDoc] = useState<ProjectDocument | null>(null);
  const [docLoading, setDocLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reloadTick, setReloadTick] = useState(0);
  const [draft, setDraft] = useState("");
  // Attachments of the open Team Relay document, for the serve-time embed
  // rewrite below. Own documents never fetch these.
  const [trAttachments, setTrAttachments] = useState<DocumentAttachment[]>([]);
  const [editing, setEditing] = useState(false);
  const [saveState, setSaveState] = useState<SaveState>({ status: "idle" });
  const [anchorMatch, setAnchorMatch] = useState<AnchorMatch | null>(null);

  const [createUnder, setCreateUnder] = useState<ProjectDocument | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ProjectDocument | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [moveTarget, setMoveTarget] = useState<ProjectDocument | null>(null);
  const [moving, setMoving] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ProjectDocument | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Set only when the target has children — a document with none has no
  // choice to make, so the dialog never appears for it (see handleExportClick).
  const [exportPrompt, setExportPrompt] = useState<{
    doc: ProjectDocument;
    format: ExportFormat;
    childCount: number;
  } | null>(null);
  const [exporting, setExporting] = useState(false);

  // Read once, at mount: the stored width is the starting point, not a live
  // source, or a second tab would yank the column mid-read.
  const [treeWidth, setTreeWidth] = useState(
    () => loadDocTreeWidth() ?? DOC_TREE_DEFAULT_WIDTH,
  );

  const [railOpen, setRailOpen] = useState(loadDocCommentsRailOpen);
  const [showResolved, setShowResolved] = useState(false);

  // Below `lg` a 320px rail and a readable column do not both fit (see the
  // layout comment further down), so the rail is dropped entirely on this
  // width rather than stacking under `DocCommentTree` — that stack is what
  // produced two "COMMENTS" headers in a row on 393px (an internal ticket; Alex's
  // choice among three options, canonical-decision-2026-08-23-docs-ui-393-lg).
  // `railOpen` itself is left untouched here: it is what the toggle persists
  // to localStorage, and it must still read back correctly once the viewport
  // widens past `lg` again.
  const isMobile = useIsMobile();

  // ---- Search (D-search-surface) --------------------------------------------
  // Reuses the `documents/search` endpoint the `[[` link menu already calls
  // (document-search.ts) — the server-side search and its content indexing were
  // built for D9, only never given a place to live outside that menu.
  const [docSearchQuery, setDocSearchQuery] = useState("");
  const [docSearchResults, setDocSearchResults] = useState<DocumentSearchHit[] | null>(
    null,
  );
  const [docSearchLoading, setDocSearchLoading] = useState(false);
  const searching = docSearchQuery.trim() !== "";

  useEffect(() => {
    const query = docSearchQuery.trim();
    if (!projectId || query === "") {
      setDocSearchResults(null);
      setDocSearchLoading(false);
      return;
    }
    let cancelled = false;
    setDocSearchLoading(true);
    const timer = setTimeout(() => {
      searchDocuments(projectId, query)
        .then((hits) => {
          if (!cancelled) setDocSearchResults(hits);
        })
        // A failed search reads as "nothing found" rather than silently keeping
        // stale results on screen from a query the user has since changed.
        .catch(() => {
          if (!cancelled) setDocSearchResults([]);
        })
        .finally(() => {
          if (!cancelled) setDocSearchLoading(false);
        });
    }, DOC_SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [docSearchQuery, projectId]);

  const clearDocSearch = useCallback(() => setDocSearchQuery(""), []);

  // The body last known to be on the server. The autosave compares against this
  // rather than against openDoc.body so that a failed save leaves the editor
  // dirty and keeps retrying, instead of quietly deciding it is up to date.
  const savedBodyRef = useRef("");
  const saveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // The version last known to be on the server — what the next PATCH sends as
  // base_version. A ref, not state: bumping it must not itself recreate
  // flushBody, or a conflict's re-read would retrigger the debounce below and
  // turn "show one message" into a silent auto-retry over whatever the other
  // tab just saved.
  const docVersionRef = useRef<number | null>(null);

  // Read by the "leaving the document" cleanup below, which runs on a render
  // where docId is already the next document but these still hold the one being
  // left behind.
  const draftRef = useRef("");
  draftRef.current = draft;
  const openDocRef = useRef<ProjectDocument | null>(null);
  openDocRef.current = openDoc;

  useEffect(() => {
    if (projectId) void fetchDocuments(projectId);
  }, [projectId, fetchDocuments]);

  // ---- Load the selected document ------------------------------------------
  useEffect(() => {
    if (!docId) {
      setOpenDoc(null);
      setDraft("");
      setTrAttachments([]);
      savedBodyRef.current = "";
      docVersionRef.current = null;
      setLoadError(null);
      setEditing(false);
      return;
    }

    let cancelled = false;
    setDocLoading(true);
    setLoadError(null);
    getDocument(docId)
      .then((doc) => {
        if (cancelled) return;
        setOpenDoc(doc);
        const body = doc.body ?? "";
        setDraft(body);
        savedBodyRef.current = body;
        docVersionRef.current = doc.version;
        // View mode on open, every time — switching document must not leave the
        // next one sitting in an editor the user did not ask for.
        setEditing(false);
        setSaveState({ status: "idle" });
        // A Team Relay copy keeps share-relative image references in its body;
        // the imported attachments are what the reader rewrites them against.
        // Best-effort: a failed listing renders the body unrewritten — exactly
        // what every copy looked like before the embed importer existed.
        setTrAttachments([]);
        if (doc.source_kind === "team_relay") {
          listDocumentAttachments(doc.id)
            .then((atts) => {
              if (!cancelled) setTrAttachments(atts);
            })
            .catch(() => {
              /* render unrewritten; see above */
            });
        }
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setOpenDoc(null);
        setLoadError(errorMessage(err, t("errors.openFailed")));
      })
      .finally(() => {
        if (!cancelled) setDocLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [docId, getDocument, reloadTick]);

  // The value the READ-ONLY renderer sees: a Team Relay copy's body with its
  // share-relative embeds rewritten to the imported attachments' download
  // paths. Everything writable — `draft`, the editing editor, every save —
  // keeps the raw body, because WriteBack pushes it to the source share and
  // a baked-in Mesh path would corrupt it there.
  const renderedDraft = useMemo(() => {
    if (openDoc?.source_kind !== "team_relay" || trAttachments.length === 0) {
      return draft;
    }
    return applyTrEmbedAttachments(draft, openDoc.source_path ?? null, trAttachments);
  }, [draft, openDoc, trAttachments]);

  // ---- Save ----------------------------------------------------------------
  const flushBody = useCallback(async () => {
    const doc = openDoc;
    if (!doc) return;
    const body = draft;
    if (body === savedBodyRef.current) return;
    setSaveState({ status: "saving" });
    try {
      const updated = await updateDocument(doc.id, {
        body,
        base_version: docVersionRef.current ?? undefined,
      });
      savedBodyRef.current = body;
      docVersionRef.current = updated.version;
      setSaveState({ status: "saved" });
    } catch (err) {
      if (isVersionConflict(err)) {
        // Refused, not failed: neither the row nor `draft` was touched. Re-read
        // so the ref holds the real current version — otherwise every future
        // save would keep 409ing on the same stale number. `draft` is left
        // alone on purpose: overwriting it with the server's text would be the
        // same silent loss this exists to prevent, just in the other direction.
        try {
          const fresh = await getDocument(doc.id);
          docVersionRef.current = fresh.version;
        } catch {
          // Best-effort re-read; the conflict notice below stands regardless.
        }
        setSaveState({ status: "conflict" });
        return;
      }
      setSaveState({
        status: "error",
        message: errorMessage(err, t("errors.saveFailed")),
      });
    }
  }, [openDoc, draft, updateDocument, getDocument, t]);

  // Debounced autosave, mirroring the task description editor.
  useEffect(() => {
    if (!editing) return;
    if (draft === savedBodyRef.current) return;
    if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
    saveTimerRef.current = setTimeout(() => {
      void flushBody();
    }, AUTOSAVE_DEBOUNCE_MS);
    return () => {
      if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
    };
  }, [draft, editing, flushBody]);

  // Picking another page in the tree, or leaving Docs entirely, happens well
  // inside the two-second debounce. Without this the pending keystrokes are
  // simply dropped, which is the one thing an autosaving editor must never do.
  useEffect(() => {
    return () => {
      const doc = openDocRef.current;
      if (!doc) return;
      const body = draftRef.current;
      if (body === savedBodyRef.current) return;
      savedBodyRef.current = body;
      void updateDocument(doc.id, {
        body,
        base_version: docVersionRef.current ?? undefined,
      }).catch((err: unknown) => {
        const message = isVersionConflict(err)
          ? t("save.conflict")
          : errorMessage(err, t("errors.saveFailed"));
        toast.error(t("errors.notSavedToast", { title: doc.title, message }));
      });
    };
  }, [docId, updateDocument, t]);

  // ---- Paragraph links (D6) ------------------------------------------------
  // The anchor rides in the hash: it names a fragment of the document, no
  // server ever needs to see it, and it survives being pasted into a chat.
  const anchor = useMemo<DocAnchor | null>(
    () => anchorFromHash(location.hash),
    [location.hash],
  );

  // A verdict belongs to one arrival. Clear it when the link or the document
  // changes, so a stale "this paragraph has changed" cannot outlive its cause.
  useEffect(() => {
    setAnchorMatch(null);
  }, [location.hash, docId]);

  const handleCopyAnchor = useCallback(
    (built: DocAnchor) => {
      if (!docId) return;
      const url = `${window.location.origin}${docsPath}/${docId}${anchorToHash(built)}`;
      void copyText(url).then(
        () => toast.success(t("anchor.copied")),
        () => toast.error(t("anchor.copyFailed")),
      );
    },
    [docId, docsPath, t],
  );

  // Canonical address of the open document, with no anchor: the link a person
  // pastes elsewhere, not a pointer into a paragraph.
  const docUrl = docId ? `${window.location.origin}${docsPath}/${docId}` : "";

  const handleCopyDocUrl = useCallback(() => {
    if (!docUrl) return;
    void copyText(docUrl).then(
      () => toast.success(t("share.copied")),
      () => toast.error(t("share.copyFailed")),
    );
  }, [docUrl, t]);

  // The system share sheet exists on phones and some desktops; where it does
  // not, the item is hidden and Copy URL covers the case.
  const canShare = typeof navigator !== "undefined" && typeof navigator.share === "function";

  const handleShareDocUrl = useCallback(
    (title: string) => {
      if (!docUrl) return;
      navigator.share({ title, url: docUrl }).catch((err: unknown) => {
        // Dismissing the sheet rejects with AbortError — not a failure.
        if (err instanceof DOMException && err.name === "AbortError") return;
        toast.error(t("share.failed"));
      });
    },
    [docUrl, t],
  );

  // ---- Comments (D7) -------------------------------------------------------
  // Unrelated to the paragraph links above: those address a fragment for a
  // shareable URL, these address a text range a thread hangs off.
  // `draft` rather than openDoc.body is the markdown the anchors are measured
  // against: openDoc is loaded once and never re-read after an autosave, so its
  // body goes stale the moment anybody types. In view mode — the only mode where
  // anchoring runs — draft is what was last written to the server.
  const comments = useDocComments({
    documentId: docId,
    source: draft,
    enabled: !editing && !!openDoc,
    onCommentCreated: () => setRailOpen(true),
  });

  // Starting a comment must not put the composer somewhere the reader cannot see.
  useEffect(() => {
    if (comments.draft) setRailOpen(true);
  }, [comments.draft]);

  // Arriving from a mention: `?comment=<id>` names the comment somebody was
  // tagged in, so open the rail and focus its thread. The id may be a reply, so
  // it is matched against roots and replies both — focusing wants the root.
  //
  // A query parameter rather than the hash, which paragraph anchors (D6) own:
  // the two would otherwise overwrite each other when a mention lands on a
  // paragraph that is also linked.
  //
  // Runs once the threads are loaded and only while the id is unchanged, so
  // clicking a different thread afterwards is not undone on the next render.
  const focusedCommentId = useMemo(
    () => new URLSearchParams(location.search).get("comment"),
    [location.search],
  );
  const focusThread = comments.focusThread;
  useEffect(() => {
    if (!focusedCommentId || comments.isLoading) return;
    const thread = comments.threads.find(
      (t) =>
        t.root.id === focusedCommentId ||
        t.replies.some((r) => r.id === focusedCommentId),
    );
    if (!thread) return;
    setRailOpen(true);
    focusThread(thread.root.id);
  }, [focusedCommentId, comments.threads, comments.isLoading, focusThread]);

  const toggleRail = useCallback(() => {
    setRailOpen((prev) => {
      saveDocCommentsRailOpen(!prev);
      return !prev;
    });
  }, []);

  const handleDone = useCallback(() => {
    if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
    setEditing(false);
    // Leaving the editor is not a save request the user should have to wait
    // for, but it must not be a way to lose the last two seconds of typing.
    void flushBody();
  }, [flushBody]);

  // ---- Tree actions --------------------------------------------------------
  const openCreate = (parent: ProjectDocument | null) => {
    setCreateUnder(parent);
    setCreateOpen(true);
  };

  const handleCreate = async (title: string) => {
    if (!projectId) return;
    setCreating(true);
    try {
      const siblings = documents.filter(
        (d) => (d.parent_id ?? null) === (createUnder?.id ?? null),
      );
      const position = siblings.reduce((max, d) => Math.max(max, d.position), -1) + 1;
      const doc = await createDocument(projectId, {
        title,
        parent_id: createUnder?.id ?? null,
        position,
        body: "",
      });
      setCreateOpen(false);
      setCreateUnder(null);
      navigate(`${docsPath}/${doc.id}`);
    } catch (err) {
      toast.error(errorMessage(err, t("errors.createFailed")));
    } finally {
      setCreating(false);
    }
  };

  const handleRename = async (title: string) => {
    if (!renameTarget) return;
    setRenaming(true);
    try {
      const updated = await updateDocument(renameTarget.id, { title });
      if (openDoc?.id === updated.id) setOpenDoc(updated);
      setRenameTarget(null);
    } catch (err) {
      toast.error(errorMessage(err, t("errors.renameFailed")));
    } finally {
      setRenaming(false);
    }
  };

  const handleMove = async (parentId: string | null) => {
    if (!moveTarget) return;
    setMoving(true);
    try {
      // clear_parent is the only spelling the API reads as "to the top level":
      // parent_id: null arrives at a *uuid.UUID indistinguishable from omitted.
      const updated = await updateDocument(
        moveTarget.id,
        parentId === null ? { clear_parent: true } : { parent_id: parentId },
      );
      if (openDoc?.id === updated.id) setOpenDoc(updated);
      setMoveTarget(null);
    } catch (err) {
      // The server refuses a move whose destination already holds a live child
      // with this slug (409). That is the one failure a user can act on, so it
      // is surfaced verbatim rather than folded into a generic message.
      toast.error(errorMessage(err, t("errors.moveFailed")));
    } finally {
      setMoving(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    const target = deleteTarget;
    try {
      await deleteDocument(target.id);
      setDeleteTarget(null);
      // The delete takes the descendants with it, so the open document is gone
      // too whenever it sat anywhere under the target.
      if (openDoc && isSelfOrDescendant(documents, openDoc.id, target.id)) {
        navigate(docsPath);
      }
    } catch (err) {
      toast.error(errorMessage(err, t("errors.deleteFailed")));
    } finally {
      setDeleting(false);
    }
  };

  // Fetches the file and forces a real browser download — same blob+anchor
  // mechanism artifact-list.tsx's handleDownload already uses for artifacts,
  // now needed here because this endpoint streams bytes directly from our own
  // backend rather than handing back a presigned URL (see apiBlob's own
  // comment for why api<T>() couldn't be reused as-is).
  const handleExport = async (doc: ProjectDocument, format: ExportFormat, scope: "self" | "tree") => {
    setExporting(true);
    try {
      const { blob, filename } = await apiBlob(`/api/v1/documents/${doc.id}/export`, {
        format,
        scope,
      });
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(objUrl);
      setExportPrompt(null);
    } catch (err) {
      toast.error(errorMessage(err, t("errors.exportFailed")));
    } finally {
      setExporting(false);
    }
  };

  // The dialog only earns its place when there is an actual choice to make —
  // a document with no children has nothing to include or exclude, so this
  // downloads immediately rather than making the reader click through a
  // dialog offering one option (per an internal ticket §2: "лишний шаг там, где выбора
  // не существует, — раздражение без пользы").
  const handleExportClick = (format: ExportFormat) => {
    if (!openDoc) return;
    const childCount = documents.filter(
      (d) => d.id !== openDoc.id && isSelfOrDescendant(documents, d.id, openDoc.id),
    ).length;
    if (childCount > 0) {
      setExportPrompt({ doc: openDoc, format, childCount });
    } else {
      void handleExport(openDoc, format, "self");
    }
  };

  const childCount = deleteTarget
    ? documents.filter(
        (d) => d.id !== deleteTarget.id && isSelfOrDescendant(documents, d.id, deleteTarget.id),
      ).length
    : 0;

  // ---- doc.view ------------------------------------------------------------
  const workspace = useWorkspaceStore((s) => s.currentWorkspace);
  const themeSettings = useThemeSettings();
  const registryVersion = useSlotRegistryVersion();
  const DocView = useMemo(
    () => withSlotFallback("doc.view", resolveSlotView("doc.view")) as DocViewView,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [registryVersion],
  );
  const viewCtx = useBuildSlotContext(
    "doc.view",
    workspace ? { id: workspace.id, slug: workspace.slug, name: workspace.name } : null,
  );

  const pageState: DocPageState = {
    docId: docId ?? "",
    docsPath,
    openDoc: openDoc as ProjectDocument,
    documents,
    comments,
    editing,
    draft,
    renderedDraft,
    setDraft,
    startEditing: () => setEditing(true),
    anchor,
    onAnchorResolved: setAnchorMatch,
    onCopyAnchor: handleCopyAnchor,
    railOpen,
    isMobile,
    showResolved,
    setShowResolved,
    openRail: () => setRailOpen(true),
    toggleRail,
    navigate,
  };

  const viewDoc: Async<DocViewDoc> = docLoading
    ? { state: "loading" }
    : loadError
      ? {
          state: "error",
          error: { code: "document_failed", message: loadError },
          retry: () => setReloadTick((n) => n + 1),
        }
      : {
          state: "ready",
          value: {
            id: openDoc?.id ?? "",
            title: openDoc?.title ?? "",
            slug: openDoc?.slug ?? "",
            path: openDoc
              ? buildDocPath(documents, openDoc.id).map((d) => ({ label: d.title, href: `${docsPath}/${d.id}` }))
              : [],
            updatedAt: openDoc?.updated_at ?? "",
            updatedBy: openDoc?.updated_by
              ? { id: openDoc.updated_by, type: "user", name: openDoc.updated_by_name ?? "" }
              : null,
            version: openDoc?.version ?? 0,
          },
        };

  const viewData: DocViewData = {
    doc: viewDoc,
    save: {
      state: saveState.status === "idle" ? "idle" : saveState.status,
      message: saveState.status === "error" ? saveState.message : undefined,
    },
    components: DOC_VIEW_BLOCKS,
    layout: { commentsOpen: railOpen && !isMobile },
    rootHref: docsPath,
    editing,
    anchorNotice:
      anchorMatch && (anchorMatch.status === "edited" || anchorMatch.status === "lost")
        ? anchorMatch.status
        : null,
  };

  const viewActions: DocViewActions = {
    toggleComments: toggleRail,
    retry: () => void flushBody(),
    exportDoc: (format) => handleExportClick(format ?? "pdf"),
    startEditing: () => setEditing(true),
    finishEditing: handleDone,
    rename: () => openDoc && setRenameTarget(openDoc),
    move: () => openDoc && setMoveTarget(openDoc),
    remove: () => openDoc && setDeleteTarget(openDoc),
    copyUrl: handleCopyDocUrl,
    share: canShare ? () => openDoc && handleShareDocUrl(openDoc.title) : undefined,
    navigate,
  };
  const viewSettings = themeSettings as DocViewSettings;

  // ---- Render --------------------------------------------------------------
  // One surface split by a thin line, not two floating cards: the tree, the
  // divider and the page are siblings on the page background, and the only
  // border between them is the divider itself — which is also the control that
  // decides how wide the tree is.
  //
  // `h-full` + `overflow-hidden` here rather than a change to <main>: the app
  // shell scrolls its own padding box for every other page, and Docs is the one
  // page that owns two independently scrolling columns.
  return (
    <div className="mesh-mobile-docs flex h-full min-h-0 overflow-hidden">
      {/* Document tree.

          Below `md` the two columns do not fit side by side, so the route
          decides which one you get: no `:docId` in the URL means the tree owns
          the screen, opening a page hands the screen to the page, and the
          "Documents" crumb walks back. Both states are real routes, so Back
          works and a deep link still lands on the page itself.

          The resizable width has to be a variable rather than an inline
          `width`: an inline style outranks every class, so it would keep the
          tree at its desktop width on a phone. `md:w-[…]` applies it only where
          there is a divider to drag. */}
      <aside
        className={cn(
          "min-h-0 w-full shrink-0 flex-col pr-3 md:flex md:w-[var(--doc-tree-width)]",
          docId ? "hidden" : "flex",
        )}
        style={{ "--doc-tree-width": `${treeWidth}px` } as CSSProperties}
      >
        <div className="flex items-center justify-between gap-2 pb-2">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {t("tree.heading")}
          </h2>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 gap-1 px-2 text-xs"
            onClick={() => openCreate(null)}
            disabled={!projectId}
          >
            <Plus className="h-3.5 w-3.5" />
            {t("tree.new")}
          </Button>
        </div>

        <div className="relative pb-2">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            value={docSearchQuery}
            onChange={(e) => setDocSearchQuery(e.target.value)}
            placeholder={t("search.placeholder")}
            aria-label={t("search.label")}
            disabled={!projectId}
            className="h-8 pl-8 pr-7 text-xs"
          />
          {searching && (
            <button
              type="button"
              onClick={clearDocSearch}
              aria-label={t("search.clear")}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto pb-4">
          {searching ? (
            docSearchLoading && docSearchResults === null ? (
              <div className="space-y-2 p-1">
                <Skeleton className="h-8 w-full" />
                <Skeleton className="h-8 w-4/5" />
              </div>
            ) : !docSearchResults || docSearchResults.length === 0 ? (
              <p className="p-2 text-xs text-muted-foreground">
                {docSearchLoading ? t("search.searching") : t("search.noMatch")}
              </p>
            ) : (
              <ul>
                {docSearchResults.map((hit) => (
                  <li key={hit.id}>
                    <button
                      type="button"
                      onClick={() => {
                        navigate(`${docsPath}/${hit.id}`);
                        clearDocSearch();
                      }}
                      className={cn(
                        "flex w-full items-start gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-accent",
                        hit.id === docId && "bg-accent",
                      )}
                    >
                      <FileText className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate">{hit.title}</span>
                        {hit.snippet && (
                          <span
                            data-snippet={hit.snippetIsMatch ? "match" : "preview"}
                            className={cn(
                              "block truncate text-[10px]",
                              hit.snippetIsMatch
                                ? "text-muted-foreground"
                                : "italic text-muted-foreground/70",
                            )}
                          >
                            {hit.snippetIsMatch
                              ? splitSnippet(hit.snippet).map((part, i) =>
                                  part.match ? (
                                    <mark key={i} className="bg-primary/25 text-foreground">
                                      {part.text}
                                    </mark>
                                  ) : (
                                    <span key={i}>{part.text}</span>
                                  ),
                                )
                              : hit.snippet}
                          </span>
                        )}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )
          ) : !isLoading && !error && documents.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 p-4 text-center">
              <NotebookText className="h-6 w-6 text-muted-foreground" />
              <p className="text-xs text-muted-foreground">{t("empty.noDocuments")}</p>
              <Button
                type="button"
                size="sm"
                className="h-7 gap-1 px-2 text-xs"
                onClick={() => openCreate(null)}
                disabled={!projectId}
              >
                <Plus className="h-3.5 w-3.5" />
                {t("tree.newPage")}
              </Button>
            </div>
          ) : (
            <DocTree
              key={docTreeStorageKey ?? "no-storage"}
              storageKey={docTreeStorageKey}
              projectId={projectId}
              documents={documents}
              selectedId={docId ?? null}
              onSelect={(doc) => navigate(`${docsPath}/${doc.id}`)}
              onCreateChild={(parent) => openCreate(parent)}
              onRename={(doc) => setRenameTarget(doc)}
              onMove={(doc) => setMoveTarget(doc)}
              onDelete={(doc) => setDeleteTarget(doc)}
              hrefFor={(doc) => `${docsPath}/${doc.id}`}
              loading={isLoading}
              error={error}
              onRetry={() => projectId && void fetchDocuments(projectId)}
            />
          )}
        </div>
      </aside>

      {/* The line between the two columns — and the handle that moves it.
          Hidden below md for the same reason the tree is: there is nothing on
          its left to resize. */}
      <ResizableDivider
        className="hidden md:block"
        label={t("tree.resize")}
        value={treeWidth}
        min={DOC_TREE_MIN_WIDTH}
        max={DOC_TREE_MAX_WIDTH}
        defaultValue={DOC_TREE_DEFAULT_WIDTH}
        onChange={setTreeWidth}
        onCommit={saveDocTreeWidth}
      />

      {/* Document area — the other half of the route-decides-the-column rule
          above: hidden below `md` until a page is actually open. */}
      <section
        className={cn(
          "min-w-0 flex-1 flex-col overflow-hidden md:flex md:pl-6",
          docId ? "flex" : "hidden",
        )}
      >
        {docId && (docLoading || loadError || openDoc) ? (
          <DocPageProvider value={pageState}>
            <DocView data={viewData} actions={viewActions} themeSettings={viewSettings} ctx={viewCtx} />
          </DocPageProvider>
        ) : (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 overflow-y-auto p-6 text-center">
            <FilePlus2 className="h-10 w-10 text-muted-foreground" />
            <h3 className="text-sm font-semibold">
              {documents.length === 0 ? t("empty.noDocuments") : t("empty.noSelection")}
            </h3>
            <p className="max-w-xs text-sm text-muted-foreground">
              {documents.length === 0
                ? t("empty.noDocumentsHint")
                : t("empty.noSelectionHint")}
            </p>
            <Button
              type="button"
              className="mt-1 gap-1"
              onClick={() => openCreate(null)}
              disabled={!projectId}
            >
              <Plus className="h-4 w-4" />
              {t("tree.newPage")}
            </Button>
          </div>
        )}
      </section>

      {createOpen && (
        <TitleDialog
          open
          title={createUnder ? t("dialog.newPageIn", { title: createUnder.title }) : t("tree.newPage")}
          label={t("dialog.titleLabel")}
          initialValue=""
          confirmText={t("dialog.create")}
          isLoading={creating}
          onClose={() => {
            setCreateOpen(false);
            setCreateUnder(null);
          }}
          onSubmit={(value) => void handleCreate(value)}
        />
      )}

      {renameTarget && (
        <TitleDialog
          open
          title={t("dialog.renameTitle")}
          label={t("dialog.titleLabel")}
          initialValue={renameTarget.title}
          confirmText={t("dialog.save")}
          isLoading={renaming}
          onClose={() => setRenameTarget(null)}
          onSubmit={(value) => void handleRename(value)}
        />
      )}

      {moveTarget && (
        <MoveDialog
          open
          doc={moveTarget}
          targets={moveTargets(documents, moveTarget.id)}
          isLoading={moving}
          onClose={() => setMoveTarget(null)}
          onSubmit={(parentId) => void handleMove(parentId)}
        />
      )}

      {exportPrompt && (
        <ExportDialog
          doc={exportPrompt.doc}
          childCount={exportPrompt.childCount}
          isLoading={exporting}
          onClose={() => setExportPrompt(null)}
          onSubmit={(scope) => void handleExport(exportPrompt.doc, exportPrompt.format, scope)}
        />
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => void handleDelete()}
        title={t("delete.title")}
        description={
          deleteTarget
            ? childCount > 0
              ? t("delete.confirmWithChildren", { title: deleteTarget.title, count: childCount })
              : t("delete.confirm", { title: deleteTarget.title })
            : ""
        }
        confirmText={t("actions.delete")}
        variant="destructive"
        isLoading={deleting}
      />
    </div>
  );
}

/** True when `id` is `ancestorId` or sits anywhere beneath it. */
function isSelfOrDescendant(
  documents: ProjectDocument[],
  id: string,
  ancestorId: string,
): boolean {
  if (id === ancestorId) return true;
  const byId = new Map(documents.map((d) => [d.id, d]));
  const seen = new Set<string>();
  let cursor = byId.get(id)?.parent_id ?? null;
  while (cursor && !seen.has(cursor)) {
    if (cursor === ancestorId) return true;
    seen.add(cursor);
    cursor = byId.get(cursor)?.parent_id ?? null;
  }
  return false;
}
