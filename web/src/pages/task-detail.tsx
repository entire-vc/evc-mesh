import { useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { Button } from "@/components/ui/button";
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
    <div className="flex h-full flex-col">
      {/* Page-level back navigation */}
      <div className="flex shrink-0 items-center border-b border-border px-4 py-2">
        <Button variant="ghost" size="sm" onClick={handleBack}>
          <ArrowLeft className="mr-1 h-4 w-4" />
          {fromTriage ? "Back to triage" : "Back to board"}
        </Button>
      </div>
      {/* Shared panel, full-width */}
      <TaskPanel taskId={taskId ?? null} className="flex-1 overflow-hidden" />
    </div>
  );
}
