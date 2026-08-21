import {
  Fragment,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  ArrowLeft,
  Bot,
  Check,
  CheckCircle2,
  Circle,
  Clock,
  ExternalLink,
  FolderKanban,
  Hourglass,
  Link,
  ListTree,
  Loader2,
  Lock,
  Package,
  Pencil,
  RefreshCw,
  SlidersHorizontal,
  Tag,
  User,
  X,
  XCircle,
} from "lucide-react";
import { useNavigate } from "react-router";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useMemberStore } from "@/stores/member";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useRecurringStore } from "@/stores/recurring";
import { useRulesStore } from "@/stores/rules";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { CommentList } from "@/components/comment-list";
import { ActivityLog } from "@/components/activity-log";
import { SubtaskList } from "@/components/subtask-list";
import { ArtifactList } from "@/components/artifact-list";
import { VCSLinks } from "@/components/vcs-links";
import { DependencyList } from "@/components/dependency-list";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import { DatePickerPopover } from "@/components/date-picker-popover";
import { RichTextEditor } from "@/components/rich-text-editor";
import { MarkdownWithRelay } from "@/components/MarkdownWithRelay";
import { RecurringHistoryPanel } from "@/components/recurring-history-panel";
import { cn } from "@/lib/cn";
import { inlineLabel } from "@/lib/user-display";
import {
  formatDate,
  formatRelative,
  fromDateTimeLocal,
  priorityConfig,
  toDateTimeLocal,
} from "@/lib/utils";
import { toast } from "@/components/ui/toast";
import type { AssigneeType, DodCheck, DodGateConfig, Priority, DelegationLevel } from "@/types";
import { DelegationLevelSelect } from "@/components/delegation-level-select";
import {
  getTaskCostSummary,
  type TaskCostSummary,
} from "@/lib/api";
import { CostQualityBlock } from "@/components/cost-quality-block";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type MobileTabId =
  | "details"
  | "description"
  | "comments"
  | "subtasks"
  | "artifacts"
  | "activity";
type RightTabId = "comments" | "subtasks" | "artifacts" | "activity";

const priorities: Priority[] = ["urgent", "high", "medium", "low", "none"];
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const MOBILE_TABS: { id: MobileTabId; label: string }[] = [
  { id: "details", label: "Details" },
  { id: "description", label: "Description" },
  { id: "comments", label: "Comments" },
  { id: "subtasks", label: "Subtasks" },
  { id: "artifacts", label: "Artifacts" },
  { id: "activity", label: "Activity" },
];

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface TaskPanelProps {
  taskId: string | null;
  onClose?: () => void;
  onBack?: () => void;
  backLabel?: string;
  onTaskUpdated?: () => void;
  className?: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function TaskPanel({
  taskId,
  onClose,
  onBack,
  backLabel = "Back",
  onTaskUpdated,
  className,
}: TaskPanelProps) {
  const navigate = useNavigate();

  // Task navigation stack — must be declared before selectors that depend on it
  const [taskIdStack, setTaskIdStack] = useState<string[]>([]);
  const effectiveTaskId =
    taskIdStack.length > 0 ? taskIdStack[taskIdStack.length - 1] : taskId;
  const backTaskId =
    taskIdStack.length > 0
      ? taskIdStack.length >= 2
        ? taskIdStack[taskIdStack.length - 2]
        : taskId
      : null;

  const currentTask = useTaskStore((state) =>
    effectiveTaskId ? state.tasksById[effectiveTaskId] ?? null : null,
  );
  const backTask = useTaskStore((state) =>
    backTaskId ? state.tasksById[backTaskId] ?? null : null,
  );
  const { fetchTask, updateTask, moveTask, moveToProject } = useTaskStore();
  const { statuses, fetchStatuses, currentProject, projects } =
    useProjectStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();
  const { projectMembers, fetchProjectMembers } = useMemberStore();
  const { user } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();
  const { schedules, fetchSchedules } = useRecurringStore();
  const { teamDirectory, fetchTeamDirectory } = useRulesStore();

  const [loading, setLoading] = useState(false);
  const [activeMobileTab, setActiveMobileTab] = useState<MobileTabId>("details");
  const [activeDesktopTab, setActiveDesktopTab] = useState<RightTabId>("comments");
  const [hideEmpty, setHideEmpty] = useState(true);
  const [recurringHistoryOpen, setRecurringHistoryOpen] = useState(false);
  const [costSummary, setCostSummary] = useState<TaskCostSummary | null>(null);
  // Bumped whenever DependencyList reports a change, so both SubtaskList
  // mounts (mobile + desktop tabs) refetch — an is_child_of edge added or
  // removed there changes the Subtasks tab without the user reopening the card.
  const [depRefreshKey, setDepRefreshKey] = useState(0);

  // Inline title editing
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const titleInputRef = useRef<HTMLInputElement>(null);

  // Inline description editing
  const [editingDescription, setEditingDescription] = useState(false);
  const [descDraft, setDescDraft] = useState("");

  // Inline estimated hours editing
  const [editingHours, setEditingHours] = useState(false);
  const [hoursDraft, setHoursDraft] = useState("");

  // Description autosave state
  const [descSaving, setDescSaving] = useState(false);
  const [descSaved, setDescSaved] = useState(false);
  const descTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Label adding
  const [addingLabel, setAddingLabel] = useState(false);
  const [labelDraft, setLabelDraft] = useState("");
  const labelInputRef = useRef<HTMLInputElement>(null);

  // Reset navigation stack when the root task changes
  useEffect(() => {
    setTaskIdStack([]);
  }, [taskId]);

  // ---- Data loading --------------------------------------------------------

  useEffect(() => {
    if (!effectiveTaskId) return;
    let cancelled = false;

    async function load() {
      setLoading(true);
      setEditingTitle(false);
      setEditingDescription(false);
      setEditingHours(false);
      try {
        const task = await fetchTask(effectiveTaskId!);
        if (cancelled) return;
        if (
          statuses.length === 0 ||
          statuses[0]?.project_id !== task.project_id
        ) {
          await fetchStatuses(task.project_id);
        }
        fetchCustomFields(task.project_id).catch(() => {});
      } catch {
        // error handled by store
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    void load();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [effectiveTaskId]);

  // Fetch project members for assignee dropdown
  useEffect(() => {
    if (currentProject) {
      void fetchProjectMembers(currentProject.id);
    }
  }, [currentProject, fetchProjectMembers]);

  // Fetch team directory for assignee name resolution (lazy — only if not cached)
  useEffect(() => {
    if (currentWorkspace && !teamDirectory) {
      void fetchTeamDirectory(currentWorkspace.id);
    }
  }, [currentWorkspace, teamDirectory, fetchTeamDirectory]);

  // Fetch recurring schedules if task belongs to a series
  useEffect(() => {
    if (currentTask?.recurring_schedule_id && currentTask.project_id) {
      void fetchSchedules(currentTask.project_id);
    }
  }, [currentTask?.recurring_schedule_id, currentTask?.project_id, fetchSchedules]);

  // Sync local drafts when task changes
  useEffect(() => {
    if (currentTask) {
      setTitleDraft(currentTask.title);
      setDescDraft(currentTask.description ?? "");
      setHoursDraft(
        currentTask.estimated_hours != null
          ? String(currentTask.estimated_hours)
          : "",
      );
    }
  }, [currentTask]);

  // Focus title input when editing starts
  useEffect(() => {
    if (editingTitle) {
      titleInputRef.current?.focus();
      titleInputRef.current?.select();
    }
  }, [editingTitle]);

  // Focus label input when adding
  useEffect(() => {
    if (addingLabel) {
      setTimeout(() => labelInputRef.current?.focus(), 0);
    }
  }, [addingLabel]);

  // Close on Escape key (drawer mode only — only when onClose is provided)
  useEffect(() => {
    if (!onClose) return;
    const handleKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [onClose]);

  // Lazy-fetch cost/quality summary when task opens (silent fail — block is hidden on error)
  useEffect(() => {
    if (!effectiveTaskId) {
      setCostSummary(null);
      return;
    }
    let cancelled = false;
    getTaskCostSummary(effectiveTaskId)
      .then((data) => {
        if (!cancelled) setCostSummary(data);
      })
      .catch(() => {
        if (!cancelled) setCostSummary(null);
      });
    return () => {
      cancelled = true;
    };
  }, [effectiveTaskId]);

  // ---- Subtask stack navigation --------------------------------------------

  const pushTask = useCallback((id: string) => {
    setTaskIdStack((s) => [...s, id]);
    setActiveMobileTab("subtasks");
    setActiveDesktopTab("subtasks");
  }, []);

  const popTask = useCallback(() => {
    setTaskIdStack((s) => s.slice(0, -1));
  }, []);

  // ---- Handlers -------------------------------------------------------------

  const handleTitleSave = useCallback(async () => {
    setEditingTitle(false);
    if (!currentTask || titleDraft.trim() === currentTask.title) return;
    if (!titleDraft.trim()) {
      setTitleDraft(currentTask.title);
      return;
    }
    try {
      await updateTask(currentTask.id, { title: titleDraft.trim() });
      onTaskUpdated?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update title");
    }
  }, [currentTask, titleDraft, updateTask, onTaskUpdated]);

  const handleTitleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") void handleTitleSave();
    if (e.key === "Escape") {
      setEditingTitle(false);
      if (currentTask) setTitleDraft(currentTask.title);
    }
  };

  const flushDescription = useCallback(async () => {
    if (!currentTask) return;
    const trimmed = descDraft.trim();
    if (trimmed === (currentTask.description ?? "")) return;
    setDescSaving(true);
    try {
      await updateTask(currentTask.id, { description: trimmed || undefined });
      onTaskUpdated?.();
      setDescSaved(true);
      setTimeout(() => setDescSaved(false), 1500);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save description",
      );
    } finally {
      setDescSaving(false);
    }
  }, [currentTask, descDraft, updateTask, onTaskUpdated]);

  // Debounced autosave for description (2s after last keystroke)
  useEffect(() => {
    if (!editingDescription) return;
    if (descTimerRef.current) clearTimeout(descTimerRef.current);
    descTimerRef.current = setTimeout(() => {
      void flushDescription();
    }, 2000);
    return () => {
      if (descTimerRef.current) clearTimeout(descTimerRef.current);
    };
  }, [descDraft, editingDescription, flushDescription]);

  const handleStatusChange = async (statusId: string) => {
    if (!currentTask || statusId === currentTask.status_id) return;
    try {
      await moveTask(currentTask.id, { status_id: statusId });
      if (taskId) await fetchTask(taskId);
      onTaskUpdated?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to change status");
    }
  };

  const handlePriorityChange = async (priority: Priority) => {
    if (!currentTask || priority === currentTask.priority) return;
    try {
      await updateTask(currentTask.id, { priority });
      onTaskUpdated?.();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to change priority",
      );
    }
  };

  const handleDelegationLevelChange = async (level: DelegationLevel) => {
    if (!currentTask || level === (currentTask.delegation_level ?? "review")) return;
    try {
      await updateTask(currentTask.id, { delegation_level: level });
      onTaskUpdated?.();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to change delegation mode",
      );
    }
  };

  const handleAssigneeChange = async (value: string) => {
    if (!currentTask) return;
    try {
      if (value === "unassigned") {
        await updateTask(currentTask.id, {
          assignee_id: null,
          assignee_type: "unassigned",
        });
      } else {
        const [type, id] = value.split(":");
        await updateTask(currentTask.id, {
          assignee_id: id,
          assignee_type: type as AssigneeType,
        });
      }
      onTaskUpdated?.();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to change assignee",
      );
    }
  };

  const handleReviewerChange = async (value: string) => {
    if (!currentTask) return;
    try {
      if (value === "unassigned") {
        await updateTask(currentTask.id, { clear_reviewer: true });
      } else {
        const [type, id] = value.split(":");
        await updateTask(currentTask.id, {
          reviewer_id: id,
          reviewer_type: type as AssigneeType,
        });
      }
      onTaskUpdated?.();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to change reviewer",
      );
    }
  };

  const handleDueDateChange = async (value: string) => {
    if (!currentTask) return;
    try {
      await updateTask(currentTask.id, {
        due_date: value ? fromDateTimeLocal(value) : null,
      });
      onTaskUpdated?.();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to change due date",
      );
    }
  };

  const handleProjectChange = async (targetProjectId: string) => {
    if (!currentTask || targetProjectId === currentTask.project_id) return;
    try {
      const updated = await moveToProject(currentTask.id, targetProjectId);
      onTaskUpdated?.();
      const targetProject = projects.find((p) => p.id === targetProjectId);
      // In page mode (no onClose), navigate to the task in its new project context
      if (targetProject && currentWorkspace && !onClose) {
        navigate(
          `/w/${currentWorkspace.slug}/p/${targetProject.slug}/t/${updated.id}`,
        );
      }
      toast.success("Task moved to another project");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to move task");
    }
  };

  const handleHoursSave = useCallback(async () => {
    setEditingHours(false);
    if (!currentTask) return;
    const num = hoursDraft.trim() === "" ? null : Number(hoursDraft);
    if (num === currentTask.estimated_hours) return;
    try {
      await updateTask(currentTask.id, { estimated_hours: num });
      onTaskUpdated?.();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to update estimate",
      );
    }
  }, [currentTask, hoursDraft, updateTask, onTaskUpdated]);

  const handleHoursKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") void handleHoursSave();
    if (e.key === "Escape") {
      setEditingHours(false);
      setHoursDraft(
        currentTask?.estimated_hours != null
          ? String(currentTask.estimated_hours)
          : "",
      );
    }
  };

  const handleAddLabel = useCallback(async () => {
    if (!currentTask || !labelDraft.trim()) {
      setAddingLabel(false);
      setLabelDraft("");
      return;
    }
    const newLabel = labelDraft.trim();
    const labels = [...(currentTask.labels ?? []), newLabel];
    setAddingLabel(false);
    setLabelDraft("");
    try {
      await updateTask(currentTask.id, { labels });
      onTaskUpdated?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to add label");
    }
  }, [currentTask, labelDraft, updateTask, onTaskUpdated]);

  const handleRemoveLabel = useCallback(
    async (label: string) => {
      if (!currentTask) return;
      const labels = (currentTask.labels ?? []).filter((l) => l !== label);
      try {
        await updateTask(currentTask.id, { labels });
        onTaskUpdated?.();
      } catch (err) {
        toast.error(
          err instanceof Error ? err.message : "Failed to remove label",
        );
      }
    },
    [currentTask, updateTask, onTaskUpdated],
  );

  const handleCustomFieldChange = useCallback(
    async (slug: string, newValue: unknown) => {
      if (!currentTask) return;
      try {
        await updateTask(currentTask.id, {
          custom_fields: {
            ...(currentTask.custom_fields ?? {}),
            [slug]: newValue,
          },
        });
        onTaskUpdated?.();
      } catch (err) {
        toast.error(
          err instanceof Error ? err.message : "Failed to update field",
        );
      }
    },
    [currentTask, updateTask, onTaskUpdated],
  );

  // ---- Derived state -------------------------------------------------------

  const currentStatus =
    currentTask ? statuses.find((s) => s.id === currentTask.status_id) : null;

  function isEmpty(value: unknown): boolean {
    if (value == null || value === "") return true;
    if (Array.isArray(value)) return value.length === 0;
    return false;
  }

  const showDueDate = !hideEmpty || !isEmpty(currentTask?.due_date);
  const showLabels = !hideEmpty || (currentTask?.labels ?? []).length > 0;
  const showHours = !hideEmpty || currentTask?.estimated_hours != null;
  const showVcsLinks = !hideEmpty || (currentTask?.vcs_link_count ?? 0) > 0;
  const showDependencies = !hideEmpty;

  // ---- Shared sub-components -----------------------------------------------

  const propertiesGrid = currentTask && (
    <div className="rounded-lg border border-border bg-muted/20 p-3">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Properties
        </span>
        <button
          type="button"
          className="text-[11px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
          onClick={() => setHideEmpty((v) => !v)}
        >
          {hideEmpty ? "Show empty" : "Hide empty"}
        </button>
      </div>

      <div className="grid grid-cols-1 items-start gap-y-2.5 sm:grid-cols-[auto_1fr] sm:gap-x-4">
        {/* Project */}
        <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
          <FolderKanban className="h-3 w-3" />
          Project
        </label>
        <Select
          value={currentTask.project_id}
          onChange={(e) => void handleProjectChange(e.target.value)}
          className="h-7 text-xs"
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </Select>

        {/* Status */}
        <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
          {currentStatus && (
            <span
              className="inline-block h-2 w-2 shrink-0 rounded-full"
              style={{ backgroundColor: currentStatus.color }}
            />
          )}
          Status
        </label>
        <Select
          value={currentTask.status_id}
          onChange={(e) => void handleStatusChange(e.target.value)}
          className="h-7 text-xs"
        >
          {[...statuses]
            .sort((a, b) => a.position - b.position)
            .map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
        </Select>

        {/* Human gate */}
        {currentTask.human_gate && (
          <>
            <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
              <Lock className="h-3 w-3 text-amber-600 dark:text-amber-400" />
              Human gate
            </label>
            <div className="flex flex-col items-start gap-1 pt-0.5">
              <Badge
                variant="secondary"
                className="gap-1 border-amber-300 bg-amber-100 text-[10px] font-medium text-amber-800 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300"
                data-testid="human-gate-badge"
                data-human-gate-class={currentTask.human_gate_class ?? "hard"}
              >
                {currentTask.human_gate_class === "soft"
                  ? "Soft gate — awaiting sign-off"
                  : "Hard gate — awaiting sign-off"}
              </Badge>
              {currentTask.human_gate_info?.owner_name && (
                <span className="text-[11px] text-muted-foreground">
                  Raised by {currentTask.human_gate_info.owner_name}
                  {currentTask.human_gate_info.marker_created_at && (
                    <>
                      {" "}
                      · {formatRelative(currentTask.human_gate_info.marker_created_at)}
                    </>
                  )}
                </span>
              )}
            </div>
          </>
        )}

        {/* Priority */}
        <label className="pt-1 text-xs text-muted-foreground">
          Priority
        </label>
        <Select
          value={currentTask.priority}
          onChange={(e) =>
            void handlePriorityChange(e.target.value as Priority)
          }
          className="h-7 text-xs"
        >
          {priorities.map((p) => (
            <option key={p} value={p}>
              {priorityConfig[p].label}
            </option>
          ))}
        </Select>

        {/* Delegation mode */}
        <label className="pt-1 text-xs text-muted-foreground">
          Delegation
        </label>
        <DelegationLevelSelect
          value={currentTask.delegation_level}
          onChange={(level) => void handleDelegationLevelChange(level)}
        />

        {/* Assignee */}
        <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
          {currentTask.assignee_id &&
          currentTask.assignee_type === "agent" ? (
            <Bot className="h-3 w-3" />
          ) : (
            <User className="h-3 w-3" />
          )}
          Assignee
        </label>
        <Select
          value={
            currentTask.assignee_id
              ? `${currentTask.assignee_type}:${currentTask.assignee_id}`
              : "unassigned"
          }
          onChange={(e) => void handleAssigneeChange(e.target.value)}
          className="h-7 text-xs"
        >
          <option value="unassigned">Unassigned</option>
          {user && !projectMembers.some((m) => m.user_id === user.id) && (
            <option value={`user:${user.id}`}>
              {user.name} (you)
            </option>
          )}
          {(() => {
            const ownerId = currentWorkspace?.owner_id;
            if (!ownerId || user?.id === ownerId) return null;
            if (projectMembers.some((m) => m.user_id === ownerId)) return null;
            const ownerName = teamDirectory?.humans.find((h) => h.id === ownerId)?.name;
            if (!ownerName) return null;
            return <option key={`owner-${ownerId}`} value={`user:${ownerId}`}>{ownerName}</option>;
          })()}
          {projectMembers.map((m) => {
            if (m.user_id && m.user) {
              const isSelf = user?.id === m.user_id;
              return (
                <option key={m.id} value={`user:${m.user_id}`}>
                  {inlineLabel(m.user)}{isSelf ? " (you)" : ""} — {m.role}
                </option>
              );
            }
            if (m.agent_id) {
              const desc = [m.agent_role, m.agent_description].filter(Boolean).join(" · ");
              return (
                <option key={m.id} value={`agent:${m.agent_id}`}>
                  {m.agent_name} (agent){desc ? ` — ${desc}` : ""}
                </option>
              );
            }
            return null;
          })}
          {currentTask.assignee_id && !projectMembers.some((m) =>
            m.user_id === currentTask.assignee_id || m.agent_id === currentTask.assignee_id
          ) && (() => {
            if (currentWorkspace?.owner_id === currentTask.assignee_id) return null;
            const resolved =
              teamDirectory?.agents.find((a) => a.id === currentTask.assignee_id)?.name ??
              teamDirectory?.humans.find((h) => h.id === currentTask.assignee_id)?.name ??
              (currentTask.assignee_name && !UUID_RE.test(currentTask.assignee_name)
                ? currentTask.assignee_name
                : null);
            if (!resolved) return null;
            return (
              <option value={`${currentTask.assignee_type}:${currentTask.assignee_id}`}>
                {resolved} (not a project member)
              </option>
            );
          })()}
        </Select>

        {/* Reviewer */}
        <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
          {currentTask.reviewer_id && currentTask.reviewer_type === "agent" ? (
            <Bot className="h-3 w-3" />
          ) : (
            <User className="h-3 w-3" />
          )}
          Reviewer
        </label>
        <Select
          value={
            currentTask.reviewer_id
              ? `${currentTask.reviewer_type}:${currentTask.reviewer_id}`
              : "unassigned"
          }
          onChange={(e) => void handleReviewerChange(e.target.value)}
          className="h-7 text-xs"
        >
          <option value="unassigned">No reviewer</option>
          {user && !projectMembers.some((m) => m.user_id === user.id) && (
            <option value={`user:${user.id}`}>
              {user.name} (you)
            </option>
          )}
          {(() => {
            const ownerId = currentWorkspace?.owner_id;
            if (!ownerId || user?.id === ownerId) return null;
            if (projectMembers.some((m) => m.user_id === ownerId)) return null;
            const ownerName = teamDirectory?.humans.find((h) => h.id === ownerId)?.name;
            if (!ownerName) return null;
            return <option key={`reviewer-owner-${ownerId}`} value={`user:${ownerId}`}>{ownerName}</option>;
          })()}
          {projectMembers.map((m) => {
            if (m.user_id && m.user) {
              const isSelf = user?.id === m.user_id;
              return (
                <option key={`reviewer-${m.id}`} value={`user:${m.user_id}`}>
                  {inlineLabel(m.user)}{isSelf ? " (you)" : ""} — {m.role}
                </option>
              );
            }
            if (m.agent_id) {
              const desc = [m.agent_role, m.agent_description].filter(Boolean).join(" · ");
              return (
                <option key={`reviewer-${m.id}`} value={`agent:${m.agent_id}`}>
                  {m.agent_name} (agent){desc ? ` — ${desc}` : ""}
                </option>
              );
            }
            return null;
          })}
          {currentTask.reviewer_id && !projectMembers.some((m) =>
            m.user_id === currentTask.reviewer_id || m.agent_id === currentTask.reviewer_id
          ) && (() => {
            if (currentWorkspace?.owner_id === currentTask.reviewer_id) return null;
            const resolved =
              teamDirectory?.agents.find((a) => a.id === currentTask.reviewer_id)?.name ??
              teamDirectory?.humans.find((h) => h.id === currentTask.reviewer_id)?.name ??
              (currentTask.reviewer_name && !UUID_RE.test(currentTask.reviewer_name)
                ? currentTask.reviewer_name
                : null);
            if (!resolved) return null;
            return (
              <option value={`${currentTask.reviewer_type}:${currentTask.reviewer_id}`}>
                {resolved} (not a project member)
              </option>
            );
          })()}
        </Select>

        {/* Due Date */}
        {showDueDate && (
          <>
            <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
              Due Date
            </label>
            <DatePickerPopover
              value={
                currentTask.due_date
                  ? toDateTimeLocal(currentTask.due_date)
                  : null
              }
              onChange={(val) =>
                void handleDueDateChange(val ?? "")
              }
              includeTime
              placeholder="Set due date"
            />
          </>
        )}

        {/* Labels */}
        {showLabels && (
          <>
            <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
              <Tag className="h-3 w-3" />
              Labels
            </label>
            <div className="flex flex-wrap items-center gap-1">
              {(currentTask.labels ?? []).map((label) => (
                <Badge
                  key={label}
                  variant="secondary"
                  className="cursor-pointer gap-1 text-[10px] hover:bg-destructive/20"
                  onClick={() => void handleRemoveLabel(label)}
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
                  onBlur={() => void handleAddLabel()}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void handleAddLabel();
                    if (e.key === "Escape") {
                      setAddingLabel(false);
                      setLabelDraft("");
                    }
                  }}
                  className="h-5 w-20 px-1 text-[10px]"
                  placeholder="Label..."
                />
              ) : (
                <button
                  type="button"
                  className="rounded border border-dashed border-border px-1.5 py-0.5 text-[10px] text-muted-foreground hover:border-primary hover:text-foreground"
                  onClick={() => setAddingLabel(true)}
                >
                  + Add
                </button>
              )}
            </div>
          </>
        )}

        {/* Estimated hours */}
        {showHours && (
          <>
            <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
              <Hourglass className="h-3 w-3" />
              Estimate
            </label>
            {editingHours ? (
              <Input
                type="number"
                value={hoursDraft}
                onChange={(e) => setHoursDraft(e.target.value)}
                onBlur={() => void handleHoursSave()}
                onKeyDown={handleHoursKeyDown}
                min={0}
                step={0.5}
                placeholder="e.g. 4"
                className="h-7 w-24 text-xs"
              />
            ) : (
              <button
                type="button"
                className="flex items-center gap-1 text-left text-xs hover:text-primary"
                onClick={() => setEditingHours(true)}
              >
                {currentTask.estimated_hours != null ? (
                  <span className="font-medium">
                    {currentTask.estimated_hours}h
                  </span>
                ) : (
                  <span className="text-muted-foreground">
                    Set estimate
                  </span>
                )}
                <Pencil className="h-2.5 w-2.5 text-muted-foreground" />
              </button>
            )}
          </>
        )}

        {/* Custom fields */}
        {customFieldDefs.length > 0 && (
          <>
            <div className="col-span-2 my-1">
              <Separator />
            </div>
            <label className="col-span-2 flex items-center gap-1 text-xs font-medium text-muted-foreground">
              <SlidersHorizontal className="h-3 w-3" />
              Custom Fields
            </label>
            {[...customFieldDefs]
              .sort((a, b) => a.position - b.position)
              .filter(
                (field) =>
                  !hideEmpty ||
                  !isEmpty(
                    currentTask.custom_fields?.[field.slug],
                  ),
              )
              .map((field) => (
                <Fragment key={field.id}>
                  <label className="pt-1 text-xs text-muted-foreground">
                    {field.name}
                    {field.is_required && (
                      <span className="ml-0.5 text-destructive">
                        *
                      </span>
                    )}
                  </label>
                  <CustomFieldRenderer
                    field={field}
                    value={
                      currentTask.custom_fields?.[field.slug]
                    }
                    onChange={(val) =>
                      void handleCustomFieldChange(field.slug, val)
                    }
                  />
                </Fragment>
              ))}
          </>
        )}

        {/* Timestamps */}
        <div className="col-span-2 flex items-center justify-between gap-2 pt-1">
          <label className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
            <Clock className="h-3 w-3" />
            Created
          </label>
          <span className="text-right text-xs">
            {formatDate(currentTask.created_at)}
          </span>
        </div>

        <div className="col-span-2 flex items-center justify-between gap-2">
          <label className="shrink-0 text-xs text-muted-foreground">Updated</label>
          <span className="text-right text-xs text-muted-foreground">
            {formatRelative(currentTask.updated_at)}
          </span>
        </div>

        {/* Recurring */}
        {currentTask.recurring_schedule_id && (() => {
          const schedule = schedules.find(
            (s) => s.id === currentTask.recurring_schedule_id,
          );
          return (
            <>
              <div className="col-span-2 my-1">
                <Separator />
              </div>
              <label className="flex items-center gap-1 pt-1 text-xs text-muted-foreground">
                <RefreshCw className="h-3 w-3" />
                Recurring
              </label>
              <div className="space-y-1">
                <p className="text-xs">
                  Run{" "}
                  {currentTask.recurring_instance_number != null
                    ? `#${currentTask.recurring_instance_number}`
                    : ""}{" "}
                  {schedule
                    ? `of "${schedule.title_template}"`
                    : "of recurring schedule"}
                </p>
                <button
                  type="button"
                  className="rounded px-1.5 py-0.5 text-xs text-primary transition-colors hover:bg-primary/10"
                  onClick={() => setRecurringHistoryOpen(true)}
                >
                  View history
                </button>
              </div>
            </>
          );
        })()}

        {/* VCS Links */}
        {showVcsLinks && (
          <>
            <div className="col-span-2 my-1">
              <Separator />
            </div>
            <div className="col-span-2">
              <VCSLinks taskId={currentTask.id} />
            </div>
          </>
        )}

        {/* Dependencies */}
        {showDependencies && (
          <>
            <div className="col-span-2 my-1">
              <Separator />
            </div>
            <div className="col-span-2">
              <DependencyList
                taskId={currentTask.id}
                onOpenTask={pushTask}
                onChanged={() => setDepRefreshKey((k) => k + 1)}
              />
            </div>
          </>
        )}

        {/* Cost & Quality */}
        {costSummary && costSummary.session_count > 0 && (
          <>
            <div className="col-span-2 my-1">
              <Separator />
            </div>
            <div className="col-span-2">
              <CostQualityBlock summary={costSummary} />
            </div>
          </>
        )}

        {/* Definition-of-Done gates */}
        {(() => {
          const proj = (projects.find((p) => p.id === currentTask.project_id) ?? currentProject);
          const dodGates: DodGateConfig[] = (proj?.settings as { dod_gates?: DodGateConfig[] })?.dod_gates ?? [];
          if (dodGates.length === 0) return null;
          const checks = currentTask.dod_checks ?? {};
          return (
            <>
              <div className="col-span-2 my-1">
                <Separator />
              </div>
              <div className="col-span-2">
                <p className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  Definition of Done
                </p>
                <div className="flex flex-col gap-1">
                  {dodGates.map((gate) => {
                    const check: DodCheck | undefined = checks[gate.name];
                    const status = check?.status ?? "pending";
                    return (
                      <div key={gate.name} className="flex items-center gap-2 text-xs">
                        {status === "pass" ? (
                          <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-green-500" />
                        ) : status === "fail" ? (
                          <XCircle className="h-3.5 w-3.5 shrink-0 text-red-500" />
                        ) : (
                          <Circle className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        )}
                        <span className={status === "pass" ? "text-muted-foreground line-through" : "text-foreground"}>
                          {gate.name}
                        </span>
                        {gate.required && status !== "pass" && (
                          <Badge variant="outline" className="text-[9px] px-1 py-0 text-orange-600 border-orange-300">
                            required
                          </Badge>
                        )}
                      </div>
                    );
                  })}
                </div>
              </div>
            </>
          );
        })()}
      </div>
    </div>
  );

  const descriptionPanel = currentTask && (
    <div>
      <div className="mb-1.5 flex items-center justify-between">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Description
        </h3>
        <div className="flex items-center gap-2">
          {editingDescription ? (
            <>
              {descSaving && (
                <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
                  <Loader2 className="h-3 w-3 animate-spin" />
                  Saving…
                </span>
              )}
              {descSaved && !descSaving && (
                <span className="flex items-center gap-1 text-[11px] text-green-600">
                  <Check className="h-3 w-3" />
                  Saved
                </span>
              )}
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  if (descTimerRef.current)
                    clearTimeout(descTimerRef.current);
                  void flushDescription().then(() =>
                    setEditingDescription(false),
                  );
                }}
              >
                Done
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  if (descTimerRef.current)
                    clearTimeout(descTimerRef.current);
                  setDescDraft(currentTask.description ?? "");
                  setEditingDescription(false);
                }}
              >
                Cancel
              </Button>
            </>
          ) : (
            <button
              type="button"
              className="flex items-center gap-1 rounded px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              onClick={() => setEditingDescription(true)}
            >
              <Pencil className="h-3 w-3" />
              Edit
            </button>
          )}
        </div>
      </div>
      {editingDescription ? (
        <RichTextEditor
          value={descDraft}
          onChange={setDescDraft}
          placeholder="Add a description..."
          projId={currentTask.project_id}
          taskId={currentTask.id}
        />
      ) : (
        <div className="min-h-[60px] rounded-lg border border-border p-3 text-sm">
          {currentTask.description ? (
            <MarkdownWithRelay content={currentTask.description} projId={currentTask.project_id} />
          ) : (
            <span
              className="cursor-text text-muted-foreground hover:text-foreground"
              onClick={() => setEditingDescription(true)}
            >
              Add a description...
            </span>
          )}
        </div>
      )}
    </div>
  );

  const titleBlock = currentTask && (
    <div className="group">
      {editingTitle ? (
        <Input
          ref={titleInputRef}
          value={titleDraft}
          onChange={(e) => setTitleDraft(e.target.value)}
          onBlur={() => void handleTitleSave()}
          onKeyDown={handleTitleKeyDown}
          className="h-auto border-none bg-transparent p-0 text-xl font-bold tracking-tight shadow-none focus-visible:ring-1"
        />
      ) : (
        <div
          className="flex cursor-pointer items-start gap-2"
          onClick={() => setEditingTitle(true)}
        >
          <h2 className="flex-1 text-lg font-bold leading-tight tracking-tight sm:text-xl">
            {currentTask.title}
          </h2>
          <Pencil className="mt-1 h-3.5 w-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
        </div>
      )}
    </div>
  );

  // ---- Render ---------------------------------------------------------------

  return (
    <div className={cn("flex flex-col", className)}>
      {/* ------------------------------------------------------------------ */}
      {/* Header                                                              */}
      {/* ------------------------------------------------------------------ */}
      <div className="sticky top-0 z-10 flex shrink-0 items-center justify-between border-b border-border bg-background px-4 py-2.5 lg:static lg:top-auto">
        <div className="flex items-center gap-2 min-w-0">
          {taskIdStack.length > 0 ? (
            <button
              type="button"
              onClick={popTask}
              className="flex shrink-0 items-center gap-1 rounded p-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              title="Back to parent task"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              <span className="max-w-[180px] truncate">
                {backTask?.title ?? "Back"}
              </span>
            </button>
          ) : (
            <>
              {/* Back arrow — shown when onBack is provided and not in sub-task stack */}
              {onBack && (
                <button
                  type="button"
                  onClick={onBack}
                  className="flex shrink-0 items-center gap-1 rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  aria-label={backLabel}
                  title={backLabel}
                >
                  <ArrowLeft className="h-4 w-4" />
                </button>
              )}
              {currentTask && (
                <>
                  <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                    {currentTask.id.slice(0, 8).toUpperCase()}
                  </span>
                  {currentProject && (
                    <span className="truncate text-xs text-muted-foreground">
                      {currentProject.name}
                    </span>
                  )}
                </>
              )}
            </>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {currentTask && (
            <>
              <a
                href={`/t/${currentTask.id}`}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1 rounded p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                title="Open in new tab"
              >
                <ExternalLink className="h-4 w-4" />
              </a>
              <button
                type="button"
                onClick={async () => {
                  const url = `${window.location.origin}/t/${currentTask.id}`;
                  await navigator.clipboard.writeText(url);
                  toast.success("Link copied");
                }}
                className="flex items-center gap-1 rounded p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                title="Copy link"
              >
                <Link className="h-4 w-4" />
              </button>
            </>
          )}
          {onClose && (
            <button
              type="button"
              onClick={onClose}
              className="rounded p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              aria-label="Close"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>

      {/* ------------------------------------------------------------------ */}
      {/* Loading state                                                       */}
      {/* ------------------------------------------------------------------ */}
      {loading && (
        <div className="flex-1 overflow-auto p-5">
          <Skeleton className="mb-4 h-8 w-3/4" />
          <div className="grid grid-cols-5 gap-4">
            <div className="col-span-3 space-y-4">
              <Skeleton className="h-4 w-1/3" />
              <Skeleton className="h-24 w-full" />
              <Skeleton className="h-4 w-1/4" />
              <Skeleton className="h-32 w-full" />
            </div>
            <div className="col-span-2 space-y-3">
              <Skeleton className="h-48 w-full" />
            </div>
          </div>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Content                                                             */}
      {/* ------------------------------------------------------------------ */}
      {!loading && currentTask && (
        <>
          {/* ============================================================= */}
          {/* MOBILE LAYOUT (<1024px): title + 6-tab bar                     */}
          {/* ============================================================= */}
          <div className="flex min-h-0 flex-1 flex-col [overflow:clip] lg:hidden">
            {/* Title */}
            <div className="shrink-0 px-5 pt-4 pb-2">
              {titleBlock}
            </div>

            {/* 6-tab bar with horizontal scroll + fade mask */}
            <div className="sticky top-[44px] z-10 shrink-0 bg-background">
              <div
                className="flex overflow-x-auto border-b border-border [scrollbar-width:none] [scroll-snap-type:x_mandatory] [-webkit-overflow-scrolling:touch]"
              >
                {MOBILE_TABS.map((tab) => (
                  <button
                    key={tab.id}
                    type="button"
                    className={cn(
                      "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 text-xs font-medium transition-colors [scroll-snap-align:start]",
                      activeMobileTab === tab.id
                        ? "border-primary text-foreground"
                        : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
                    )}
                    onClick={() => setActiveMobileTab(tab.id)}
                  >
                    {tab.id === "subtasks" && <ListTree className="h-3.5 w-3.5" />}
                    {tab.id === "artifacts" && <Package className="h-3.5 w-3.5" />}
                    {tab.label}
                    {tab.id === "subtasks" &&
                      currentTask.subtask_count != null &&
                      currentTask.subtask_count > 0 && (
                        <Badge variant="secondary" className="text-[10px]">
                          {currentTask.subtask_count}
                        </Badge>
                      )}
                    {tab.id === "artifacts" &&
                      currentTask.artifact_count != null &&
                      currentTask.artifact_count > 0 && (
                        <Badge variant="secondary" className="text-[10px]">
                          {currentTask.artifact_count}
                        </Badge>
                      )}
                  </button>
                ))}
              </div>
              {/* Right-edge fade mask */}
              <div className="pointer-events-none absolute inset-y-0 right-0 w-10 bg-gradient-to-l from-background to-transparent" />
            </div>

            {/* Mobile tab content — comments gets its own bounded flex container so
                the reply form stays pinned at the bottom; all other tabs share a
                single scrollable wrapper. */}
            {activeMobileTab === "comments" ? (
              <div className="flex flex-1 min-h-0 flex-col overflow-hidden">
                <CommentList taskId={currentTask.id} projId={currentTask.project_id} />
              </div>
            ) : (
              <div className="flex-1 overflow-y-auto">
                {activeMobileTab === "details" && (
                  <div className="space-y-5 px-5 py-4">
                    {propertiesGrid}
                  </div>
                )}
                {activeMobileTab === "description" && (
                  <div className="px-5 py-4">
                    {descriptionPanel}
                  </div>
                )}
                {activeMobileTab === "subtasks" && (
                  <div className="p-3">
                    <SubtaskList
                      taskId={currentTask.id}
                      onOpenSubtask={pushTask}
                      refreshKey={depRefreshKey}
                    />
                  </div>
                )}
                {activeMobileTab === "artifacts" && (
                  <div className="p-3">
                    <ArtifactList
                      taskId={currentTask.id}
                      projId={currentTask.project_id}
                      projectSettings={
                        (projects.find((p) => p.id === currentTask.project_id) ?? currentProject)?.settings
                      }
                      onRelayDocSelect={(url) => {
                        setDescDraft((prev) => {
                          const sep = prev && !prev.endsWith("\n") ? "\n" : "";
                          return prev + sep + url + "\n";
                        });
                        setEditingDescription(true);
                        setActiveMobileTab("description");
                      }}
                    />
                  </div>
                )}
                {activeMobileTab === "activity" && (
                  <div className="p-4">
                    <ActivityLog taskId={currentTask.id} />
                  </div>
                )}
              </div>
            )}
          </div>

          {/* ============================================================= */}
          {/* DESKTOP LAYOUT (>=1024px): two-column                          */}
          {/* ============================================================= */}
          <div className="hidden min-h-0 flex-1 overflow-hidden lg:flex lg:flex-row">
            {/* LEFT PANEL */}
            <div className="flex min-w-0 flex-1 flex-col overflow-y-auto lg:border-r lg:border-border">
              <div className="flex-1 space-y-5 px-5 py-4">
                {titleBlock}
                {propertiesGrid}
                {descriptionPanel}
              </div>
            </div>

            {/* RIGHT PANEL — Comments / Subtasks / Artifacts / Activity */}
            <div className="flex w-full shrink-0 flex-col overflow-hidden border-t border-border lg:w-2/5 lg:border-t-0">
              {/* Tab bar */}
              <div className="flex shrink-0 overflow-x-auto border-b border-border">
                <button
                  type="button"
                  className={cn(
                    "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 text-xs font-medium transition-colors",
                    activeDesktopTab === "comments"
                      ? "border-primary text-foreground"
                      : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
                  )}
                  onClick={() => setActiveDesktopTab("comments")}
                >
                  Comments
                </button>
                <button
                  type="button"
                  className={cn(
                    "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 text-xs font-medium transition-colors",
                    activeDesktopTab === "subtasks"
                      ? "border-primary text-foreground"
                      : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
                  )}
                  onClick={() => setActiveDesktopTab("subtasks")}
                >
                  <ListTree className="h-3.5 w-3.5" />
                  Subtasks
                  {currentTask.subtask_count != null &&
                    currentTask.subtask_count > 0 && (
                      <Badge variant="secondary" className="text-[10px]">
                        {currentTask.subtask_count}
                      </Badge>
                    )}
                </button>
                <button
                  type="button"
                  className={cn(
                    "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 text-xs font-medium transition-colors",
                    activeDesktopTab === "artifacts"
                      ? "border-primary text-foreground"
                      : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
                  )}
                  onClick={() => setActiveDesktopTab("artifacts")}
                >
                  <Package className="h-3.5 w-3.5" />
                  Artifacts
                  {currentTask.artifact_count != null &&
                    currentTask.artifact_count > 0 && (
                      <Badge variant="secondary" className="text-[10px]">
                        {currentTask.artifact_count}
                      </Badge>
                    )}
                </button>
                <button
                  type="button"
                  className={cn(
                    "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 text-xs font-medium transition-colors",
                    activeDesktopTab === "activity"
                      ? "border-primary text-foreground"
                      : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
                  )}
                  onClick={() => setActiveDesktopTab("activity")}
                >
                  Activity
                </button>
              </div>

              {/* Tab content */}
              {activeDesktopTab === "comments" && (
                <CommentList taskId={currentTask.id} projId={currentTask.project_id} />
              )}
              {activeDesktopTab === "subtasks" && (
                <div className="flex-1 overflow-y-auto p-3">
                  <SubtaskList
                    taskId={currentTask.id}
                    onOpenSubtask={pushTask}
                    refreshKey={depRefreshKey}
                  />
                </div>
              )}
              {activeDesktopTab === "artifacts" && (
                <div className="flex-1 overflow-y-auto p-3">
                  <ArtifactList
                    taskId={currentTask.id}
                    projId={currentTask.project_id}
                    projectSettings={
                      (projects.find((p) => p.id === currentTask.project_id) ?? currentProject)?.settings
                    }
                    onRelayDocSelect={(url) => {
                      setDescDraft((prev) => {
                        const sep = prev && !prev.endsWith("\n") ? "\n" : "";
                        return prev + sep + url + "\n";
                      });
                      setEditingDescription(true);
                    }}
                  />
                </div>
              )}
              {activeDesktopTab === "activity" && (
                <div className="flex-1 overflow-y-auto p-4">
                  <ActivityLog taskId={currentTask.id} />
                </div>
              )}
            </div>
          </div>
        </>

      )}

      {/* No task loaded */}
      {!loading && !currentTask && taskId && (
        <div className="flex flex-1 items-center justify-center text-muted-foreground">
          <p className="text-sm">Task not found.</p>
        </div>
      )}

      {/* Recurring history panel */}
      {currentTask?.recurring_schedule_id && (() => {
        const schedule = schedules.find(
          (s) => s.id === currentTask.recurring_schedule_id,
        );
        if (!schedule) return null;
        return (
          <RecurringHistoryPanel
            open={recurringHistoryOpen}
            onOpenChange={setRecurringHistoryOpen}
            schedule={schedule}
            currentInstanceNumber={currentTask.recurring_instance_number}
          />
        );
      })()}
    </div>
  );
}
