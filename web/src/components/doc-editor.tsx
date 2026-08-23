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
import { createPortal } from "react-dom";
import type { Node as ProseNode, Schema } from "@milkdown/kit/prose/model";
import type { EditorView } from "@milkdown/kit/prose/view";
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
import {
  type AnchorMatch,
  type DocAnchor,
  buildBlocks,
  encodeAnchor,
  makeAnchor,
  resolveAnchor,
} from "@/lib/docs/anchor";
import { toast } from "@/components/ui/toast";
import "@/components/doc-editor.css";
import { apiErrorMessage } from "@/lib/api-error";

/**
 * Tell the user an upload failed, by name.
 *
 * Every failure on this path used to be swallowed: the toolbar handler ran as
 * `void insertUploaded(...)` with no catch, and the drag/drop uploader rejected
 * into Milkdown's plugin, which logs to the console and leaves its "Upload in
 * progress..." decoration on screen forever. Both produced the same thing for
 * the user — they picked a file and the document did not change — which is
 * indistinguishable from the button being dead.
 */
function reportUploadFailure(file: File, err: unknown): void {
  const detail = apiErrorMessage(err, "upload failed");
  toast.error(`Could not attach ${file.name}`, { description: detail });
}

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
  /**
   * A paragraph to reveal once the document is on screen — the target of a
   * "copy link to this paragraph" link. Viewer only.
   */
  anchor?: DocAnchor | null;
  /**
   * How `anchor` resolved. The page owns telling the reader when the paragraph
   * has changed or gone: this component knows the answer, but the honest
   * message belongs next to the document, not inside the prose.
   */
  onAnchorResolved?: (match: AnchorMatch) => void;
  /**
   * Right-click on a paragraph in the viewer. Receives the anchor; the page
   * turns it into a URL, because the route is the page's business.
   */
  onCopyAnchor?: (anchor: DocAnchor) => void;
  /**
   * Extra classes for the outermost element. The page uses it to hand the
   * editor a share of the column's height (`flex-1`); how much room there is
   * to fill is the page's business, not this component's.
   */
  className?: string;
}

// The prose classes are shared by the editor and the viewer on purpose: they are
// the same engine in two modes, and the whole reason this unit exists is that
// the previous two renderers were free to drift apart.
const PROSE_CLASS =
  "mesh-doc-prose min-h-[8rem] text-sm text-foreground focus:outline-none";

// ---------------------------------------------------------------------------
// Toolbar
// ---------------------------------------------------------------------------

/**
 * The pointer-over fill for a control in the toolbar.
 *
 * `--secondary`, the palette's light teal-green, with the foreground that ships
 * with it. It used to be `--accent` — a saturated near-black teal — carrying
 * either the inherited body foreground (2.82:1 light, 1.73:1 dark) or the
 * resting `--muted-foreground` (1.21:1 light, 1.64:1 dark). Both are far under
 * the 4.5:1 WCAG AA floor, and "the buttons go dark and I cannot read them" is
 * how it was reported. The pairing here measures 16.31:1 and 11.39:1.
 *
 * The same pairing the Docs tree's selected row was moved to, deliberately: one
 * fix for one defect, not two colours that drift apart. Numbers are computed
 * from brandkit.css by the tests, never eyeballed.
 */
const TOOLBAR_HOVER =
  "text-muted-foreground transition-colors hover:bg-secondary hover:text-secondary-foreground";

/**
 * Held down.
 *
 * `--secondary` is a light tint, so a pressed button cannot be told from a
 * hovered one by fill alone — and darkening the fill is what produced the bug
 * above. So pressing adds a cue instead of changing the colour: a brand-coloured
 * inset ring, 5.78:1 against the fill in light and 6.59:1 in dark, comfortably
 * over the 3:1 WCAG non-text floor. Same move as the tree's selected-row bar.
 *
 * This is the only press feedback these buttons have: `onMouseDown` is
 * preventDefault-ed so the editor keeps its selection, which also means the
 * button never takes focus and never shows a focus ring from the pointer.
 */
const TOOLBAR_PRESSED =
  "active:bg-secondary active:text-secondary-foreground active:ring-1 active:ring-inset active:ring-primary";

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
      className={cn(
        "flex h-7 w-7 items-center justify-center rounded",
        TOOLBAR_HOVER,
        TOOLBAR_PRESSED,
      )}
    >
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Paragraph context menu
// ---------------------------------------------------------------------------

/** Class the revealed paragraph wears; the fade-out lives in the stylesheet. */
const ANCHOR_HIT_CLASS = "mesh-doc-anchor-hit";

function ParagraphMenu({
  x,
  y,
  onCopy,
  onClose,
}: {
  x: number;
  y: number;
  onCopy: () => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({ left: x, top: y });

  // Measure, then keep the menu inside the viewport. Done after mount because
  // the size is not known until it is rendered.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const { width, height } = el.getBoundingClientRect();
    setPosition({
      left: Math.min(x, Math.max(8, window.innerWidth - width - 8)),
      top: Math.min(y, Math.max(8, window.innerHeight - height - 8)),
    });
    el.querySelector("button")?.focus();
  }, [x, y]);

  useEffect(() => {
    const dismiss = () => onClose();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    // `true` on scroll: a menu pinned to viewport coordinates is wrong the
    // moment anything under it moves, and the scroll happens on an inner
    // container that does not bubble.
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    document.addEventListener("mousedown", dismiss);
    document.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
      document.removeEventListener("mousedown", dismiss);
      document.removeEventListener("keydown", onKey);
    };
  }, [onClose]);

  return createPortal(
    <div
      ref={ref}
      role="menu"
      style={{ left: position.left, top: position.top }}
      className="fixed z-50 min-w-[13rem] rounded-md border border-border bg-popover p-1 shadow-md"
      onMouseDown={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        role="menuitem"
        onClick={onCopy}
        // Same pairing as the toolbar, and for the same reason: this item was
        // `text-foreground` over `hover:bg-accent`, the 2.82:1 / 1.73:1 fill
        // that had to be fixed on the tree. One item is still an item you have
        // to read while the pointer is on it.
        className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-foreground hover:bg-secondary hover:text-secondary-foreground"
      >
        <LinkIcon className="h-3.5 w-3.5" />
        Copy link to this paragraph
      </button>
    </div>,
    document.body,
  );
}

// ---------------------------------------------------------------------------
// The editing / viewing surface
// ---------------------------------------------------------------------------

function MilkdownDoc({
  value,
  onChange,
  readOnly,
  documentId,
  anchor,
  onAnchorResolved,
  onCopyAnchor,
  className,
}: DocEditorProps) {
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
        // Caught per file rather than around the loop: Milkdown's upload plugin
        // treats a rejected uploader as "log it and walk away", which strands
        // its placeholder decoration in the document. Answering with the files
        // that did succeed keeps the plugin's own bookkeeping intact, and the
        // toast is what stops a failure from being silent.
        let url: string;
        try {
          url = await uploadTarget(file);
        } catch (err) {
          reportUploadFailure(file, err);
          continue;
        }
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

  // ---- Paragraph anchors --------------------------------------------------
  // The editor is the only place that knows which DOM element is which
  // top-level block, so making and resolving anchors lives here. Everything
  // about *what an anchor means* is in lib/docs/anchor.ts and is tested there
  // without a browser.

  const [menu, setMenu] = useState<{ x: number; y: number; index: number } | null>(
    null,
  );
  const highlightedRef = useRef<HTMLElement | null>(null);
  const onAnchorResolvedRef = useRef(onAnchorResolved);
  onAnchorResolvedRef.current = onAnchorResolved;

  const withView = useCallback(
    <T,>(fn: (view: EditorView) => T): T | null => {
      const editor = getInstance();
      if (!editor) return null;
      return editor.action((ctx) => fn(ctx.get(editorViewCtx))) as T;
    },
    [getInstance],
  );

  // The text projection the anchor module works in: one entry per top-level
  // block, in document order — the same order as view.dom.children, which is
  // what lets an index name both a block and an element.
  const blocksFromView = (view: EditorView) => {
    const texts: string[] = [];
    view.state.doc.forEach((node) => {
      texts.push(node.textContent);
    });
    return buildBlocks(texts);
  };

  const anchorsEnabled = !!readOnly && !!onCopyAnchor;

  const handleContextMenu = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      if (!anchorsEnabled) return;
      const target = e.target as HTMLElement;
      // Links and images keep their own menu — "open in new tab" and "save
      // image" are the reason a reader right-clicks those, and taking them away
      // to offer one extra item is a bad trade.
      if (target.closest("a, img")) return;

      const index = withView((view) => {
        const root = view.dom as HTMLElement;
        let el: HTMLElement | null = target;
        while (el && el !== root && el.parentElement !== root) {
          el = el.parentElement;
        }
        if (!el || el === root || el.parentElement !== root) return null;
        const i = Array.prototype.indexOf.call(root.children, el);
        return i >= 0 ? i : null;
      });
      if (index === null || index === undefined) return;

      e.preventDefault();
      setMenu({ x: e.clientX, y: e.clientY, index });
    },
    [anchorsEnabled, withView],
  );

  const copyAnchor = useCallback(() => {
    const index = menu?.index;
    setMenu(null);
    if (index === undefined || !onCopyAnchor) return;
    const built = withView((view) => makeAnchor(blocksFromView(view), index));
    if (built) onCopyAnchor(built);
  }, [menu, onCopyAnchor, withView]);

  // Reveal the linked paragraph. Keyed on the encoded anchor rather than the
  // object so that an unchanged link does not re-scroll on every render.
  const anchorKey = anchor ? encodeAnchor(anchor) : null;
  useEffect(() => {
    if (loading || !anchor || !readOnly) return;

    // One frame: ProseMirror has the document, but the browser has not laid it
    // out yet, and scrollIntoView before layout scrolls to the wrong place.
    const frame = requestAnimationFrame(() => {
      const found = withView((view) => {
        const match = resolveAnchor(blocksFromView(view), anchor);
        const child =
          match.index === null ? null : view.dom.children[match.index];
        return {
          match,
          el: child instanceof HTMLElement ? child : null,
        };
      });
      if (!found) return;

      onAnchorResolvedRef.current?.(found.match);
      if (!found.el) return;

      highlightedRef.current?.classList.remove(ANCHOR_HIT_CLASS);
      found.el.scrollIntoView({ block: "center", behavior: "smooth" });
      found.el.classList.add(ANCHOR_HIT_CLASS);
      highlightedRef.current = found.el;
    });

    return () => cancelAnimationFrame(frame);
    // `value` is here because the body arrives after the first render: the
    // document loads empty and is filled in once the fetch lands.
  }, [anchorKey, anchor, loading, readOnly, value, withView]);

  // The highlight is a transient, so it must not outlive the view that owns it.
  useEffect(() => {
    return () => {
      highlightedRef.current?.classList.remove(ANCHOR_HIT_CLASS);
      highlightedRef.current = null;
    };
  }, []);

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
          let url: string;
          try {
            url = await uploadTarget(file);
          } catch (err) {
            // One bad file does not cancel the rest of the selection, and the
            // user hears about it either way.
            reportUploadFailure(file, err);
            continue;
          }
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
        // Both modes are a flex column so the prose can be told to fill the
        // height the page hands down; only the editor draws a box around it.
        "flex flex-col",
        !readOnly &&
          "rounded-lg border border-input bg-background focus-within:ring-2 focus-within:ring-ring",
        className,
      )}
    >
      {/* The toolbar follows the reader down a long document.

          `position: sticky` and NOT an inner scrolling container. The editor
          deliberately grows and lets the page column do the scrolling, because
          the inline-comment affordance is positioned from selection rects minus
          this container's bounding rect with no `scrollTop` term — a scrollport
          anywhere inside it would drift the affordance away from its own text
          by however far the reader had scrolled. Sticky needs no such thing: it
          resolves against the page column's scrollport, which already exists,
          and leaves the scroll structure exactly as it was.

          Consequences of sticking, both required: an opaque `bg-background`, or
          the prose would scroll visibly through the bar, and a stacking order
          above the flowing content it now overlaps. The link form lives inside
          the same sticky box rather than under it — it is opened from the
          toolbar and closes onto the toolbar, so it must not be the one part
          that scrolls away. */}
      {!readOnly && (
        <div
          data-doc-toolbar=""
          className="sticky top-0 z-10 shrink-0 rounded-t-lg bg-background"
        >
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

          {linkOpen && (
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
                className={cn(TOOLBAR_HOVER, TOOLBAR_PRESSED, "h-7 rounded px-2 text-xs")}
              >
                Apply
              </button>
            </form>
          )}
        </div>
      )}

      {/* `mesh-doc-editor-body` carries the fill down to the contenteditable
          across the two wrapper elements Milkdown renders for itself, which
          take no className from us — see doc-editor.css. No `min-h-0`: this box
          must be free to grow past its share, or a long document would be
          clipped instead of lengthening the page. */}
      <div
        ref={containerRef}
        onClick={handleClick}
        onContextMenu={handleContextMenu}
        className={cn("mesh-doc-editor-body flex flex-1 flex-col", !readOnly && "px-3 py-2")}
      >
        <Milkdown />
      </div>

      {menu && (
        <ParagraphMenu
          x={menu.x}
          y={menu.y}
          onCopy={copyAnchor}
          onClose={() => setMenu(null)}
        />
      )}

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
  anchor,
  onAnchorResolved,
  onCopyAnchor,
  className,
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
        className={className}
        anchor={anchor}
        onAnchorResolved={onAnchorResolved}
        onCopyAnchor={onCopyAnchor}
      />
    </MilkdownProvider>
  );
}
