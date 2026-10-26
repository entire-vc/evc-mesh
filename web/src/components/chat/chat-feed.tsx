import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, ChevronRight, Copy, Download, FileText, Loader2, RotateCcw, WifiOff } from "lucide-react";
import { MarkdownView } from "@/components/markdown-view";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { cn } from "@/lib/cn";
import { formatTime } from "@/i18n/format";
import { copyText } from "@/lib/clipboard";
import { fetchChatAttachmentBlob } from "@/lib/chat/api";
import { agentColorClass, agentInitials } from "@/lib/chat/agent-color";
import { LOCAL_PREFIX } from "@/lib/chat/thread-state";
import type { ChatAttachment, ChatMessage, ChatToolEvent, ChatTurnRef } from "@/lib/chat/types";

function formatClock(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : formatTime(d);
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function AttachmentView({ projectId, attachment }: { projectId: string; attachment: ChatAttachment }) {
  const { t } = useTranslation("chat");
  const isImage = attachment.mime.startsWith("image/");
  const [src, setSrc] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!isImage) return;
    let url: string | null = null;
    let cancelled = false;
    fetchChatAttachmentBlob(projectId, attachment.id)
      .then((blob) => {
        if (cancelled) return;
        url = URL.createObjectURL(blob);
        setSrc(url);
      })
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [projectId, attachment.id, isImage]);

  if (isImage && !failed) {
    return (
      <a
        href={src ?? undefined}
        target="_blank"
        rel="noreferrer"
        data-testid="chat-attachment-image"
        className="block h-24 w-24 overflow-hidden rounded-md border border-border bg-muted"
        title={attachment.file_name}
      >
        {src ? (
          <img src={src} alt={attachment.file_name} className="h-full w-full object-cover" />
        ) : (
          <span className="flex h-full w-full items-center justify-center">
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-label={t("feed.loading")} />
          </span>
        )}
      </a>
    );
  }
  return (
    <button
      type="button"
      data-testid="chat-attachment-file"
      onClick={() =>
        fetchChatAttachmentBlob(projectId, attachment.id)
          .then((b) => saveBlob(b, attachment.file_name))
          .catch(() => toast.error(t("feed.downloadFailed")))
      }
      className="flex max-w-[14rem] items-center gap-2 rounded-md border border-border bg-muted px-2 py-1.5 text-left text-xs hover:bg-muted"
    >
      <FileText className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1">
        <span className="block truncate">{attachment.file_name}</span>
        <span className="text-muted-foreground">{formatSize(attachment.size)}</span>
      </span>
      <Download className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
    </button>
  );
}

// Adds a copy button to every code block the markdown renderer produces. The
// blocks appear asynchronously, so a MutationObserver decorates them as they come.
function useCodeCopy(host: React.RefObject<HTMLDivElement | null>, label: string, done: string) {
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const decorate = () => {
      el.querySelectorAll("pre").forEach((pre) => {
        if (pre.dataset.copyReady) return;
        pre.dataset.copyReady = "1";
        pre.style.position = "relative";
        const btn = document.createElement("button");
        btn.type = "button";
        btn.textContent = label;
        btn.setAttribute("aria-label", label);
        btn.className =
          "absolute right-1.5 top-1.5 rounded border border-border bg-background/90 px-1.5 py-0.5 text-[11px] text-muted-foreground hover:text-foreground";
        btn.addEventListener("click", () => {
          void copyText(pre.querySelector("code")?.textContent ?? pre.textContent ?? "").then(() => {
            btn.textContent = done;
            setTimeout(() => (btn.textContent = label), 1500);
          });
        });
        pre.appendChild(btn);
      });
    };
    decorate();
    const obs = new MutationObserver(decorate);
    obs.observe(el, { childList: true, subtree: true });
    return () => obs.disconnect();
  }, [host, label, done]);
}

function Bubble({ projectId, message, wsSlug }: { projectId: string; message: ChatMessage; wsSlug?: string }) {
  const { t } = useTranslation("chat");
  const mine = message.role === "user";
  const host = useRef<HTMLDivElement>(null);
  useCodeCopy(host, t("feed.copyCode"), t("feed.copied"));
  return (
    <li
      data-testid="chat-message"
      data-role={message.role}
      data-status={message.status}
      className={cn("flex", mine ? "justify-end" : "justify-start")}
    >
      <div className={cn("flex min-w-0 max-w-[92%] flex-col gap-1 sm:max-w-[80%]", mine ? "items-end" : "items-start")}>
        <div
          ref={host}
          className={cn(
            "min-w-0 max-w-full overflow-hidden break-words rounded-lg px-3 py-2 text-sm",
            mine ? "bg-primary text-primary-foreground" : "border border-border bg-card text-card-foreground",
            message.status === "failed" && "border-destructive",
            message.status === "cancelled" && "opacity-70",
          )}
        >
          {message.body !== "" && (
            <MarkdownView
              content={message.body}
              breaks={mine}
              wsSlug={wsSlug}
              className={cn("chat-markdown", mine && "text-primary-foreground [&_a]:text-inherit [&_a]:underline [&_strong]:text-inherit [&_em]:text-inherit")}
            />
          )}
          {message.attachments.length > 0 && (
            <div className={cn("flex flex-wrap gap-2", message.body !== "" && "mt-2")}>
              {message.attachments.map((a) => (
                <AttachmentView key={a.id} projectId={projectId} attachment={a} />
              ))}
            </div>
          )}
          {message.status === "streaming" && message.body === "" && (
            <span className="flex items-center gap-1 text-muted-foreground" aria-label={t("feed.thinking")}>
              <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
              <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current [animation-delay:150ms]" />
              <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current [animation-delay:300ms]" />
            </span>
          )}
          {message.truncated && <p className="mt-1 text-xs text-muted-foreground">{t("feed.truncated")}</p>}
        </div>
        <div className="flex items-center gap-2 px-1 text-[11px] text-muted-foreground">
          <time dateTime={message.created_at}>{formatClock(message.created_at)}</time>
          {message.id.startsWith(LOCAL_PREFIX) && <span>{t("feed.sending")}</span>}
          {message.status === "queued" && !message.id.startsWith(LOCAL_PREFIX) && mine && <span>{t("feed.queued")}</span>}
          {message.status === "cancelled" && <span>{t("feed.stopped")}</span>}
          {message.status === "failed" && <span className="text-destructive">{t("feed.failed")}</span>}
          {!mine && message.body !== "" && (
            <button
              type="button"
              onClick={() => void copyText(message.body).then(() => toast.success(t("feed.copied")))}
              className="inline-flex items-center gap-1 hover:text-foreground"
              aria-label={t("feed.copyMessage")}
            >
              <Copy className="h-3 w-3" />
            </button>
          )}
        </div>
      </div>
    </li>
  );
}

export interface ChatFeedProps {
  projectId: string;
  wsSlug?: string;
  messages: ChatMessage[];
  tools: ChatToolEvent[];
  activeTurn: ChatTurnRef | null;
  error: { code: string; turnId: string } | null;
  agentName: string;
  agentColorToken?: string;
  hasMore: boolean;
  loadingMore: boolean;
  loading: boolean;
  loadError: string | null;
  online: boolean;
  onLoadMore: () => void;
  onRetryLoad: () => void;
  onRetryTurn: () => void;
  language: string;
}

export function ChatFeed(props: ChatFeedProps) {
  const { t } = useTranslation("chat");
  const { messages, tools, activeTurn, error } = props;
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const prevHeight = useRef(0);
  const [toolsOpen, setToolsOpen] = useState(false);

  // Follow the stream unless the reader scrolled up to read something.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (prevHeight.current && !stick.current && el.scrollHeight > prevHeight.current && props.loadingMore === false) {
      // older page prepended: keep the reader where they were
      el.scrollTop += el.scrollHeight - prevHeight.current;
    } else if (stick.current) {
      el.scrollTop = el.scrollHeight;
    }
    prevHeight.current = el.scrollHeight;
  }, [messages, tools, activeTurn, error, props.loadingMore]);

  // Images and rendered Markdown grow the feed after the first paint: keep the
  // newest message in view while the reader is following.
  useEffect(() => {
    const el = scroller.current;
    const inner = content.current;
    if (!el || !inner || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (stick.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(inner);
    return () => ro.disconnect();
  }, [props.loading]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };

  if (props.loading) {
    return (
      <div className="flex-1 space-y-3 overflow-hidden p-4" aria-busy="true" data-testid="chat-feed-loading">
        {[60, 40, 70].map((w, i) => (
          <div key={i} className={cn("h-12 animate-pulse rounded-lg bg-muted", i % 2 ? "ml-auto" : "")} style={{ width: `${w}%` }} />
        ))}
      </div>
    );
  }
  if (props.loadError) {
    return (
      <div role="alert" className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
        <AlertCircle className="h-8 w-8 text-destructive" />
        <p className="text-sm">{props.loadError}</p>
        <Button variant="outline" size="sm" onClick={props.onRetryLoad}>
          <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
          {t("feed.retry")}
        </Button>
      </div>
    );
  }

  const last = messages[messages.length - 1];
  const waiting = activeTurn && !(last && last.role === "assistant" && last.status === "streaming");

  return (
    <div ref={scroller} onScroll={onScroll} className="min-h-0 flex-1 overflow-y-auto" data-testid="chat-feed">
      <div ref={content} className="mx-auto flex max-w-3xl flex-col gap-3 p-3 sm:p-4">
        {!props.online && (
          <div role="status" className="flex items-center gap-2 rounded-md border border-border bg-muted px-3 py-2 text-xs">
            <WifiOff className="h-3.5 w-3.5" aria-hidden />
            {t("states.offlineNetwork")}
          </div>
        )}
        {props.hasMore && (
          <Button variant="ghost" size="sm" className="self-center" onClick={props.onLoadMore} disabled={props.loadingMore}>
            {props.loadingMore ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : null}
            {t("feed.older")}
          </Button>
        )}
        {messages.length === 0 && (
          <p className="py-10 text-center text-sm text-muted-foreground" data-testid="chat-empty-thread">
            {t("feed.emptyThread", { agent: props.agentName })}
          </p>
        )}
        <ul className="flex flex-col gap-3">
          {messages.map((m) => (
            <Bubble key={m.id} projectId={props.projectId} message={m} wsSlug={props.wsSlug} />
          ))}
        </ul>

        {activeTurn && (
          <div className="flex items-start gap-2" data-testid="chat-turn-status" data-turn-state={activeTurn.state}>
            <span
              className={cn(
                "mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold text-primary-foreground",
                agentColorClass(props.agentColorToken, props.agentName),
              )}
              aria-hidden
            >
              {agentInitials(props.agentName)}
            </span>
            <div className="min-w-0 flex-1 text-xs text-muted-foreground">
              {activeTurn.state === "queued" ? (
                activeTurn.agent_offline ? (
                  <p data-testid="chat-agent-offline">{t("states.agentOffline", { agent: props.agentName })}</p>
                ) : (
                  <p data-testid="chat-queued">
                    {t("states.queued", { count: activeTurn.queue_position ?? 1 })}
                  </p>
                )
              ) : (
                waiting && <p>{t("states.starting")}</p>
              )}
              {tools.length > 0 && (
                <div data-testid="chat-tools">
                  <button
                    type="button"
                    onClick={() => setToolsOpen((o) => !o)}
                    aria-expanded={toolsOpen}
                    className="flex items-center gap-1 hover:text-foreground"
                  >
                    <ChevronRight className={cn("h-3 w-3 transition-transform", toolsOpen && "rotate-90")} />
                    <Loader2 className="h-3 w-3 animate-spin" aria-hidden />
                    <span className="truncate">{t("feed.working", { label: tools[tools.length - 1]!.label })}</span>
                  </button>
                  {toolsOpen && (
                    <ul className="ml-4 mt-1 space-y-0.5 border-l border-border pl-2">
                      {tools.map((tool, i) => (
                        <li key={i} className="truncate">
                          {tool.label}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
            </div>
          </div>
        )}

        {error && (
          <div role="alert" data-testid="chat-turn-error" className="flex items-start gap-2 rounded-md border border-destructive bg-card p-3 text-sm">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" aria-hidden />
            <div className="min-w-0 flex-1">
              <p>{t(`errors.${error.code}`, { defaultValue: t("errors.generic") })}</p>
              <p className="mt-0.5 font-mono text-xs text-muted-foreground">{error.code}</p>
            </div>
            <Button variant="outline" size="sm" onClick={props.onRetryTurn} data-testid="chat-retry">
              <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
              {t("feed.retry")}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}
