import { type FormEvent, useEffect, useState } from "react";
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
import { useAgentStore } from "@/stores/agent";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import type { AssigneeType, Priority, CreateTaskRequest } from "@/types";

interface CreateTaskDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultStatusId?: string;
  defaultDueDate?: string;
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
  defaultDueDate,
}: CreateTaskDialogProps) {
  const { currentProject, statuses } = useProjectStore();
  const { createTask } = useTaskStore();
  const { agents, fetchAgents } = useAgentStore();
  const { user } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();

  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<Priority>("none");
  const [labelsRaw, setLabelsRaw] = useState("");
  const [statusId, setStatusId] = useState(defaultStatusId ?? "");
  const [dueDate, setDueDate] = useState(defaultDueDate ?? "");
  // "unassigned" | "user:{id}" | "agent:{id}"
  const [assigneeValue, setAssigneeValue] = useState("unassigned");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const resetForm = () => {
    setTitle("");
    setDescription("");
    setPriority("none");
    setLabelsRaw("");
    setStatusId(defaultStatusId ?? "");
    setDueDate(defaultDueDate ?? "");
    setAssigneeValue("unassigned");
    setError(null);
  };

  // Fetch agents and reset form when dialog opens
  useEffect(() => {
    if (open) {
      resetForm();
      if (currentWorkspace) {
        void fetchAgents(currentWorkspace.id);
      }
    }
  }, [open, currentWorkspace, fetchAgents]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleOpenChange = (nextOpen: boolean) => {
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

      let assigneeId: string | undefined;
      let assigneeType: AssigneeType | undefined;
      if (assigneeValue !== "unassigned") {
        const [type, id] = assigneeValue.split(":");
        assigneeId = id;
        assigneeType = type as AssigneeType;
      }

      const req: CreateTaskRequest = {
        title: title.trim(),
        description: description.trim() || undefined,
        priority,
        labels: labels.length > 0 ? labels : undefined,
        assignee_id: assigneeId,
        assignee_type: assigneeType,
        due_date: dueDate ? `${dueDate}T00:00:00Z` : undefined,
      };

      // If a specific status was chosen, we create the task and then move it
      // The API creates tasks in the default status; we move after if needed
      const task = await createTask(currentProject.id, req);

      if (statusId && statusId !== task.status_id) {
        const { moveTask } = useTaskStore.getState();
        await moveTask(task.id, { status_id: statusId });
      }

      resetForm();
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

          <div className="space-y-1.5">
            <label htmlFor="ct-due-date" className="text-sm font-medium">
              Due Date
            </label>
            <Input
              id="ct-due-date"
              type="date"
              value={dueDate}
              onChange={(e) => setDueDate(e.target.value)}
            />
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ct-assignee" className="text-sm font-medium">
              Assignee
            </label>
            <Select
              id="ct-assignee"
              value={assigneeValue}
              onChange={(e) => setAssigneeValue(e.target.value)}
            >
              <option value="unassigned">Unassigned</option>
              {user && (
                <option value={`user:${user.id}`}>{user.name} (you)</option>
              )}
              {agents.map((agent) => (
                <option key={agent.id} value={`agent:${agent.id}`}>
                  {agent.name} (agent)
                </option>
              ))}
            </Select>
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
