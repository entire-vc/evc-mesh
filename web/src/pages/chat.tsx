import { type CSSProperties, useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, MessageSquare, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useWebSocket } from "@/hooks/use-websocket";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ResizableDivider } from "@/components/resizable-divider";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { apiErrorMessage } from "@/lib/api-error";
import { ApiRequestError } from "@/lib/api";
import {
  createChatThread,
  deleteChatThread,
  getChatProjectSettings,
  getChatThread,
  listChatAgents,
  listChatMessages,
  listChatThreads,
  renameChatThread,
  sendChatMessage,
  stopChatTurn,
} from "@/lib/chat/api";
import { setChatEnabled } from "@/lib/chat/use-chat-enabled";
import { useChatEvents } from "@/lib/chat/use-chat-events";
import { LOCAL_PREFIX, initialThreadState, threadReducer } from "@/lib/chat/thread-state";
import {
  CHAT_TREE_DEFAULT_WIDTH,
  CHAT_TREE_MAX_WIDTH,
  CHAT_TREE_MIN_WIDTH,
  loadChatTreeCollapsed,
  loadChatTreeWidth,
  saveChatTreeCollapsed,
  saveChatTreeWidth,
} from "@/lib/chat/layout-storage";
import type {
  ChatAgent,
  ChatEvent,
  ChatProjectSettings,
  ChatSessionState,
  ChatThread,
} from "@/lib/chat/types";
import { AgentBadge, ChatSidebar, NoThreadSelected } from "@/components/chat/chat-sidebar";
import { ChatFeed } from "@/components/chat/chat-feed";
import { ChatComposer } from "@/components/chat/chat-composer";

const PAGE_SIZE = 50;

function useOnline(): boolean {
  const [online, setOnline] = useState(() => (typeof navigator === "undefined" ? true : navigator.onLine));
  useEffect(() => {
    const up = () => setOnline(true);
    const down = () => setOnline(false);
    window.addEventListener("online", up);
    window.addEventListener("offline", down);
    return () => {
      window.removeEventListener("online", up);
      window.removeEventListener("offline", down);
    };
  }, []);
  return online;
}

function newClientId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `c${Date.now()}${Math.random().toString(16).slice(2)}`;
}

function RenameDialog({
  thread,
  onClose,
  onSubmit,
}: {
  thread: ChatThread | null;
  onClose: () => void;
  onSubmit: (title: string) => Promise<void>;
}) {
  const { t } = useTranslation("chat");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => setValue(thread?.title ?? ""), [thread]);
  return (
    <Dialog open={thread !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("rename.title")}</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            if (!value.trim()) return;
            setBusy(true);
            try {
              await onSubmit(value.trim());
            } finally {
              setBusy(false);
            }
          }}
          className="space-y-4"
        >
          <Input
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            maxLength={200}
            aria-label={t("rename.label")}
            data-testid="chat-rename-input"
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("rename.cancel")}
            </Button>
            <Button type="submit" disabled={busy || !value.trim()} data-testid="chat-rename-submit">
              {t("rename.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const SESSION_LABEL: Record<ChatSessionState, string> = {
  working: "session.working",
  starting: "session.starting",
  ready: "session.ready",
  stopped: "session.stopped",
};

export function ChatPage() {
  const { wsSlug, projectSlug, threadId } = useParams();
  const navigate = useNavigate();
  const { t, i18n } = useTranslation("chat");
  const { currentProject } = useProjectStore();
  const userId = useAuthStore((s) => s.user?.id);
  const projectId = currentProject?.id;
  const online = useOnline();
  const basePath = `/w/${wsSlug}/p/${projectSlug}/chat`;
  const settingsPath = `/w/${wsSlug}/p/${projectSlug}/settings?tab=integrations`;

  useWebSocket({ workspaceSlug: wsSlug, projectId });

  // ---- tree data -----------------------------------------------------------
  const [settings, setSettings] = useState<ChatProjectSettings | null>(null);
  const [allAgents, setAllAgents] = useState<ChatAgent[]>([]);
  const [threads, setThreads] = useState<ChatThread[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [treeLoading, setTreeLoading] = useState(true);
  const [treeError, setTreeError] = useState<string | null>(null);
  const [creatingFor, setCreatingFor] = useState<string | null>(null);
  const treeSeq = useRef(0);

  const loadTree = useCallback(async () => {
    if (!projectId) return;
    const seq = ++treeSeq.current;
    setTreeLoading(true);
    setTreeError(null);
    try {
      const s = await getChatProjectSettings(projectId);
      if (seq !== treeSeq.current) return;
      setSettings(s);
      setChatEnabled(projectId, s.enabled && s.agents.length > 0);
      // A project with the chat off refuses the agent and thread lists: the
      // settings alone decide the "chat is off" state.
      if (!s.enabled) {
        setAllAgents([]);
        setThreads([]);
        setCursor(null);
        return;
      }
      const [a, th] = await Promise.all([
        listChatAgents(projectId),
        listChatThreads(projectId, { limit: PAGE_SIZE }),
      ]);
      if (seq !== treeSeq.current) return;
      setAllAgents(a);
      setThreads(th.items);
      setCursor(th.has_more ? th.next_cursor : null);
    } catch (err) {
      if (seq === treeSeq.current) setTreeError(apiErrorMessage(err, t("errors.treeLoad")));
    } finally {
      if (seq === treeSeq.current) setTreeLoading(false);
    }
  }, [projectId, t]);

  useEffect(() => {
    setSettings(null);
    setThreads([]);
    setAllAgents([]);
    void loadTree();
  }, [loadTree]);

  // Only the agents the project turned on, in picker order.
  const agents = useMemo(() => {
    if (!settings?.enabled) return [];
    const on = new Set(settings.agents);
    return allAgents.filter((a) => on.has(a.agent_id));
  }, [settings, allAgents]);
  const isAdmin = settings?.allowed_roles !== undefined;

  const loadMoreThreads = async () => {
    if (!projectId || !cursor) return;
    // loadTree runs on every project switch and bumps treeSeq: a page that
    // answers after that belongs to the previous project.
    const seq = treeSeq.current;
    try {
      const page = await listChatThreads(projectId, { limit: PAGE_SIZE, cursor });
      if (seq !== treeSeq.current) return;
      setThreads((cur) => [...cur, ...page.items.filter((x) => !cur.some((c) => c.id === x.id))]);
      setCursor(page.has_more ? page.next_cursor : null);
    } catch (err) {
      toast.error(apiErrorMessage(err, t("errors.treeLoad")));
    }
  };

  // ---- open thread ---------------------------------------------------------
  const [state, dispatch] = useReducer(threadReducer, undefined, () => initialThreadState("", ""));
  const [thread, setThread] = useState<ChatThread | null>(null);
  const [msgLoading, setMsgLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [moreCursor, setMoreCursor] = useState<string | null>(null);
  // Which thread a Stop / older-page request is in flight for: the flags below
  // are true only for the open thread, so a request of another one never leaks.
  const [loadingMoreScope, setLoadingMoreScope] = useState<string | null>(null);
  const [stoppingScope, setStoppingScope] = useState<string | null>(null);
  const loadSeq = useRef(0);
  const openScope = useRef("");
  openScope.current = `${projectId}/${threadId}`;
  const loadingMore = loadingMoreScope === openScope.current;
  const stopping = stoppingScope === openScope.current;

  const loadThread = useCallback(
    async (opts: { silent?: boolean } = {}) => {
      if (!projectId || !threadId) return;
      const seq = ++loadSeq.current;
      if (!opts.silent) {
        setMsgLoading(true);
        setLoadError(null);
        setNotFound(false);
      }
      try {
        const [th, page] = await Promise.all([
          getChatThread(projectId, threadId),
          listChatMessages(projectId, threadId, { limit: PAGE_SIZE }),
        ]);
        if (seq !== loadSeq.current) return;
        setThread(th);
        setMoreCursor(page.has_more ? page.next_cursor : null);
        dispatch({ type: "loaded", projectId, threadId, messages: page.items, activeTurn: page.active_turn, sessionState: th.session_state });
      } catch (err) {
        if (seq !== loadSeq.current) return;
        // Another user's thread answers 404 like an unknown one: say nothing more.
        if (err instanceof ApiRequestError && err.status === 404) setNotFound(true);
        else if (!opts.silent) setLoadError(apiErrorMessage(err, t("errors.threadLoad")));
      } finally {
        if (seq === loadSeq.current) setMsgLoading(false);
      }
    },
    [projectId, threadId, t],
  );

  useEffect(() => {
    setThread(null);
    setNotFound(false);
    setLoadError(null);
    setMoreCursor(null);
    if (projectId && threadId) {
      dispatch({ type: "reset", projectId, threadId });
      void loadThread();
    }
    // loadThread is rebuilt with the same inputs as this effect
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, threadId]);

  // ---- live events ---------------------------------------------------------
  const handleEvent = useCallback(
    (e: ChatEvent) => {
      if (!projectId || e.project_id !== projectId) return;
      if (e.event === "chat.thread.changed") {
        const th = e.thread;
        if (e.change === "deleted") {
          setThreads((cur) => cur.filter((x) => x.id !== e.thread_id));
          if (e.thread_id === threadId) navigate(basePath, { replace: true });
        } else if (th) {
          setThreads((cur) =>
            cur.some((x) => x.id === th.id)
              ? cur.map((x) => (x.id === th.id ? { ...x, ...th } : x))
              : [th, ...cur],
          );
          if (th.id === threadId) setThread((cur) => (cur ? { ...cur, title: th.title } : cur));
        }
        return;
      }
      if (e.event === "chat.message.created") {
        setThreads((cur) => {
          const idx = cur.findIndex((x) => x.id === e.thread_id);
          if (idx < 0) return cur;
          const bumped = { ...cur[idx]!, last_message_at: e.message.created_at };
          return [bumped, ...cur.filter((_, i) => i !== idx)];
        });
      }
      dispatch({ type: "event", event: e });
    },
    [projectId, threadId, navigate, basePath],
  );
  useChatEvents(userId, handleEvent);

  // A hole in the stream is healed by refetching the page, not by guessing.
  useEffect(() => {
    if (state.needsResync && state.threadId === threadId) void loadThread({ silent: true });
  }, [state.needsResync, state.threadId, threadId, loadThread]);

  // After a reconnect we may have missed events: refetch when the network returns.
  useEffect(() => {
    if (online && threadId) void loadThread({ silent: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [online]);

  // ---- actions -------------------------------------------------------------
  const agentOf = (id: string | undefined) => allAgents.find((a) => a.agent_id === id);
  const threadAgent = agentOf(thread?.agent_id);

  const startChat = async (agentId: string) => {
    if (!projectId) return;
    setCreatingFor(agentId);
    try {
      const th = await createChatThread(projectId, agentId);
      setThreads((cur) => [th, ...cur.filter((x) => x.id !== th.id)]);
      navigate(`${basePath}/${th.id}`);
    } catch (err) {
      toast.error(apiErrorMessage(err, t("errors.create")));
    } finally {
      setCreatingFor(null);
    }
  };

  const send = async (body: string, attachmentIds: string[], files: File[]) => {
    if (!projectId || !threadId) return;
    const clientMessageId = newClientId();
    dispatch({
      type: "optimistic",
      projectId,
      threadId,
      message: {
        id: `${LOCAL_PREFIX}${clientMessageId}`,
        thread_id: threadId,
        role: "user",
        body,
        status: "queued",
        truncated: false,
        turn_id: null,
        error_code: null,
        client_message_id: clientMessageId,
        attachments: files.map((f, i) => ({
          id: attachmentIds[i] ?? `${LOCAL_PREFIX}att-${i}`,
          file_name: f.name,
          mime: f.type,
          size: f.size,
          width: null,
          height: null,
        })),
        created_at: new Date().toISOString(),
      },
    });
    try {
      const out = await sendChatMessage(projectId, threadId, { clientMessageId, body, attachmentIds });
      dispatch({ type: "sent", projectId, threadId, message: out.message, turn: out.turn });
    } catch (err) {
      dispatch({ type: "send-failed", projectId, threadId, clientMessageId });
      toast.error(apiErrorMessage(err, t("errors.send")));
      if (err instanceof ApiRequestError && err.code === "turn_in_progress" && openScope.current === `${projectId}/${threadId}`) void loadThread({ silent: true });
      throw err;
    }
  };

  const retryTurn = async () => {
    const lastUser = [...state.messages].reverse().find((m) => m.role === "user");
    if (!lastUser) return;
    try {
      await send(
        lastUser.body,
        lastUser.attachments.map((a) => a.id),
        [],
      );
    } catch {
      // reported by send
    }
  };

  const stop = async () => {
    if (!projectId || !threadId) return;
    const scope = `${projectId}/${threadId}`;
    setStoppingScope(scope);
    try {
      const out = await stopChatTurn(projectId, threadId);
      // The user may have opened another thread meanwhile: nothing here is theirs.
      if (openScope.current !== scope) return;
      dispatch({ type: "turn", projectId, threadId, turn: out.stopped ? null : out.turn });
      if (out.stopped) void loadThread({ silent: true });
    } catch (err) {
      if (openScope.current === scope) toast.error(apiErrorMessage(err, t("errors.stop")));
    } finally {
      setStoppingScope((cur) => (cur === scope ? null : cur));
    }
  };

  const loadOlder = async () => {
    if (!projectId || !threadId || !moreCursor) return;
    const scope = `${projectId}/${threadId}`;
    setLoadingMoreScope(scope);
    try {
      const page = await listChatMessages(projectId, threadId, { limit: PAGE_SIZE, before: moreCursor });
      // The user may have opened another thread meanwhile: its cursor is not ours.
      if (openScope.current !== scope) return;
      dispatch({ type: "prepend", projectId, threadId, messages: page.items });
      setMoreCursor(page.has_more ? page.next_cursor : null);
    } catch (err) {
      if (openScope.current === scope) toast.error(apiErrorMessage(err, t("errors.threadLoad")));
    } finally {
      setLoadingMoreScope((cur) => (cur === scope ? null : cur));
    }
  };

  const [renaming, setRenaming] = useState<ChatThread | null>(null);
  const [deleting, setDeleting] = useState<ChatThread | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);

  const rename = async (title: string) => {
    if (!projectId || !renaming) return;
    try {
      const th = await renameChatThread(projectId, renaming.id, title);
      setThreads((cur) => cur.map((x) => (x.id === th.id ? { ...x, ...th } : x)));
      if (th.id === threadId) setThread((cur) => (cur ? { ...cur, title: th.title } : cur));
      setRenaming(null);
    } catch (err) {
      toast.error(apiErrorMessage(err, t("errors.rename")));
    }
  };

  const confirmDelete = async () => {
    if (!projectId || !deleting) return;
    setDeleteBusy(true);
    try {
      await deleteChatThread(projectId, deleting.id);
      setThreads((cur) => cur.filter((x) => x.id !== deleting.id));
      if (deleting.id === threadId) navigate(basePath, { replace: true });
      setDeleting(null);
    } catch (err) {
      toast.error(apiErrorMessage(err, t("errors.delete")));
    } finally {
      setDeleteBusy(false);
    }
  };

  // ---- layout --------------------------------------------------------------
  const [treeWidth, setTreeWidth] = useState(loadChatTreeWidth);
  const [collapsed, setCollapsed] = useState(loadChatTreeCollapsed);
  const toggleCollapsed = () =>
    setCollapsed((c) => {
      saveChatTreeCollapsed(!c);
      return !c;
    });

  const live = state.threadId === threadId && state.projectId === projectId;
  const sessionState: ChatSessionState = live ? state.sessionState : (thread?.session_state ?? "stopped");
  const busy = live && state.activeTurn !== null;

  if (!projectId) return null;

  const disabledReason = settings && !settings.enabled ? "disabled" : null;

  return (
    <div className="mesh-mobile-chat flex h-full min-h-0 overflow-hidden" data-testid="chat-page">
      <aside
        className={cn(
          "min-h-0 w-full shrink-0 flex-col pr-2 md:w-[var(--chat-tree-width)]",
          threadId ? "hidden" : "flex",
          collapsed ? "md:hidden" : "md:flex",
        )}
        style={{ "--chat-tree-width": `${treeWidth}px` } as CSSProperties}
        aria-label={t("sidebar.heading")}
      >
        <div className="flex items-center justify-between gap-2 pb-2 pl-2">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{t("sidebar.heading")}</h2>
        </div>
        {disabledReason ? (
          <div className="p-4 text-sm" data-testid="chat-disabled">
            <p className="font-medium">{t("empty.disabledTitle")}</p>
            <p className="mt-1 text-muted-foreground">{isAdmin ? t("empty.disabledAdmin") : t("empty.disabledMember")}</p>
            {isAdmin && (
              <Link
                to={settingsPath}
                className="mt-3 inline-flex h-8 items-center rounded-md border border-input bg-background px-3 text-sm hover:bg-accent"
              >
                {t("empty.openSettings")}
              </Link>
            )}
          </div>
        ) : (
          <ChatSidebar
            basePath={basePath}
            agents={agents}
            threads={threads}
            selectedThreadId={threadId}
            loading={treeLoading}
            error={treeError}
            hasMore={cursor !== null}
            isAdmin={isAdmin}
            settingsPath={settingsPath}
            language={i18n.language}
            creatingFor={creatingFor}
            onRetry={() => void loadTree()}
            onLoadMore={() => void loadMoreThreads()}
            onNewChat={(id) => void startChat(id)}
            onRename={setRenaming}
            onDelete={setDeleting}
          />
        )}
      </aside>

      {!collapsed && (
        <ResizableDivider
          className="hidden md:block"
          label={t("sidebar.resize")}
          value={treeWidth}
          min={CHAT_TREE_MIN_WIDTH}
          max={CHAT_TREE_MAX_WIDTH}
          defaultValue={CHAT_TREE_DEFAULT_WIDTH}
          onChange={setTreeWidth}
          onCommit={saveChatTreeWidth}
        />
      )}

      <section className={cn("min-w-0 flex-1 flex-col overflow-hidden md:flex md:pl-4", threadId ? "flex" : "hidden")}>
        <div className="flex items-center gap-2 border-b border-border pb-2">
          <Button
            variant="ghost"
            size="icon"
            className="hidden md:inline-flex"
            onClick={toggleCollapsed}
            aria-label={collapsed ? t("sidebar.expand") : t("sidebar.collapse")}
            title={collapsed ? t("sidebar.expand") : t("sidebar.collapse")}
            data-testid="chat-collapse"
          >
            {collapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
          </Button>
          {threadId && (
            <Button variant="ghost" size="icon" className="md:hidden" onClick={() => navigate(basePath)} aria-label={t("sidebar.back")}>
              <ArrowLeft className="h-4 w-4" />
            </Button>
          )}
          {thread && threadAgent && <AgentBadge agent={threadAgent} />}
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-sm font-semibold" data-testid="chat-title">
              {thread ? thread.title || t("sidebar.untitled") : t("page.title")}
            </h1>
            {thread && (
              <p className="truncate text-xs text-muted-foreground" data-testid="chat-session-state" data-state={sessionState}>
                {threadAgent?.name ? `${threadAgent.name} · ` : ""}
                {t(SESSION_LABEL[sessionState])}
              </p>
            )}
          </div>
        </div>

        {!threadId ? (
          disabledReason || agents.length === 0 ? (
            <div className="hidden flex-1 items-center justify-center p-6 text-center text-sm text-muted-foreground md:flex">
              <MessageSquare className="mr-2 h-5 w-5" aria-hidden />
              {t("empty.nothingToOpen")}
            </div>
          ) : (
            <NoThreadSelected agents={agents} onNewChat={(id) => void startChat(id)} />
          )
        ) : notFound ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center" data-testid="chat-not-found">
            <p className="text-sm">{t("empty.threadNotFound")}</p>
            <Link to={basePath} className="text-sm underline">
              {t("empty.backToChats")}
            </Link>
          </div>
        ) : (
          <>
            <ChatFeed
              key={threadId}
              projectId={projectId}
              wsSlug={wsSlug}
              messages={live ? state.messages : []}
              tools={live ? state.tools : []}
              activeTurn={live ? state.activeTurn : null}
              error={live ? state.error : null}
              agentName={threadAgent?.name ?? t("page.agent")}
              agentColorToken={threadAgent?.color_token}
              hasMore={moreCursor !== null}
              loadingMore={loadingMore}
              loading={msgLoading || !live}
              loadError={loadError}
              online={online}
              onLoadMore={() => void loadOlder()}
              onRetryLoad={() => void loadThread()}
              onRetryTurn={() => void retryTurn()}
              language={i18n.language}
            />
            <div className="pb-2 pt-2">
              <ChatComposer
                key={`${projectId}/${threadId}`}
                projectId={projectId}
                threadId={threadId}
                busy={busy}
                disabled={!online || notFound || loadError !== null}
                onSend={send}
                onStop={() => void stop()}
                stopping={stopping}
              />
            </div>
          </>
        )}
      </section>

      <RenameDialog thread={renaming} onClose={() => setRenaming(null)} onSubmit={rename} />
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => void confirmDelete()}
        title={t("delete.title")}
        description={t("delete.description", { title: deleting?.title || t("sidebar.untitled") })}
        confirmText={t("delete.confirm")}
        variant="destructive"
        isLoading={deleteBusy}
        dismissible
      />
    </div>
  );
}
