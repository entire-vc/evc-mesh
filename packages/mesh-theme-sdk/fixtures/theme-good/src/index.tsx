import { Draggable, InlineEdit, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";
import type { BoardCardView, FieldDescriptor, TaskData } from "@entire-vc/mesh-theme-sdk";
import "./theme.css";

const text = (v: unknown): string => (v == null || v === "" ? "" : Array.isArray(v) ? v.join(", ") : String(v));

// One chosen field, or nothing: the card draws what `data.fields` names, in that order.
function field(f: FieldDescriptor, task: TaskData) {
  if (f.key === "labels") {
    return task.labels.length ? (
      <ul key="labels" className="card-labels">
        {task.labels.map((l) => <li key={l}>{l}</li>)}
      </ul>
    ) : null;
  }
  if (f.key === "due_date") {
    return task.dueDate ? (
      <span key="due" className={task.overdue ? "card-due card-overdue" : "card-due"}>
        {task.overdue ? "Overdue: " : "Due: "}{task.dueDate.slice(0, 10)}
      </span>
    ) : null;
  }
  if (f.key.startsWith("cf:")) {
    const value = text(task.customFields[f.key.slice(3)]);
    return value ? <span key={f.key} data-field={f.key}>{f.label}: {value}</span> : null;
  }
  return null;
}

const Card: BoardCardView = ({ data }) => {
  const { task } = data;
  const body = (
    <div className="card">
      <div className="card-title">
        {/* The drag preview is a picture of the card: no editor, no link. */}
        {data.isOverlay ? task.title : (
          <InlineEdit taskId={task.id} field="title">
            <TaskLink taskId={task.id}>{task.title}</TaskLink>
          </InlineEdit>
        )}
        <TaskBadges task={task} />
      </div>
      {data.fields.map((f) => field(f, task))}
    </div>
  );
  // The overlay has no Draggable: a second draggable would be a second item under the pointer.
  return data.isOverlay ? body : <Draggable item={{ kind: "task", id: task.id }} label={task.title}>{body}</Draggable>;
};

export default { views: { "board.card": { contract: "^1", view: Card } } };
