// Core blocks of the `doc.view` slot. They are module-level components that read the page's live
// state from a context, so their identity never changes: a view that draws them (any theme) cannot
// remount the editor by re-rendering, and the page state a keystroke touches stays in the container.
import { createContext, useContext, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Pencil } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DocBreadcrumbs } from "@/components/doc-breadcrumbs";
import { DocEditor } from "@/components/doc-editor";
import { DocMeta } from "@/components/doc-meta";
import { DocWatchToggle } from "@/components/doc-watch-toggle";
import { DocCommentTree } from "@/components/doc-comment-tree";
import {
  DocCommentAffordance,
  DocCommentPageEntry,
  DocCommentRail,
  DocCommentToggle,
} from "@/components/doc-comment-rail";
import type { DocAnchor, AnchorMatch } from "@/lib/docs/anchor";
import type { useDocComments } from "@/lib/doc-comments/use-doc-comments";
import type { ProjectDocument } from "@/types";
import type { DocViewData } from "./types";

export interface DocPageState {
  docId: string;
  docsPath: string;
  openDoc: ProjectDocument;
  documents: ProjectDocument[];
  comments: ReturnType<typeof useDocComments>;
  editing: boolean;
  draft: string;
  renderedDraft: string;
  setDraft: (value: string) => void;
  startEditing: () => void;
  anchor: DocAnchor | null;
  onAnchorResolved: (match: AnchorMatch) => void;
  onCopyAnchor: (anchor: DocAnchor) => void;
  railOpen: boolean;
  isMobile: boolean;
  showResolved: boolean;
  setShowResolved: (next: boolean) => void;
  openRail: () => void;
  toggleRail: () => void;
  navigate: (href: string) => void;
}

const DocPageContext = createContext<DocPageState | null>(null);

export function DocPageProvider({ value, children }: { value: DocPageState; children: ReactNode }) {
  return <DocPageContext.Provider value={value}>{children}</DocPageContext.Provider>;
}

function usePage(): DocPageState {
  const page = useContext(DocPageContext);
  if (!page) throw new Error("doc.view block rendered outside DocPageProvider");
  return page;
}

function EditorBlock() {
  const { t } = useTranslation("docs");
  const p = usePage();
  return (
    // `relative` so the selection affordance can be placed inside it; the ref is what the comment
    // layer measures and reads selections from. Neither touches the DOM ProseMirror owns.
    // `flex-1` with the default `min-height: auto` fills the space that is there and grows when the
    // body needs more.
    <div
      ref={p.comments.containerRef}
      data-testid="doc-measured-body"
      className="relative flex flex-1 flex-col"
    >
      {p.editing ? (
        <DocEditor className="flex-1" value={p.draft} onChange={p.setDraft} documentId={p.docId} />
      ) : p.draft.trim() ? (
        <DocEditor
          className="flex-1"
          value={p.renderedDraft}
          onChange={p.setDraft}
          readOnly
          anchor={p.anchor}
          onAnchorResolved={p.onAnchorResolved}
          onCopyAnchor={p.onCopyAnchor}
        />
      ) : (
        <div className="flex flex-col items-start gap-2">
          <DocEditor value={p.renderedDraft} onChange={p.setDraft} readOnly />
          <Button type="button" size="sm" className="h-7 gap-1 px-2 text-xs" onClick={p.startEditing}>
            <Pencil className="h-3.5 w-3.5" />
            {t("empty.startWriting")}
          </Button>
        </div>
      )}
      <DocCommentAffordance controller={p.comments} />
    </div>
  );
}

function CommentsBlock() {
  const p = usePage();
  // Outside the measured body on purpose: that element is flattened into "the text of this
  // document" to locate each quote, so a discussion inside it would become part of the haystack.
  // Always mounted, rail open or closed: it carries the composer that lives
  // permanently under the document.
  return (
    <DocCommentTree
      controller={p.comments}
      showResolved={p.showResolved}
      onShowResolvedChange={p.setShowResolved}
      revealWhenActive={!(p.railOpen && !p.isMobile)}
    />
  );
}

function CommentsRailBlock() {
  const p = usePage();
  return (
    <DocCommentRail controller={p.comments} showResolved={p.showResolved} onShowResolvedChange={p.setShowResolved} />
  );
}

function CommentsToggleBlock() {
  const p = usePage();
  // Hidden below `lg`, where the rail itself never renders: a visible control for a surface that
  // cannot open is worse than no control at all (AC2).
  if (p.isMobile) return null;
  return <DocCommentToggle controller={p.comments} open={p.railOpen} onToggle={p.toggleRail} />;
}

function CommentsEntryBlock() {
  const p = usePage();
  // Not gated on the rail being open: this entry point works whether or not it is.
  return <DocCommentPageEntry controller={p.comments} onOpenRail={p.openRail} />;
}

function WatchToggleBlock() {
  return <DocWatchToggle documentId={usePage().openDoc.id} />;
}

function BreadcrumbsBlock() {
  const p = usePage();
  return (
    <DocBreadcrumbs
      documents={p.documents}
      current={p.openDoc}
      onNavigateRoot={() => p.navigate(p.docsPath)}
      onNavigate={(doc) => p.navigate(`${p.docsPath}/${doc.id}`)}
    />
  );
}

function MetaBlock() {
  return <DocMeta doc={usePage().openDoc} className="mt-1" />;
}

// The document's properties are drawn by the editor today (it splits its own frontmatter).
function PropertiesBlock() {
  return null;
}

export const DOC_VIEW_BLOCKS: DocViewData["components"] = {
  Editor: EditorBlock,
  Properties: PropertiesBlock,
  Comments: CommentsBlock,
  WatchToggle: WatchToggleBlock,
  Breadcrumbs: BreadcrumbsBlock,
  CommentsRail: CommentsRailBlock,
  CommentsToggle: CommentsToggleBlock,
  CommentsEntry: CommentsEntryBlock,
  Meta: MetaBlock,
};
