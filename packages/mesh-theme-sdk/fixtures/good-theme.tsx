// A theme view written against the SDK only: must typecheck.
import { Draggable, InlineEdit, TaskLink, useCan } from "../src";
import type { BoardCardView, NavSidebarView, TaskRowView } from "../src";

export const Card: BoardCardView = ({ data, actions }) => (
  <Draggable item={{ kind: "task", id: data.task.id }}>
    <TaskLink taskId={data.task.id}>{data.task.title}</TaskLink>
    <InlineEdit taskId={data.task.id} field="priority">{data.task.priority}</InlineEdit>
    {useCan("task.move") && <button onClick={() => actions.openTask()}>open</button>}
  </Draggable>
);

export const Row: TaskRowView = ({ data }) => <tr><td>{data.task.title}</td></tr>;
export const Sidebar: NavSidebarView = ({ data }) => <nav>{data.items.map((i) => <a key={i.id} href={i.href}>{i.label}</a>)}</nav>;
