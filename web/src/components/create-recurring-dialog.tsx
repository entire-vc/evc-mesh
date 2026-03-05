import { type FormEvent, useEffect, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
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
  const [labelsRaw, setLabelsRaw] = useState("");
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
      setLabelsRaw((editSchedule.labels ?? []).join(", "));
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
      setLabelsRaw("");
      setStartsAt("");
      setEndsAt("");
      setMaxInstances("");
    }
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

      const labels = labelsRaw
        .split(",")
        .map((l) => l.trim())
        .filter(Boolean);

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

        <form onSubmit={handleSubmit} className="mt-2 max-h-[70vh] space-y-4 overflow-y-auto pr-1">
          {/* Title template */}
          <div className="space-y-1.5">
            <label htmlFor="rd-title" className="text-sm font-medium">
              Title Template <span className="text-destructive">*</span>
            </label>
            <Input
              id="rd-title"
              placeholder='e.g. Weekly Review — {{.Date}} (#{{.Number}})'
              value={titleTemplate}
              onChange={(e) => setTitleTemplate(e.target.value)}
              autoFocus
            />
            <p className="text-xs text-muted-foreground">
              Variables: <code className="rounded bg-muted px-1">{"{{.Date}}"}</code>{" "}
              <code className="rounded bg-muted px-1">{"{{.Number}}"}</code>{" "}
              <code className="rounded bg-muted px-1">{"{{.Week}}"}</code>{" "}
              <code className="rounded bg-muted px-1">{"{{.Month}}"}</code>
            </p>
          </div>

          {/* Description template */}
          <div className="space-y-1.5">
            <label htmlFor="rd-desc" className="text-sm font-medium">
              Description Template
            </label>
            <Textarea
              id="rd-desc"
              placeholder={"## Previous run summary\n{{.PrevSummary}}\n\n## This run\nDescribe what needs to be done..."}
              value={descriptionTemplate}
              onChange={(e) => setDescriptionTemplate(e.target.value)}
              rows={4}
            />
            <p className="text-xs text-muted-foreground">
              Additional variable:{" "}
              <code className="rounded bg-muted px-1">{"{{.PrevSummary}}"}</code>{" "}
              — last comment from previous instance (truncated to 2000 chars)
            </p>
          </div>

          {/* Frequency */}
          <div className="space-y-1.5">
            <label className="text-sm font-medium">Frequency</label>
            <div className="flex gap-2 flex-wrap">
              {FREQUENCIES.map((f) => (
                <label
                  key={f.value}
                  className={`flex cursor-pointer items-center gap-1.5 rounded-md border px-3 py-2 text-sm transition-colors ${
                    frequency === f.value
                      ? "border-primary bg-primary/10 text-foreground"
                      : "border-border text-muted-foreground hover:border-border/80 hover:text-foreground"
                  }`}
                >
                  <input
                    type="radio"
                    name="rd-frequency"
                    value={f.value}
                    checked={frequency === f.value}
                    onChange={() => {
                      setFrequency(f.value);
                      if (f.value !== "custom" && f.cronHint) {
                        setCronExpr(f.cronHint);
                      }
                    }}
                    className="sr-only"
                  />
                  {f.label}
                </label>
              ))}
            </div>
          </div>

          {/* Cron expression — shown when custom, or as info for presets */}
          {frequency === "custom" && (
            <div className="space-y-1.5">
              <label htmlFor="rd-cron" className="text-sm font-medium">
                Cron Expression <span className="text-destructive">*</span>
              </label>
              <Input
                id="rd-cron"
                placeholder="0 9 * * 1"
                value={cronExpr}
                onChange={(e) => setCronExpr(e.target.value)}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">
                5-field cron: minute hour day month weekday.{" "}
                Example: <code className="rounded bg-muted px-1">0 9 * * 1</code> = every Monday at 9am
              </p>
            </div>
          )}

          {/* Timezone */}
          <div className="space-y-1.5">
            <label htmlFor="rd-tz" className="text-sm font-medium">
              Timezone
            </label>
            <Select
              id="rd-tz"
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
            >
              {TIMEZONES.map((tz) => (
                <option key={tz} value={tz}>
                  {tz}
                </option>
              ))}
            </Select>
          </div>

          <div className="grid grid-cols-2 gap-4">
            {/* Priority */}
            <div className="space-y-1.5">
              <label htmlFor="rd-priority" className="text-sm font-medium">
                Priority
              </label>
              <Select
                id="rd-priority"
                value={priority}
                onChange={(e) => setPriority(e.target.value as Priority)}
              >
                {PRIORITIES.map((p) => (
                  <option key={p.value} value={p.value}>
                    {p.label}
                  </option>
                ))}
              </Select>
            </div>

            {/* Max instances */}
            <div className="space-y-1.5">
              <label htmlFor="rd-max" className="text-sm font-medium">
                Max Instances
              </label>
              <Input
                id="rd-max"
                type="number"
                min={1}
                placeholder="Unlimited"
                value={maxInstances}
                onChange={(e) => setMaxInstances(e.target.value)}
              />
            </div>
          </div>

          {/* Assignee */}
          <div className="space-y-1.5">
            <label htmlFor="rd-assignee" className="text-sm font-medium">
              Assignee
            </label>
            <Select
              id="rd-assignee"
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

          {/* Labels */}
          <div className="space-y-1.5">
            <label htmlFor="rd-labels" className="text-sm font-medium">
              Labels
            </label>
            <Input
              id="rd-labels"
              placeholder="Comma-separated labels"
              value={labelsRaw}
              onChange={(e) => setLabelsRaw(e.target.value)}
            />
          </div>

          <div className="grid grid-cols-2 gap-4">
            {/* Starts at */}
            <div className="space-y-1.5">
              <label htmlFor="rd-starts" className="text-sm font-medium">
                Starts At
              </label>
              <Input
                id="rd-starts"
                type="date"
                value={startsAt}
                onChange={(e) => setStartsAt(e.target.value)}
              />
            </div>

            {/* Ends at */}
            <div className="space-y-1.5">
              <label htmlFor="rd-ends" className="text-sm font-medium">
                Ends At (optional)
              </label>
              <Input
                id="rd-ends"
                type="date"
                value={endsAt}
                onChange={(e) => setEndsAt(e.target.value)}
              />
            </div>
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={isSubmitting}>
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
