import { useMemo } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { useProjectStore } from "@/stores/project";
import { TaskPanel } from "@/components/task-panel";
import type { Task } from "@/types";

// Full-screen task-creation page, mounted at
// /w/:wsSlug/p/:projectSlug/new (contract fixed on subtask #26217917).
// Mirrors TaskDetailPage's container/chrome exactly — same route family,
// same AppLayout <main> wrapper, same TaskPanel host — just a different
// `mode`. AppLayout already resolves `:projectSlug` into
// useProjectStore().currentProject (see components/layout/app-layout.tsx),
// so this page reads it the same way board.tsx / list-view.tsx do rather
// than re-resolving the slug itself.
export function TaskCreatePage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { currentProject } = useProjectStore();

  // Optional pre-fill from board/calendar "create in this status/date" flows
  // (?status=<id>&due=<date>). Reading them is this task's scope; wiring
  // board.tsx/calendar.tsx to actually pass them is subtask #73b449ea.
  const statusParam = searchParams.get("status");
  const dueParam = searchParams.get("due");
  const createDefaults = useMemo(
    () => ({
      ...(statusParam ? { statusId: statusParam } : {}),
      ...(dueParam ? { dueDate: dueParam } : {}),
    }),
    [statusParam, dueParam],
  );

  const handleCancel = () => {
    if (wsSlug && projectSlug) {
      navigate(`/w/${wsSlug}/p/${projectSlug}`);
    } else {
      navigate(-1);
    }
  };

  const handleCreated = (createdTask: Task) => {
    if (!wsSlug || !projectSlug) return;
    // Land on the canonical task URL, replacing the /new entry so back
    // navigation doesn't return to an empty create form.
    navigate(`/w/${wsSlug}/p/${projectSlug}/t/${createdTask.id}`, {
      replace: true,
    });
  };

  if (!currentProject) {
    return (
      <div className="flex items-center justify-center py-12">
        <p className="text-muted-foreground">
          Project &quot;{projectSlug}&quot; not found
        </p>
      </div>
    );
  }

  return (
    <TaskPanel
      taskId={null}
      mode="create"
      createProjectId={currentProject.id}
      createDefaults={createDefaults}
      onCreated={handleCreated}
      onBack={handleCancel}
      backLabel="Cancel"
      className="h-full"
    />
  );
}
