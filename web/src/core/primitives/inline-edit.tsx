import { useState, type KeyboardEvent, type ReactNode } from "react";
import { apiErrorMessage } from "@/lib/api-error";
import { useMemberStore } from "@/stores/member";
import { useTaskStore } from "@/stores/task";
import type { Task } from "@/types";
import { useCoreActions } from "./actions";
import { useCan } from "./permissions";
import type { Id, QuickEditField } from "./types";

const PRIORITIES = ["urgent", "high", "medium", "low", "none"] as const;

function currentValue(task: Task | undefined, field: QuickEditField): string {
  if (!task) return "";
  switch (field) {
    case "title":
      return task.title;
    case "priority":
      return task.priority;
    case "due_date":
      return task.due_date ? task.due_date.slice(0, 10) : "";
    case "labels":
      return (task.labels ?? []).join(", ");
    case "assignee":
      return task.assignee_id ?? "";
    default: {
      const v = (task.custom_fields as Record<string, unknown> | undefined)?.[field.slice(3)];
      return v == null ? "" : String(v);
    }
  }
}

interface AssigneeOption {
  id: Id;
  type: "user" | "agent";
  label: string;
}

function parseValue(field: QuickEditField, raw: string, assignees: AssigneeOption[]): unknown {
  if (field === "assignee") {
    const o = assignees.find((a) => a.id === raw);
    return o ? { id: o.id, type: o.type } : null;
  }
  if (field === "labels") return raw.split(",").map((s) => s.trim()).filter(Boolean);
  return raw;
}

/**
 * Edit in place. Without task.edit it is just its children. With it, click or
 * Enter opens an editor for the field; Enter/blur commits through
 * actions.quickEdit, Esc cancels.
 */
export function InlineEdit({
  taskId,
  field,
  children,
}: {
  taskId: Id;
  field: QuickEditField;
  children: ReactNode;
}) {
  const allowed = useCan("task.edit", { kind: "task", id: taskId });
  const task = useTaskStore((s) => s.tasks.find((t) => t.id === taskId));
  const { quickEdit } = useCoreActions();
  const members = useMemberStore((s) => s.projectMembers);
  const assignees = members.flatMap<AssigneeOption>((m) =>
    m.agent_id
      ? [{ id: m.agent_id, type: "agent", label: m.agent_name ?? m.agent_id }]
      : m.user_id
        ? [{ id: m.user_id, type: "user", label: m.user?.name ?? m.user_id }]
        : [],
  );
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);

  if (!allowed) return <>{children}</>;

  const open = () => {
    setDraft(currentValue(task, field));
    setError(null);
    setEditing(true);
  };

  const commit = async () => {
    if (draft === currentValue(task, field)) {
      setEditing(false);
      return;
    }
    try {
      await quickEdit(taskId, field, parseValue(field, draft, assignees));
      setEditing(false);
    } catch (e) {
      setError(apiErrorMessage(e));
    }
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      setEditing(false);
    } else if (e.key === "Enter") {
      e.preventDefault();
      void commit();
    }
  };

  if (!editing) {
    return (
      <span
        role="button"
        tabIndex={0}
        data-inline-edit={field}
        onClick={(e) => {
          e.stopPropagation();
          open();
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            open();
          }
        }}
      >
        {children}
      </span>
    );
  }

  const common = {
    autoFocus: true,
    value: draft,
    onKeyDown,
    onBlur: () => void commit(),
    "aria-label": field,
    "aria-invalid": error ? true : undefined,
    onClick: (e: React.MouseEvent) => e.stopPropagation(),
  };
  return (
    <span data-inline-edit-open={field}>
      {field === "assignee" ? (
        <select {...common} onChange={(e) => setDraft(e.target.value)}>
          <option value="" />
          {assignees.map((a) => (
            <option key={a.id} value={a.id}>
              {a.label}
            </option>
          ))}
        </select>
      ) : field === "priority" ? (
        <select {...common} onChange={(e) => setDraft(e.target.value)}>
          {PRIORITIES.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      ) : (
        <input
          {...common}
          type={field === "due_date" ? "date" : "text"}
          onChange={(e) => setDraft(e.target.value)}
        />
      )}
      {error && <span role="alert">{error}</span>}
    </span>
  );
}
