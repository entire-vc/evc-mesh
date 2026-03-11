import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Textarea } from "@/components/ui/textarea";
import { DatePickerPopover } from "@/components/date-picker-popover";
import { Bot, Tag, User, X } from "lucide-react";
import { useRecurringStore } from "@/stores/recurring";
import { useProjectStore } from "@/stores/project";
import { useAgentStore } from "@/stores/agent";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import type {
  AssigneeType,
  CreateRecurringRequest,
  Priority,
  RecurringFrequency,
  RecurringSchedule,
  UpdateRecurringRequest,
} from "@/types";

// Common IANA timezones — a representative subset
const COMMON_TIMEZONES = [
  "UTC",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Paris",
  "Europe/Berlin",
  "Europe/Moscow",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Asia/Shanghai",
  "Australia/Sydney",
  "Pacific/Auckland",
];

// Supplement with browser-supported timezones if available
function getTimezones(): string[] {
  try {
    const all = (Intl as unknown as { supportedValuesOf?: (key: string) => string[] })
      .supportedValuesOf?.("timeZone");
    if (all && all.length > 0) return all;
  } catch {
    // browser doesn't support Intl.supportedValuesOf
  }
  return COMMON_TIMEZONES;
}

const TIMEZONES = getTimezones();

const PRIORITIES: { value: Priority; label: string }[] = [
  { value: "none", label: "None" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "urgent", label: "Urgent" },
];

const FREQUENCIES: { value: RecurringFrequency; label: string; cronHint: string }[] = [
  { value: "daily", label: "Daily", cronHint: "0 9 * * *" },
  { value: "weekly", label: "Weekly", cronHint: "0 9 * * 1" },
  { value: "monthly", label: "Monthly", cronHint: "0 9 1 * *" },
  { value: "custom", label: "Custom (cron)", cronHint: "" },
];

interface CreateRecurringDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  /** If provided, dialog operates in edit mode */
  editSchedule?: RecurringSchedule;
}

export function CreateRecurringDialog({
  open,
  onOpenChange,
  projectId,
  editSchedule,
}: CreateRecurringDialogProps) {
  const { createSchedule, updateSchedule } = useRecurringStore();
  const { agents, fetchAgents } = useAgentStore();
  const { user } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();
  const { currentProject } = useProjectStore();

  const isEditMode = Boolean(editSchedule);

  // Form state
  const [titleTemplate, setTitleTemplate] = useState("");
  const [descriptionTemplate, setDescriptionTemplate] = useState("");
  const [frequency, setFrequency] = useState<RecurringFrequency>("weekly");
  const [cronExpr, setCronExpr] = useState("");
  const [timezone, setTimezone] = useState("UTC");
  const [assigneeValue, setAssigneeValue] = useState("unassigned");
  const [priority, setPriority] = useState<Priority>("medium");
  const [labels, setLabels] = useState<string[]>([]);
  const [addingLabel, setAddingLabel] = useState(false);
  const [labelDraft, setLabelDraft] = useState("");
  const labelInputRef = useRef<HTMLInputElement>(null);
  const [startsAt, setStartsAt] = useState("");
  const [endsAt, setEndsAt] = useState("");
  const [maxInstances, setMaxInstances] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const resetForm = () => {
    if (editSchedule) {
      setTitleTemplate(editSchedule.title_template);
      setDescriptionTemplate(editSchedule.description_template ?? "");
      setFrequency(editSchedule.frequency);
      setCronExpr(editSchedule.cron_expr ?? "");
      setTimezone(editSchedule.timezone ?? "UTC");
      const aType = editSchedule.assignee_type;
      const aId = editSchedule.assignee_id;
      setAssigneeValue(aId ? `${aType}:${aId}` : "unassigned");
      setPriority(editSchedule.priority);
      setLabels(editSchedule.labels ?? []);
      setStartsAt(editSchedule.starts_at ? editSchedule.starts_at.slice(0, 10) : "");
      setEndsAt(editSchedule.ends_at ? editSchedule.ends_at.slice(0, 10) : "");
      setMaxInstances(
        editSchedule.max_instances != null
          ? String(editSchedule.max_instances)
          : "",
      );
    } else {
      setTitleTemplate("");
      setDescriptionTemplate("");
      setFrequency("weekly");
      setCronExpr("");
      setTimezone("UTC");
      setAssigneeValue("unassigned");
      setPriority("medium");
      setLabels([]);
      setStartsAt("");
      setEndsAt("");
      setMaxInstances("");
    }
    setAddingLabel(false);
    setLabelDraft("");
    setError(null);
  };

  useEffect(() => {
    if (open) {
      resetForm();
      if (currentWorkspace) {
        void fetchAgents(currentWorkspace.id);
      }
    }
  }, [open, editSchedule, currentWorkspace]); // eslint-disable-line react-hooks/exhaustive-deps

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

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!titleTemplate.trim()) {
      setError("Title template is required");
      return;
    }
    if (frequency === "custom" && !cronExpr.trim()) {
      setError("Cron expression is required for custom frequency");
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

      if (isEditMode && editSchedule) {
        const req: UpdateRecurringRequest = {
          title_template: titleTemplate.trim(),
          description_template: descriptionTemplate.trim() || undefined,
          frequency,
          cron_expr: frequency === "custom" ? cronExpr.trim() : undefined,
          timezone,
          assignee_id: assigneeId ?? null,
          assignee_type: assigneeType ?? "unassigned",
          priority,
          labels,
          starts_at: startsAt ? `${startsAt}T00:00:00Z` : undefined,
          ends_at: endsAt ? `${endsAt}T23:59:59Z` : null,
          max_instances: maxInstances ? parseInt(maxInstances, 10) : null,
        };
        await updateSchedule(editSchedule.id, req);
      } else {
        const req: CreateRecurringRequest = {
          title_template: titleTemplate.trim(),
          description_template: descriptionTemplate.trim() || undefined,
          frequency,
          cron_expr: frequency === "custom" ? cronExpr.trim() : undefined,
          timezone,
          assignee_id: assigneeId,
          assignee_type: assigneeType,
          priority,
          labels: labels.length > 0 ? labels : undefined,
          is_active: true,
          starts_at: startsAt ? `${startsAt}T00:00:00Z` : undefined,
          ends_at: endsAt ? `${endsAt}T23:59:59Z` : undefined,
          max_instances: maxInstances ? parseInt(maxInstances, 10) : undefined,
        };
        await createSchedule(projectId, req);
      }

      onOpenChange(false);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to save recurring schedule",
      );
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent onClose={() => onOpenChange(false)}>
        <DialogHeader>
          <DialogTitle>
            {isEditMode ? "Edit Recurring Schedule" : "New Recurring Schedule"}
          </DialogTitle>
          <DialogDescription>
            {isEditMode
              ? `Editing schedule for ${currentProject?.name ?? "this project"}`
              : `Automatically create recurring tasks in ${currentProject?.name ?? "this project"}`}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="mt-3 max-h-[70vh] space-y-3 overflow-y-auto pr-1">
          {/* Title template */}
          <div>
            <Input
              placeholder="Title template *"
              value={titleTemplate}
              onChange={(e) => setTitleTemplate(e.target.value)}
              className="text-base font-medium"
              autoFocus
            />
            <p className="mt-1 text-[11px] text-muted-foreground">
              Variables: <code className="rounded bg-muted px-1">{"{{.Date}}"}</code>{" "}
              <code className="rounded bg-muted px-1">{"{{.Number}}"}</code>{" "}
              <code className="rounded bg-muted px-1">{"{{.Week}}"}</code>{" "}
              <code className="rounded bg-muted px-1">{"{{.Month}}"}</code>
            </p>
          </div>

          {/* Description template */}
          <div>
            <Textarea
              placeholder={"Description template (Markdown)"}
              value={descriptionTemplate}
              onChange={(e) => setDescriptionTemplate(e.target.value)}
              rows={3}
              className="text-sm"
            />
            <p className="mt-1 text-[11px] text-muted-foreground">
              Extra: <code className="rounded bg-muted px-1">{"{{.PrevSummary}}"}</code>{" "}
              — last comment from previous instance
            </p>
          </div>

          {/* Frequency */}
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground">Frequency</label>
            <div className="flex flex-wrap gap-1.5">
              {FREQUENCIES.map((f) => (
                <button
                  key={f.value}
                  type="button"
                  className={`rounded-md border px-2.5 py-1 text-xs transition-colors ${
                    frequency === f.value
                      ? "border-primary bg-primary/10 text-foreground"
                      : "border-border text-muted-foreground hover:border-border/80 hover:text-foreground"
                  }`}
                  onClick={() => {
                    setFrequency(f.value);
                    if (f.value !== "custom" && f.cronHint) {
                      setCronExpr(f.cronHint);
                    }
                  }}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </div>

          {/* Cron expression — shown when custom */}
          {frequency === "custom" && (
            <div>
              <Input
                placeholder="Cron expression *  (e.g. 0 9 * * 1)"
                value={cronExpr}
                onChange={(e) => setCronExpr(e.target.value)}
                className="h-7 font-mono text-xs"
              />
              <p className="mt-1 text-[11px] text-muted-foreground">
                5-field cron: minute hour day month weekday
              </p>
            </div>
          )}

          <Separator />

          {/* Properties grid — matching CreateTaskDialog compact style */}
          <div className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-2.5">
            {/* Timezone */}
            <label className="text-xs text-muted-foreground">Timezone</label>
            <Select
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              className="h-7 text-xs"
            >
              {TIMEZONES.map((tz) => (
                <option key={tz} value={tz}>
                  {tz}
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
              {PRIORITIES.map((p) => (
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

            {/* Max Instances */}
            <label className="text-xs text-muted-foreground">Max Instances</label>
            <Input
              type="number"
              min={1}
              placeholder="Unlimited"
              value={maxInstances}
              onChange={(e) => setMaxInstances(e.target.value)}
              className="h-7 text-xs"
            />

            {/* Starts At */}
            <label className="text-xs text-muted-foreground">Starts At</label>
            <DatePickerPopover
              value={startsAt || null}
              onChange={(val) => setStartsAt(val ?? "")}
              placeholder="Set start date"
            />

            {/* Ends At */}
            <label className="text-xs text-muted-foreground">Ends At</label>
            <DatePickerPopover
              value={endsAt || null}
              onChange={(val) => setEndsAt(val ?? "")}
              placeholder="No end date"
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

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onOpenChange(false)}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={isSubmitting}>
              {isSubmitting
                ? isEditMode
                  ? "Saving..."
                  : "Creating..."
                : isEditMode
                  ? "Save Changes"
                  : "Create Schedule"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
