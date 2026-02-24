import { type FormEvent, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import type { Priority, CreateTaskRequest } from "@/types";

interface CreateTaskDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultStatusId?: string;
}

const priorities: { value: Priority; label: string }[] = [
  { value: "none", label: "None" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "urgent", label: "Urgent" },
];

export function CreateTaskDialog({
  open,
  onOpenChange,
  defaultStatusId,
}: CreateTaskDialogProps) {
  const { currentProject, statuses } = useProjectStore();
  const { createTask } = useTaskStore();

  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<Priority>("none");
  const [labelsRaw, setLabelsRaw] = useState("");
  const [statusId, setStatusId] = useState(defaultStatusId ?? "");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Reset form when dialog opens
  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen) {
      setTitle("");
      setDescription("");
      setPriority("none");
      setLabelsRaw("");
      setStatusId(defaultStatusId ?? "");
      setError(null);
    }
    onOpenChange(nextOpen);
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!currentProject) return;
    if (!title.trim()) {
      setError("Title is required");
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const labels = labelsRaw
        .split(",")
        .map((l) => l.trim())
        .filter(Boolean);

      const req: CreateTaskRequest = {
        title: title.trim(),
        description: description.trim() || undefined,
        priority,
        labels: labels.length > 0 ? labels : undefined,
      };

      // If a specific status was chosen, we create the task and then move it
      // The API creates tasks in the default status; we move after if needed
      const task = await createTask(currentProject.id, req);

      if (statusId && statusId !== task.status_id) {
        const { moveTask } = useTaskStore.getState();
        await moveTask(task.id, { status_id: statusId });
      }

      handleOpenChange(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create task");
    } finally {
      setIsSubmitting(false);
    }
  };

  const sortedStatuses = [...statuses].sort((a, b) => a.position - b.position);

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent onClose={() => handleOpenChange(false)}>
        <DialogHeader>
          <DialogTitle>Create Task</DialogTitle>
          <DialogDescription>
            Add a new task to {currentProject?.name ?? "the project"}.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="ct-title" className="text-sm font-medium">
              Title <span className="text-destructive">*</span>
            </label>
            <Input
              id="ct-title"
              placeholder="Task title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              autoFocus
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ct-desc" className="text-sm font-medium">
              Description
            </label>
            <Textarea
              id="ct-desc"
              placeholder="Optional description..."
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <label htmlFor="ct-priority" className="text-sm font-medium">
                Priority
              </label>
              <Select
                id="ct-priority"
                value={priority}
                onChange={(e) => setPriority(e.target.value as Priority)}
              >
                {priorities.map((p) => (
                  <option key={p.value} value={p.value}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ct-status" className="text-sm font-medium">
                Status
              </label>
              <Select
                id="ct-status"
                value={statusId}
                onChange={(e) => setStatusId(e.target.value)}
              >
                <option value="">Default</option>
                {sortedStatuses.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </div>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ct-labels" className="text-sm font-medium">
              Labels
            </label>
            <Input
              id="ct-labels"
              placeholder="Comma-separated labels"
              value={labelsRaw}
              onChange={(e) => setLabelsRaw(e.target.value)}
            />
          </div>

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => handleOpenChange(false)}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={isSubmitting}>
              {isSubmitting ? "Creating..." : "Create Task"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
