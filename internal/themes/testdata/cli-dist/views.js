// src/index.tsx
import { Draggable, InlineEdit, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";
import { jsx, jsxs } from "react/jsx-runtime";
var text = (v) => v == null || v === "" ? "" : Array.isArray(v) ? v.join(", ") : String(v);
function field(f, task) {
  if (f.key === "labels") {
    return task.labels.length ? /* @__PURE__ */ jsx("ul", { className: "card-labels", children: task.labels.map((l) => /* @__PURE__ */ jsx("li", { children: l }, l)) }, "labels") : null;
  }
  if (f.key === "due_date") {
    return task.dueDate ? /* @__PURE__ */ jsxs("span", { className: task.overdue ? "card-due card-overdue" : "card-due", children: [
      task.overdue ? "Overdue: " : "Due: ",
      task.dueDate.slice(0, 10)
    ] }, "due") : null;
  }
  if (f.key.startsWith("cf:")) {
    const value = text(task.customFields[f.key.slice(3)]);
    return value ? /* @__PURE__ */ jsxs("span", { "data-field": f.key, children: [
      f.label,
      ": ",
      value
    ] }, f.key) : null;
  }
  return null;
}
var Card = ({ data }) => {
  const { task } = data;
  const body = /* @__PURE__ */ jsxs("div", { className: "card", children: [
    /* @__PURE__ */ jsxs("div", { className: "card-title", children: [
      data.isOverlay ? task.title : /* @__PURE__ */ jsx(InlineEdit, { taskId: task.id, field: "title", children: /* @__PURE__ */ jsx(TaskLink, { taskId: task.id, children: task.title }) }),
      /* @__PURE__ */ jsx(TaskBadges, { task })
    ] }),
    data.fields.map((f) => field(f, task))
  ] });
  return data.isOverlay ? body : /* @__PURE__ */ jsx(Draggable, { item: { kind: "task", id: task.id }, label: task.title, children: body });
};
var src_default = { views: { "board.card": { contract: "^1", view: Card } } };
export {
  src_default as default
};
