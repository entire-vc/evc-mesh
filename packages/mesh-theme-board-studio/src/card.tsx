import { Can, Draggable, InlineEdit, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";
import type { BoardCardSettings, BoardCardView, BoardCardViewData, FieldDescriptor, TaskData } from "@entire-vc/mesh-theme-sdk";
import { dimmed, initials, nextDay, raised, say } from "./logic";

const text = (v: unknown): string => (v == null || v === "" ? "" : Array.isArray(v) ? v.join(", ") : typeof v === "boolean" ? (v ? "✓" : "") : String(v));
const day = (iso: string) => iso.slice(0, 10);

function Meta({ f, task, data, locale }: { f: FieldDescriptor; task: TaskData; data: BoardCardViewData; locale: string }) {
  switch (f.key) {
    case "assignee":
      return (
        <span className="studio-who" data-field="assignee">
          <span className="studio-avatar" aria-hidden="true">{initials(task.assignee?.name ?? "")}</span>
          <span className="studio-who-name">{task.assignee?.name ?? say(locale, "unassigned")}</span>
        </span>
      );
    case "due_date":
      return task.dueDate ? (
        <span className="studio-due" data-overdue={task.overdue || undefined} data-field="due_date">
          {say(locale, task.overdue ? "overdue" : "due", { date: day(task.dueDate) })}
        </span>
      ) : null;
    case "gate":
      return task.gate ? (
        <span className="studio-flag" data-field="gate" title={task.gate.reason || undefined}>{say(locale, "gate")}</span>
      ) : null;
    case "labels":
      return task.labels.length ? (
        <ul className="studio-labels" data-field="labels">
          {task.labels.map((l) => <li key={l}>{l}</li>)}
        </ul>
      ) : null;
    case "subtasks":
      return task.counts.subtasks ? <span className="studio-count" data-field="subtasks">{say(locale, "subtasks", { count: task.counts.subtasks })}</span> : null;
    case "estimate":
      return task.estimatedHours != null ? <span className="studio-count" data-field="estimate">{task.estimatedHours}h</span> : null;
    default: {
      if (!f.key.startsWith("cf:")) return null;
      const v = text(task.customFields[f.key.slice(3)]);
      return v ? <span className="studio-count" data-field={f.key}>{f.label}: {v}</span> : null;
    }
  }
}

/** The card body; also what the drag preview shows (`plain`: no editor, no link, no actions). */
function Body({ data, actions, themeSettings, locale, plain }: { data: BoardCardViewData; actions?: Parameters<BoardCardView>[0]["actions"]; themeSettings: BoardCardSettings; locale: string; plain?: boolean }) {
  const { task } = data;
  const cover = themeSettings.cover_style === "none" || themeSettings.cover_style === "strip" ? themeSettings.cover_style : "block";
  const dim = dimmed(themeSettings.studio_focus as string | null | undefined, task.assignee?.id);
  return (
    <article className="studio-card" data-priority={task.priority} data-cover={cover} data-dim={dim || undefined} data-testid="studio-card">
      {cover !== "none" && <span className="studio-cover" aria-hidden="true" />}
      <div className="studio-body">
        <h3 className="studio-title">
          {plain ? task.title : (
            <InlineEdit taskId={task.id} field="title">
              <TaskLink taskId={task.id}>{task.title}</TaskLink>
            </InlineEdit>
          )}
        </h3>
        <div className="studio-meta">
          {data.fields.map((f) => <Meta key={f.key} f={f} task={task} data={data} locale={locale} />)}
          {/* The task.badge insertion point: extensions (GitLab, GitHub…) draw their badges here. */}
          <TaskBadges task={task} />
        </div>
        {!plain && actions && (
          <Can permission="task.edit" resource={{ kind: "task", id: task.id }}>
            <div className="studio-actions" role="group" aria-label={task.title}>
              {actions.quickEdit && (
                <>
                  <button type="button" className="studio-act" data-action="raise" disabled={task.priority === "urgent"} onClick={() => void actions.quickEdit?.({ field: "priority", value: raised(task.priority) })}>
                    {say(locale, "raise")}
                  </button>
                  <button type="button" className="studio-act" data-action="postpone" onClick={() => void actions.quickEdit?.({ field: "due_date", value: nextDay(task.dueDate) })}>
                    {say(locale, "postpone")}
                  </button>
                </>
              )}
              <button type="button" className="studio-act" data-action="open" onClick={() => actions.openTask()}>
                {say(locale, "open")}
              </button>
            </div>
          </Can>
        )}
      </div>
    </article>
  );
}

export const Card: BoardCardView = ({ data, actions, themeSettings, ctx }) => {
  // The overlay has no Draggable: a second draggable would be a second item under the pointer.
  if (data.isOverlay) return <Body data={data} themeSettings={themeSettings} locale={ctx.locale} plain />;
  return (
    <Draggable item={{ kind: "task", id: data.task.id }} label={data.task.title} data-testid="task-card" data-task-id={data.task.id}>
      <Body data={data} actions={actions} themeSettings={themeSettings} locale={ctx.locale} />
    </Draggable>
  );
};
