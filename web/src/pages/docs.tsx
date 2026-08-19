import {
  type FormEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { useNavigate, useParams } from "react-router";
import {
  AlertCircle,
  Check,
  FilePlus2,
  FileText,
  Loader2,
  NotebookText,
  Pencil,
  Plus,
} from "lucide-react";
import { DocEditor } from "@/components/doc-editor";
import { DocTree, moveTargets } from "@/components/doc-tree";
import { ConfirmDialog } from "@/components/confirm-dialog";
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
  | { status: "error"; message: string };

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
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
                  "flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-sm hover:bg-accent",
                  selected === null && "bg-accent font-medium",
                )}
              >
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
                    "flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-sm hover:bg-accent",
                    selected === target.id && "bg-accent font-medium",
                  )}
                  style={{ paddingLeft: `${8 + depth * 12}px` }}
                >
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
  return null;
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function DocsPage() {
  const { wsSlug, projectSlug, docId } = useParams();
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

  const [createUnder, setCreateUnder] = useState<ProjectDocument | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ProjectDocument | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [moveTarget, setMoveTarget] = useState<ProjectDocument | null>(null);
  const [moving, setMoving] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ProjectDocument | null>(null);
  const [deleting, setDeleting] = useState(false);

  // The body last known to be on the server. The autosave compares against this
  // rather than against openDoc.body so that a failed save leaves the editor
  // dirty and keeps retrying, instead of quietly deciding it is up to date.
  const savedBodyRef = useRef("");
  const saveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

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
      await updateDocument(doc.id, { body });
      savedBodyRef.current = body;
      setSaveState({ status: "saved" });
    } catch (err) {
      setSaveState({
        status: "error",
        message: errorMessage(err, "save failed"),
      });
    }
  }, [openDoc, draft, updateDocument]);

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
      void updateDocument(doc.id, { body }).catch((err: unknown) => {
        toast.error(
          `"${doc.title}" was not saved: ${errorMessage(err, "save failed")}`,
        );
      });
    };
  }, [docId, updateDocument]);

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
  return (
    <div className="flex h-full min-h-0 gap-3 overflow-hidden">
      {/* Document tree */}
      <div className="hidden w-64 shrink-0 flex-col rounded-lg border border-border md:flex">
        <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2">
          <h2 className="text-sm font-semibold">Documents</h2>
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

        <div className="flex-1 overflow-y-auto p-2">
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
      </div>

      {/* Document area */}
      <div className="flex flex-1 flex-col overflow-hidden rounded-lg border border-border">
        {docLoading ? (
          <div className="space-y-3 p-6">
            <Skeleton className="h-7 w-64" />
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
            <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-2">
              <h1 className="truncate text-base font-semibold">
                {openDoc.title}
              </h1>
              <div className="flex shrink-0 items-center gap-2">
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
              </div>
            </div>
            <div className="flex-1 overflow-y-auto p-4">
              {editing ? (
                <DocEditor value={draft} onChange={setDraft} documentId={docId} />
              ) : draft.trim() ? (
                <DocEditor value={draft} onChange={setDraft} readOnly />
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
      </div>

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
