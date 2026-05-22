import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { AtSign, MessageSquare } from "lucide-react";
import { cn } from "@/lib/cn";
import { api } from "@/lib/api";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { useWebSocketStore } from "@/stores/websocket";
import { MarkdownRenderer } from "@/components/markdown-renderer";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import type { CommentView, CommentViewPage, Mention } from "@/types";

// Persists which mention IDs have been rendered in the feed (shown-state).
// Distinguishes "new" (never rendered) from "shown" (rendered, not yet clicked).
const SHOWN_KEY = "mesh.mentions.shown";
const SHOWN_MAX = 2000;

function readShownIds(): Set<string> {
  try {
    const raw = localStorage.getItem(SHOWN_KEY);
    if (!raw) return new Set();
    return new Set(JSON.parse(raw) as string[]);
  } catch {
    return new Set();
  }
}

function saveShownIds(ids: Set<string>): void {
  try {
    let arr = [...ids];
    if (arr.length > SHOWN_MAX) arr = arr.slice(-SHOWN_MAX);
    localStorage.setItem(SHOWN_KEY, JSON.stringify(arr));
  } catch {}
}

function formatRelative(isoStr: string): string {
  const diff = Date.now() - new Date(isoStr).getTime();
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

type Tab = "mentions" | "my-comments" | "all-recent";

const TABS: { id: Tab; label: string }[] = [
  { id: "mentions", label: "Mentions" },
  { id: "my-comments", label: "My comments" },
  { id: "all-recent", label: "All recent" },
];

// Grace period after Mentions tab opens before last_visit is bumped — gives the
// user enough time to actually see the «Новое» label on the items they came for.
const LAST_VISIT_DELAY_MS = 5_000;
const lastVisitKey = (ws: string | undefined) => `mesh:activity:last_visit:${ws ?? "global"}`;

// Three visual states for a mention row:
//   new    — arrived since last page open (not yet rendered in feed)
//   shown  — rendered in feed this or a prior session, not yet clicked
//   opened — clicked through (seen_at set on backend)
type MentionDisplayState = "new" | "shown" | "opened";

function getMentionDisplayState(
  mention: Mention,
  shownIds: Set<string>,
): MentionDisplayState {
  if (mention.seen_at) return "opened";
  if (shownIds.has(mention.comment_id)) return "shown";
  return "new";
}

export function ActivityPage() {
  const { wsSlug } = useParams();
  const navigate = useNavigate();
  const { projects } = useProjectStore();
  const { currentWorkspace } = useWorkspaceStore();
  const { lastEvent } = useWebSocketStore();
  const [activeTab, setActiveTab] = useState<Tab>("mentions");
  const [mentions, setMentions] = useState<Mention[]>([]);
  const [myComments, setMyComments] = useState<CommentView[]>([]);
  const [recentComments, setRecentComments] = useState<CommentView[]>([]);
  const [loading, setLoading] = useState(false);
  const [lastVisit, setLastVisit] = useState<Date | null>(null);
  const [myCommentsNextCursor, setMyCommentsNextCursor] = useState<string | null>(null);
  const [recentNextCursor, setRecentNextCursor] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  // Snapshot of shown IDs at mount (from localStorage). Updated via ref after each fetch
  // so subsequent re-renders (e.g. after a WS-triggered refetch) know which items were
  // already shown this session — without triggering extra re-renders.
  const shownIdsRef = useRef<Set<string>>(readShownIds());
  const hasDispatchedBadgeRef = useRef(false);

  const fetchMentions = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api<Mention[]>("/api/v1/me/mentions", { params: { limit: 50 } });
      const sorted = [...(data ?? [])].sort((a, b) => {
        if (!a.seen_at && b.seen_at) return -1;
        if (a.seen_at && !b.seen_at) return 1;
        return new Date(b.extracted_at).getTime() - new Date(a.extracted_at).getTime();
      });
      setMentions(sorted);
    } catch {
      toast.error("Failed to load mentions");
    } finally {
      setLoading(false);
    }
  }, []);

  const fetchMyComments = useCallback(async (before?: string) => {
    if (before) setLoadingMore(true);
    else {
      setLoading(true);
      setMyCommentsNextCursor(null);
    }
    try {
      const params: Record<string, string | number | undefined> = { limit: 50 };
      if (before) params.before = before;
      const data = await api<CommentViewPage>("/api/v1/me/comments", { params });
      const items = data?.items ?? [];
      if (before) setMyComments((prev) => [...prev, ...items]);
      else setMyComments(items);
      setMyCommentsNextCursor(data?.next_cursor ?? null);
    } catch {
      toast.error("Failed to load comments");
    } finally {
      if (before) setLoadingMore(false);
      else setLoading(false);
    }
  }, []);

  const fetchRecentComments = useCallback(async (before?: string) => {
    if (!currentWorkspace) return;
    if (before) setLoadingMore(true);
    else {
      setLoading(true);
      setRecentNextCursor(null);
    }
    try {
      const params: Record<string, string | number | undefined> = { limit: 50 };
      if (before) params.before = before;
      const data = await api<CommentViewPage>(
        `/api/v1/workspaces/${currentWorkspace.id}/comments/recent`,
        { params },
      );
      const items = data?.items ?? [];
      if (before) setRecentComments((prev) => [...prev, ...items]);
      else setRecentComments(items);
      setRecentNextCursor(data?.next_cursor ?? null);
    } catch {
      toast.error("Failed to load recent comments");
    } finally {
      if (before) setLoadingMore(false);
      else setLoading(false);
    }
  }, [currentWorkspace]);

  useEffect(() => {
    if (activeTab === "mentions") void fetchMentions();
    else if (activeTab === "my-comments") void fetchMyComments();
    else if (activeTab === "all-recent") void fetchRecentComments();
  }, [activeTab, fetchMentions, fetchMyComments, fetchRecentComments]);

  useEffect(() => {
    if (activeTab !== "mentions") return;
    const key = lastVisitKey(wsSlug);
    const stored = localStorage.getItem(key);
    setLastVisit(stored ? new Date(stored) : null);
    const timer = window.setTimeout(() => {
      localStorage.setItem(key, new Date().toISOString());
    }, LAST_VISIT_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [activeTab, wsSlug]);

  // After rendering: persist unseen mention IDs to localStorage so the next page load
  // shows them as "shown" (muted). Update the ref so a WS-triggered refetch within this
  // session correctly renders prior items as "shown" rather than "new" again.
  useEffect(() => {
    if (mentions.length === 0) return;
    const updated = new Set(shownIdsRef.current);
    for (const m of mentions) {
      if (!m.seen_at) updated.add(m.comment_id);
    }
    saveShownIds(updated);
    shownIdsRef.current = updated;
    // Clear sidebar badge once on initial page open.
    if (!hasDispatchedBadgeRef.current) {
      hasDispatchedBadgeRef.current = true;
      window.dispatchEvent(
        new CustomEvent("mesh:mentions:shown", { detail: { newCount: 0 } }),
      );
    }
  }, [mentions]);

  // Keep feed live: refetch when a new mention arrives via WebSocket.
  // New mention is not yet in shownIdsRef so it renders as "new".
  useEffect(() => {
    if (lastEvent?.type === "mention.created" && activeTab === "mentions") {
      void fetchMentions();
    }
  }, [lastEvent, activeTab, fetchMentions]);

  const handleMentionClick = useCallback(
    (mention: Mention) => {
      if (!mention.seen_at) {
        api(`/api/v1/me/mentions/${mention.comment_id}/seen`, { method: "POST" })
          .then(() => {
            setMentions((prev) =>
              prev.map((m) =>
                m.comment_id === mention.comment_id
                  ? { ...m, seen_at: new Date().toISOString() }
                  : m,
              ),
            );
            localStorage.removeItem("mesh_unseen_ts");
          })
          .catch(() => {});
      }
      const project = projects.find((p) => p.id === mention.project_id);
      if (wsSlug && project) {
        navigate(`/w/${wsSlug}/p/${project.slug}/t/${mention.task_id}`);
      }
    },
    [wsSlug, navigate, projects],
  );

  const handleCommentClick = useCallback(
    (comment: CommentView) => {
      const project = projects.find((p) => p.id === comment.project_id);
      if (wsSlug && project) {
        navigate(`/w/${wsSlug}/p/${project.slug}/t/${comment.task_id}`);
      }
    },
    [wsSlug, navigate, projects],
  );

  const handleLoadMoreMyComments = useCallback(() => {
    if (myCommentsNextCursor) void fetchMyComments(myCommentsNextCursor);
  }, [myCommentsNextCursor, fetchMyComments]);

  const handleLoadMoreRecent = useCallback(() => {
    if (recentNextCursor) void fetchRecentComments(recentNextCursor);
  }, [recentNextCursor, fetchRecentComments]);

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Activity</h1>

      <div className="flex gap-1 border-b border-border">
        {TABS.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
              activeTab === tab.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {activeTab === "mentions" && (
        <MentionsTab
          mentions={mentions}
          loading={loading}
          lastVisit={lastVisit}
          shownIds={shownIdsRef.current}
          onMentionClick={handleMentionClick}
        />
      )}

      {activeTab === "my-comments" && (
        <CommentsTab
          comments={myComments}
          loading={loading}
          onCommentClick={handleCommentClick}
          emptyMessage="You haven't commented on anything yet."
          onLoadMore={handleLoadMoreMyComments}
          hasMore={myCommentsNextCursor !== null}
          loadingMore={loadingMore}
        />
      )}

      {activeTab === "all-recent" && (
        <CommentsTab
          comments={recentComments}
          loading={loading}
          onCommentClick={handleCommentClick}
          emptyMessage="No comments in this workspace yet."
          showAuthor
          onLoadMore={handleLoadMoreRecent}
          hasMore={recentNextCursor !== null}
          loadingMore={loadingMore}
        />
      )}
    </div>
  );
}

function MentionsTab({
  mentions,
  loading,
  lastVisit,
  shownIds,
  onMentionClick,
}: {
  mentions: Mention[];
  loading: boolean;
  lastVisit: Date | null;
  shownIds: Set<string>;
  onMentionClick: (m: Mention) => void;
}) {
  const { fresh, shown } = useMemo(() => splitByLastVisit(mentions, lastVisit), [mentions, lastVisit]);

  if (loading) {
    return (
      <div className="space-y-3">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-24 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  if (mentions.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground">
        <AtSign className="mb-3 h-8 w-8 opacity-30" />
        <p className="text-sm">
          No mentions yet. When someone tags you with @username, it'll show up here.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {fresh.length > 0 && (
        <section className="space-y-2">
          <SectionHeader label="Новое" count={fresh.length} accent />
          {fresh.map((m) => (
            <MentionCard key={m.comment_id} mention={m} shownIds={shownIds} onClick={onMentionClick} />
          ))}
        </section>
      )}

      {shown.length > 0 && (
        <section className="space-y-2">
          {fresh.length > 0 && <SectionHeader label="Показано" count={shown.length} />}
          {shown.map((m) => (
            <MentionCard key={m.comment_id} mention={m} shownIds={shownIds} onClick={onMentionClick} />
          ))}
        </section>
      )}
    </div>
  );
}

function CommentsTab({
  comments,
  loading,
  onCommentClick,
  emptyMessage,
  showAuthor = false,
  onLoadMore,
  hasMore = false,
  loadingMore = false,
}: {
  comments: CommentView[];
  loading: boolean;
  onCommentClick: (c: CommentView) => void;
  emptyMessage: string;
  showAuthor?: boolean;
  onLoadMore?: () => void;
  hasMore?: boolean;
  loadingMore?: boolean;
}) {
  if (loading) {
    return (
      <div className="space-y-3">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-24 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  if (comments.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground">
        <MessageSquare className="mb-3 h-8 w-8 opacity-30" />
        <p className="text-sm">{emptyMessage}</p>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {comments.map((comment) => (
        <CommentCard
          key={comment.comment_id}
          comment={comment}
          showAuthor={showAuthor}
          onClick={onCommentClick}
        />
      ))}
      {hasMore && onLoadMore && (
        <button
          onClick={onLoadMore}
          disabled={loadingMore}
          className="mt-2 w-full rounded-md py-2 text-sm text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
        >
          {loadingMore ? "Loading…" : "Load more"}
        </button>
      )}
    </div>
  );
}

function CommentCard({
  comment,
  showAuthor,
  onClick,
}: {
  comment: CommentView;
  showAuthor: boolean;
  onClick: (c: CommentView) => void;
}) {
  const preview =
    comment.comment_body.length > 200
      ? comment.comment_body.slice(0, 200) + "…"
      : comment.comment_body;

  return (
    <button
      onClick={() => onClick(comment)}
      className="w-full cursor-pointer rounded-lg border p-3 text-left transition-colors hover:bg-white/[0.02] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-blue-500/40"
    >
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium">{comment.task_title}</span>
          <span className="shrink-0 text-xs text-muted-foreground">
            {formatRelative(comment.created_at)}
          </span>
        </div>
        <p className="mb-1 text-xs text-muted-foreground">
          {showAuthor ? comment.author_name : comment.project_name}
        </p>
        <div className="line-clamp-2 text-xs text-muted-foreground">
          <MarkdownRenderer content={preview} className="text-xs" />
        </div>
      </div>
    </button>
  );
}

function splitByLastVisit(
  mentions: Mention[],
  lastVisit: Date | null,
): { fresh: Mention[]; shown: Mention[] } {
  if (!lastVisit) return { fresh: [], shown: mentions };
  const cutoff = lastVisit.getTime();
  const fresh: Mention[] = [];
  const shown: Mention[] = [];
  for (const m of mentions) {
    if (new Date(m.extracted_at).getTime() > cutoff) fresh.push(m);
    else shown.push(m);
  }
  return { fresh, shown };
}

function SectionHeader({ label, count, accent }: { label: string; count: number; accent?: boolean }) {
  return (
    <div className="flex items-center gap-2 pb-1">
      <span
        className={cn(
          "text-xs font-semibold uppercase tracking-wide",
          accent ? "text-primary" : "text-muted-foreground",
        )}
      >
        {label}
      </span>
      <span className="text-xs text-muted-foreground">({count})</span>
      <div className={cn("h-px flex-1", accent ? "bg-primary/20" : "bg-border")} />
    </div>
  );
}

function MentionCard({
  mention,
  shownIds,
  onClick,
}: {
  mention: Mention;
  shownIds: Set<string>;
  onClick: (m: Mention) => void;
}) {
  const displayState = getMentionDisplayState(mention, shownIds);
  const isNew = displayState === "new";
  const isShown = displayState === "shown";
  const hasQuestion = mention.comment_body.includes("❓");
  const preview =
    mention.comment_body.length > 200
      ? mention.comment_body.slice(0, 200) + "…"
      : mention.comment_body;

  return (
    <button
      onClick={() => onClick(mention)}
      className={cn(
        "w-full cursor-pointer rounded-lg border p-3 text-left transition-colors hover:bg-white/[0.02] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-blue-500/40",
        isNew && "border-primary/30 bg-primary/5",
        isShown && "opacity-70",
        hasQuestion &&
          "border-l-4 border-l-amber-400 bg-amber-50/50 dark:bg-amber-950/20",
      )}
    >
      <div className="flex items-start gap-2">
        {isNew && (
          <span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-red-500" />
        )}
        <div className={cn("min-w-0 flex-1", !isNew && "pl-4")}>
          <div className="flex flex-wrap items-center gap-2">
            <span className={cn("truncate text-sm", isNew && "font-medium")}>
              {mention.task_title}
            </span>
            <span className="shrink-0 text-xs text-muted-foreground">
              {formatRelative(mention.extracted_at)}
            </span>
          </div>
          <p className="mb-1 text-xs text-muted-foreground">{mention.author_name}</p>
          <div className="line-clamp-2 text-xs text-muted-foreground">
            <MarkdownRenderer content={preview} className="text-xs" />
          </div>
        </div>
      </div>
    </button>
  );
}
