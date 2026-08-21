import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  Bold,
  BookOpen,
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
import type { EditorView } from "@milkdown/kit/prose/view";
import { editorViewCtx } from "@milkdown/kit/core";
import {
  createCodeBlockCommand,
  toggleEmphasisCommand,
  toggleInlineCodeCommand,
  toggleLinkCommand,
  toggleStrongCommand,
  wrapInBlockquoteCommand,
  wrapInBulletListCommand,
  wrapInOrderedListCommand,
} from "@milkdown/kit/preset/commonmark";
import { insertTableCommand } from "@milkdown/kit/preset/gfm";
import { upload, uploadConfig } from "@milkdown/kit/plugin/upload";
import { callCommand, replaceAll } from "@milkdown/kit/utils";
import { Milkdown, MilkdownProvider, useEditor, useInstance } from "@milkdown/react";
import { cn } from "@/lib/cn";
import { toast } from "@/components/ui/toast";
import { RelayDocPicker } from "@/components/RelayDocPicker";
import { DocLinkMenu } from "@/components/doc-link-menu";
import { MentionMenu, type Mentionable } from "@/components/mention-menu";
import { useProjectTrIntegration } from "@/hooks/useProjectTrIntegration";
import {
  artifactDownloadPath,
  handleArtifactLinkClick,
  resolveArtifactImages,
} from "@/lib/artifact-links";
import {
  type PendingImage,
  pendingImageHref,
  uploadArtifact,
} from "@/lib/task-artifacts";
import { makeDocEditor } from "@/lib/milkdown/editor";
import {
  type Suggestion,
  caretCoords,
  findSuggestion,
  replaceWithLink,
  replaceWithText,
} from "@/lib/milkdown/suggestion";
import { useDocSuggestions } from "@/hooks/use-doc-suggestions";
import { useMentionSuggestions } from "@/hooks/use-mention-suggestions";
import { documentHref, linkLabel } from "@/lib/docs/doc-link";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import "@/components/doc-editor.css";

/**
 * The editor for task descriptions and comments.
 *
 * Same engine as the Docs editor — one markdown implementation, which is the
 * point of the unit this belongs to. What differs is ownership and affordances,
 * not dialect: uploads here become artifacts of a TASK rather than attachments
 * of a document, and the two inline menus (`[[` for documents, `@` for people)
 * belong to this surface.
 *
 * It replaces three overlapping textarea editors — MarkdownEditor,
 * DescriptionEditor and the bare Textarea in the comment box — which between
 * them carried two separately hand-written markdown parsers. Those parsers
 * disagreed with each other and with the Docs renderer about what markdown
 * means; none of the three understood tables, task lists or nested lists.
 */

const PROSE_CLASS = "mesh-doc-prose text-sm text-foreground focus:outline-none";

/**
 * Tell the user an upload failed, by name.
 *
 * Named rather than echoing the server's message alone: a file selection or a
 * drop can hold several files, and "insufficient permissions" on its own does
 * not say which one was refused. The same shape as artifact-list.tsx and
 * lib/pending-images.ts, which is the shape PR #655 settled on when it made
 * upload failures visible across the three sites that had each swallowed them
 * differently. Nothing is written into the document — a failed upload must
 * leave the writer's text exactly as they wrote it.
 */
function reportUploadFailure(file: File, err: unknown): void {
  toast.error(`Could not attach ${file.name}`, {
    description: err instanceof Error ? err.message : "upload failed",
  });
}

/**
 * Idle/hover. `--accent`/`--foreground` measured 2.82:1 (light) / 1.73:1
 * (dark) — near-invisible on dark. `--secondary`/`--secondary-foreground`
 * measures 16.31:1 / 11.39:1. Same pairing as the doc-editor toolbar fix
 * (#630) — one fix for one defect, not two colours that drift apart.
 */
const TOOLBAR_HOVER =
  "transition-colors hover:bg-secondary hover:text-secondary-foreground";

/**
 * Held down. `--secondary` is a light tint, so a pressed button can't be told
 * from a hovered one by fill alone — darkening the fill is the bug above.
 * Pressing adds a cue instead: a brand-coloured inset ring, 5.78:1 (light) /
 * 6.59:1 (dark) against the fill, clearing the 3:1 WCAG non-text floor.
 */
const TOOLBAR_PRESSED =
  "active:bg-secondary active:text-secondary-foreground active:ring-1 active:ring-inset active:ring-primary";

function ToolbarButton({
  title,
  onClick,
  disabled,
  children,
}: {
  title: string;
  onClick: () => void;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      title={title}
      disabled={disabled}
      // The editor loses its selection to a focus change, and the command then
      // has nothing to apply to.
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className={cn(
        "flex h-7 w-7 items-center justify-center rounded text-muted-foreground",
        TOOLBAR_HOVER,
        TOOLBAR_PRESSED,
        "disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted-foreground",
      )}
    >
      {children}
    </button>
  );
}

export interface RichTextEditorProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  /** Project the surrounding task belongs to — enables the `[[` document menu. */
  projId?: string;
  /** Task that owns uploaded files. Without it the upload buttons are dropped. */
  taskId?: string;
  /** Workspace, for the `@` menu. Falls back to the current workspace. */
  wsId?: string;
  /**
   * Called for an image pasted or dropped before the task exists. Without a
   * task to own the bytes the editor inserts a placeholder and hands the file
   * to the caller, which uploads it once there is somewhere to put it.
   */
  onPendingImage?: (pending: PendingImage) => void;
  /** Rendered under the box. Null drops the footer entirely. */
  hint?: string | null;
  /** Minimum height of the writing area. */
  minHeight?: string;
  onKeyDown?: (e: React.KeyboardEvent) => void;
}

function RichTextEditorInner({
  value,
  onChange,
  placeholder,
  className,
  projId,
  taskId,
  onPendingImage,
  hint,
  minHeight = "7rem",
  onKeyDown,
}: RichTextEditorProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  // Read by the editor factory, which is created once and must not close over
  // a stale first render.
  const valueRef = useRef(value);
  valueRef.current = value;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  // The last markdown the editor and this component agree on — see doc-editor:
  // it distinguishes a change that came from inside the editor from one pushed
  // in from outside.
  const syncedRef = useRef(value);
  const applyingRef = useRef(false);

  const [uploading, setUploading] = useState(false);
  const [linkOpen, setLinkOpen] = useState(false);
  const [linkHref, setLinkHref] = useState("");
  const [relayOpen, setRelayOpen] = useState(false);
  const [suggestion, setSuggestion] = useState<Suggestion | null>(null);
  const [menuAt, setMenuAt] = useState<{ left: number; top: number } | null>(null);
  const [activeIndex, setActiveIndex] = useState(0);

  const { enabled: hasTrIntegration } = useProjectTrIntegration(projId);
  const projects = useProjectStore((s) => s.projects);
  const currentProject = useProjectStore((s) => s.currentProject);
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);

  const uploadTarget = useMemo(() => {
    if (!taskId) return null;
    return async (file: File) => {
      const artifact = await uploadArtifact(taskId, file);
      return artifactDownloadPath(artifact.id);
    };
  }, [taskId]);

  const onPendingImageRef = useRef(onPendingImage);
  onPendingImageRef.current = onPendingImage;

  // The upload plugin is also what handles paste and drop, so it is enabled
  // whenever files can be accepted AT ALL — including the create dialog, where
  // they are parked rather than uploaded.
  const uploadsEnabled = !!uploadTarget || !!onPendingImage;

  const buildNodes = useCallback(
    async (files: readonly File[], schema: Schema): Promise<ProseNode[]> => {
      const nodes: ProseNode[] = [];
      for (const file of files) {
        // No task yet: park the file and insert a placeholder the caller can
        // find again by exact string once the task exists.
        if (!uploadTarget) {
          const report = onPendingImageRef.current;
          if (!report || !file.type.startsWith("image/")) continue;
          const href = pendingImageHref();
          const node = schema.nodes.image?.createAndFill({ src: href, alt: file.name });
          if (!node) continue;
          report({ file, placeholder: `![${file.name}](${href})` });
          nodes.push(node);
          continue;
        }
        // Per file, not around the loop: Milkdown's upload plugin treats a
        // rejected uploader as "log it and walk away", stranding its
        // "Upload in progress" decoration in the document forever.
        let url: string;
        try {
          url = await uploadTarget(file);
        } catch (err) {
          reportUploadFailure(file, err);
          continue;
        }
        if (file.type.startsWith("image/")) {
          const node = schema.nodes.image?.createAndFill({ src: url, alt: file.name });
          if (node) nodes.push(node);
        } else if (schema.marks.link) {
          nodes.push(schema.text(file.name, [schema.marks.link.create({ href: url })]));
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
        editable: true,
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
          if (applyingRef.current) return;
          if (markdown === syncedRef.current) return;
          syncedRef.current = markdown;
          onChangeRef.current(markdown);
        },
      }),
    [uploadsEnabled],
  );

  const [loading, getInstance] = useInstance();

  const withView = useCallback(
    <T,>(fn: (view: EditorView) => T): T | null => {
      const editor = getInstance();
      if (!editor) return null;
      return editor.action((ctx) => fn(ctx.get(editorViewCtx))) as T;
    },
    [getInstance],
  );

  // Push an externally-changed value in — the form being reset after a comment
  // is posted, a different task loading into the same panel.
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
  // presigned URL — the same contract the deleted renderers had.
  useEffect(() => {
    if (loading || !containerRef.current) return;
    void resolveArtifactImages(containerRef.current);
  }, [value, loading, uploading]);

  // ---- inline menus -------------------------------------------------------

  const closeSuggestion = useCallback(() => {
    setSuggestion(null);
    setMenuAt(null);
    setActiveIndex(0);
  }, []);

  // Recomputed after every keystroke rather than tracked incrementally: the
  // caret can also move by click, by arrow key and by undo, and a menu that
  // only closes on typing is a menu that inserts into the wrong place.
  const refreshSuggestion = useCallback(() => {
    const next = withView((view) => {
      const found = findSuggestion(view);
      return found ? { found, coords: caretCoords(view, found.from) } : null;
    });
    if (!next) {
      closeSuggestion();
      return;
    }
    setSuggestion((prev) => {
      if (prev?.kind !== next.found.kind || prev.query !== next.found.query) {
        setActiveIndex(0);
      }
      return next.found;
    });
    setMenuAt({ left: next.coords.left, top: next.coords.bottom });
  }, [withView, closeSuggestion]);

  useEffect(() => {
    if (loading) return;
    refreshSuggestion();
    // `value` is the trigger: every document change flows through it.
  }, [value, loading, refreshSuggestion]);

  const docQuery = suggestion?.kind === "doc" ? suggestion.query : null;
  const mentionQuery = suggestion?.kind === "mention" ? suggestion.query : null;

  const docs = useDocSuggestions(projId, docQuery);
  const mentions = useMentionSuggestions(currentWorkspace?.id, mentionQuery);

  const items: readonly unknown[] =
    suggestion?.kind === "doc" ? docs.suggestions : mentions.suggestions;

  const pickDoc = useCallback(
    (hit: { id: string; title: string; relayUrl?: string }) => {
      const range = suggestion;
      if (!range) return;
      closeSuggestion();
      withView((view) => {
        // Re-derived from the live document rather than trusting the range the
        // menu opened with: between opening and choosing, the caret may have
        // moved, and a stale range rewrites text the writer is not looking at.
        const live = findSuggestion(view);
        if (!live || live.kind !== "doc") return;
        if (hit.relayUrl) {
          // A Team Relay document has no route in this app — that is why the
          // relay:// pseudo-scheme exists — so it goes in as the URL the relay
          // renderer recognises, as plain text, since the href allow-list would
          // (correctly) refuse it as a link.
          replaceWithText(view, live, `[${linkLabel(hit.title)}](${hit.relayUrl})`);
          return;
        }
        const wsSlug = currentWorkspace?.slug;
        const project =
          projects.find((p) => p.id === projId) ??
          (currentProject?.id === projId ? currentProject : undefined);
        if (!wsSlug || !project) return;
        replaceWithLink(
          view,
          live,
          linkLabel(hit.title),
          documentHref(wsSlug, project.slug, hit.id),
        );
      });
    },
    [suggestion, closeSuggestion, withView, currentWorkspace, projects, currentProject, projId],
  );

  const pickMention = useCallback(
    (m: Mentionable) => {
      const range = suggestion;
      if (!range) return;
      closeSuggestion();
      withView((view) => {
        const live = findSuggestion(view);
        if (!live || live.kind !== "mention") return;
        replaceWithText(view, live, `@${m.slug}`);
      });
    },
    [suggestion, closeSuggestion, withView],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (suggestion) {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          closeSuggestion();
          return;
        }
        if (items.length > 0) {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setActiveIndex((i) => Math.min(i + 1, items.length - 1));
            return;
          }
          if (e.key === "ArrowUp") {
            e.preventDefault();
            setActiveIndex((i) => Math.max(i - 1, 0));
            return;
          }
          if (e.key === "Enter" || e.key === "Tab") {
            e.preventDefault();
            e.stopPropagation();
            if (suggestion.kind === "doc") {
              const hit = docs.suggestions[activeIndex];
              if (hit) pickDoc(hit);
            } else {
              const m = mentions.suggestions[activeIndex];
              if (m) pickMention(m);
            }
            return;
          }
        }
      }
      onKeyDown?.(e);
    },
    [
      suggestion,
      items.length,
      activeIndex,
      docs.suggestions,
      mentions.suggestions,
      pickDoc,
      pickMention,
      closeSuggestion,
      onKeyDown,
    ],
  );

  // ---- toolbar ------------------------------------------------------------

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
            // user hears about it either way — named, because a selection can
            // hold several files and "insufficient permissions" on its own does
            // not say which one was refused (PR #655, same shape as
            // artifact-list and lib/pending-images).
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

  const submitLink = (e: React.FormEvent) => {
    e.preventDefault();
    runCommand(toggleLinkCommand.key, { href: linkHref });
    setLinkHref("");
    setLinkOpen(false);
  };

  const handleRelaySelect = useCallback(
    (relayUrl: string) => {
      withView((view) => {
        const { state } = view;
        // Its own paragraph, as the textarea version did: the relay renderer
        // splits these out of the text and draws a card, which cannot sit
        // mid-sentence.
        view.dispatch(state.tr.replaceSelectionWith(state.schema.text(relayUrl), false));
        view.focus();
      });
    },
    [withView],
  );

  const handleClick = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    handleArtifactLinkClick(e);
  }, []);

  return (
    <div
      className={cn(
        "flex flex-col rounded-lg border border-input bg-background focus-within:ring-2 focus-within:ring-ring",
        className,
      )}
    >
      <div className="flex flex-wrap items-center gap-0.5 border-b border-border px-2 py-1">
        <ToolbarButton title="Bold (Ctrl+B)" onClick={() => runCommand(toggleStrongCommand.key)}>
          <Bold className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Italic (Ctrl+I)" onClick={() => runCommand(toggleEmphasisCommand.key)}>
          <Italic className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Inline code" onClick={() => runCommand(toggleInlineCodeCommand.key)}>
          <Code className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Link" onClick={() => setLinkOpen((v) => !v)}>
          <LinkIcon className="h-3.5 w-3.5" />
        </ToolbarButton>

        <div className="mx-1 h-3.5 w-px bg-border" />

        <ToolbarButton title="Bullet list" onClick={() => runCommand(wrapInBulletListCommand.key)}>
          <List className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton
          title="Numbered list"
          onClick={() => runCommand(wrapInOrderedListCommand.key)}
        >
          <ListOrdered className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Quote" onClick={() => runCommand(wrapInBlockquoteCommand.key)}>
          <Quote className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Code block" onClick={() => runCommand(createCodeBlockCommand.key)}>
          <SquareCode className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton title="Table" onClick={() => runCommand(insertTableCommand.key)}>
          <TableIcon className="h-3.5 w-3.5" />
        </ToolbarButton>

        <div className="mx-1 h-3.5 w-px bg-border" />

        <ToolbarButton
          title={taskId ? "Insert image" : "Save the task to attach images"}
          disabled={!taskId}
          onClick={() => imageInputRef.current?.click()}
        >
          <ImageIcon className="h-3.5 w-3.5" />
        </ToolbarButton>
        <ToolbarButton
          title={taskId ? "Attach file" : "Save the task to attach files"}
          disabled={!taskId}
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

        {hasTrIntegration && (
          <ToolbarButton title="Attach Obsidian doc" onClick={() => setRelayOpen(true)}>
            <BookOpen className="h-3.5 w-3.5" />
          </ToolbarButton>
        )}

        <div className="flex-1" />
        {uploading && <span className="mr-2 text-xs text-muted-foreground">Uploading...</span>}
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
            className={cn("h-7 rounded px-2 text-xs text-muted-foreground", TOOLBAR_HOVER)}
          >
            Apply
          </button>
        </form>
      )}

      <div
        ref={containerRef}
        onClick={handleClick}
        onKeyDown={handleKeyDown}
        data-placeholder={placeholder}
        style={{ minHeight }}
        className="mesh-doc-editor-body mesh-rich-text-body flex flex-1 flex-col px-3 py-2"
      >
        <Milkdown />
      </div>

      {hint !== null && (
        <div className="border-t border-border px-3 py-1 text-[11px] text-muted-foreground">
          {hint ?? "Rich text — tables, task lists and footnotes supported."}
        </div>
      )}

      {suggestion?.kind === "doc" &&
        menuAt &&
        createPortal(
          <div
            className="fixed z-50"
            style={{ left: menuAt.left, top: menuAt.top }}
            onMouseDown={(e) => e.preventDefault()}
          >
            <DocLinkMenu
              suggestions={docs.suggestions}
              activeIndex={activeIndex}
              onPick={pickDoc}
              onHover={setActiveIndex}
              scope={docs.scope}
              scopes={
                hasTrIntegration
                  ? [
                      { id: "docs", label: "Docs" },
                      { id: "relay", label: "Team Relay" },
                    ]
                  : [{ id: "docs", label: "Docs" }]
              }
              onScope={docs.setScope}
              loading={docs.loading}
            />
          </div>,
          document.body,
        )}

      {suggestion?.kind === "mention" &&
        menuAt &&
        mentions.suggestions.length > 0 &&
        createPortal(
          <div
            className="fixed z-50"
            style={{ left: menuAt.left, top: menuAt.top }}
            onMouseDown={(e) => e.preventDefault()}
          >
            <MentionMenu
              suggestions={mentions.suggestions}
              activeIndex={activeIndex}
              onPick={pickMention}
              onHover={setActiveIndex}
            />
          </div>,
          document.body,
        )}

      {hasTrIntegration && projId && (
        <RelayDocPicker
          projId={projId}
          open={relayOpen}
          onClose={() => setRelayOpen(false)}
          onSelect={handleRelaySelect}
        />
      )}
    </div>
  );
}

/**
 * RichTextEditor — the single seam between task/comment surfaces and the
 * markdown engine.
 *
 * Nothing outside this file may build its own markdown editing surface.
 */
export function RichTextEditor(props: RichTextEditorProps) {
  return (
    <MilkdownProvider>
      <RichTextEditorInner {...props} />
    </MilkdownProvider>
  );
}
