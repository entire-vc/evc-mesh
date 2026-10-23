import { AgentShortTag } from "@/components/agent-short-tag";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import {
  Bot,
  Check,
  CornerDownRight,
  Link2Off,
  MessageSquare,
  MessageSquarePlus,
  Pencil,
  RotateCcw,
  Trash2,
  User,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { MentionPickerMenu } from "@/components/mention-menu";
import { MentionText } from "@/components/mention-text";
import { useMentionPicker } from "@/hooks/use-mention-picker";
import { useMentionDirectory } from "@/hooks/use-mention-directory";
import { useWorkspaceStore } from "@/stores/workspace";
import { apiErrorMessage } from "@/lib/api-error";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { DRAFT_MARK_ID } from "@/lib/doc-comments/use-doc-comments";
import type {
  DocCommentsController,
  PlacedThread,
  ThreadPlacement,
} from "@/lib/doc-comments/use-doc-comments";
import type { ActorType, DocumentComment } from "@/types";

/** What the body renderer needs to tell a mention from an @ in prose. */
type MentionDirectory = ReturnType<typeof useMentionDirectory>;
import "@/components/doc-comments.css";
import { useTranslation } from "react-i18next";

// ---------------------------------------------------------------------------
// Small pieces
// ---------------------------------------------------------------------------

function ActorLabel({ id, type, name }: { id?: string; type: ActorType; name?: string | null }) {
  const { t } = useTranslation("docs");
  const Icon = type === "agent" ? Bot : User;
  const fallback = type === "agent" ? t("comments.actor.agent") : type === "system" ? t("comments.actor.system") : t("comments.actor.user");
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-xs font-medium">
      <Icon
        className={cn(
          "h-3.5 w-3.5 shrink-0",
          type === "agent" ? "text-tone-violet-500" : "text-tone-sky-500",
        )}
      />
      <span className="truncate">{name?.trim() || fallback}<AgentShortTag id={id} type={type} /></span>
    </span>
  );
}

/**
 * The quote the thread was written about.
 *
 * Shown for every anchored thread, not only the broken ones: the rail is beside
 * the document, not inside it, and "this is the sentence I meant" is the whole
 * content of an inline comment's context.
 */
function Quote({
  text,
  placement,
}: {
  text: string;
  placement: ThreadPlacement;
}) {
  const broken = placement === "orphaned" || placement === "detached";
  return (
    <blockquote
      className={cn(
        "mb-2 border-l-2 pl-2 text-xs italic",
        broken
          ? "border-muted-foreground/40 text-muted-foreground line-through decoration-muted-foreground/50"
          : "border-tone-attention-400 text-muted-foreground",
      )}
    >
      {text}
    </blockquote>
  );
}

/**
 * Says, in the rail, that a thread has lost its place in the text.
 *
 * These are deliberately loud rather than hidden. A thread nobody can find is
 * still a thread somebody wrote, and dropping it silently — which is what
 * filtering it out would be — loses the discussion along with the position.
 */
function PlacementNotice({ placement }: { placement: ThreadPlacement }) {
  const { t } = useTranslation("docs");
  if (placement === "anchored") return null;

  const text =
    placement === "page"
      ? t("comments.placement.page")
      : placement === "orphaned"
        ? t("comments.placement.orphaned")
        : t("comments.placement.detached");

  return (
    <p className="mb-2 flex items-start gap-1.5 text-[11px] text-muted-foreground">
      <Link2Off className="mt-0.5 h-3 w-3 shrink-0" />
      <span>{text}</span>
    </p>
  );
}

// ---------------------------------------------------------------------------
// Composer
// ---------------------------------------------------------------------------

const REVEAL_DELAY_MS = 250;

export function Composer({
  placeholder,
  initialValue = "",
  submitLabel,
  autoFocus,
  onSubmit,
  onCancel,
}: {
  placeholder: string;
  initialValue?: string;
  submitLabel: string;
  autoFocus?: boolean;
  onSubmit: (body: string) => Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useTranslation("docs");
  const [value, setValue] = useState(initialValue);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  // Wired here rather than at each of the three call sites, because all three —
  // a new thread, a reply and an edit — go through this one component.
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);
  const mentions = useMentionPicker(currentWorkspace?.id, value, setValue, ref);

  useEffect(() => {
    // preventScroll: a new draft card is laid out at top 0 until the rail has
    // measured its mark, and a plain focus() would scroll the document to it.
    if (!autoFocus) return;
    ref.current?.focus({ preventScroll: true });
    // Once the rail has placed the card, bring the whole form — buttons
    // included — into view; "nearest" is a no-op when it already is. Rail cards
    // animate `top` for 150ms (AlignedItems), and until that ends the card is
    // still near the top of the rail, so "nearest" would scroll up to it.
    const timer = window.setTimeout(() => {
      rootRef.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
    }, REVEAL_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [autoFocus]);

  const submit = async () => {
    const body = value.trim();
    if (!body || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit(body);
      setValue("");
    } catch (err) {
      // The field detail, not the generic "Validation failed" the server puts
      // in `message`: a refused @-mention names the slug that resolved to
      // nobody and says how to write an @ that is not a mention, and that
      // sentence is the entire remedy for the failure this feature exists to
      // make visible.
      setError(apiErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // The mention menu gets the key first when it is open, so Enter picks a name
    // instead of submitting and Escape closes the menu instead of throwing away
    // the draft. It returns false for every key it did not handle.
    if (mentions.onKeyDown(e)) return;
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      void submit();
    }
    if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    }
  };

  return (
    <div ref={rootRef} className="relative space-y-2">
      <MentionPickerMenu picker={mentions} />
      <Textarea
        ref={ref}
        rows={3}
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          setValue(e.target.value);
          mentions.onValueChange(
            e.target.value,
            e.target.selectionStart ?? e.target.value.length,
          );
        }}
        onKeyDown={onKeyDown}
        className="min-h-[64px] text-sm"
      />
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div className="flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          className="h-7 px-2 text-xs"
          disabled={!value.trim() || busy}
          onClick={() => void submit()}
        >
          {busy ? t("save.saving") : submitLabel}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs"
          onClick={onCancel}
          disabled={busy}
        >
          {t("actions.cancel")}
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// One comment
// ---------------------------------------------------------------------------

function CommentBody({
  comment,
  controller,
  directory,
  isReply,
}: {
  comment: DocumentComment;
  controller: DocCommentsController;
  directory: MentionDirectory;
  isReply?: boolean;
}) {
  const { t } = useTranslation("docs");
  const [editing, setEditing] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const mine = controller.canModify(comment);
  const edited = comment.updated_at !== comment.created_at;

  return (
    <div className={cn(isReply && "mt-2 border-l-2 border-border pl-2.5")}>
      <div className="flex items-baseline justify-between gap-2">
        <ActorLabel id={comment.author_id} type={comment.author_type} name={comment.author_name} />
        <span className="shrink-0 text-[11px] text-muted-foreground">
          {formatRelative(comment.created_at)}
          {edited && t("comments.edited")}
        </span>
      </div>

      {editing ? (
        <div className="mt-1.5">
          <Composer
            placeholder={t("comments.editPlaceholder")}
            initialValue={comment.body}
            submitLabel={t("dialog.save")}
            autoFocus
            onSubmit={async (body) => {
              await controller.edit(comment.id, body);
              setEditing(false);
            }}
            onCancel={() => setEditing(false)}
          />
        </div>
      ) : (
        <MentionText
          text={comment.body}
          mentionables={directory.mentionables}
          wsSlug={directory.wsSlug}
          className="mt-1 block whitespace-pre-wrap break-words text-sm"
        />
      )}

      {/* Edit and delete are author-only — the server refuses them for anybody
          else, so offering them would be offering a 403. */}
      {mine && !editing && (
        <div className="mt-1 flex items-center gap-1">
          {confirmingDelete ? (
            <>
              <span className="text-[11px] text-muted-foreground">{t("comments.deleteConfirm")}</span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 px-1.5 text-[11px] text-destructive"
                onClick={() => void controller.remove(comment.id)}
              >
                {t("comments.yes")}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 px-1.5 text-[11px]"
                onClick={() => setConfirmingDelete(false)}
              >
                {t("comments.no")}
              </Button>
            </>
          ) : (
            <>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
                onClick={() => setEditing(true)}
                disabled={controller.readOnly}
              >
                <Pencil className="h-3 w-3" />
                {t("actions.edit")}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
                onClick={() => setConfirmingDelete(true)}
                disabled={controller.readOnly}
              >
                <Trash2 className="h-3 w-3" />
                {t("actions.delete")}
              </Button>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One thread
// ---------------------------------------------------------------------------

export function Thread({
  thread,
  controller,
  directory,
  revealWhenActive = true,
}: {
  thread: PlacedThread;
  controller: DocCommentsController;
  directory: MentionDirectory;
  /** The rail aligns cards to their marks itself; revealing would fight that scroll. */
  revealWhenActive?: boolean;
}) {
  const { t } = useTranslation("docs");
  const [replying, setReplying] = useState(false);
  const ref = useRef<HTMLElement>(null);
  const active = controller.activeThreadId === thread.root.id;

  useEffect(() => {
    // Not when the click came from a card: the duplicate in the other surface
    // would scroll itself and fight the document's scroll to the mark.
    if (active && revealWhenActive && controller.activeSource !== "card") {
      ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }, [active, revealWhenActive, controller.activeSource]);

  return (
    <article
      ref={ref}
      data-testid="doc-comment-thread"
      data-thread-id={thread.root.id}
      data-placement={thread.placement}
      data-resolved={thread.resolved ? "true" : "false"}
      onClick={(e) => {
        controller.focusThread(thread.root.id, "card");
        // Reply / edit / resolve / delete act on the card; they must not also
        // move the document away from where the reader is working.
        if ((e.target as HTMLElement).closest("button, a, input, textarea")) return;
        controller.scrollToMark(thread.root.id);
      }}
      className={cn(
        "cursor-default rounded-lg border p-3 transition-colors",
        active ? "border-tone-attention-400 bg-tone-attention-50/60 dark:bg-tone-attention-400/10" : "border-border",
        thread.resolved && "opacity-70",
      )}
    >
      {thread.resolved && (
        <p className="mb-2 flex items-center gap-1.5 text-[11px] font-medium text-success">
          <Check className="h-3 w-3" />
          {t("comments.resolved")}
          {thread.root.resolved_by_name ? t("comments.resolvedBy", { name: thread.root.resolved_by_name }) : ""}
        </p>
      )}

      <PlacementNotice placement={thread.placement} />
      {thread.root.anchor?.exact && (
        <Quote text={thread.root.anchor.exact} placement={thread.placement} />
      )}

      <CommentBody comment={thread.root} controller={controller} directory={directory} />

      {thread.replies.map((reply) => (
        <CommentBody
          key={reply.id}
          comment={reply}
          controller={controller}
          directory={directory}
          isReply
        />
      ))}

      {!controller.readOnly && (
        <div className="mt-2 flex items-center gap-1">
          {replying ? null : (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
              onClick={() => setReplying(true)}
            >
              <CornerDownRight className="h-3 w-3" />
              {t("comments.reply")}
            </Button>
          )}
          {/* Resolve is open to any workspace member — putting a thread away is
              part of taking part in it, not an ownership power. */}
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
            onClick={() => void controller.setResolved(thread.root.id, !thread.resolved)}
          >
            {thread.resolved ? (
              <>
                <RotateCcw className="h-3 w-3" />
                {t("comments.unresolve")}
              </>
            ) : (
              <>
                <Check className="h-3 w-3" />
                {t("comments.resolve")}
              </>
            )}
          </Button>
        </div>
      )}

      {replying && (
        <div className="mt-2">
          <Composer
            placeholder={t("comments.replyPlaceholder")}
            submitLabel={t("comments.reply")}
            autoFocus
            onSubmit={async (body) => {
              await controller.reply(thread.root.id, body);
              setReplying(false);
            }}
            onCancel={() => setReplying(false)}
          />
        </div>
      )}
    </article>
  );
}

// ---------------------------------------------------------------------------
// The unsaved draft
// ---------------------------------------------------------------------------

/** The quote and composer for a comment that has been started but not saved. */
export function DraftCard({
  controller,
  draft,
}: {
  controller: DocCommentsController;
  draft: NonNullable<DocCommentsController["draft"]>;
}) {
  const { t } = useTranslation("docs");
  return (
    <div className="rounded-lg border border-tone-attention-400 bg-tone-attention-50/60 p-3 dark:bg-tone-attention-400/10">
      {draft.anchor ? (
        <>
          <Quote
            text={draft.anchor.exact}
            placement={draft.unplaceable ? "orphaned" : "anchored"}
          />
          {draft.unplaceable && (
            <p className="mb-2 flex items-start gap-1.5 text-[11px] text-muted-foreground">
              <Link2Off className="mt-0.5 h-3 w-3 shrink-0" />
              <span>
                {t("comments.unplaceable")}
              </span>
            </p>
          )}
        </>
      ) : (
        // No quote to show — this draft has no anchor at all. Reuses the
        // same wording a page-placed thread already carries once saved
        // (see PlacementNotice), so the composer doesn't introduce new
        // copy of its own.
        <PlacementNotice placement="page" />
      )}
      <Composer
        placeholder={t("comments.addPlaceholder")}
        submitLabel={t("comments.submit")}
        autoFocus
        onSubmit={controller.submitDraft}
        onCancel={controller.cancelDraft}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Alignment: each card sits level with its mark
// ---------------------------------------------------------------------------

interface AlignedItem {
  /** A thread root id, or DRAFT_MARK_ID. */
  id: string;
  node: ReactNode;
}

const ITEM_GAP = 8;

/**
 * Lays the cards out in one absolutely-positioned column, each at the height of
 * its mark in the text.
 *
 * The document and the rail live in the same scroller, so a card that is level
 * with its mark stays level while the page scrolls — there is nothing to sync.
 * Marks that sit close together would overlap their cards; those are pushed down
 * in document order. Cards with no mark (page-level, orphaned) follow the last
 * placed one. Heights are measured, so an opened composer or a long reply moves
 * the cards below it.
 */
function AlignedItems({
  controller,
  items,
}: {
  controller: DocCommentsController;
  items: AlignedItem[];
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const cardRefs = useRef(new Map<string, HTMLDivElement>());
  const [layout, setLayout] = useState<{ tops: Record<string, number>; height: number }>({
    tops: {},
    height: 0,
  });
  const { getMarkRect, layoutTick, activeThreadId } = controller;

  const measure = useCallback(() => {
    const host = hostRef.current;
    if (!host) return;
    const hostTop = host.getBoundingClientRect().top;
    const entries = items.map((item, order) => {
      const rect = getMarkRect(item.id);
      return {
        id: item.id,
        order,
        desired: rect ? rect.top - hostTop : null,
        height: cardRefs.current.get(item.id)?.offsetHeight ?? 0,
      };
    });
    const marked = entries
      .filter((e) => e.desired !== null)
      .sort((a, b) => (a.desired as number) - (b.desired as number) || a.order - b.order);
    const unmarked = entries.filter((e) => e.desired === null);
    const tops: Record<string, number> = {};
    let cursor = 0;
    for (const e of [...marked, ...unmarked]) {
      const top = Math.max(e.desired ?? cursor, cursor);
      tops[e.id] = top;
      cursor = top + e.height + ITEM_GAP;
    }
    const height = Math.max(0, cursor - ITEM_GAP);
    setLayout((prev) => {
      const same =
        prev.height === height &&
        Object.keys(tops).length === Object.keys(prev.tops).length &&
        Object.keys(tops).every((k) => prev.tops[k] === tops[k]);
      return same ? prev : { tops, height };
    });
  }, [items, getMarkRect]);

  // Every render: items are rebuilt each time, and measure() bails out when
  // nothing moved, so this settles after one pass.
  useLayoutEffect(() => {
    measure();
  }, [measure, layoutTick, activeThreadId]);

  useEffect(() => {
    const onResize = () => measure();
    window.addEventListener("resize", onResize);
    let ro: ResizeObserver | null = null;
    if (typeof ResizeObserver !== "undefined") {
      ro = new ResizeObserver(onResize);
      cardRefs.current.forEach((el) => ro?.observe(el));
      const article = hostRef.current?.closest("[data-testid='doc-comment-rail']")
        ?.parentElement?.querySelector("article");
      if (article) ro.observe(article);
    }
    return () => {
      window.removeEventListener("resize", onResize);
      ro?.disconnect();
    };
  }, [measure, items.length]);

  return (
    <div
      ref={hostRef}
      data-testid="doc-comment-aligned"
      className="relative pb-8"
      style={{ height: layout.height + 32 }}
    >
      {items.map((item) => (
        <div
          key={item.id}
          ref={(el) => {
            if (el) cardRefs.current.set(item.id, el);
            else cardRefs.current.delete(item.id);
          }}
          data-aligned-id={item.id}
          className="absolute inset-x-0 transition-[top] duration-150"
          style={{ top: layout.tops[item.id] ?? 0 }}
        >
          {item.node}
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// The rail
// ---------------------------------------------------------------------------

export interface DocCommentRailProps {
  controller: DocCommentsController;
  showResolved: boolean;
  onShowResolvedChange: (next: boolean) => void;
}

export function DocCommentRail({
  controller,
  showResolved,
  onShowResolvedChange,
}: DocCommentRailProps) {
  const { t } = useTranslation("docs");
  const { threads, draft, isLoading, error } = controller;
  // Once for the whole rail, not once per comment: fetchTeamDirectory is
  // unconditional, so a hook inside CommentBody would issue one request per
  // comment on screen.
  const directory = useMentionDirectory();
  const visible = showResolved ? threads : threads.filter((t) => !t.resolved);
  const resolvedCount = threads.length - threads.filter((t) => !t.resolved).length;

  const items: AlignedItem[] = [];
  if (draft) {
    items.push({ id: DRAFT_MARK_ID, node: <DraftCard controller={controller} draft={draft} /> });
  }
  for (const thread of visible) {
    items.push({
      id: thread.root.id,
      node: (
        <Thread
          thread={thread}
          controller={controller}
          directory={directory}
          revealWhenActive={false}
        />
      ),
    });
  }

  return (
    <aside
      aria-label={t("comments.title")}
      data-testid="doc-comment-rail"
      className="w-full shrink-0 border-t border-border pt-4 lg:w-80 lg:border-l lg:border-t-0 lg:pl-5 lg:pt-0"
    >
      <div className="flex items-center justify-between gap-2 pb-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {t("comments.title")}
        </h2>
        {resolvedCount > 0 && (
          <label className="flex cursor-pointer items-center gap-1.5 text-[11px] text-muted-foreground">
            <input
              type="checkbox"
              className="h-3 w-3 accent-current"
              checked={showResolved}
              onChange={(e) => onShowResolvedChange(e.target.checked)}
            />
            {t("comments.showResolved", { count: resolvedCount })}
          </label>
        )}
      </div>

      {controller.readOnly && (
        <p className="mb-3 rounded-md bg-muted px-2.5 py-2 text-[11px] text-muted-foreground">
          {t("comments.readOnly")}
        </p>
      )}

      {error && <p className="mb-2 text-xs text-destructive">{error}</p>}
      {error && !draft && threads.length === 0 ? null : isLoading && threads.length === 0 && !draft ? (
        <p className="text-xs text-muted-foreground">{t("comments.loading")}</p>
      ) : visible.length === 0 && !draft ? (
        <div className="flex flex-col items-start gap-1 text-xs text-muted-foreground">
          <MessageSquare className="h-5 w-5" />
          <p>
            {threads.length === 0
              ? t("comments.empty")
              : t("comments.noOpen")}
          </p>
        </div>
      ) : (
        <AlignedItems controller={controller} items={items} />
      )}
    </aside>
  );
}

// ---------------------------------------------------------------------------
// The affordance beside the selection
// ---------------------------------------------------------------------------

/**
 * The "Comment" button that appears at the end of a selection.
 *
 * Absolutely positioned inside the document container, which must therefore be
 * `relative`. onMouseDown is prevented: a mousedown anywhere else collapses the
 * selection, and the button would then act on nothing.
 */
export function DocCommentAffordance({
  controller,
}: {
  controller: DocCommentsController;
}) {
  const { t } = useTranslation("docs");
  const pending = controller.pendingSelection;
  if (!pending || controller.readOnly) return null;

  return (
    <div
      className="absolute z-20"
      style={{ top: `${pending.rect.top + 6}px`, left: `${pending.rect.left}px` }}
    >
      <Button
        type="button"
        size="sm"
        data-testid="doc-comment-affordance"
        className="h-7 gap-1 px-2 text-xs shadow-md"
        onMouseDown={(e) => e.preventDefault()}
        onClick={controller.startDraft}
      >
        <MessageSquarePlus className="h-3.5 w-3.5" />
        {t("comments.submit")}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The entry point that needs no selection
// ---------------------------------------------------------------------------

/**
 * Starts a comment on the document as a whole — the only entry that does not
 * require selecting text first (`DocCommentAffordance` above does).
 *
 * Lives in the page's own action row, not inside the rail or the tree: those
 * two are mutually exclusive, and a button mounted inside
 * either one would only be reachable while that particular surface happens to
 * be on screen. This one is not gated on `railOpen` at all — the click opens
 * the rail itself (`onOpenRail`) so the composer this starts always has
 * somewhere to render, then starts the draft.
 *
 * Icon-only for now: a visible label is copy under §1r.A review on
 * an internal ticket, and `web/**` deploys on merge in
 * between (see an internal ticket) — so the button ships with no visible text at
 * all rather than an unapproved placeholder. `aria-label`/`title` are exempt
 * from that gate (they exist for crawlers and screen readers, not as
 * product copy) and carry the button's only description until the approved
 * label lands as a follow-up commit.
 */
export function DocCommentPageEntry({
  controller,
  onOpenRail,
}: {
  controller: DocCommentsController;
  onOpenRail: () => void;
}) {
  const { t } = useTranslation("docs");
  if (controller.readOnly) return null;

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      data-testid="doc-comment-page-entry"
      className="h-7 w-7"
      title={t("comments.onWholeDocument")}
      aria-label={t("comments.onWholeDocument")}
      onClick={() => {
        onOpenRail();
        controller.startPageDraft();
      }}
    >
      <MessageSquarePlus className="h-3.5 w-3.5" />
    </Button>
  );
}

// ---------------------------------------------------------------------------
// The count in the actions row
// ---------------------------------------------------------------------------

export function DocCommentToggle({
  controller,
  open,
  onToggle,
}: {
  controller: DocCommentsController;
  open: boolean;
  onToggle: () => void;
}) {
  const { t } = useTranslation("docs");
  const { unresolvedCount, totalCount } = controller;

  return (
    <Button
      type="button"
      variant={open ? "secondary" : "ghost"}
      size="sm"
      data-testid="doc-comment-toggle"
      aria-pressed={open}
      className="h-7 gap-1.5 px-2 text-xs"
      onClick={onToggle}
    >
      <MessageSquare className="h-3.5 w-3.5" />
      {t("comments.title")}
      {/* The badge counts what is still OPEN, never the total. A document with
          one resolved thread and nothing outstanding showing "1" reads as one
          comment waiting for you, which is the opposite of what it means — so
          that case gets a tick rather than a number. */}
      {unresolvedCount > 0 ? (
        <span
          className="rounded bg-tone-attention-400/25 px-1 text-[11px] font-medium tabular-nums text-tone-attention-900 dark:text-tone-attention-200"
          title={t("comments.openOf", { open: unresolvedCount, total: totalCount })}
        >
          {unresolvedCount}
        </span>
      ) : totalCount > 0 ? (
        <Check
          className="h-3.5 w-3.5 text-success"
          aria-label={t("comments.allResolved", { total: totalCount })}
        />
      ) : null}
    </Button>
  );
}
