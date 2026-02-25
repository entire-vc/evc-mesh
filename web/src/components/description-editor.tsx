import { useCallback, useEffect, useRef, useState } from "react";
import { Bold, Code, Italic, Link } from "lucide-react";
import { cn } from "@/lib/cn";

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

  // Link [text](url)
  out = out.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, label, href) => {
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
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function DescriptionEditor({
  value,
  onChange,
  placeholder = "Add a description...",
  className,
}: DescriptionEditorProps) {
  const [mode, setMode] = useState<"edit" | "preview">("edit");
  const textareaRef = useRef<HTMLTextAreaElement>(null);

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

  const previewHtml = renderMarkdown(value);

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
          </div>
        )}
      </div>

      {/* Edit mode */}
      {mode === "edit" && (
        <textarea
          ref={textareaRef}
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
            autoResize();
          }}
          onBlur={() => onChange(value)}
          placeholder={placeholder}
          className="block w-full resize-none bg-transparent px-3 py-2.5 font-mono text-xs leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none"
          style={{ minHeight: "120px" }}
        />
      )}

      {/* Preview mode */}
      {mode === "preview" && (
        <div
          className="prose-mesh min-h-[120px] px-3 py-2.5 text-sm leading-relaxed"
          // eslint-disable-next-line react/no-danger
          dangerouslySetInnerHTML={{
            __html: previewHtml || `<span class="text-muted-foreground text-xs">${placeholder}</span>`,
          }}
        />
      )}
    </div>
  );
}
