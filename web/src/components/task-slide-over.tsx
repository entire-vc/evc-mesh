import { cn } from "@/lib/cn";
import { TaskPanel } from "@/components/task-panel";

export interface TaskSlideOverProps {
  taskId: string | null;
  onClose: () => void;
  onTaskUpdated?: () => void;
}

export function TaskSlideOver({
  taskId,
  onClose,
  onTaskUpdated,
}: TaskSlideOverProps) {
  const open = !!taskId;
  return (
    <>
      {/* Backdrop */}
      <div
        className={cn(
          "fixed inset-0 z-40 bg-black/30 transition-opacity duration-200",
          open ? "opacity-100" : "pointer-events-none opacity-0",
        )}
        onClick={onClose}
        aria-hidden="true"
      />
      {/* Panel */}
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Task detail"
        className={cn(
          "fixed right-0 top-0 z-50 flex h-full w-full flex-col bg-background shadow-2xl transition-transform duration-300 ease-in-out sm:w-[90vw] lg:w-[72vw] xl:w-[65vw]",
          open ? "translate-x-0" : "translate-x-full",
        )}
      >
        <TaskPanel
          taskId={taskId}
          onClose={onClose}
          onTaskUpdated={onTaskUpdated}
          className="h-full"
        />
      </div>
    </>
  );
}
