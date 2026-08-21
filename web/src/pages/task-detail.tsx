import { useEffect } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { TaskPanel } from "@/components/task-panel";

export function TaskDetailPage() {
  const { wsSlug, projectSlug, taskId } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams] = useSearchParams();
  const fromTriage = searchParams.get("from") === "triage";
  const { projects } = useProjectStore();
  const currentTask = useTaskStore((state) =>
    taskId ? state.tasksById[taskId] ?? null : null,
  );

  // The URL's :projectSlug is never checked against the task it names — a
  // task loads identically under any project's slug in this workspace
  // (#b2be54e8). There's no separate "wrong project" state to render; once
  // the task and this workspace's projects have both loaded, replace a
  // mismatched slug with the task's real one so stale or hand-edited links
  // self-heal onto the canonical URL instead of silently lying about which
  // project owns the task. `replace: true` avoids leaving the wrong-slug URL
  // in history, and the effect is a no-op once `projectSlug` already equals
  // `realProject.slug` — the redirect target — so it settles after firing
  // once and does not loop.
  useEffect(() => {
    if (!wsSlug || !taskId || !currentTask || projects.length === 0) return;
    const realProject = projects.find((p) => p.id === currentTask.project_id);
    if (!realProject || realProject.slug === projectSlug) return;
    navigate(
      `/w/${wsSlug}/p/${realProject.slug}/t/${taskId}${location.search}`,
      { replace: true },
    );
  }, [wsSlug, taskId, projectSlug, currentTask, projects, location.search, navigate]);

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
