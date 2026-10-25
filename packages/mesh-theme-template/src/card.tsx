import { Draggable, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";
import type { BoardCardView, FieldDescriptor, TaskData } from "@entire-vc/mesh-theme-sdk";
import { dueLabel, text } from "./fields";

// The card draws the fields `data.fields` names, in that order: the board's field picker is the user's choice.
function field(f: FieldDescriptor, task: TaskData) {
  if (f.key === "labels") {
    return task.labels.length ? (
      <ul key="labels" className="my-card-labels">
        {task.labels.map((l) => <li key={l}>{l}</li>)}
      </ul>
    ) : null;
  }
  if (f.key === "due_date") {
    const label = dueLabel(task.dueDate, task.overdue);
    return label ? <span key="due" className={task.overdue ? "my-card-due my-card-overdue" : "my-card-due"}>{label}</span> : null;
  }
  if (f.key.startsWith("cf:")) {
    const value = text(task.customFields[f.key.slice(3)]);
    return value ? <span key={f.key} data-field={f.key}>{f.label}: {value}</span> : null;
  }
  return null;
}

export const Card: BoardCardView = ({ data }) => {
  const { task } = data;
  const body = (
    <div className="my-card">
      <div className="my-card-title">
        {/* The drag preview is a picture of the card: no editor, no link. */}
        {data.isOverlay ? task.title : (
          <TaskLink taskId={task.id}>{task.title}</TaskLink>
        )}
        {/* The task.badge insertion point: extensions (GitLab, GitHub…) draw their badges here. */}
        <TaskBadges task={task} />
      </div>
      {data.fields.map((f) => field(f, task))}
    </div>
  );
  // The overlay has no Draggable: a second draggable would be a second item under the pointer.
  return data.isOverlay ? body : <Draggable item={{ kind: "task", id: task.id }} label={task.title}>{body}</Draggable>;
};
