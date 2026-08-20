import {
  type ClipboardEvent,
  type DragEvent,
  type KeyboardEvent,
  useCallback,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Bold,
  BookOpen,
  Code,
  Eye,
  EyeOff,
  Image,
  Italic,
  Link,
  List,
  Paperclip,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import { MarkdownRenderer } from "@/components/markdown-renderer";
import { RelayDocPicker } from "@/components/RelayDocPicker";
import { DocLinkMenu } from "@/components/doc-link-menu";
import { useDocLinkPicker } from "@/hooks/use-doc-link-picker";
import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { artifactDownloadPath, documentAttachmentDownloadPath } from "@/lib/artifact-links";
import { uploadDocumentAttachment } from "@/lib/document-attachments";
import type { Artifact, ArtifactType } from "@/types";

// Pending image: clipboard File + placeholder text used before upload
interface PendingImage {
  file: File;
  placeholder: string;
}

interface MarkdownEditorProps {
  value: string;
  onChange: (value: string) => void;
  /** Present when editing an existing task — enables immediate image upload */
  taskId?: string;
  /**
   * Present when editing a document body — uploads become document attachments
   * instead of task artifacts. Mutually exclusive with taskId in practice; if
   * both arrive, the document wins, because that is the owner of the text being
   * edited.
   */
  documentId?: string;
  projectId?: string;
  projectSettings?: Record<string, unknown>;
  placeholder?: string;
  rows?: number;
  disabled?: boolean;
  /**
   * Replaces the footer hint. The default mentions task attachments, which is
   * wrong wherever this editor is not editing a task (documents, for one).
   */
  hint?: string;
  /**
   * Set false to drop the upload affordances entirely, rather than offer buttons
   * that insert a placeholder nothing will ever replace. Only needed where there
   * is neither a task nor a document to own the bytes.
   */
  attachments?: boolean;
  /** Callback fired after a file is successfully uploaded as artifact */
  onArtifactUploaded?: (artifact: Artifact) => void;
  /** @deprecated Use onArtifactUploaded instead */
  onImageUploaded?: (artifact: Artifact) => void;
  /** Accumulate pending images when taskId is not yet known (create flow) */
  onPendingImage?: (pending: PendingImage) => void;
}

// ---------------------------------------------------------------------------
// Upload helper — separate from the main api() helper because we need
// multipart/form-data, not JSON, and we reuse the same auth token.
// ---------------------------------------------------------------------------

/** Detect artifact type from MIME type */
function detectArtifactType(mime: string): ArtifactType {
  if (mime.startsWith("image/")) return "image";
  if (mime.startsWith("text/") || mime.includes("json") || mime.includes("xml") || mime.includes("yaml")) return "code";
  if (mime.includes("pdf") || mime.includes("document") || mime.includes("spreadsheet")) return "report";
  if (mime.includes("zip") || mime.includes("tar") || mime.includes("gzip")) return "data";
  return "file";
}

/** Check if a file is an image */
function isImageFile(file: File): boolean {
  return file.type.startsWith("image/");
}

/**
 * Tell the user an upload failed, by name, and take the placeholder back out.
 *
 * A failure here used to be reported by splicing `<!-- upload failed: ... -->`
 * into the description in place of the "Uploading..." placeholder. That is not
 * a message: an HTML comment renders as nothing, so in Preview — and in the
 * saved task description, which is where the text ends up — the user saw the
 * placeholder disappear and nothing take its place. It also quietly wrote the
 * comment into text the user was about to save. A toast says it where the user
 * is looking, and leaves their text as they wrote it.
 */
function reportUploadFailure(
  file: File,
  err: unknown,
  placeholder: string,
  currentValue: string,
  onChange: (value: string) => void,
): void {
  const detail = err instanceof Error ? err.message : "upload failed";
  toast.error(`Could not attach ${file.name}`, { description: detail });
  onChange(currentValue.replace(placeholder, ""));
}

/**
 * Upload a file as an attachment on a task.
 *
 * Routed through `api()` with a FormData body, which api() passes to fetch
 * untouched and without a Content-Type of its own (see serializeBody there) —
 * the browser must set that header itself because it carries the multipart
 * boundary.
 *
 * This used to be a raw fetch, for exactly that Content-Type reason, and the
 * cost of that shortcut was the whole of this bug: api() is also where a 401 is
 * turned into a token refresh and a replay of the request. Mesh access tokens
 * live 15 minutes in tab memory (internal/auth/service.go) and are only
 * refreshed when something gets a 401 back, so an upload attempted across that
 * boundary was the one action on a task that could not recover — it just threw,
 * while every neighbouring action (autosave, posting a comment) sailed through.
 * That is why the defect reads as "sometimes it doesn't attach" rather than as
 * a broken button. Same fix as PR #620 made for document attachments.
 */
export async function uploadArtifact(
  taskId: string,
  file: File,
  artifactType?: ArtifactType,
): Promise<Artifact> {
  const form = new FormData();
  form.append("file", file, file.name);
  form.append("name", file.name);
  form.append("artifact_type", artifactType ?? detectArtifactType(file.type));

  return api<Artifact>(`/api/v1/tasks/${taskId}/artifacts`, {
    method: "POST",
    body: form,
  });
}


// ---------------------------------------------------------------------------
// Toolbar button
// ---------------------------------------------------------------------------
interface ToolbarButtonProps {
  title: string;
  onClick: () => void;
  children: React.ReactNode;
  active?: boolean;
  disabled?: boolean;
  /** Custom tooltip shown on hover when disabled (native title is suppressed on disabled buttons). */
  disabledTooltip?: string;
}

function ToolbarButton({ title, onClick, children, active, disabled, disabledTooltip }: ToolbarButtonProps) {
  const btn = (
    <button
      type="button"
      title={disabled ? undefined : title}
      onClick={disabled ? undefined : onClick}
      disabled={disabled}
      className={cn(
        "flex h-7 w-7 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
        active && "bg-accent text-foreground",
        disabled && "cursor-not-allowed opacity-40 hover:bg-transparent hover:text-muted-foreground",
      )}
    >
      {children}
    </button>
  );

  if (disabled && disabledTooltip) {
    return (
      <span className="group/tb relative inline-flex">
        {btn}
        <span className="pointer-events-none absolute bottom-full left-1/2 z-50 mb-1.5 hidden w-max max-w-[200px] -translate-x-1/2 whitespace-normal rounded border border-border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md group-hover/tb:block">
          {disabledTooltip}
        </span>
      </span>
    );
  }

  return btn;
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------
export function MarkdownEditor({
  value,
  onChange,
  taskId,
  documentId,
  projectId,
  projectSettings,
  placeholder = "Write a description... (Markdown supported)",
  rows = 6,
  disabled = false,
  hint,
  attachments = true,
  onArtifactUploaded,
  onImageUploaded,
  onPendingImage,
}: MarkdownEditorProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [showPreview, setShowPreview] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);

  // `[[` links a document from this project. Unlike the Relay picker below it is
  // not gated on an integration: our own documents are always there.
  const docLinks = useDocLinkPicker(projectId, value, onChange, textareaRef);

  const hasTrIntegration =
    projectId &&
    typeof projectSettings?.tr_share_id === "string" &&
    !!projectSettings.tr_share_id;
  // Team Relay appears as a scope only when the project is connected to one.
  // A switcher with a single option is a control that cannot do anything.
  const docLinkScopes = useMemo(
    () =>
      hasTrIntegration
        ? ([{ id: "docs", label: "Docs" }, { id: "relay", label: "Team Relay" }] as const)
        : ([{ id: "docs", label: "Docs" }] as const),
    [hasTrIntegration],
  );

  // Keep a ref to the latest value so async upload callbacks don't use stale closures
  const valueRef = useRef(value);
  valueRef.current = value;

  // Combined callback for upload notifications
  const notifyUploaded = useCallback(
    (artifact: Artifact) => {
      onArtifactUploaded?.(artifact);
      onImageUploaded?.(artifact);
    },
    [onArtifactUploaded, onImageUploaded],
  );

  // ---------------------------------------------------------------------------
  // Cursor-aware text insertion
  // ---------------------------------------------------------------------------
  const insertText = useCallback(
    (before: string, after = "", defaultContent = "") => {
      const ta = textareaRef.current;
      if (!ta) return;

      const start = ta.selectionStart;
      const end = ta.selectionEnd;
      const selected = value.slice(start, end) || defaultContent;
      const newValue =
        value.slice(0, start) + before + selected + after + value.slice(end);

      onChange(newValue);

      // Restore cursor inside the inserted snippet on next tick
      requestAnimationFrame(() => {
        ta.focus();
        const cursorPos = start + before.length + selected.length;
        ta.setSelectionRange(cursorPos, cursorPos);
      });
    },
    [value, onChange],
  );

  const handleDocSelect = useCallback(
    (relayUrl: string) => {
      const ta = textareaRef.current;
      if (!ta) return;
      const start = ta.selectionStart;
      const v = valueRef.current;
      const prefix = start > 0 && v[start - 1] !== "\n" ? "\n" : "";
      const insertion = prefix + relayUrl + "\n";
      const newValue = v.slice(0, start) + insertion + v.slice(start);
      onChange(newValue);
      setTimeout(() => {
        ta.setSelectionRange(start + insertion.length, start + insertion.length);
        ta.focus();
      }, 0);
    },
    [onChange],
  );

  const handleBold = () => insertText("**", "**", "bold text");
  const handleItalic = () => insertText("*", "*", "italic text");
  const handleCode = () => insertText("`", "`", "code");
  const handleLink = () => insertText("[", "](url)", "link text");
  const handleList = () => {
    const ta = textareaRef.current;
    if (!ta) return;
    const start = ta.selectionStart;
    // Insert at the beginning of the current line
    const lineStart = value.lastIndexOf("\n", start - 1) + 1;
    const newValue =
      value.slice(0, lineStart) + "- " + value.slice(lineStart);
    onChange(newValue);
    requestAnimationFrame(() => {
      ta.focus();
      ta.setSelectionRange(start + 2, start + 2);
    });
  };

  // ---------------------------------------------------------------------------
  // Tab key: insert two spaces instead of losing focus
  // ---------------------------------------------------------------------------
  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // The document-link menu gets the key first, and Tab means "accept this
    // suggestion" while it is open. Indenting instead would be the one keystroke
    // a writer cannot take back without also losing the menu.
    if (docLinks.onKeyDown(e)) return;
    if (e.key === "Tab") {
      e.preventDefault();
      insertText("  ");
    }
  };

  // ---------------------------------------------------------------------------
  // Clipboard paste: detect image files
  // ---------------------------------------------------------------------------
  // Where uploaded bytes go.
  //
  // One seam rather than an `if (taskId) … else if (documentId) …` at each of the
  // four upload sites (paste, picker, drop, toolbar state). Both owners answer
  // the same two questions — where to POST the file, and what stable path to
  // write into the markdown — and the rest of the editor only needs the answers.
  //
  // `null` means there is no owner yet: the task-create flow, where files are
  // held as pending images until the task exists. A document is never in that
  // state, because the Docs page creates the row before it can be edited.
  // ---------------------------------------------------------------------------
  const uploadTarget = useMemo(() => {
    if (documentId) {
      return {
        upload: async (file: File) => {
          const att = await uploadDocumentAttachment(documentId, file);
          // No Artifact to hand back: onArtifactUploaded is a task-side
          // affordance (it refreshes the task's attachment list), and a document
          // has no such list on screen.
          return { url: documentAttachmentDownloadPath(att.id), artifact: null };
        },
      };
    }
    if (taskId) {
      return {
        upload: async (file: File) => {
          const artifact = await uploadArtifact(taskId, file);
          return { url: artifactDownloadPath(artifact.id), artifact };
        },
      };
    }
    return null;
  }, [documentId, taskId]);

  const handlePaste = useCallback(
    async (e: ClipboardEvent<HTMLTextAreaElement>) => {
      if (!attachments) return;
      const items = Array.from(e.clipboardData.items);
      const imageItem = items.find((item) => item.type.startsWith("image/"));
      if (!imageItem) return;

      e.preventDefault();

      const file = imageItem.getAsFile();
      if (!file) return;

      // Give the file a readable name with timestamp
      const ext = file.type.split("/")[1] ?? "png";
      const fileName = `pasted-image-${Date.now()}.${ext}`;
      const renamedFile = new File([file], fileName, { type: file.type });

      if (uploadTarget) {
        // The owner exists — upload immediately and insert the real path
        setUploading(true);
        const placeholder = `![Uploading ${fileName}...]()`;
        insertText(placeholder);

        try {
          const { url: imageUrl, artifact } = await uploadTarget.upload(renamedFile);
          const finalMd = `![${fileName}](${imageUrl})`;
          onChange(valueRef.current.replace(placeholder, finalMd));
          if (artifact) notifyUploaded(artifact);
        } catch (err) {
          reportUploadFailure(renamedFile, err, placeholder, valueRef.current, onChange);
        } finally {
          setUploading(false);
        }
      } else {
        // No owner yet (task-create flow) — insert placeholder and notify parent
        const placeholder = `![${fileName}](pending:${fileName})`;
        insertText(placeholder);
        onPendingImage?.({ file: renamedFile, placeholder });
      }
    },
    [attachments, uploadTarget, onChange, insertText, notifyUploaded, onPendingImage],
  );

  // ---------------------------------------------------------------------------
  // Toolbar: image upload via file picker (in addition to paste)
  // ---------------------------------------------------------------------------
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleImageButtonClick = () => {
    fileInputRef.current?.click();
  };

  /** Upload a single file (image or general attachment) into the editor */
  const handleUploadFile = useCallback(
    async (file: File) => {
      const image = isImageFile(file);

      if (!uploadTarget) {
        if (image) {
          const placeholder = `![${file.name}](pending:${file.name})`;
          insertText(placeholder);
          onPendingImage?.({ file, placeholder });
        }
        // Non-image files can't be attached before task creation
        return;
      }

      setUploading(true);
      const placeholder = image
        ? `![Uploading ${file.name}...]()`
        : `[Uploading ${file.name}...]()`;
      insertText(placeholder);

      try {
        const { url, artifact } = await uploadTarget.upload(file);
        const finalMd = image
          ? `![${file.name}](${url})`
          : `[${file.name}](${url})`;
        onChange(valueRef.current.replace(placeholder, finalMd));
        if (artifact) notifyUploaded(artifact);
      } catch (err) {
        reportUploadFailure(file, err, placeholder, valueRef.current, onChange);
      } finally {
        setUploading(false);
      }
    },
    [uploadTarget, onChange, insertText, notifyUploaded, onPendingImage],
  );

  const handleFileChange = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      // Copy FileList into array BEFORE resetting input — resetting clears the FileList
      const fileList = Array.from(e.target.files ?? []);
      if (!fileList.length) return;
      e.target.value = "";
      for (const file of fileList) {
        await handleUploadFile(file);
      }
    },
    [handleUploadFile],
  );

  // ---------------------------------------------------------------------------
  // Drag-and-drop
  // ---------------------------------------------------------------------------
  const handleDragOver = useCallback((e: DragEvent<HTMLTextAreaElement | HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(true);
  }, []);

  const handleDragLeave = useCallback((e: DragEvent<HTMLTextAreaElement | HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
  }, []);

  const handleDrop = useCallback(
    async (e: DragEvent<HTMLTextAreaElement | HTMLDivElement>) => {
      e.preventDefault();
      e.stopPropagation();
      setDragOver(false);
      if (!attachments) return;

      const files = Array.from(e.dataTransfer.files);
      if (!files.length) return;

      for (const file of files) {
        await handleUploadFile(file);
      }
    },
    [attachments, handleUploadFile],
  );

  // Ref for the general file attachment picker (all file types)
  const attachInputRef = useRef<HTMLInputElement>(null);

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------
  return (
    <div className="flex flex-col rounded-lg border border-input bg-background focus-within:ring-2 focus-within:ring-ring">
      {/* Toolbar */}
      <div className="flex items-center gap-0.5 border-b border-border px-2 py-1">
        <ToolbarButton title="Bold (Ctrl+B)" onClick={handleBold}>
          <Bold className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Italic (Ctrl+I)" onClick={handleItalic}>
          <Italic className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Inline code" onClick={handleCode}>
          <Code className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Link" onClick={handleLink}>
          <Link className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Bullet list" onClick={handleList}>
          <List className="h-3.5 w-3.5" />
        </ToolbarButton>
        {attachments && (
          <>
            <ToolbarButton
              title={uploadTarget ? "Insert image" : "Insert image (available after task is created)"}
              onClick={handleImageButtonClick}
            >
              <Image className="h-3.5 w-3.5" />
            </ToolbarButton>
            <ToolbarButton
              title="Attach file"
              onClick={() => attachInputRef.current?.click()}
              disabled={!uploadTarget}
              disabledTooltip="Файл можно прикрепить после создания задачи"
            >
              <Paperclip className="h-3.5 w-3.5" />
            </ToolbarButton>
          </>
        )}
        {hasTrIntegration && attachments && (
          <>
            <div className="mx-1 h-3.5 w-px bg-border" />
            <ToolbarButton title="Attach Obsidian doc" onClick={() => setPickerOpen(true)}>
              <BookOpen className="h-3.5 w-3.5" />
            </ToolbarButton>
          </>
        )}

        {/* hidden file inputs */}
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          className="hidden"
          onChange={(e) => void handleFileChange(e)}
        />
        <input
          ref={attachInputRef}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => void handleFileChange(e)}
        />

        {/* spacer */}
        <div className="flex-1" />

        {/* Upload status */}
        {uploading && (
          <span className="mr-2 text-xs text-muted-foreground">
            Uploading...
          </span>
        )}

        {/* Preview toggle */}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 gap-1 px-2 text-xs text-muted-foreground"
          onClick={() => setShowPreview((v) => !v)}
        >
          {showPreview ? (
            <>
              <EyeOff className="h-3.5 w-3.5" />
              Edit
            </>
          ) : (
            <>
              <Eye className="h-3.5 w-3.5" />
              Preview
            </>
          )}
        </Button>
      </div>

      {/* Editor / Preview */}
      {showPreview ? (
        <div className="min-h-[80px] px-3 py-2">
          {value.trim() ? (
            <MarkdownRenderer content={value} />
          ) : (
            <span className="text-sm text-muted-foreground">
              Nothing to preview.
            </span>
          )}
        </div>
      ) : (
        <div
          className="relative"
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={(e) => void handleDrop(e)}
        >
          <textarea
            ref={textareaRef}
            value={value}
            onChange={(e) => {
              onChange(e.target.value);
              docLinks.onValueChange(
                e.target.value,
                e.target.selectionStart ?? e.target.value.length,
              );
            }}
            onKeyDown={handleKeyDown}
            onBlur={docLinks.close}
            onPaste={(e) => void handlePaste(e)}
            placeholder={placeholder}
            rows={rows}
            disabled={disabled || uploading}
            className={cn(
              "w-full resize-none bg-transparent px-3 py-2 font-mono text-sm leading-relaxed placeholder:text-muted-foreground focus:outline-none disabled:cursor-not-allowed disabled:opacity-50",
            )}
          />
          {docLinks.trigger && (
            <DocLinkMenu
              suggestions={docLinks.suggestions}
              activeIndex={docLinks.activeIndex}
              onPick={docLinks.pick}
              onHover={docLinks.setActiveIndex}
              scope={docLinks.scope}
              scopes={docLinkScopes}
              onScope={docLinks.setScope}
              loading={docLinks.loading}
            />
          )}
          {dragOver && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center rounded-b-lg border-2 border-dashed border-primary bg-primary/5">
              <span className="text-sm font-medium text-primary">
                Drop files to attach
              </span>
            </div>
          )}
        </div>
      )}

      {/* Hint */}
      {!showPreview && (
        <div className="border-t border-border px-3 py-1 text-[11px] text-muted-foreground">
          {hint ?? (
            <>
              Markdown supported &middot; Paste, drop, or attach files
              {!uploadTarget && " (uploads after task is saved)"}
            </>
          )}
        </div>
      )}

      {hasTrIntegration && attachments && (
        <RelayDocPicker
          projId={projectId!}
          open={pickerOpen}
          onClose={() => setPickerOpen(false)}
          onSelect={handleDocSelect}
        />
      )}
    </div>
  );
}

// Re-export types so the create dialog can import from one place
export type { PendingImage };
