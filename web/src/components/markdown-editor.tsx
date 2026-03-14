import {
  type ClipboardEvent,
  type DragEvent,
  type KeyboardEvent,
  useCallback,
  useRef,
  useState,
} from "react";
import {
  Bold,
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
import { getAccessToken } from "@/lib/api";
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
  projectId?: string;
  placeholder?: string;
  rows?: number;
  disabled?: boolean;
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

export async function uploadArtifact(
  taskId: string,
  file: File,
  artifactType?: ArtifactType,
): Promise<Artifact> {
  const token = getAccessToken();
  const baseUrl = import.meta.env.VITE_API_URL || "";

  const form = new FormData();
  form.append("file", file, file.name);
  form.append("name", file.name);
  form.append("artifact_type", artifactType ?? detectArtifactType(file.type));

  const headers: HeadersInit = {};
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }

  const res = await fetch(
    `${baseUrl}/api/v1/tasks/${taskId}/artifacts`,
    { method: "POST", headers, body: form },
  );

  if (!res.ok) {
    const err = (await res.json().catch(() => ({}))) as { message?: string };
    throw new Error(err.message ?? `Upload failed (${res.status})`);
  }

  return (await res.json()) as Artifact;
}


// ---------------------------------------------------------------------------
// Toolbar button
// ---------------------------------------------------------------------------
interface ToolbarButtonProps {
  title: string;
  onClick: () => void;
  children: React.ReactNode;
  active?: boolean;
}

function ToolbarButton({ title, onClick, children, active }: ToolbarButtonProps) {
  return (
    <button
      type="button"
      title={title}
      onClick={onClick}
      className={cn(
        "flex h-7 w-7 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
        active && "bg-accent text-foreground",
      )}
    >
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------
export function MarkdownEditor({
  value,
  onChange,
  taskId,
  placeholder = "Write a description... (Markdown supported)",
  rows = 6,
  disabled = false,
  onArtifactUploaded,
  onImageUploaded,
  onPendingImage,
}: MarkdownEditorProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [showPreview, setShowPreview] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);

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
    if (e.key === "Tab") {
      e.preventDefault();
      insertText("  ");
    }
  };

  // ---------------------------------------------------------------------------
  // Clipboard paste: detect image files
  // ---------------------------------------------------------------------------
  const handlePaste = useCallback(
    async (e: ClipboardEvent<HTMLTextAreaElement>) => {
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

      if (taskId) {
        // Task exists — upload immediately and insert real URL
        setUploading(true);
        const placeholder = `![Uploading ${fileName}...]()`;
        insertText(placeholder);

        try {
          const artifact = await uploadArtifact(taskId, renamedFile);
          const imageUrl = artifact.storage_url;
          const finalMd = `![${fileName}](${imageUrl})`;
          onChange(value.replace(placeholder, finalMd));
          notifyUploaded(artifact);
        } catch (err) {
          // Replace placeholder with error note
          onChange(
            value.replace(
              placeholder,
              `<!-- image upload failed: ${err instanceof Error ? err.message : "unknown error"} -->`,
            ),
          );
        } finally {
          setUploading(false);
        }
      } else {
        // No taskId yet (create flow) — insert placeholder and notify parent
        const placeholder = `![${fileName}](pending:${fileName})`;
        insertText(placeholder);
        onPendingImage?.({ file: renamedFile, placeholder });
      }
    },
    [taskId, value, onChange, insertText, notifyUploaded, onPendingImage],
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

      if (!taskId) {
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
        const artifact = await uploadArtifact(taskId, file);
        const url = artifact.storage_url;
        const finalMd = image
          ? `![${file.name}](${url})`
          : `[${file.name}](${url})`;
        onChange(value.replace(placeholder, finalMd));
        notifyUploaded(artifact);
      } catch (err) {
        onChange(
          value.replace(
            placeholder,
            `<!-- upload failed: ${err instanceof Error ? err.message : "unknown error"} -->`,
          ),
        );
      } finally {
        setUploading(false);
      }
    },
    [taskId, value, onChange, insertText, notifyUploaded, onPendingImage],
  );

  const handleFileChange = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      const files = e.target.files;
      if (!files?.length) return;
      e.target.value = ""; // reset so same file can be re-selected
      for (const file of Array.from(files)) {
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

      const files = Array.from(e.dataTransfer.files);
      if (!files.length) return;

      for (const file of files) {
        await handleUploadFile(file);
      }
    },
    [handleUploadFile],
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
        <ToolbarButton
          title={taskId ? "Insert image" : "Insert image (available after task is created)"}
          onClick={handleImageButtonClick}
        >
          <Image className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton
          title={taskId ? "Attach file" : "Attach file (available after task is created)"}
          onClick={() => attachInputRef.current?.click()}
        >
          <Paperclip className="h-3.5 w-3.5" />
        </ToolbarButton>

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
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={(e) => void handlePaste(e)}
            placeholder={placeholder}
            rows={rows}
            disabled={disabled || uploading}
            className={cn(
              "w-full resize-none bg-transparent px-3 py-2 font-mono text-sm leading-relaxed placeholder:text-muted-foreground focus:outline-none disabled:cursor-not-allowed disabled:opacity-50",
            )}
          />
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
          Markdown supported &middot; Paste, drop, or attach files
          {!taskId && " (uploads after task is saved)"}
        </div>
      )}
    </div>
  );
}

// Re-export types so the create dialog can import from one place
export type { PendingImage };
