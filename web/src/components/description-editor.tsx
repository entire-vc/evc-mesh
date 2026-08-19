import {
  type ClipboardEvent,
  type DragEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Bold, BookOpen, Code, Image as ImageIcon, Italic, Link, Paperclip } from "lucide-react";
import { cn } from "@/lib/cn";
import { RelayDocPicker } from "@/components/RelayDocPicker";
import { DocLinkMenu } from "@/components/doc-link-menu";
import { useDocLinkPicker } from "@/hooks/use-doc-link-picker";
import { useProjectTrIntegration } from "@/hooks/useProjectTrIntegration";
import { uploadArtifact } from "@/components/markdown-editor";
import { toast } from "@/components/ui/toast";
import {
  artifactDownloadPath,
  handleArtifactLinkClick,
  isArtifactDownloadPath,
  renderArtifactAwareImage,
  renderArtifactAwareLink,
  resolveArtifactImages,
} from "@/lib/artifact-links";

// ---------------------------------------------------------------------------
// Markdown renderer (no external library)
// ---------------------------------------------------------------------------

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

export function renderMarkdown(text: string): string {
  if (!text) return "";

  // Split into fenced code blocks and prose sections to handle them separately
  const parts = text.split(/(```[\s\S]*?```)/g);

  const rendered = parts
    .map((part) => {
      // Fenced code block
      if (part.startsWith("```")) {
        const inner = part.slice(3, part.endsWith("```") ? -3 : undefined).trim();
        const firstNewline = inner.indexOf("\n");
        const code =
          firstNewline === -1 ? inner : inner.slice(firstNewline + 1);
        return `<pre class="bg-muted rounded p-3 overflow-x-auto text-xs font-mono my-2"><code>${escapeHtml(code)}</code></pre>`;
      }

      // Prose: process line by line for block-level constructs, then inline
      const lines = part.split("\n");
      const outputLines: string[] = [];
      let i = 0;

      while (i < lines.length) {
        const line = lines[i] ?? "";

        // Headings
        const h2Match = line.match(/^## (.+)$/);
        if (h2Match) {
          outputLines.push(
            `<h2 class="text-base font-semibold mt-3 mb-1">${applyInline(h2Match[1] ?? "")}</h2>`,
          );
          i++;
          continue;
        }
        const h3Match = line.match(/^### (.+)$/);
        if (h3Match) {
          outputLines.push(
            `<h3 class="text-sm font-semibold mt-2 mb-1">${applyInline(h3Match[1] ?? "")}</h3>`,
          );
          i++;
          continue;
        }

        // Unordered list — collect consecutive list items
        if (/^- (.+)$/.test(line)) {
          const items: string[] = [];
          while (i < lines.length && /^- (.+)$/.test(lines[i] ?? "")) {
            const m = (lines[i] ?? "").match(/^- (.+)$/);
            items.push(`<li class="ml-4 list-disc">${applyInline(m?.[1] ?? "")}</li>`);
            i++;
          }
          outputLines.push(`<ul class="my-1 space-y-0.5">${items.join("")}</ul>`);
          continue;
        }

        // Blank line
        if (line.trim() === "") {
          outputLines.push("<br>");
          i++;
          continue;
        }

        // Regular paragraph line
        outputLines.push(`<span>${applyInline(escapeHtml(line))}</span><br>`);
        i++;
      }

      return outputLines.join("");
    })
    .join("");

  return rendered;
}

/** Apply inline markdown transforms. Input must already be HTML-escaped for prose. */
function applyInline(text: string): string {
  let out = text;

  // Bold **text** — escape the content first to avoid double-escaping
  out = out.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");

  // Italic *text* (single asterisk, not preceded/followed by another)
  out = out.replace(/\*(.+?)\*/g, "<em>$1</em>");

  // Inline code `code`
  out = out.replace(
    /`([^`]+)`/g,
    '<code class="bg-muted rounded px-1 py-0.5 text-xs font-mono">$1</code>',
  );

  // Image ![alt](url) — must run before the link regex below, since it shares
  // the same [..](..) shape with a leading "!".
  out = out.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (_match, alt, url) =>
    renderArtifactAwareImage(alt, url),
  );

  // Link [text](url)
  out = out.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, label, href) => {
    if (isArtifactDownloadPath(href)) {
      return renderArtifactAwareLink(label, href);
    }
    // Only allow http/https links for safety
    const safe =
      href.startsWith("http://") || href.startsWith("https://")
        ? href
        : "#";
    return `<a href="${escapeHtml(safe)}" target="_blank" rel="noopener noreferrer" class="text-primary underline underline-offset-2 hover:opacity-80">${label}</a>`;
  });

  return out;
}

// ---------------------------------------------------------------------------
// Toolbar helper: insert markdown around selection or at cursor
// ---------------------------------------------------------------------------

function wrapSelection(
  textarea: HTMLTextAreaElement,
  before: string,
  after: string,
  placeholder: string,
): string {
  const start = textarea.selectionStart;
  const end = textarea.selectionEnd;
  const value = textarea.value;
  const selected = value.slice(start, end);
  const replacement = selected.length > 0 ? selected : placeholder;
  const newValue =
    value.slice(0, start) + before + replacement + after + value.slice(end);
  const newCursorStart = start + before.length;
  const newCursorEnd = newCursorStart + replacement.length;

  // We return the new value; caller must also call textarea.setSelectionRange
  // after the state update, so we schedule it.
  setTimeout(() => {
    textarea.setSelectionRange(newCursorStart, newCursorEnd);
  }, 0);

  return newValue;
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface DescriptionEditorProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  /** @deprecated No longer read. Will be removed after callers are cleaned up. */
  projectSettings?: Record<string, unknown>;
  /** Project ID — required to show the TR doc picker. */
  projId?: string;
  /** Task ID — required to attach files/images (uploaded as task artifacts). */
  taskId?: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function DescriptionEditor({
  value,
  onChange,
  placeholder = "Add a description...",
  className,
  projId,
  taskId,
}: DescriptionEditorProps) {
  const [mode, setMode] = useState<"edit" | "preview">("edit");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const previewRef = useRef<HTMLDivElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const attachInputRef = useRef<HTMLInputElement>(null);

  // Keep a ref to the latest value so async upload callbacks (which may
  // resolve after further keystrokes) don't clobber newer edits.
  const valueRef = useRef(value);
  valueRef.current = value;

  const { enabled: hasTrIntegration } = useProjectTrIntegration(projId);

  // `[[` links a document from this project. Not gated on an integration the way
  // the Relay picker is: our own documents are always there.
  const docLinks = useDocLinkPicker(projId, value, onChange, textareaRef);
  // Team Relay appears as a scope only when the project is connected to one.
  // A switcher with a single option is a control that cannot do anything.
  const docLinkScopes = useMemo(
    () =>
      hasTrIntegration
        ? ([{ id: "docs", label: "Docs" }, { id: "relay", label: "Team Relay" }] as const)
        : ([{ id: "docs", label: "Docs" }] as const),
    [hasTrIntegration],
  );

  // Auto-resize textarea height
  const autoResize = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, []);

  useEffect(() => {
    if (mode === "edit") {
      autoResize();
    }
  }, [value, mode, autoResize]);

  // Toolbar action
  const applyFormat = useCallback(
    (before: string, after: string, placeholder: string) => {
      const textarea = textareaRef.current;
      if (!textarea) return;
      const newValue = wrapSelection(textarea, before, after, placeholder);
      onChange(newValue);
    },
    [onChange],
  );

  const handleDocSelect = useCallback(
    (relayUrl: string) => {
      const textarea = textareaRef.current;
      if (!textarea) return;
      const start = textarea.selectionStart;
      const v = textarea.value;
      // Ensure relay:// goes on its own line
      const prefix = start > 0 && v[start - 1] !== "\n" ? "\n" : "";
      const insertion = prefix + relayUrl + "\n";
      const newValue = v.slice(0, start) + insertion + v.slice(start);
      const newPos = start + insertion.length;
      onChange(newValue);
      setTimeout(() => {
        textarea.setSelectionRange(newPos, newPos);
        textarea.focus();
      }, 0);
    },
    [onChange],
  );

  // Insert text at the cursor (or at the end, if the textarea isn't mounted).
  const insertAtCursor = useCallback(
    (text: string) => {
      const textarea = textareaRef.current;
      if (!textarea) {
        onChange(value + text);
        return;
      }
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const newValue = value.slice(0, start) + text + value.slice(end);
      onChange(newValue);
      const newPos = start + text.length;
      setTimeout(() => {
        textarea.setSelectionRange(newPos, newPos);
        textarea.focus();
      }, 0);
    },
    [value, onChange],
  );

  // Upload a file as a task artifact and insert a markdown reference to it.
  // Images become ![name](url); everything else becomes a [name](url) link.
  const handleUploadFile = useCallback(
    async (file: File) => {
      if (!taskId) return;
      const isImage = file.type.startsWith("image/");
      setUploading(true);
      const placeholder = isImage
        ? `![Uploading ${file.name}...]()`
        : `[Uploading ${file.name}...]()`;
      insertAtCursor(placeholder);

      try {
        const artifact = await uploadArtifact(taskId, file);
        const url = artifactDownloadPath(artifact.id);
        const finalMd = isImage ? `![${file.name}](${url})` : `[${file.name}](${url})`;
        onChange(valueRef.current.replace(placeholder, finalMd));
      } catch (err) {
        onChange(valueRef.current.replace(placeholder, ""));
        toast(err instanceof Error ? err.message : "Upload failed");
      } finally {
        setUploading(false);
      }
    },
    [taskId, insertAtCursor, onChange],
  );

  // Clipboard paste: upload a pasted screenshot/image immediately.
  const handlePaste = useCallback(
    (e: ClipboardEvent<HTMLTextAreaElement>) => {
      if (!taskId) return;
      const imageItem = Array.from(e.clipboardData.items).find((item) =>
        item.type.startsWith("image/"),
      );
      if (!imageItem) return;
      const file = imageItem.getAsFile();
      if (!file) return;
      e.preventDefault();
      const ext = file.type.split("/")[1] ?? "png";
      const renamedFile = new File([file], `pasted-image-${Date.now()}.${ext}`, {
        type: file.type,
      });
      void handleUploadFile(renamedFile);
    },
    [taskId, handleUploadFile],
  );

  // Drag-and-drop: upload any dropped files (image or not) in order.
  const handleDragOver = useCallback(
    (e: DragEvent<HTMLTextAreaElement>) => {
      if (!taskId) return;
      e.preventDefault();
      e.stopPropagation();
      setDragOver(true);
    },
    [taskId],
  );

  const handleDragLeave = useCallback((e: DragEvent<HTMLTextAreaElement>) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
  }, []);

  const handleDrop = useCallback(
    async (e: DragEvent<HTMLTextAreaElement>) => {
      if (!taskId) return;
      e.preventDefault();
      e.stopPropagation();
      setDragOver(false);
      for (const file of Array.from(e.dataTransfer.files)) {
        await handleUploadFile(file);
      }
    },
    [taskId, handleUploadFile],
  );

  // Toolbar / file-picker uploads (image button restricts to image/*, attach allows anything).
  const handleFileInputChange = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      const fileList = Array.from(e.target.files ?? []);
      if (!fileList.length) return;
      e.target.value = "";
      for (const file of fileList) {
        await handleUploadFile(file);
      }
    },
    [handleUploadFile],
  );

  const previewHtml = renderMarkdown(value);

  // Resolve internal artifact images to a fresh presigned URL whenever the
  // preview is shown or its content changes — see artifact-links.ts.
  useEffect(() => {
    if (mode === "preview" && previewRef.current) {
      void resolveArtifactImages(previewRef.current);
    }
  }, [mode, previewHtml]);

  return (
    <div className={cn("rounded-lg border border-border", className)}>
      {/* Mode toggle bar */}
      <div className="flex items-center gap-1 border-b border-border px-2 py-1">
        <button
          type="button"
          onClick={() => setMode("edit")}
          className={cn(
            "rounded px-2.5 py-1 text-xs font-medium transition-colors",
            mode === "edit"
              ? "bg-muted text-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          Edit
        </button>
        <button
          type="button"
          onClick={() => setMode("preview")}
          className={cn(
            "rounded px-2.5 py-1 text-xs font-medium transition-colors",
            mode === "preview"
              ? "bg-muted text-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          Preview
        </button>

        {/* Toolbar (edit mode only) */}
        {mode === "edit" && (
          <div className="ml-2 flex items-center gap-0.5 border-l border-border pl-2">
            <button
              type="button"
              title="Bold"
              onClick={() => applyFormat("**", "**", "bold text")}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <Bold className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              title="Italic"
              onClick={() => applyFormat("*", "*", "italic text")}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <Italic className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              title="Inline code"
              onClick={() => applyFormat("`", "`", "code")}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <Code className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              title="Link"
              onClick={() => applyFormat("[", "](https://)", "link text")}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <Link className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              title={taskId ? "Insert image" : "Save the task to attach images"}
              onClick={() => imageInputRef.current?.click()}
              disabled={!taskId}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted-foreground"
            >
              <ImageIcon className="h-3.5 w-3.5" />
            </button>
            <button
              type="button"
              title={taskId ? "Attach file" : "Save the task to attach files"}
              onClick={() => attachInputRef.current?.click()}
              disabled={!taskId}
              className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted-foreground"
            >
              <Paperclip className="h-3.5 w-3.5" />
            </button>
            <input
              ref={imageInputRef}
              type="file"
              accept="image/*"
              className="hidden"
              onChange={(e) => void handleFileInputChange(e)}
            />
            <input
              ref={attachInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => void handleFileInputChange(e)}
            />
            {uploading && (
              <span className="ml-1 text-[11px] text-muted-foreground">Uploading…</span>
            )}
            {hasTrIntegration && (
              <>
                <div className="mx-1 h-3.5 w-px bg-border" />
                <button
                  type="button"
                  title="Attach Obsidian doc"
                  onClick={() => setPickerOpen(true)}
                  className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                >
                  <BookOpen className="h-3.5 w-3.5" />
                </button>
              </>
            )}
          </div>
        )}
      </div>

      {/* Edit mode */}
      {mode === "edit" && (
        <div className="relative">
          <textarea
            ref={textareaRef}
            value={value}
            onChange={(e) => {
              onChange(e.target.value);
              autoResize();
              docLinks.onValueChange(
                e.target.value,
                e.target.selectionStart ?? e.target.value.length,
              );
            }}
            onKeyDown={(e) => {
              docLinks.onKeyDown(e);
            }}
            onBlur={() => {
              docLinks.close();
              onChange(value);
            }}
            onPaste={handlePaste}
            onDragOver={handleDragOver}
            onDragLeave={handleDragLeave}
            onDrop={(e) => void handleDrop(e)}
            placeholder={placeholder}
            disabled={uploading}
            className="block w-full resize-none bg-transparent px-3 py-2.5 font-mono text-xs leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none disabled:cursor-not-allowed disabled:opacity-60"
            style={{ minHeight: "120px" }}
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
              <span className="text-sm font-medium text-primary">Drop files to attach</span>
            </div>
          )}
        </div>
      )}

      {/* Preview mode */}
      {mode === "preview" && (
        <div
          ref={previewRef}
          className="prose-mesh min-h-[120px] px-3 py-2.5 text-sm leading-relaxed"
          onClick={(e) => {
            handleArtifactLinkClick(e);
          }}
          // eslint-disable-next-line react/no-danger
          dangerouslySetInnerHTML={{
            __html: previewHtml || `<span class="text-muted-foreground text-xs">${placeholder}</span>`,
          }}
        />
      )}

      {mode === "edit" && taskId && (
        <div className="border-t border-border px-3 py-1 text-[11px] text-muted-foreground">
          Paste, drop, or attach files and images
        </div>
      )}

      {hasTrIntegration && (
        <RelayDocPicker
          projId={projId!}
          open={pickerOpen}
          onClose={() => setPickerOpen(false)}
          onSelect={handleDocSelect}
        />
      )}
    </div>
  );
}
