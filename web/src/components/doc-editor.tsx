import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useNavigate } from "react-router";
import {
  Bold,
  Code,
  Image as ImageIcon,
  Italic,
  Link as LinkIcon,
  List,
  ListOrdered,
  Paperclip,
  Quote,
  SquareCode,
  Table as TableIcon,
} from "lucide-react";
import type { Node as ProseNode, Schema } from "@milkdown/kit/prose/model";
import { editorViewCtx } from "@milkdown/kit/core";
import {
  toggleEmphasisCommand,
  toggleInlineCodeCommand,
  toggleLinkCommand,
  toggleStrongCommand,
  wrapInBlockquoteCommand,
  wrapInBulletListCommand,
  wrapInOrderedListCommand,
  createCodeBlockCommand,
} from "@milkdown/kit/preset/commonmark";
import { insertTableCommand } from "@milkdown/kit/preset/gfm";
import { upload, uploadConfig } from "@milkdown/kit/plugin/upload";
import { callCommand, replaceAll } from "@milkdown/kit/utils";
import { Milkdown, MilkdownProvider, useEditor, useInstance } from "@milkdown/react";
import { cn } from "@/lib/cn";
import {
  documentAttachmentDownloadPath,
  handleArtifactLinkClick,
  resolveArtifactImages,
} from "@/lib/artifact-links";
import { uploadDocumentAttachment } from "@/lib/document-attachments";
import { makeDocEditor } from "@/lib/milkdown/editor";
import "@/components/doc-editor.css";

export interface DocEditorProps {
  /** Markdown source of the document. */
  value: string;
  /** Called on every keystroke while editing. Never called when readOnly. */
  onChange: (value: string) => void;
  /** Render the document instead of editing it. Default false. */
  readOnly?: boolean;
  /**
   * The document being edited. Uploads become attachments owned by it; without
   * it the upload affordances are dropped rather than left inserting
   * placeholders nothing will replace.
   */
  documentId?: string;
}

// The prose classes are shared by the editor and the viewer on purpose: they are
// the same engine in two modes, and the whole reason this unit exists is that
// the previous two renderers were free to drift apart.
const PROSE_CLASS =
  "mesh-doc-prose min-h-[8rem] text-sm text-foreground focus:outline-none";

// ---------------------------------------------------------------------------
// Toolbar
// ---------------------------------------------------------------------------

function ToolbarButton({
  title,
  onClick,
  children,
}: {
  title: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      title={title}
      // The editor loses its selection to a focus change, and the command then
      // has nothing to apply to.
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className="flex h-7 w-7 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
    >
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// The editing / viewing surface
// ---------------------------------------------------------------------------

function MilkdownDoc({ value, onChange, readOnly, documentId }: DocEditorProps) {
  const navigate = useNavigate();
  const containerRef = useRef<HTMLDivElement>(null);

  // Read by the editor factory, which is created once per mode and must not
  // close over a stale first render.
  const valueRef = useRef(value);
  valueRef.current = value;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  // The last markdown the editor and this component agree on. Compared against
  // incoming `value` to decide whether a change came from inside the editor
  // (do nothing — the editor already has it) or from outside (push it in).
  const syncedRef = useRef(value);
  // True while an external value is being written in, so the listener can tell
  // the resulting transaction apart from a user edit and not echo it back.
  const applyingRef = useRef(false);

  const [uploading, setUploading] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  const [linkHref, setLinkHref] = useState("");

  // Where uploaded bytes go. Null when there is no document to own them, which
  // is what turns the affordances off rather than offering a button that
  // inserts a placeholder nothing will replace.
  const uploadTarget = useMemo(() => {
    if (!documentId) return null;
    return async (file: File) => {
      const att = await uploadDocumentAttachment(documentId, file);
      return documentAttachmentDownloadPath(att.id);
    };
  }, [documentId]);

  // Drag-and-drop and paste of files, handled by the editor itself rather than
  // by a second code path bolted onto the surrounding div.
  const uploadsEnabled = !!uploadTarget && !readOnly;

  const buildNodes = useCallback(
    async (files: readonly File[], schema: Schema): Promise<ProseNode[]> => {
      if (!uploadTarget) return [];
      const nodes: ProseNode[] = [];
      for (const file of files) {
        const url = await uploadTarget(file);
        if (file.type.startsWith("image/")) {
          const node = schema.nodes.image?.createAndFill({
            src: url,
            alt: file.name,
          });
          if (node) nodes.push(node);
        } else if (schema.marks.link) {
          // No image node for a PDF: a named link is what the reader can use,
          // and it survives the markdown round-trip as an ordinary link.
          nodes.push(
            schema.text(file.name, [schema.marks.link.create({ href: url })]),
          );
        }
      }
      return nodes;
    },
    [uploadTarget],
  );
  const buildNodesRef = useRef(buildNodes);
  buildNodesRef.current = buildNodes;

  useEditor(
    (root) =>
      makeDocEditor({
        root,
        defaultValue: valueRef.current,
        editable: !readOnly,
        editorClassName: PROSE_CLASS,
        plugins: uploadsEnabled ? upload : [],
        configure: uploadsEnabled
          ? (ctx) => {
              ctx.update(uploadConfig.key, (prev) => ({
                ...prev,
                uploader: (files, schema) =>
                  buildNodesRef.current(Array.from(files), schema),
              }));
            }
          : undefined,
        onMarkdown: (markdown) => {
          // Echo of our own replaceAll, not something the user typed.
          if (applyingRef.current) return;
          if (markdown === syncedRef.current) return;
          syncedRef.current = markdown;
          onChangeRef.current(markdown);
        },
      }),
    // Not `value`: the editor owns its document and re-creating it per keystroke
    // would destroy the selection. External changes go through replaceAll below.
    [readOnly, uploadsEnabled],
  );

  const [loading, getInstance] = useInstance();

  // Push an externally-changed value into the editor — switching documents,
  // a failed save being retried, anything that is not the user typing here.
  useEffect(() => {
    if (loading) return;
    if (value === syncedRef.current) return;
    const editor = getInstance();
    if (!editor) return;
    applyingRef.current = true;
    try {
      editor.action(replaceAll(value));
      syncedRef.current = value;
    } finally {
      applyingRef.current = false;
    }
  }, [value, loading, getInstance]);

  // Internal attachments cannot be a plain <img src> (no auth header), so the
  // schema parks the path on data-artifact-src and this swaps in a fresh
  // presigned URL — same contract the old renderer had.
  useEffect(() => {
    if (loading || !containerRef.current) return;
    void resolveArtifactImages(containerRef.current);
  }, [value, loading, uploading]);

  const runCommand = useCallback(
    (key: Parameters<typeof callCommand>[0], payload?: unknown) => {
      const editor = getInstance();
      if (!editor) return;
      editor.action(callCommand(key, payload));
      editor.action((ctx) => {
        ctx.get(editorViewCtx).focus();
      });
    },
    [getInstance],
  );

  const insertUploaded = useCallback(
    async (files: readonly File[]) => {
      if (!uploadTarget) return;
      const editor = getInstance();
      if (!editor) return;
      setUploading(true);
      try {
        for (const file of files) {
          const url = await uploadTarget(file);
          editor.action((ctx) => {
            const view = ctx.get(editorViewCtx);
            const { schema } = view.state;
            const node = file.type.startsWith("image/")
              ? schema.nodes.image?.createAndFill({ src: url, alt: file.name })
              : schema.marks.link
                ? schema.text(file.name, [schema.marks.link.create({ href: url })])
                : null;
            if (!node) return;
            view.dispatch(view.state.tr.replaceSelectionWith(node, false));
          });
        }
      } finally {
        setUploading(false);
      }
    },
    [uploadTarget, getInstance],
  );

  const imageInputRef = useRef<HTMLInputElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleFiles = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const files = Array.from(e.target.files ?? []);
      e.target.value = "";
      if (files.length) void insertUploaded(files);
    },
    [insertUploaded],
  );

  const submitLink = (e: FormEvent) => {
    e.preventDefault();
    // An unsafe href is refused by the schema anyway; closing without applying
    // is a clearer answer than silently storing a link that goes nowhere.
    runCommand(toggleLinkCommand.key, { href: linkHref });
    setLinkHref("");
    setLinkOpen(false);
  };

  // Internal attachment links carry no usable href (see artifact-links.ts) and
  // are opened by resolving a fresh URL on click instead.
  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      if (handleArtifactLinkClick(e)) return;
      if (!readOnly) return;
      const link = (e.target as HTMLElement).closest("a[href]");
      if (!(link instanceof HTMLAnchorElement)) return;
      const href = link.getAttribute("href") ?? "";
      if (href.startsWith("/")) {
        e.preventDefault();
        void navigate(href);
      }
    },
    [readOnly, navigate],
  );

  return (
    <div
      className={cn(
        !readOnly &&
          "flex flex-col rounded-lg border border-input bg-background focus-within:ring-2 focus-within:ring-ring",
      )}
    >
      {!readOnly && (
        <div className="flex flex-wrap items-center gap-0.5 border-b border-border px-2 py-1">
          <ToolbarButton
            title="Bold (Ctrl+B)"
            onClick={() => runCommand(toggleStrongCommand.key)}
          >
            <Bold className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton
            title="Italic (Ctrl+I)"
            onClick={() => runCommand(toggleEmphasisCommand.key)}
          >
            <Italic className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton
            title="Inline code"
            onClick={() => runCommand(toggleInlineCodeCommand.key)}
          >
            <Code className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton title="Link" onClick={() => setLinkOpen((v) => !v)}>
            <LinkIcon className="h-3.5 w-3.5" />
          </ToolbarButton>

          <div className="mx-1 h-3.5 w-px bg-border" />

          <ToolbarButton
            title="Bullet list"
            onClick={() => runCommand(wrapInBulletListCommand.key)}
          >
            <List className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton
            title="Numbered list"
            onClick={() => runCommand(wrapInOrderedListCommand.key)}
          >
            <ListOrdered className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton
            title="Quote"
            onClick={() => runCommand(wrapInBlockquoteCommand.key)}
          >
            <Quote className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton
            title="Code block"
            onClick={() => runCommand(createCodeBlockCommand.key)}
          >
            <SquareCode className="h-3.5 w-3.5" />
          </ToolbarButton>
          <ToolbarButton
            title="Table"
            onClick={() => runCommand(insertTableCommand.key)}
          >
            <TableIcon className="h-3.5 w-3.5" />
          </ToolbarButton>

          {uploadTarget && (
            <>
              <div className="mx-1 h-3.5 w-px bg-border" />
              <ToolbarButton
                title="Insert image"
                onClick={() => imageInputRef.current?.click()}
              >
                <ImageIcon className="h-3.5 w-3.5" />
              </ToolbarButton>
              <ToolbarButton
                title="Attach file"
                onClick={() => fileInputRef.current?.click()}
              >
                <Paperclip className="h-3.5 w-3.5" />
              </ToolbarButton>
              <input
                ref={imageInputRef}
                type="file"
                accept="image/*"
                multiple
                className="hidden"
                onChange={handleFiles}
              />
              <input
                ref={fileInputRef}
                type="file"
                multiple
                className="hidden"
                onChange={handleFiles}
              />
            </>
          )}

          <div className="flex-1" />
          {uploading && (
            <span className="mr-2 text-xs text-muted-foreground">Uploading...</span>
          )}
        </div>
      )}

      {!readOnly && linkOpen && (
        <form
          onSubmit={submitLink}
          className="flex items-center gap-2 border-b border-border px-2 py-1"
        >
          <input
            autoFocus
            value={linkHref}
            onChange={(e) => setLinkHref(e.target.value)}
            placeholder="https://example.com"
            aria-label="Link URL"
            className="h-7 flex-1 rounded border border-input bg-background px-2 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
          />
          <button
            type="submit"
            className="h-7 rounded px-2 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            Apply
          </button>
        </form>
      )}

      <div
        ref={containerRef}
        onClick={handleClick}
        className={cn(!readOnly && "px-3 py-2")}
      >
        <Milkdown />
      </div>

      {!readOnly && (
        <div className="border-t border-border px-3 py-1 text-[11px] text-muted-foreground">
          {uploadTarget
            ? "Rich text — tables, task lists and footnotes supported. Paste or drop a file to attach it."
            : "Rich text — tables, task lists and footnotes supported."}
        </div>
      )}
    </div>
  );
}

/**
 * DocEditor — the single seam between the Docs page and whatever renders or
 * edits a document body.
 *
 * The viewer and the editor are one engine (Milkdown 7.22 over ProseMirror) with
 * `editable` flipped, so a document cannot look like one thing on the page and
 * another in the editor. They used to be two hand-written renderers, and they
 * had already drifted: only the editor understood the toolbar's output, and
 * neither understood tables, task lists, nested lists or footnotes.
 *
 * Nothing outside this file may import the markdown components for document
 * bodies.
 */
export function DocEditor({
  value,
  onChange,
  readOnly = false,
  documentId,
}: DocEditorProps) {
  if (readOnly && !value.trim()) {
    return <p className="text-sm text-muted-foreground">This page is empty.</p>;
  }

  return (
    // Remounting between modes rather than reconfiguring in place: an editable
    // ProseMirror view and a read-only one differ in plugins as well as in the
    // `editable` flag, and a fresh view is cheaper to reason about than a
    // half-updated one.
    <MilkdownProvider key={readOnly ? "view" : "edit"}>
      <MilkdownDoc
        value={value}
        onChange={onChange}
        readOnly={readOnly}
        documentId={documentId}
      />
    </MilkdownProvider>
  );
}
