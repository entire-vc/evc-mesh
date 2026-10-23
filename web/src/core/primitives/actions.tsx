import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { UpdateTaskRequest } from "@/types";
import type { Id, QuickEditField } from "./types";

export interface CoreActions {
  openTask: (taskId: Id) => void;
  quickEdit: (taskId: Id, field: QuickEditField, value: unknown) => Promise<void>;
  /** Real href of the task page. A container that supplies it (with a value stable across navigation) keeps the primitives out of router context. */
  taskHref?: (taskId: Id) => string;
}

const ActionsContext = createContext<Partial<CoreActions> | null>(null);

/** Lets a container (or a test) replace what the primitives call. */
export function CoreActionsProvider({
  actions,
  children,
}: {
  actions: Partial<CoreActions>;
  children: ReactNode;
}) {
  return <ActionsContext.Provider value={actions}>{children}</ActionsContext.Provider>;
}

export function quickEditRequest(field: QuickEditField, value: unknown): UpdateTaskRequest {
  switch (field) {
    case "title":
      return { title: String(value) };
    case "priority":
      return { priority: value as UpdateTaskRequest["priority"] };
    case "due_date":
      return { due_date: value ? String(value) : null };
    case "assignee": {
      const v = value as { id: Id; type: "user" | "agent" } | null;
      return v ? { assignee_id: v.id, assignee_type: v.type } : { assignee_id: null };
    }
    case "labels":
      return { labels: value as string[] };
    default:
      return { custom_fields: { [field.slice(3)]: value } };
  }
}

export function taskPath(wsSlug: string | undefined, projectSlug: string | undefined, taskId: Id): string {
  return wsSlug && projectSlug ? `/w/${wsSlug}/p/${projectSlug}/t/${taskId}` : `/t/${taskId}`;
}

/* A provider that supplies the whole action set (or taskHref) sits above a component for its whole life, so the
 * early returns below never change a component's hook order; they keep it out of router context, whose every
 * navigation would re-render it (a board card commits once per task open otherwise). */
/* eslint-disable react-hooks/rules-of-hooks */

/** Project slug of the open board/page, else of the task's own project. */
function useTaskHref(taskId: Id): string {
  const override = useContext(ActionsContext);
  if (override?.taskHref) return override.taskHref(taskId);
  const { wsSlug, projectSlug } = useParams<{ wsSlug: string; projectSlug: string }>();
  const currentSlug = useProjectStore((s) => s.currentProject?.slug);
  return taskPath(wsSlug, projectSlug ?? currentSlug, taskId);
}

export function useTaskHrefFor(taskId: Id): string {
  return useTaskHref(taskId);
}

export function useCoreActions(): CoreActions {
  const override = useContext(ActionsContext);
  if (override?.openTask && override.quickEdit) return override as CoreActions;
  const navigate = useNavigate();
  const { wsSlug, projectSlug } = useParams<{ wsSlug: string; projectSlug: string }>();
  const currentSlug = useProjectStore((s) => s.currentProject?.slug);
  const updateTask = useTaskStore((s) => s.updateTask);
  return useMemo<CoreActions>(
    () => ({
      openTask: override?.openTask ?? ((id) => navigate(taskPath(wsSlug, projectSlug ?? currentSlug, id))),
      quickEdit:
        override?.quickEdit ??
        (async (id, field, value) => {
          await updateTask(id, quickEditRequest(field, value));
        }),
    }),
    [override, navigate, wsSlug, projectSlug, currentSlug, updateTask],
  );
}
/* eslint-enable react-hooks/rules-of-hooks */
