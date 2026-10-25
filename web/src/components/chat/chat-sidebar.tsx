import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { AlertCircle, MessageSquarePlus, MoreVertical, Pencil, Plus, Search, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { formatDate, formatTime } from "@/i18n/format";
import { cn } from "@/lib/cn";
import { agentColorClass, agentInitials } from "@/lib/chat/agent-color";
import type { ChatAgent, ChatThread } from "@/lib/chat/types";

function formatWhen(iso: string | null, fallback: string): string {
  const d = new Date(iso ?? fallback);
  if (Number.isNaN(d.getTime())) return "";
  const today = new Date();
  return d.toDateString() === today.toDateString()
    ? formatTime(d)
    : formatDate(d, { day: "numeric", month: "short" });
}

export function AgentBadge({ agent, size = "md" }: { agent: Pick<ChatAgent, "name" | "color_token">; size?: "sm" | "md" }) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex shrink-0 items-center justify-center rounded-full font-semibold text-primary-foreground",
        agentColorClass(agent.color_token, agent.name),
        size === "sm" ? "h-6 w-6 text-[10px]" : "h-8 w-8 text-xs",
      )}
    >
      {agentInitials(agent.name)}
    </span>
  );
}

export interface ChatSidebarProps {
  basePath: string;
  agents: ChatAgent[];
  threads: ChatThread[];
  selectedThreadId?: string;
  loading: boolean;
  error: string | null;
  hasMore: boolean;
  isAdmin: boolean;
  settingsPath: string;
  language: string;
  onRetry: () => void;
  onLoadMore: () => void;
  onNewChat: (agentId: string) => void;
  onRename: (thread: ChatThread) => void;
  onDelete: (thread: ChatThread) => void;
  creatingFor?: string | null;
}

export function ChatSidebar(props: ChatSidebarProps) {
  const { t } = useTranslation("chat");
  const [filter, setFilter] = useState("");
  const q = filter.trim().toLowerCase();

  const threadsByAgent = useMemo(() => {
    const map = new Map<string, ChatThread[]>();
    for (const th of props.threads) {
      if (q && !th.title.toLowerCase().includes(q)) continue;
      const list = map.get(th.agent_id) ?? [];
      list.push(th);
      map.set(th.agent_id, list);
    }
    return map;
  }, [props.threads, q]);

  if (props.loading) {
    return (
      <div className="space-y-2 p-2" aria-busy="true" data-testid="chat-sidebar-loading">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-9 animate-pulse rounded-md bg-muted" />
        ))}
      </div>
    );
  }
  if (props.error) {
    return (
      <div role="alert" className="flex flex-col items-start gap-2 p-3 text-sm">
        <span className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-4 w-4" aria-hidden />
          {props.error}
        </span>
        <Button size="sm" variant="outline" onClick={props.onRetry}>
          {t("feed.retry")}
        </Button>
      </div>
    );
  }
  if (props.agents.length === 0) {
    return (
      <div className="p-4 text-sm" data-testid="chat-no-agents">
        <p className="font-medium">{t("empty.noAgentsTitle")}</p>
        <p className="mt-1 text-muted-foreground">
          {props.isAdmin ? t("empty.noAgentsAdmin") : t("empty.noAgentsMember")}
        </p>
        {props.isAdmin && (
          <Link
            to={props.settingsPath}
            className="mt-3 inline-flex h-8 items-center rounded-md border border-input bg-background px-3 text-sm hover:bg-muted"
          >
            {t("empty.openSettings")}
          </Link>
        )}
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="relative px-2 pb-2">
        <Search className="pointer-events-none absolute left-4 top-2.5 h-3.5 w-3.5 text-muted-foreground" aria-hidden />
        <Input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={t("sidebar.filter")}
          aria-label={t("sidebar.filter")}
          className="h-8 pl-8 text-sm"
        />
      </div>
      <nav className="min-h-0 flex-1 overflow-y-auto px-2 pb-3" aria-label={t("sidebar.heading")}>
        {props.agents.map((agent) => {
          const list = threadsByAgent.get(agent.agent_id) ?? [];
          if (q && list.length === 0) return null;
          return (
            <section key={agent.agent_id} className="mb-3" data-testid="chat-agent" data-agent-id={agent.agent_id}>
              <div className="flex items-center gap-2 py-1">
                <AgentBadge agent={agent} />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{agent.name}</p>
                  <p className="flex items-center gap-1 text-[11px] text-muted-foreground">
                    <span
                      aria-hidden
                      className={cn(
                        "h-1.5 w-1.5 rounded-full",
                        agent.presence === "online" ? "bg-success" : agent.presence === "busy" ? "bg-warning" : "bg-muted-foreground",
                      )}
                    />
                    {t(`sidebar.presence.${agent.presence}`)}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={() => props.onNewChat(agent.agent_id)}
                  disabled={props.creatingFor === agent.agent_id}
                  aria-label={t("sidebar.newChatWith", { agent: agent.name })}
                  title={t("sidebar.newChat")}
                  data-testid="chat-new"
                >
                  <Plus className="h-4 w-4" />
                </Button>
              </div>
              {list.length === 0 ? (
                <p className="ml-10 py-1 text-xs text-muted-foreground">{t("empty.noChats")}</p>
              ) : (
                <ul className="ml-4 border-l border-border pl-2">
                  {list.map((th) => {
                    const active = th.id === props.selectedThreadId;
                    return (
                      <li
                        key={th.id}
                        data-testid="chat-thread-item"
                        data-thread-id={th.id}
                        className={cn("group flex items-center rounded-md", active ? "bg-muted font-medium" : "hover:bg-muted/60")}
                      >
                        <Link
                          to={`${props.basePath}/${th.id}`}
                          aria-current={active ? "page" : undefined}
                          className="flex min-w-0 flex-1 flex-col px-2 py-1.5"
                        >
                          <span className="truncate text-sm">{th.title || t("sidebar.untitled")}</span>
                          <span className="text-[11px] text-muted-foreground">
                            {formatWhen(th.last_message_at, th.created_at)}
                          </span>
                        </Link>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-8 w-8 shrink-0 opacity-100 md:opacity-0 md:group-hover:opacity-100 md:focus-visible:opacity-100"
                              aria-label={t("sidebar.chatMenu", { title: th.title || t("sidebar.untitled") })}
                              data-testid="chat-thread-menu"
                            >
                              <MoreVertical className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onClick={() => props.onRename(th)}>
                              <Pencil className="mr-2 h-4 w-4" />
                              {t("sidebar.rename")}
                            </DropdownMenuItem>
                            <DropdownMenuItem onClick={() => props.onDelete(th)} className="text-destructive">
                              <Trash2 className="mr-2 h-4 w-4" />
                              {t("sidebar.delete")}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </li>
                    );
                  })}
                </ul>
              )}
            </section>
          );
        })}
        {props.hasMore && (
          <Button variant="ghost" size="sm" className="w-full" onClick={props.onLoadMore}>
            {t("sidebar.more")}
          </Button>
        )}
      </nav>
    </div>
  );
}

export function NoThreadSelected({ onNewChat, agents }: { onNewChat: (agentId: string) => void; agents: ChatAgent[] }) {
  const { t } = useTranslation("chat");
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center" data-testid="chat-no-thread">
      <MessageSquarePlus className="h-10 w-10 text-muted-foreground" aria-hidden />
      <p className="text-sm text-muted-foreground">{t("empty.pickChat")}</p>
      {agents[0] && (
        <Button size="sm" onClick={() => onNewChat(agents[0]!.agent_id)}>
          <Plus className="mr-1.5 h-3.5 w-3.5" />
          {t("sidebar.newChatWith", { agent: agents[0].name })}
        </Button>
      )}
    </div>
  );
}
