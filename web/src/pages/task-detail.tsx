import { useNavigate, useParams, useSearchParams } from "react-router";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { TaskPanel } from "@/components/task-panel";

export function TaskDetailPage() {
  const { wsSlug, projectSlug, taskId } = useParams();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const fromTriage = searchParams.get("from") === "triage";
  const { projects } = useProjectStore();
  const currentTask = useTaskStore((state) =>
    taskId ? state.tasksById[taskId] ?? null : null,
  );

  const handleBack = () => {
    if (fromTriage) {
      navigate(`/w/${wsSlug}/triage`);
      return;
    }
    const taskProject = projects.find((p) => p.id === currentTask?.project_id);
    const resolvedSlug = taskProject?.slug;
    if (resolvedSlug) {
      navigate(`/w/${wsSlug}/p/${resolvedSlug}`);
    } else if (projectSlug && projectSlug !== "undefined") {
      navigate(`/w/${wsSlug}/p/${projectSlug}`);
    } else {
      navigate(-1);
    }
  };

  return (
    <TaskPanel
      taskId={taskId ?? null}
      onBack={handleBack}
      backLabel={fromTriage ? "Back to triage" : "Back to board"}
      className="h-full"
    />
  );
}
