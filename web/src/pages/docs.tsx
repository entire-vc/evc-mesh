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
  Loader2,
  NotebookText,
  Pencil,
  Plus,
} from "lucide-react";
import { DocEditor } from "@/components/doc-editor";
import { DocTree } from "@/components/doc-tree";
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
  const [deleteTarget, setDeleteTarget] = useState<ProjectDocument | null>(null);
  const [deleting, setDeleting] = useState(false);

  // The body last known to be on the server. The autosave compares against this
  // rather than against openDoc.body so that a failed save leaves the editor
  // dirty and keeps retrying, instead of quietly deciding it is up to date.
  const savedBodyRef = useRef("");
  const saveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

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
                <DocEditor value={draft} onChange={setDraft} />
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
