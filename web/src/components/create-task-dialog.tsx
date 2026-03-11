import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { DatePickerPopover } from "@/components/date-picker-popover";
import { MarkdownEditor, type PendingImage } from "@/components/markdown-editor";
import { Bot, Tag, User, X } from "lucide-react";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useAgentStore } from "@/stores/agent";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useTemplateStore } from "@/stores/template";
import { getAccessToken } from "@/lib/api";
import type { AssigneeType, Artifact, Priority, CreateTaskRequest } from "@/types";

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
  const { templates, fetchTemplates } = useTemplateStore();

  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<Priority>("none");
  const [labels, setLabels] = useState<string[]>([]);
  const [addingLabel, setAddingLabel] = useState(false);
  const [labelDraft, setLabelDraft] = useState("");
  const labelInputRef = useRef<HTMLInputElement>(null);
  const [statusId, setStatusId] = useState(defaultStatusId ?? "");
  const [dueDate, setDueDate] = useState(defaultDueDate ?? "");
  // "unassigned" | "user:{id}" | "agent:{id}"
  const [assigneeValue, setAssigneeValue] = useState("unassigned");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Pending images pasted before task is created
  const pendingImagesRef = useRef<PendingImage[]>([]);

  const resetForm = () => {
    setTitle("");
    setDescription("");
    setPriority("none");
    setLabels([]);
    setLabelDraft("");
    setAddingLabel(false);
    setStatusId(defaultStatusId ?? "");
    setDueDate(defaultDueDate ?? "");
    setAssigneeValue("unassigned");
    setError(null);
    pendingImagesRef.current = [];
  };

  // Fetch agents, templates and reset form when dialog opens
  useEffect(() => {
    if (open) {
      resetForm();
      if (currentWorkspace) {
        void fetchAgents(currentWorkspace.id);
      }
      if (currentProject) {
        void fetchTemplates(currentProject.id);
      }
    }
  }, [open, currentWorkspace, currentProject, fetchAgents, fetchTemplates]); // eslint-disable-line react-hooks/exhaustive-deps

  // Focus label input when adding starts
  useEffect(() => {
    if (addingLabel) {
      setTimeout(() => labelInputRef.current?.focus(), 0);
    }
  }, [addingLabel]);

  const handleAddLabel = () => {
    const newLabel = labelDraft.trim();
    setAddingLabel(false);
    setLabelDraft("");
    if (!newLabel) return;
    if (labels.some((l) => l.toLowerCase() === newLabel.toLowerCase())) return;
    setLabels((prev) => [...prev, newLabel]);
  };

  const handleRemoveLabel = (label: string) => {
    setLabels((prev) => prev.filter((l) => l !== label));
  };

  const handleLabelKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      handleAddLabel();
    }
    if (e.key === "Escape") {
      setAddingLabel(false);
      setLabelDraft("");
    }
  };

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
        status_id: statusId || undefined,
      };

      const createdTask = await createTask(currentProject.id, req);

      // Upload any images that were pasted before the task existed
      if (pendingImagesRef.current.length > 0 && createdTask?.id) {
        let updatedDescription = description.trim();
        const token = getAccessToken();
        const baseUrl = import.meta.env.VITE_API_URL || "";

        for (const pending of pendingImagesRef.current) {
          try {
            const form = new FormData();
            form.append("file", pending.file, pending.file.name);
            form.append("name", pending.file.name);
            form.append("artifact_type", "image");

            const headers: HeadersInit = {};
            if (token) headers["Authorization"] = `Bearer ${token}`;

            const res = await fetch(
              `${baseUrl}/api/v1/tasks/${createdTask.id}/artifacts`,
              { method: "POST", headers, body: form },
            );

            if (res.ok) {
              const artifact = (await res.json()) as Artifact;
              const realMd = `![${pending.file.name}](${artifact.storage_url})`;
              updatedDescription = updatedDescription.replace(
                pending.placeholder,
                realMd,
              );
            }
          } catch {
            // leave the placeholder in place — not fatal
          }
        }

        // If description changed (URLs replaced), patch the task
        if (updatedDescription !== description.trim()) {
          try {
            await useTaskStore
              .getState()
              .updateTask(createdTask.id, { description: updatedDescription });
          } catch {
            // non-fatal: task is created, images are uploaded, link is cosmetic
          }
        }
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

        <form onSubmit={handleSubmit} className="mt-3 space-y-3">
          {/* Template selector */}
          {templates.length > 0 && (
            <Select
              defaultValue=""
              onChange={(e) => {
                const tmpl = templates.find((t) => t.id === e.target.value);
                if (!tmpl) return;
                if (tmpl.title_template) setTitle(tmpl.title_template);
                if (tmpl.description_template) setDescription(tmpl.description_template);
                if (tmpl.priority) setPriority(tmpl.priority as Priority);
                if (tmpl.labels && tmpl.labels.length > 0) setLabels(tmpl.labels);
              }}
              className="h-7 text-xs"
            >
              <option value="">From template...</option>
              {templates.map((tmpl) => (
                <option key={tmpl.id} value={tmpl.id}>
                  {tmpl.name}
                </option>
              ))}
            </Select>
          )}

          {/* Title */}
          <Input
            placeholder="Task title *"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="text-base font-medium"
            autoFocus
          />

          {/* Description */}
          <MarkdownEditor
            value={description}
            onChange={setDescription}
            projectId={currentProject?.id}
            placeholder="Add a description... (Markdown, paste images)"
            rows={3}
            onPendingImage={(pending) => {
              pendingImagesRef.current.push(pending);
            }}
          />

          <Separator />

          {/* Properties grid — mirrors TaskDetail / SlideOver */}
          <div className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-2.5">
            {/* Status */}
            <label className="flex items-center gap-1 text-xs text-muted-foreground">
              {(() => {
                const s = sortedStatuses.find((st) => st.id === statusId);
                return s ? (
                  <span
                    className="inline-block h-2 w-2 shrink-0 rounded-full"
                    style={{ backgroundColor: s.color }}
                  />
                ) : null;
              })()}
              Status
            </label>
            <Select
              value={statusId}
              onChange={(e) => setStatusId(e.target.value)}
              className="h-7 text-xs"
            >
              <option value="">Default</option>
              {sortedStatuses.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </Select>

            {/* Priority */}
            <label className="text-xs text-muted-foreground">Priority</label>
            <Select
              value={priority}
              onChange={(e) => setPriority(e.target.value as Priority)}
              className="h-7 text-xs"
            >
              {priorities.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </Select>

            {/* Assignee */}
            <label className="flex items-center gap-1 text-xs text-muted-foreground">
              {assigneeValue.startsWith("agent:") ? (
                <Bot className="h-3 w-3" />
              ) : (
                <User className="h-3 w-3" />
              )}
              Assignee
            </label>
            <Select
              value={assigneeValue}
              onChange={(e) => setAssigneeValue(e.target.value)}
              className="h-7 text-xs"
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

            {/* Due Date */}
            <label className="text-xs text-muted-foreground">Due Date</label>
            <DatePickerPopover
              value={dueDate || null}
              onChange={(val) => setDueDate(val ?? "")}
              placeholder="Set due date"
            />

            {/* Labels */}
            <label className="flex items-center gap-1 self-start pt-1 text-xs text-muted-foreground">
              <Tag className="h-3 w-3" />
              Labels
            </label>
            <div className="flex flex-wrap items-center gap-1">
              {labels.map((label) => (
                <Badge
                  key={label}
                  variant="secondary"
                  className="cursor-pointer gap-1 text-[10px] hover:bg-destructive/20"
                  onClick={() => handleRemoveLabel(label)}
                  title="Click to remove"
                >
                  {label}
                  <X className="h-2 w-2" />
                </Badge>
              ))}
              {addingLabel ? (
                <Input
                  ref={labelInputRef}
                  value={labelDraft}
                  onChange={(e) => setLabelDraft(e.target.value)}
                  onBlur={handleAddLabel}
                  onKeyDown={handleLabelKeyDown}
                  className="h-6 w-24 px-1.5 text-xs"
                  placeholder="Label..."
                />
              ) : (
                <button
                  type="button"
                  className="rounded border border-dashed border-border px-1.5 py-0.5 text-xs text-muted-foreground hover:border-primary hover:text-foreground"
                  onClick={() => setAddingLabel(true)}
                >
                  + Add
                </button>
              )}
            </div>
          </div>

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => handleOpenChange(false)}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={isSubmitting}>
              {isSubmitting ? "Creating..." : "Create Task"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
