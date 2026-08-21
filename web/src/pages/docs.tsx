import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useLocation, useNavigate, useParams } from "react-router";
import {
  AlertCircle,
  Check,
  FilePlus2,
  FileText,
  Loader2,
  MoreHorizontal,
  NotebookText,
  Pencil,
  Plus,
  Unlink,
} from "lucide-react";
import { DocBreadcrumbs } from "@/components/doc-breadcrumbs";
import {
  DocCommentAffordance,
  DocCommentRail,
  DocCommentToggle,
} from "@/components/doc-comment-rail";
import { DocEditor } from "@/components/doc-editor";
import { DocWatchToggle } from "@/components/doc-watch-toggle";
import { DocMeta } from "@/components/doc-meta";
import { DocCommentTree } from "@/components/doc-comment-tree";
import { DocTree, moveTargets } from "@/components/doc-tree";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ResizableDivider } from "@/components/resizable-divider";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
import { ApiRequestError } from "@/lib/api";
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

type SaveState =
  | { status: "idle" }
  | { status: "saving" }
  | { status: "saved" }
  | { status: "error"; message: string }
  // The write was refused because base_version no longer matched — nothing was
  // written, not the row and not the draft still sitting in the editor.
  | { status: "conflict" };

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
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
              placeholder="Untitled"
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={onClose}
              disabled={isLoading}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!value.trim() || isLoading}>
              {isLoading ? "Working..." : confirmText}
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
  const [selected, setSelected] = useState<string | null>(doc.parent_id);

  const currentParent = doc.parent_id;

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent onClose={onClose}>
        <DialogHeader>
          <DialogTitle>Move "{doc.title}"</DialogTitle>
        </DialogHeader>

        <div className="mt-4 max-h-72 overflow-y-auto rounded-md border border-border p-1">
          <ul role="listbox" aria-label="New parent" className="space-y-0.5">
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
                <span className="truncate">Top level</span>
                {currentParent === null && (
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                    current
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
                      current
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
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() => onSubmit(selected)}
            disabled={isLoading || selected === currentParent}
          >
            {isLoading ? "Moving..." : "Move"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Save indicator — says what actually happened, including when it failed
// ---------------------------------------------------------------------------

function SaveIndicator({
  state,
  onRetry,
}: {
  state: SaveState;
  onRetry: () => void;
}) {
  if (state.status === "saving") {
    return (
      <span className="flex items-center gap-1 text-xs text-muted-foreground">
        <Loader2 className="h-3 w-3 animate-spin" />
        Saving...
      </span>
    );
  }
  if (state.status === "saved") {
    return (
      <span className="flex items-center gap-1 text-xs text-muted-foreground">
        <Check className="h-3 w-3" />
        Saved
      </span>
    );
  }
  if (state.status === "error") {
    return (
      <span className="flex items-center gap-1 text-xs text-destructive">
        <AlertCircle className="h-3 w-3" />
        Not saved: {state.message}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-6 px-2 text-xs"
          onClick={onRetry}
        >
          Retry
        </Button>
      </span>
    );
  }
  if (state.status === "conflict") {
    // Approved copy (#c8a9bbce), verbatim — no Retry button and no added
    // hint about what to do next. Typing further re-arms the debounce on its
    // own; a button here would be a second, unapproved sentence.
    return (
      <span className="flex items-center gap-1 text-xs text-destructive">
        <AlertCircle className="h-3 w-3" />
        this page changed elsewhere
      </span>
    );
  }
  return null;
}

// ---------------------------------------------------------------------------
// What a paragraph link found when it arrived
// ---------------------------------------------------------------------------

/**
 * Said out loud only when the answer is not "here it is".
 *
 * A link to a paragraph has two ways of going wrong and they need different
 * sentences. `edited` means the place is known but the words are not the ones
 * the link was made from — the reader is looking at the right spot and must not
 * assume it is the quoted text. `lost` means nothing could be located at all,
 * and the page stays where it is rather than scrolling somewhere arbitrary. The
 * two statuses this does not render — `exact` and `moved` — are the cases where
 * the paragraph itself is on screen, and a banner would only be noise.
 */
function AnchorNotice({ match }: { match: AnchorMatch }) {
  if (match.status === "exact" || match.status === "moved") return null;

  const text =
    match.status === "edited"
      ? "This paragraph has been edited since the link was created. You are at its place in the document, but the text is not what was linked."
      : "The linked paragraph is no longer in this document. It was edited or removed after the link was created.";

  return (
    <div className="flex items-start gap-2 border-b border-border bg-muted/60 px-4 py-2 text-xs text-muted-foreground">
      <Unlink className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <p>{text}</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function DocsPage() {
  const { wsSlug, projectSlug, docId } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
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
  const docsPath = `/w/${wsSlug}/p/${projectSlug}/docs`;

  const [openDoc, setOpenDoc] = useState<ProjectDocument | null>(null);
  const [docLoading, setDocLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
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

  // Read once, at mount: the stored width is the starting point, not a live
  // source, or a second tab would yank the column mid-read.
  const [treeWidth, setTreeWidth] = useState(
    () => loadDocTreeWidth() ?? DOC_TREE_DEFAULT_WIDTH,
  );

  const [railOpen, setRailOpen] = useState(loadDocCommentsRailOpen);
  const [showResolved, setShowResolved] = useState(false);

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
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setOpenDoc(null);
        setLoadError(errorMessage(err, "Failed to open document"));
      })
      .finally(() => {
        if (!cancelled) setDocLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [docId, getDocument]);

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
        message: errorMessage(err, "save failed"),
      });
    }
  }, [openDoc, draft, updateDocument, getDocument]);

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
          ? "this page changed elsewhere"
          : errorMessage(err, "save failed");
        toast.error(`"${doc.title}" was not saved: ${message}`);
      });
    };
  }, [docId, updateDocument]);

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
        () => toast.success("Link to paragraph copied"),
        () => toast.error("Could not copy the link"),
      );
    },
    [docId, docsPath],
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
      toast.error(errorMessage(err, "Failed to create page"));
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
      toast.error(errorMessage(err, "Failed to rename page"));
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
      toast.error(errorMessage(err, "Failed to move page"));
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
      toast.error(errorMessage(err, "Failed to delete page"));
    } finally {
      setDeleting(false);
    }
  };

  const childCount = deleteTarget
    ? documents.filter(
        (d) => d.id !== deleteTarget.id && isSelfOrDescendant(documents, d.id, deleteTarget.id),
      ).length
    : 0;

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
    <div className="flex h-full min-h-0 overflow-hidden">
      {/* Document tree */}
      <aside
        className="hidden min-h-0 shrink-0 flex-col pr-3 md:flex"
        style={{ width: `${treeWidth}px` }}
      >
        <div className="flex items-center justify-between gap-2 pb-2">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Documents
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
            New
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto pb-4">
          {isLoading ? (
            <div className="space-y-2 p-1">
              <Skeleton className="h-5 w-full" />
              <Skeleton className="h-5 w-4/5" />
              <Skeleton className="h-5 w-3/5" />
            </div>
          ) : error ? (
            <p className="p-2 text-xs text-destructive">{error}</p>
          ) : documents.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 p-4 text-center">
              <NotebookText className="h-6 w-6 text-muted-foreground" />
              <p className="text-xs text-muted-foreground">No documents yet</p>
              <Button
                type="button"
                size="sm"
                className="h-7 gap-1 px-2 text-xs"
                onClick={() => openCreate(null)}
                disabled={!projectId}
              >
                <Plus className="h-3.5 w-3.5" />
                New page
              </Button>
            </div>
          ) : (
            <DocTree
              documents={documents}
              selectedId={docId ?? null}
              onSelect={(doc) => navigate(`${docsPath}/${doc.id}`)}
              onCreateChild={(parent) => openCreate(parent)}
              onRename={(doc) => setRenameTarget(doc)}
              onMove={(doc) => setMoveTarget(doc)}
              onDelete={(doc) => setDeleteTarget(doc)}
            />
          )}
        </div>
      </aside>

      {/* The line between the two columns — and the handle that moves it.
          Hidden below md for the same reason the tree is: there is nothing on
          its left to resize. */}
      <ResizableDivider
        className="hidden md:block"
        label="Resize document tree"
        value={treeWidth}
        min={DOC_TREE_MIN_WIDTH}
        max={DOC_TREE_MAX_WIDTH}
        defaultValue={DOC_TREE_DEFAULT_WIDTH}
        onChange={setTreeWidth}
        onCommit={saveDocTreeWidth}
      />

      {/* Document area */}
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden md:pl-6">
        {docLoading ? (
          <div className="space-y-3 pt-1">
            <Skeleton className="h-4 w-48" />
            <Skeleton className="h-9 w-80" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-5/6" />
          </div>
        ) : loadError ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
            <AlertCircle className="h-8 w-8 text-destructive" />
            <p className="text-sm text-destructive">{loadError}</p>
          </div>
        ) : openDoc ? (
          <>
            {/* Breadcrumbs, title and actions stay put while the body scrolls:
                Edit must not be something you have to scroll back up to find. */}
            <div className="shrink-0 pb-4">
              <DocBreadcrumbs
                documents={documents}
                current={openDoc}
                onNavigateRoot={() => navigate(docsPath)}
                onNavigate={(doc) => navigate(`${docsPath}/${doc.id}`)}
              />

              {/* Title and actions share one row. They used to be two: the
                  actions owned a full-width line of their own above the title,
                  which cost a row of vertical space and bought nothing. */}
              <div className="mt-2 flex flex-wrap items-center gap-y-1 gap-x-3">
                {/* `truncate` needs the `min-w-0`, or the flex item refuses to
                    shrink below its text and shoves the actions off the row.
                    The full title stays reachable on hover and to a screen
                    reader, so nothing is actually lost to the ellipsis. */}
                <h1
                  title={openDoc.title}
                  className="min-w-0 flex-1 truncate text-2xl font-semibold tracking-tight md:text-3xl"
                >
                  {openDoc.title}
                </h1>

                {/* The actions themselves wrap onto a second line once the
                    row can't fit them next to the title (narrow viewports,
                    or a long SaveIndicator message like the conflict
                    notice) — `min-w-0` lets this group shrink at all, and
                    `flex-wrap` is what turns "shrink" into "wrap" instead of
                    silently overflowing the row's `overflow-hidden`
                    ancestor. `shrink-0` here used to pin the actions at
                    their full unwrapped width regardless of available
                    space, which is exactly what pushed them off-screen. */}
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  {/* How many conversations are open on this page, and the
                      switch for the rail that holds them. */}
                  <DocCommentToggle
                    controller={comments}
                    open={railOpen}
                    onToggle={toggleRail}
                  />
                  <DocWatchToggle documentId={openDoc.id} />
                  <SaveIndicator state={saveState} onRetry={() => void flushBody()} />
                  {editing ? (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="h-7 px-2 text-xs"
                      onClick={handleDone}
                    >
                      Done
                    </Button>
                  ) : (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="h-7 gap-1 px-2 text-xs"
                      onClick={() => setEditing(true)}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                      Edit
                    </Button>
                  )}
                  {/* Rare and destructive live behind the overflow, so the one
                      thing you came to do stays the one obvious button. */}
                  <DropdownMenu>
                    <DropdownMenuTrigger
                      title="More actions"
                      aria-label="More actions"
                      className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
                    >
                      <MoreHorizontal className="h-4 w-4" />
                    </DropdownMenuTrigger>
                    <DropdownMenuContent className="min-w-[9rem]">
                      <DropdownMenuItem onClick={() => setRenameTarget(openDoc)}>
                        Rename
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setMoveTarget(openDoc)}>
                        Move
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onClick={() => setDeleteTarget(openDoc)}
                        className="text-destructive"
                      >
                        Delete
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </div>

              <DocMeta doc={openDoc} className="mt-1" />
            </div>
            {/* Outside the scroll container, as before: the notice explains why
                the page did not jump where the link promised, and scrolling it
                out of sight would take the explanation away with it. */}
            {!editing && anchorMatch && <AnchorNotice match={anchorMatch} />}

            {/* The one scroller for the document area. Everything below grows
                rather than scrolling on its own, so a long page moves this box
                and nothing else — no scrollbar inside a scrollbar. */}
            <div className="min-h-0 flex-1 overflow-y-auto">
              {/* Beside the article on a wide screen, under it on a narrow one:
                  a 320px rail and a readable column do not both fit below lg.
                  `min-h-full` is what lets the editor below reach the bottom of
                  the viewport on a short document: it gives the column a height
                  to fill, while leaving it free to grow past that on a long one. */}
              <div className="flex min-h-full flex-col gap-8 lg:flex-row">
                <article className="flex min-w-0 flex-1 flex-col pb-16 xl:max-w-4xl">
                  {/* `relative` so the selection affordance can be placed inside
                      it; the ref is what the comment layer measures and reads
                      selections from. Neither touches the DOM ProseMirror owns.
                      `flex-1` with the default `min-height: auto` fills the
                      space that is there and grows when the body needs more. */}
                  <div
                    ref={comments.containerRef}
                    data-testid="doc-measured-body"
                    className="relative flex flex-1 flex-col"
                  >
                    {editing ? (
                      <DocEditor
                        className="flex-1"
                        value={draft}
                        onChange={setDraft}
                        documentId={docId}
                      />
                    ) : draft.trim() ? (
                      <DocEditor
                        className="flex-1"
                        value={draft}
                        onChange={setDraft}
                        readOnly
                        anchor={anchor}
                        onAnchorResolved={setAnchorMatch}
                        onCopyAnchor={handleCopyAnchor}
                      />
                    ) : (
                      <div className="flex flex-col items-start gap-2">
                        <DocEditor value={draft} onChange={setDraft} readOnly />
                        <Button
                          type="button"
                          size="sm"
                          className="h-7 gap-1 px-2 text-xs"
                          onClick={() => setEditing(true)}
                        >
                          <Pencil className="h-3.5 w-3.5" />
                          Start writing
                        </Button>
                      </div>
                    )}
                    <DocCommentAffordance controller={comments} />
                  </div>
                  {/* Outside `comments.containerRef` on purpose: that element is
                      flattened into "the text of this document" to locate each
                      quote, so a discussion rendered inside it would become part
                      of the haystack and anchors would start matching comment
                      bodies. See doc-comment-tree.tsx. */}
                  <DocCommentTree
                    controller={comments}
                    showResolved={showResolved}
                    onShowResolvedChange={setShowResolved}
                  />
                </article>

                {railOpen && (
                  <DocCommentRail
                    controller={comments}
                    showResolved={showResolved}
                    onShowResolvedChange={setShowResolved}
                  />
                )}
              </div>
            </div>
          </>
        ) : (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 overflow-y-auto p-6 text-center">
            <FilePlus2 className="h-10 w-10 text-muted-foreground" />
            <h3 className="text-sm font-semibold">
              {documents.length === 0 ? "No documents yet" : "No document selected"}
            </h3>
            <p className="max-w-xs text-sm text-muted-foreground">
              {documents.length === 0
                ? "Project documents live here. Create the first page to get started."
                : "Pick a page from the tree, or create a new one."}
            </p>
            <Button
              type="button"
              className="mt-1 gap-1"
              onClick={() => openCreate(null)}
              disabled={!projectId}
            >
              <Plus className="h-4 w-4" />
              New page
            </Button>
          </div>
        )}
      </section>

      {createOpen && (
        <TitleDialog
          open
          title={createUnder ? `New page in "${createUnder.title}"` : "New page"}
          label="Title"
          initialValue=""
          confirmText="Create"
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
          title="Rename page"
          label="Title"
          initialValue={renameTarget.title}
          confirmText="Save"
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

      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => void handleDelete()}
        title="Delete page"
        description={
          deleteTarget
            ? childCount > 0
              ? `Delete "${deleteTarget.title}" and its ${childCount} nested page${childCount === 1 ? "" : "s"}?`
              : `Delete "${deleteTarget.title}"?`
            : ""
        }
        confirmText="Delete"
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
