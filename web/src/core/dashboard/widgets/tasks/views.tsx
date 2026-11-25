// mesh-default views of the "Tasks and triage" group (PRD §5.8): my-tasks, triage-queue, task-list. They speak the
// dashboard.widget.<id> contract: rows arrive as `data.result` (the core read them with the instance settings, filtered
// on the server, so the count is the real total) and a view never fetches. The card follows its cell through
// container queries, never the window: at 3 cells nothing is cut or scrolls sideways.
import { useMemo, type ElementType, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArrowRight, CheckSquare, Inbox, ListFilter } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { ProjectLabel } from "@/components/ui/project-label";
import { InlineEdit, ListedTasksProvider, TaskLink, useCan } from "@/core/primitives";
import { cn } from "@/lib/cn";
import type { Task } from "@/types";
import type { Async, DashboardWidgetView, TasksData } from "../../types";
import { useWidgetTitle } from "../../config";
import { PRIORITY_COLORS, priorityLabel } from "../legacy/views";
import { Body, WidgetEmpty, formatShort } from "../legacy/parts";

// -- shared pieces -------------------------------------------------------------------------------------------------

function PriorityBadge({ priority }: { priority: string }) {
  const { t } = useTranslation("notifications");
  return (
    <span className={cn("shrink-0 rounded-full px-1.5 py-0.5 text-[10px] font-medium", PRIORITY_COLORS[priority] ?? PRIORITY_COLORS.none)}>
      {priorityLabel(t, priority)}
    </span>
  );
}

function Shell({ title: typeTitle, icon: Icon, count, children }: { title: string; icon: ElementType; count?: number; children: ReactNode }) {
  const title = useWidgetTitle(typeTitle);
  return (
    <Card className="@container h-full min-w-0">
      <CardContent className="flex h-full max-h-[40rem] min-w-0 flex-col pt-4">
        <div className="mb-3 flex shrink-0 items-center gap-1.5">
          <h3 className="flex min-w-0 items-center gap-1.5 text-sm font-semibold">
            <Icon className="h-4 w-4 shrink-0" />
            <span className="truncate">{title}</span>
          </h3>
          {count !== undefined ? (
            <span className="shrink-0 rounded-full bg-muted px-1.5 text-[10px] font-medium text-muted-foreground" data-testid="widget-total">
              {count}
            </span>
          ) : null}
        </div>
        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto overflow-x-hidden">{children}</div>
      </CardContent>
    </Card>
  );
}

/** Loading, error and empty are three different things (CT-widget-01). */
function States({ result, empty, icon, children }: { result: Async<TasksData>; empty: string; icon: ElementType; children: (v: TasksData) => ReactNode }) {
  return (
    <Body result={result} rows={3} isEmpty={(v) => v.items.length === 0} empty={<WidgetEmpty icon={icon} message={empty} />}>
      {children}
    </Body>
  );
}

const total = (r: Async<TasksData>) => (r.state === "ready" ? r.value.total : undefined);

/** A row: the title is a real link to the task (Ctrl+click opens a tab); the rest drops out as the cell narrows. */
function TaskRow({ task, project, icon, edit, extra }: { task: Task; project: string; icon?: string; edit: boolean; extra?: ReactNode }) {
  const { t } = useTranslation("notifications");
  const due = task.due_date ? task.due_date.slice(0, 10) : "";
  return (
    <li className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-muted" data-task-row={task.id}>
      {edit ? (
        <InlineEdit taskId={task.id} field="priority">
          <PriorityBadge priority={task.priority} />
        </InlineEdit>
      ) : (
        <PriorityBadge priority={task.priority} />
      )}
      <TaskLink taskId={task.id} className="min-w-0 flex-1 truncate font-medium hover:underline">
        {task.title}
      </TaskLink>
      <ProjectLabel icon={icon} name={project} className="hidden shrink-0 text-[10px] text-muted-foreground @xs:inline-flex @xs:max-w-24 @md:max-w-40" />
      {edit ? (
        <span className="hidden shrink-0 text-[10px] text-muted-foreground @sm:inline">
          <InlineEdit taskId={task.id} field="due_date">
            <span title={t("dashboard.tasks.due")}>{due || "—"}</span>
          </InlineEdit>
        </span>
      ) : due ? (
        <span className="hidden shrink-0 text-[10px] text-muted-foreground @sm:inline" title={t("dashboard.tasks.due")}>
          {due}
        </span>
      ) : null}
      {extra}
    </li>
  );
}

function TaskRows({ data, edit }: { data: TasksData; edit: boolean }) {
  const { t } = useTranslation("notifications");
  const byId = useMemo(() => Object.fromEntries(data.items.map((x) => [x.id, x])), [data.items]);
  return (
    <ListedTasksProvider tasks={byId}>
      <ul className="space-y-0.5">
        {data.items.map((task) => (
          <TaskRow key={task.id} task={task} edit={edit} icon={data.projectIcons?.[task.project_id]} project={data.projectNames[task.project_id] ?? t("dashboard.tasks.unknownProject")} />
        ))}
      </ul>
    </ListedTasksProvider>
  );
}

// -- my-tasks ------------------------------------------------------------------------------------------------------

export const MyTasksView: DashboardWidgetView<"my-tasks"> = ({ data }) => {
  const { t } = useTranslation("notifications");
  const result = data.result;
  const edit = useCan("task.edit");
  return (
    <Shell title={t("dashboard.myTasks.title")} icon={CheckSquare} count={total(result)}>
      <States result={result} icon={CheckSquare} empty={t("dashboard.myTasks.empty")}>
        {(v) => <TaskRows data={v} edit={edit} />}
      </States>
    </Shell>
  );
};

// -- triage-queue --------------------------------------------------------------------------------------------------

export const TriageQueueView: DashboardWidgetView<"triage-queue"> = ({ data, actions }) => {
  const { t } = useTranslation("notifications");
  const shown = data.result;
  // the core's move dialog; the action exists only with task.move
  const moveTask = actions.moveTask;
  return (
    <Shell title={t("dashboard.tasks.triage.title")} icon={Inbox} count={total(shown)}>
      <States result={shown} icon={Inbox} empty={t("dashboard.triage.empty")}>
        {(v) => (
          <ul className="space-y-1">
            {v.items.map((task) => (
              <li key={task.id} className="min-w-0 rounded-md px-2 py-1.5 text-xs hover:bg-muted" data-task-row={task.id}>
                <div className="flex min-w-0 items-center gap-2">
                  <PriorityBadge priority={task.priority} />
                  <TaskLink taskId={task.id} className="min-w-0 flex-1 truncate font-medium hover:underline">
                    {task.title}
                  </TaskLink>
                  <TaskLink taskId={task.id} className="hidden shrink-0 text-[10px] text-muted-foreground hover:text-foreground @sm:inline">
                    {t("dashboard.tasks.triage.open")}
                  </TaskLink>
                  {moveTask ? (
                    <button
                      type="button"
                      className="flex shrink-0 items-center gap-0.5 rounded px-1 text-[10px] text-muted-foreground hover:bg-background hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                      onClick={() => moveTask(task.id)}
                      aria-label={t("dashboard.tasks.triage.moveTask", { title: task.title })}
                    >
                      {t("dashboard.tasks.triage.move")}
                      <ArrowRight className="h-3 w-3" />
                    </button>
                  ) : null}
                </div>
                <p className="mt-0.5 flex min-w-0 flex-wrap gap-x-1.5 text-[10px] text-muted-foreground">
                  <ProjectLabel icon={v.projectIcons?.[task.project_id]} name={v.projectNames[task.project_id] ?? t("dashboard.tasks.unknownProject")} />
                  {task.created_by_name ? <span className="max-w-full truncate">· {t("dashboard.tasks.triage.by", { name: task.created_by_name })}</span> : null}
                  <span title={task.created_at}>· {formatShort(task.created_at)}</span>
                </p>
              </li>
            ))}
          </ul>
        )}
      </States>
    </Shell>
  );
};

// -- task-list -----------------------------------------------------------------------------------------------------

export const TaskListView: DashboardWidgetView<"task-list"> = ({ data }) => {
  const { t } = useTranslation("notifications");
  const result = data.result;
  const edit = useCan("task.edit");
  return (
    <Shell title={t("dashboard.tasks.list.title")} icon={ListFilter} count={total(result)}>
      <States result={result} icon={ListFilter} empty={t("dashboard.tasks.list.empty")}>
        {(v) => <TaskRows data={v} edit={edit} />}
      </States>
    </Shell>
  );
};

export const views = { "my-tasks": MyTasksView, "triage-queue": TriageQueueView, "task-list": TaskListView };
