import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { AtSign, Bell, Check, CheckCheck, Loader2, Settings } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useNotificationStore } from "@/stores/notification";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import {
  fetchMentionInbox,
  fetchUnseenMentionCount,
  markMentionSeen,
  mentionHref,
  type MentionInboxItem,
} from "@/lib/mentions/inbox";
import type { Notification } from "@/types";

// ---------------------------------------------------------------------------
// NotificationBell component — header bell icon with dropdown
//
// This bell and the two @-mentions inboxes (`lib/mentions/inbox.ts`) are two
// independently-gated systems on the server, not one feature with a display
// bug: a "notifications" row is only created for a recipient who has an
// *enabled* `web_push` channel preference for that event type
// (notification_service.go dispatch()), which nobody is auto-subscribed to —
// while `/me/mentions` and `/me/document-mentions` are written unconditionally
// whenever a comment mentions someone. So an account with no notification
// preferences configured sees a permanently empty bell no matter how many
// real mentions exist, while the Mentions tab (already merging both inboxes
// since #639) shows them correctly. Same "two inboxes, one screen disagrees"
// shape as #639, one level up: that PR merged the mention inboxes into each
// other; this merges the mention inboxes into the *bell*, which had never
// been wired to either of them.
//
// document.mentioned goes through the same web_push-gated dispatch() as
// task.mentioned — document_comment_mentions.go's notifyMentionedUser() calls
// notifySvc.Notify() too, so a web_push-subscribed account can just as well
// have a document.mentioned row in the generic list. Both event types are
// excluded below (MENTION_EVENT_TYPES) for that reason: without the
// exclusion, that account would see the same mention twice — once from each
// source.
// ---------------------------------------------------------------------------

const MENTION_EVENT_TYPES = new Set(["task.mentioned", "document.mentioned"]);

// A row in the dropdown, normalized from whichever of the two sources it
// came from so the list can render and sort them together.
interface BellRow {
  id: string;
  kind: "notification" | "mention";
  title: string;
  body: string;
  createdAt: string;
  isRead: boolean;
  notification?: Notification;
  mention?: MentionInboxItem;
}

function notificationToRow(n: Notification): BellRow {
  return {
    id: n.id,
    kind: "notification",
    title: n.title,
    body: n.body,
    createdAt: n.created_at,
    isRead: n.is_read,
    notification: n,
  };
}

function mentionToRow(m: MentionInboxItem): BellRow {
  return {
    id: m.comment_id,
    kind: "mention",
    title: `${m.author_name} mentioned you on: ${m.title}`,
    body: m.comment_body,
    createdAt: m.extracted_at,
    isRead: m.seen_at !== null,
    mention: m,
  };
}

export function NotificationBell() {
  const navigate = useNavigate();
  const { user, isAuthenticated } = useAuthStore();
  // Needed by handleOpenSettings (the notification-settings route is
  // workspace-scoped) and by the document branch of handleNotificationClick,
  // which has no deep-link resolver to fall back on.
  const { currentWorkspace } = useWorkspaceStore();
  const { projects } = useProjectStore();
  const {
    notifications,
    isLoading,
    startPolling,
    stopPolling,
    markAsRead,
    markAllAsRead,
    fetchNotifications,
  } = useNotificationStore();
  const lastEvent = useWebSocketStore((s) => s.lastEvent);
  const wsSubscribe = useWebSocketStore((s) => s.subscribe);
  const wsUnsubscribe = useWebSocketStore((s) => s.unsubscribe);

  const [open, setOpen] = useState(false);
  const [mentions, setMentions] = useState<MentionInboxItem[]>([]);
  const [mentionCount, setMentionCount] = useState(0);
  const [mentionsFailed, setMentionsFailed] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Start polling when user is authenticated
  useEffect(() => {
    if (!isAuthenticated) return;
    startPolling();
    return () => stopPolling();
  }, [isAuthenticated, startPolling, stopPolling]);

  // Unseen count across both mention inboxes, fetched once so the badge is
  // right before the dropdown is ever opened — same source the sidebar
  // Activity badge uses (fetchUnseenMentionCount), kept independent here so
  // the bell doesn't depend on the sidebar having mounted.
  useEffect(() => {
    if (!isAuthenticated) return;
    fetchUnseenMentionCount()
      .then(setMentionCount)
      .catch(() => {});
  }, [isAuthenticated]);

  // Live badge: the server publishes mention.badge (comment_service.go,
  // document_comment_mentions.go) on the user's personal channel for both
  // task and document mentions; mention.created is accepted too — see the
  // identical comment in sidebar.tsx, same reasoning applies here.
  useEffect(() => {
    if (!user?.id) return;
    const channel = `ws:user:${user.id}`;
    wsSubscribe(channel);
    return () => wsUnsubscribe(channel);
  }, [user?.id, wsSubscribe, wsUnsubscribe]);

  useEffect(() => {
    if (lastEvent?.type === "mention.badge" || lastEvent?.type === "mention.created") {
      setMentionCount((n) => n + 1);
    }
  }, [lastEvent]);

  // Close dropdown on outside click
  useEffect(() => {
    const handleClick = (e: MouseEvent) => {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, []);

  const handleToggle = useCallback(() => {
    setOpen((prev) => {
      if (!prev) {
        void fetchNotifications();
        fetchMentionInbox(20)
          .then(({ items, failed }) => {
            setMentions(items);
            setMentionsFailed(failed.length > 0);
            setMentionCount(items.filter((m) => !m.seen_at).length);
          })
          .catch(() => setMentionsFailed(true));
      }
      return !prev;
    });
  }, [fetchNotifications]);

  const handleNotificationClick = useCallback(
    (n: Notification) => {
      // Mark as read
      if (!n.is_read) {
        void markAsRead([n.id]);
      }
      // Navigate to task if task_id is present in metadata.
      // /t/:taskId is the deep-link resolver (task-deep-link.tsx) — it looks
      // up the task's workspace/project slugs itself, so no need for
      // project_id or currentWorkspace here.
      const meta = n.metadata as Record<string, unknown>;
      const taskId = meta?.task_id as string | undefined;

      if (taskId) {
        // notifyUserMention (task.mentioned) puts comment_id in metadata
        // alongside task_id — carry it through the /t/:id resolver so
        // task-panel.tsx can focus that comment, same as the document branch
        // below already does.
        const commentId = meta?.comment_id as string | undefined;
        const query = commentId ? `?comment=${commentId}` : "";
        navigate(`/t/${taskId}${query}`);
        setOpen(false);
        return;
      }

      // A document notification carries document_id and no task_id, so the
      // branch above left it inert: the bell showed it and clicking did
      // nothing. There is no /d/:id resolver, so the route is assembled here
      // from the workspace and the project the metadata names.
      const documentId = meta?.document_id as string | undefined;
      const projectId = meta?.project_id as string | undefined;
      if (documentId && currentWorkspace) {
        const project = projects.find((p) => p.id === projectId);
        if (project) {
          const commentId = meta?.comment_id as string | undefined;
          const query = commentId ? `?comment=${commentId}` : "";
          navigate(
            `/w/${currentWorkspace.slug}/p/${project.slug}/docs/${documentId}${query}`,
          );
        }
      }
      setOpen(false);
    },
    [markAsRead, navigate, currentWorkspace, projects],
  );

  const handleMentionClick = useCallback(
    (mention: MentionInboxItem) => {
      if (!mention.seen_at) {
        markMentionSeen(mention)
          .then(() => {
            setMentions((prev) =>
              prev.map((m) =>
                m.comment_id === mention.comment_id
                  ? { ...m, seen_at: new Date().toISOString() }
                  : m,
              ),
            );
            setMentionCount((n) => Math.max(0, n - 1));
          })
          .catch(() => {});
      }
      const project = projects.find((p) => p.id === mention.project_id);
      const href = mentionHref(mention, currentWorkspace?.slug, project?.slug);
      if (href) navigate(href);
      setOpen(false);
    },
    [projects, currentWorkspace, navigate],
  );

  const handleRowClick = useCallback(
    (row: BellRow) => {
      if (row.kind === "mention" && row.mention) {
        handleMentionClick(row.mention);
      } else if (row.notification) {
        handleNotificationClick(row.notification);
      }
    },
    [handleMentionClick, handleNotificationClick],
  );

  const handleMarkAll = useCallback(
    (e: React.MouseEvent) => {
      e.stopPropagation();
      void markAllAsRead();
      const unseen = mentions.filter((m) => !m.seen_at);
      if (unseen.length > 0) {
        void Promise.allSettled(unseen.map((m) => markMentionSeen(m))).then(() => {
          setMentions((prev) =>
            prev.map((m) => ({ ...m, seen_at: m.seen_at ?? new Date().toISOString() })),
          );
          setMentionCount(0);
        });
      }
    },
    [markAllAsRead, mentions],
  );

  const handleOpenSettings = useCallback(
    (e: React.MouseEvent) => {
      e.stopPropagation();
      if (currentWorkspace) {
        navigate(`/w/${currentWorkspace.slug}/notifications`);
      }
      setOpen(false);
    },
    [navigate, currentWorkspace],
  );

  // Mention events are dropped from the generic list (see the module-level
  // comment) so a rare web_push-subscribed account doesn't see them twice.
  const rows = useMemo<BellRow[]>(() => {
    const notifRows = notifications
      .filter((n) => !MENTION_EVENT_TYPES.has(n.event_type))
      .map(notificationToRow);
    const mentionRows = mentions.map(mentionToRow);
    return [...notifRows, ...mentionRows].sort(
      (a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime(),
    );
  }, [notifications, mentions]);

  const unreadCount =
    notifications.filter((n) => !n.is_read && !MENTION_EVENT_TYPES.has(n.event_type)).length +
    mentionCount;
  const unreadItems = rows.filter((r) => !r.isRead);
  // "No notifications" must not be shown when a source we merged in simply
  // failed to answer — same principle #639 applied to the Mentions tab
  // itself (see the "unanswered source" note in lib/mentions/inbox.ts).
  const showEmptyState = rows.length === 0 && !mentionsFailed;

  return (
    <div ref={containerRef} className="relative">
      <Button
        variant="ghost"
        size="icon"
        onClick={handleToggle}
        aria-label={`Notifications${unreadCount > 0 ? ` (${unreadCount} unread)` : ""}`}
        className="relative"
      >
        <Bell className="h-4 w-4" />
        {unreadCount > 0 && (
          <span className="absolute -right-0.5 -top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-primary text-[10px] font-medium text-primary-foreground">
            {unreadCount > 9 ? "9+" : unreadCount}
          </span>
        )}
      </Button>

      {open && (
        <div className="absolute right-0 top-full z-50 mt-1 w-80 overflow-hidden rounded-lg border border-border bg-popover shadow-lg">
          {/* Header */}
          <div className="flex items-center justify-between border-b border-border px-3 py-2">
            <span className="text-sm font-medium">Notifications</span>
            <div className="flex items-center gap-1">
              {isLoading && (
                <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
              )}
              {unreadCount > 0 && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 gap-1 px-1.5 text-xs text-muted-foreground hover:text-foreground"
                  onClick={handleMarkAll}
                  title="Mark all as read"
                >
                  <CheckCheck className="h-3.5 w-3.5" />
                  Mark all read
                </Button>
              )}
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6 text-muted-foreground hover:text-foreground"
                onClick={handleOpenSettings}
                title="Notification settings"
                aria-label="Notification settings"
              >
                <Settings className="h-3.5 w-3.5" />
              </Button>
            </div>
          </div>

          {/* Notification list */}
          <ul className="max-h-96 overflow-y-auto">
            {showEmptyState ? (
              <li className="px-3 py-6 text-center text-sm text-muted-foreground">
                No notifications
              </li>
            ) : (
              rows.map((row) => (
                <BellRowItem
                  key={`${row.kind}-${row.id}`}
                  row={row}
                  onMarkRead={() => {
                    const mention = row.mention;
                    if (row.kind === "mention" && mention) {
                      void markMentionSeen(mention).then(() => {
                        setMentions((prev) =>
                          prev.map((m) =>
                            m.comment_id === mention.comment_id
                              ? { ...m, seen_at: new Date().toISOString() }
                              : m,
                          ),
                        );
                        setMentionCount((n) => Math.max(0, n - 1));
                      });
                    } else if (row.notification) {
                      void markAsRead([row.notification.id]);
                    }
                  }}
                  onClick={() => handleRowClick(row)}
                />
              ))
            )}
            {mentionsFailed && (
              <li className="px-3 py-2 text-center text-xs text-muted-foreground">
                Some mentions could not be loaded
              </li>
            )}
          </ul>

          {/* Footer */}
          {unreadItems.length === 0 && rows.length > 0 && (
            <div className="border-t border-border px-3 py-2 text-center text-xs text-muted-foreground">
              All caught up
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// BellRowItem — single row in the dropdown (a generic notification or a
// merged-in @-mention)
// ---------------------------------------------------------------------------

interface BellRowItemProps {
  row: BellRow;
  onMarkRead: () => void;
  onClick: () => void;
}

function BellRowItem({ row, onMarkRead, onClick }: BellRowItemProps) {
  const handleMarkRead = (e: React.MouseEvent) => {
    e.stopPropagation();
    onMarkRead();
  };

  const relativeTime = formatRelative(row.createdAt);

  return (
    <li
      className={`flex cursor-pointer items-start gap-2.5 border-b border-border/50 px-3 py-2.5 text-sm transition-colors last:border-b-0 hover:bg-accent ${
        !row.isRead ? "bg-accent/30" : ""
      }`}
      onClick={onClick}
    >
      {/* Unread indicator */}
      <span
        className={`mt-1 h-1.5 w-1.5 shrink-0 rounded-full ${
          !row.isRead ? "bg-primary" : "bg-transparent"
        }`}
      />

      {row.kind === "mention" && (
        <AtSign className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      )}

      <div className="min-w-0 flex-1">
        <p className="truncate font-medium leading-tight">{row.title}</p>
        {row.body && (
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
            {row.body}
          </p>
        )}
        <p className="mt-1 text-xs text-muted-foreground">{relativeTime}</p>
      </div>

      {/* Mark read button */}
      {!row.isRead && (
        <button
          className="ml-auto shrink-0 rounded p-0.5 text-muted-foreground hover:bg-background hover:text-foreground"
          onClick={handleMarkRead}
          title="Mark as read"
        >
          <Check className="h-3 w-3" />
        </button>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------
// formatRelative — simple relative time formatter
// ---------------------------------------------------------------------------

function formatRelative(isoString: string): string {
  const date = new Date(isoString);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSeconds = Math.floor(diffMs / 1000);

  if (diffSeconds < 60) return "just now";
  const diffMinutes = Math.floor(diffSeconds / 60);
  if (diffMinutes < 60) return `${diffMinutes}m ago`;
  const diffHours = Math.floor(diffMinutes / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 7) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}
