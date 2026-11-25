// mesh-default views of the «Коммуникации» widgets. Data and actions arrive through the slot contract; the body
// scrolls inside the card, the header (title, filters) stays. Widths follow the cell (container queries), not the window.
import { useMemo, useState, type ElementType, type ReactNode } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { AtSign, FileText, ListTodo, MessageSquare } from "lucide-react";
import { AgentShortTag } from "@/components/agent-short-tag";
import { Card, CardContent } from "@/components/ui/card";
import { ProjectLabel } from "@/components/ui/project-label";
import { cn } from "@/lib/cn";
import { useProjectStore } from "@/stores/project";
import type { CommentItem, DashboardWidgetView, MentionItem } from "../../types";
import { Body, WidgetEmpty, formatShort, plainClick } from "../legacy/parts";
import { boolSetting, numberSetting, stringSetting, useWidgetConfig, useWidgetTitle } from "../../config";

/** Markdown reduced to a line of text: a row is a link, so it cannot hold the links and mentions of rendered markdown. */
export function plainPreview(md: string, max = 240): string {
  const t = md
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/[`*_>#~]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  return t.length > max ? `${t.slice(0, max)}…` : t;
}

function Shell({ title: typeTitle, icon: Icon, controls, children }: { title: string; icon: ElementType; controls?: ReactNode; children: ReactNode }) {
  const title = useWidgetTitle(typeTitle);
  return (
    <Card className="@container min-w-0">
      <CardContent className="flex max-h-[32rem] min-w-0 flex-col pt-4">
        <div className="mb-3 flex shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-2">
          <h3 className="flex min-w-0 items-center gap-1.5 text-sm font-semibold">
            <Icon className="h-4 w-4 shrink-0" />
            <span className="truncate">{title}</span>
          </h3>
          {controls ? <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted-foreground">{controls}</div> : null}
        </div>
        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto overflow-x-hidden">{children}</div>
      </CardContent>
    </Card>
  );
}

function ProjectSelect({ value, onChange, projects }: { value: string; onChange: (v: string) => void; projects: { id: string; name: string }[] }) {
  const { t } = useTranslation("notifications");
  return (
    <select
      aria-label={t("dashboard.comms.project")}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-7 max-w-full min-w-0 rounded-md border border-input bg-background px-2 text-xs text-foreground"
    >
      <option value="">{t("dashboard.comms.allProjects")}</option>
      {projects.map((p) => (
        <option key={p.id} value={p.id}>
          {p.name}
        </option>
      ))}
    </select>
  );
}

function SectionLabel({ label, count, accent }: { label: string; count: number; accent?: boolean }) {
  return (
    <div className="flex items-center gap-2 px-2 pb-1 pt-2">
      <span className={cn("text-[10px] font-semibold uppercase tracking-wide", accent ? "text-primary" : "text-muted-foreground")}>{label}</span>
      <span className="text-[10px] text-muted-foreground">({count})</span>
      <div className={cn("h-px flex-1", accent ? "bg-primary/20" : "bg-border")} />
    </div>
  );
}

function MentionRow({ m, onOpen, fallbackTo }: { m: MentionItem; onOpen: (m: MentionItem, o?: { newTab?: boolean }) => void; fallbackTo: string }) {
  const { t } = useTranslation("notifications");
  const SourceIcon = m.source === "document" ? FileText : ListTodo;
  const isNew = m.state === "new";
  const isShown = m.state === "shown";
  const preview = m.body ? plainPreview(m.body) : "";
  return (
    <li>
      <Link
        to={m.href ?? fallbackTo}
        data-testid={`mention-card-${m.source}`}
        onClick={(e) => {
          if (!plainClick(e)) return;
          e.preventDefault();
          onOpen(m);
        }}
        className={cn(
          "flex min-w-0 items-start gap-2 rounded-md border border-transparent px-2 py-1.5 text-xs hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-tone-info-500/40",
          isNew && "border-primary/30 bg-primary/5",
          isShown && "opacity-80",
          m.question && "border-l-4 border-l-tone-warning-400 bg-tone-warning-50/50 dark:bg-tone-warning-950/20",
        )}
      >
        <span className={cn("mt-1 h-1.5 w-1.5 shrink-0 rounded-full", isNew && "bg-tone-danger-500")} aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <SourceIcon
              className="h-3 w-3 shrink-0 text-muted-foreground"
              aria-label={m.source === "document" ? t("dashboard.mentions.document") : t("dashboard.mentions.task")}
            />
            {m.question ? <span aria-label={t("dashboard.mentions.question")} title={t("dashboard.mentions.question")}>❓</span> : null}
            <span className={cn("min-w-0 flex-1 truncate", !m.seen && "font-medium", m.seen && "text-muted-foreground")}>{m.title}</span>
            <span className="shrink-0 text-[10px] text-muted-foreground">{formatShort(m.at)}</span>
          </div>
          {m.authorName ? (
            <p className="truncate text-[11px] text-muted-foreground">
              {m.authorName}
              {m.authorId ? <AgentShortTag id={m.authorId} /> : null}
            </p>
          ) : null}
          {preview ? <p className="line-clamp-2 break-words text-[11px] text-muted-foreground @max-xs:hidden">{preview}</p> : null}
        </div>
      </Link>
    </li>
  );
}

export const MentionsWidgetView: DashboardWidgetView<"mentions"> = ({ data, actions, ctx }) => {
  const { t } = useTranslation("notifications");
  const cfg = useWidgetConfig();
  const [onlyNew, setOnlyNew] = useState(boolSetting(cfg, "onlyNew"));
  const limit = numberSetting(cfg, "limit", 10, 5, 50);
  // A mention without its own address opens the home dashboard (the Activity page is gone, §5.9).
  const fallbackTo = ctx.wsSlug ? `/w/${ctx.wsSlug}/dashboard` : "/";
  const failed = data.result.state === "ready" ? data.result.value.failedSources : [];
  const failedText = (f: typeof failed) =>
    f.length > 1 ? t("dashboard.mentions.failedAll") : f[0] === "document" ? t("dashboard.mentions.failedDocument") : t("dashboard.mentions.failedTask");
  return (
    <Shell
      title={t("dashboard.mentions.title")}
      icon={AtSign}
      controls={
        <label className="flex cursor-pointer items-center gap-1.5">
          <input type="checkbox" checked={onlyNew} onChange={(e) => setOnlyNew(e.target.checked)} />
          {t("dashboard.mentions.onlyNew")}
        </label>
      }
    >
      <Body
        result={data.result}
        rows={4}
        isEmpty={(v) => (onlyNew ? v.items.filter((m) => !m.seen) : v.items).length === 0}
        empty={
          <WidgetEmpty
            icon={AtSign}
            message={
              failed.length > 0
                ? `${failedText(failed)} ${t("dashboard.mentions.incomplete")}`
                : onlyNew
                  ? t("dashboard.mentions.emptyNew")
                  : t("dashboard.mentions.empty")
            }
          />
        }
      >
        {(v) => {
          const items = (onlyNew ? v.items.filter((m) => !m.seen) : v.items).slice(0, limit);
          const fresh = items.filter((m) => m.fresh && !m.seen);
          const rest = items.filter((m) => !(m.fresh && !m.seen));
          const row = (m: MentionItem) => <MentionRow key={m.id} m={m} onOpen={actions.openMention} fallbackTo={fallbackTo} />;
          return (
            <div>
              {failed.length > 0 ? (
                <p
                  data-testid="mentions-load-failed"
                  className="mb-2 rounded-md border border-tone-warning-400/40 bg-tone-warning-50/50 px-3 py-2 text-xs text-muted-foreground dark:bg-tone-warning-950/20"
                >
                  {failedText(failed)} {t("dashboard.mentions.partial")}
                </p>
              ) : null}
              {fresh.length > 0 ? (
                <>
                  <SectionLabel label={t("dashboard.mentions.sectionNew")} count={fresh.length} accent />
                  <ul className="space-y-0.5">{fresh.map(row)}</ul>
                </>
              ) : null}
              {rest.length > 0 ? (
                <>
                  {fresh.length > 0 ? <SectionLabel label={t("dashboard.mentions.sectionShown")} count={rest.length} /> : null}
                  <ul className="space-y-0.5">{rest.map(row)}</ul>
                </>
              ) : null}
            </div>
          );
        }}
      </Body>
    </Shell>
  );
};

function CommentRow({ c, showAuthor, onOpen }: { c: CommentItem; showAuthor: boolean; onOpen: (c: CommentItem, o?: { newTab?: boolean }) => void }) {
  const { t } = useTranslation("notifications");
  return (
    <li>
      <Link
        to={c.href ?? "#"}
        onClick={(e) => {
          if (!plainClick(e)) return;
          e.preventDefault();
          onOpen(c);
        }}
        className="flex min-w-0 flex-col gap-0.5 rounded-md px-2 py-1.5 text-xs hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-tone-info-500/40"
      >
        <div className="flex min-w-0 items-center gap-1.5">
          <span className="min-w-0 flex-1 truncate font-medium">{c.title}</span>
          {c.internal ? (
            <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground">{t("dashboard.comms.internal")}</span>
          ) : null}
          <span className="shrink-0 text-[10px] text-muted-foreground">{formatShort(c.at)}</span>
        </div>
        <p className="truncate text-[11px] text-muted-foreground">
          {showAuthor ? (
            <>
              {c.authorName}
              <AgentShortTag id={c.authorId} type={c.authorKind} />
              {" · "}
            </>
          ) : null}
          <ProjectLabel icon={c.projectIcon} name={c.projectName} />
        </p>
        <p className="line-clamp-2 break-words text-[11px] text-muted-foreground @max-xs:hidden">{plainPreview(c.body)}</p>
      </Link>
    </li>
  );
}

function CommentsView({
  title,
  icon,
  emptyKey,
  showAuthor,
  data,
  actions,
  withInternalToggle,
}: {
  title: string;
  icon: ElementType;
  emptyKey: string;
  showAuthor: boolean;
  data: { result: import("../../types").Async<import("../../types").CommentsData> };
  actions: { openComment(c: CommentItem, o?: { newTab?: boolean }): void; loadMore(): void };
  withInternalToggle?: boolean;
}) {
  const { t } = useTranslation("notifications");
  const cfg = useWidgetConfig();
  const { projects } = useProjectStore();
  const [projectId, setProjectId] = useState(stringSetting(cfg, "projectId") ?? "");
  const [internal, setInternal] = useState(boolSetting(cfg, "showInternal", false));
  const ready = data.result.state === "ready" ? data.result.value : null;
  // The server decides who sees internal notes: the toggle exists only when the list actually has some.
  const hasInternal = !!withInternalToggle && !!ready?.items.some((c) => c.internal);
  const projectOptions = useMemo(() => projects.map((p) => ({ id: p.id, name: p.name })), [projects]);
  return (
    <Shell
      title={title}
      icon={icon}
      controls={
        <>
          {hasInternal ? (
            <label className="flex cursor-pointer items-center gap-1.5">
              <input type="checkbox" checked={internal} onChange={(e) => setInternal(e.target.checked)} />
              {t("dashboard.comms.showInternal")}
            </label>
          ) : null}
          <ProjectSelect value={projectId} onChange={setProjectId} projects={projectOptions} />
        </>
      }
    >
      <Body
        result={data.result}
        rows={4}
        isEmpty={(v) => v.items.length === 0}
        empty={<WidgetEmpty icon={MessageSquare} message={t(emptyKey)} />}
      >
        {(v) => {
          const items = v.items.filter((c) => (!projectId || c.projectId === projectId) && (internal || !c.internal));
          return (
            <>
              {items.length === 0 ? (
                <WidgetEmpty icon={MessageSquare} message={t("dashboard.comms.noneInProject")} />
              ) : (
                <ul className="space-y-0.5">
                  {items.map((c) => (
                    <CommentRow key={c.id} c={c} showAuthor={showAuthor} onOpen={actions.openComment} />
                  ))}
                </ul>
              )}
              {v.hasMore ? (
                <button
                  type="button"
                  onClick={actions.loadMore}
                  disabled={v.loadingMore}
                  className="mt-2 w-full rounded-md py-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
                >
                  {v.loadingMore ? t("dashboard.comms.loading") : t("dashboard.comms.more")}
                </button>
              ) : null}
            </>
          );
        }}
      </Body>
    </Shell>
  );
}

export const MyCommentsWidgetView: DashboardWidgetView<"my-comments"> = ({ data, actions }) => {
  const { t } = useTranslation("notifications");
  return (
    <CommentsView title={t("dashboard.myComments.title")} icon={MessageSquare} emptyKey="dashboard.myComments.empty" showAuthor={false} data={data} actions={actions} />
  );
};

export const RecentCommentsWidgetView: DashboardWidgetView<"recent-comments"> = ({ data, actions }) => {
  const { t } = useTranslation("notifications");
  return (
    <CommentsView
      title={t("dashboard.recentComments.title")}
      icon={MessageSquare}
      emptyKey="dashboard.recentComments.empty"
      showAuthor
      data={data}
      actions={actions}
      withInternalToggle
    />
  );
};

export const views = { "my-comments": MyCommentsWidgetView, "recent-comments": RecentCommentsWidgetView };
