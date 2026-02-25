import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { useNavigate, useParams } from "react-router";
import {
  ArrowLeft,
  Bot,
  Clock,
  Copy,
  Hourglass,
  MessageSquare,
  Activity,
  ListTree,
  Package,
  Pencil,
  SlidersHorizontal,
  Tag,
  User,
} from "lucide-react";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useAgentStore } from "@/stores/agent";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Separator } from "@/components/ui/separator";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ArtifactList } from "@/components/artifact-list";
import { CommentList } from "@/components/comment-list";
import { ActivityLog } from "@/components/activity-log";
import { SubtaskList } from "@/components/subtask-list";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import { DatePickerPopover } from "@/components/date-picker-popover";
import { VCSLinks } from "@/components/vcs-links";
import { cn } from "@/lib/cn";
import {
  formatDate,
  formatRelative,
  fromDateTimeLocal,
  priorityConfig,
  statusCategoryConfig,
  toDateTimeLocal,
} from "@/lib/utils";
import type { AssigneeType, Priority } from "@/types";

type TabId = "comments" | "activity" | "subtasks" | "artifacts";

const tabs: { id: TabId; label: string; icon: typeof MessageSquare }[] = [
  { id: "comments", label: "Comments", icon: MessageSquare },
  { id: "activity", label: "Activity", icon: Activity },
  { id: "subtasks", label: "Subtasks", icon: ListTree },
  { id: "artifacts", label: "Artifacts", icon: Package },
];

const priorities: Priority[] = ["urgent", "high", "medium", "low", "none"];

export function TaskDetailPage() {
  const { wsSlug, projectSlug, taskId } = useParams();
  const navigate = useNavigate();
  const { currentTask, fetchTask, updateTask, moveTask, duplicateTask } =
    useTaskStore();
  const { statuses, fetchStatuses } = useProjectStore();
  const { fields: customFieldDefs, fetchFields: fetchCustomFields } =
    useCustomFieldStore();
  const { agents, fetchAgents } = useAgentStore();
  const { user } = useAuthStore();
  const { currentWorkspace } = useWorkspaceStore();

  const [loading, setLoading] = useState(true);
  const [activeTab, setActiveTab] = useState<TabId>("comments");

  // Inline title editing
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const titleInputRef = useRef<HTMLInputElement>(null);

  // Load task, statuses, and custom field definitions
  useEffect(() => {
    async function load() {
      setLoading(true);
      try {
        if (taskId) {
          const task = await fetchTask(taskId);
          // Fetch statuses for the task's project if not already loaded
          if (statuses.length === 0 || statuses[0]?.project_id !== task.project_id) {
            await fetchStatuses(task.project_id);
          }
          // Fetch custom field definitions for the project
          fetchCustomFields(task.project_id).catch(() => {
            // Custom fields API may not be available yet
          });
        }
      } catch {
        // error handled by store
      } finally {
        setLoading(false);
      }
    }
    void load();
  }, [taskId, fetchTask, fetchStatuses, fetchCustomFields, statuses.length, statuses]);

  // Fetch agents for assignee dropdown
  useEffect(() => {
    if (currentWorkspace) {
      void fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchAgents]);

  // Sync title draft with current task
  useEffect(() => {
    if (currentTask) {
      setTitleDraft(currentTask.title);
    }
  }, [currentTask]);

  // Focus input when editing starts
  useEffect(() => {
    if (editingTitle) {
      titleInputRef.current?.focus();
      titleInputRef.current?.select();
    }
  }, [editingTitle]);

  const handleTitleSave = useCallback(async () => {
    setEditingTitle(false);
    if (!currentTask || titleDraft.trim() === currentTask.title) return;
    if (!titleDraft.trim()) {
      setTitleDraft(currentTask.title);
      return;
    }
    await updateTask(currentTask.id, { title: titleDraft.trim() });
  }, [currentTask, titleDraft, updateTask]);

  const handleTitleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      void handleTitleSave();
    }
    if (e.key === "Escape") {
      setEditingTitle(false);
      if (currentTask) setTitleDraft(currentTask.title);
    }
  };

  const handleStatusChange = async (statusId: string) => {
    if (!currentTask || statusId === currentTask.status_id) return;
    await moveTask(currentTask.id, { status_id: statusId });
    // Re-fetch to get updated task
    if (taskId) await fetchTask(taskId);
  };

  const handlePriorityChange = async (priority: Priority) => {
    if (!currentTask || priority === currentTask.priority) return;
    await updateTask(currentTask.id, { priority });
  };

  const handleAssigneeChange = async (value: string) => {
    if (!currentTask) return;
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
  };

  const handleDueDateChange = async (value: string) => {
    if (!currentTask) return;
    await updateTask(currentTask.id, {
      due_date: value ? fromDateTimeLocal(value) : null,
    });
  };

  const handleDuplicate = useCallback(async () => {
    if (!currentTask) return;
    const newTask = await duplicateTask(currentTask);
    if (newTask?.id) {
      navigate(
        `/w/${wsSlug}/p/${projectSlug}/t/${newTask.id}`,
      );
    }
  }, [currentTask, duplicateTask, navigate, wsSlug, projectSlug]);

  const handleCustomFieldChange = useCallback(
    async (slug: string, newValue: unknown) => {
      if (!currentTask) return;
      await updateTask(currentTask.id, {
        custom_fields: { ...(currentTask.custom_fields ?? {}), [slug]: newValue },
      });
    },
    [currentTask, updateTask],
  );

  if (loading || !currentTask) {
    return (
      <div className="mx-auto max-w-6xl space-y-6 px-4">
        <Skeleton className="h-8 w-32" />
        <Skeleton className="h-10 w-2/3" />
        <div className="grid grid-cols-3 gap-6">
          <div className="col-span-2 space-y-4">
            <Skeleton className="h-32 w-full" />
            <Skeleton className="h-48 w-full" />
          </div>
          <div className="space-y-4">
            <Skeleton className="h-64 w-full" />
          </div>
        </div>
      </div>
    );
  }

  const currentStatus = statuses.find((s) => s.id === currentTask.status_id);
  const category = currentStatus
    ? statusCategoryConfig[currentStatus.category]
    : null;
  const pConfig = priorityConfig[currentTask.priority];

  return (
    <div className="mx-auto max-w-6xl space-y-6 px-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => navigate(`/w/${wsSlug}/p/${projectSlug}`)}
        >
          <ArrowLeft className="h-4 w-4" />
          Back to board
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void handleDuplicate()}
        >
          <Copy className="mr-1 h-3.5 w-3.5" />
          Duplicate
        </Button>
      </div>

      {/* Title */}
      <div className="group">
        {editingTitle ? (
          <Input
            ref={titleInputRef}
            value={titleDraft}
            onChange={(e) => setTitleDraft(e.target.value)}
            onBlur={() => void handleTitleSave()}
            onKeyDown={handleTitleKeyDown}
            className="h-auto border-none bg-transparent p-0 text-2xl font-bold tracking-tight shadow-none focus-visible:ring-1"
          />
        ) : (
          <div
            className="flex cursor-pointer items-center gap-2"
            onClick={() => setEditingTitle(true)}
          >
            <h1 className="text-2xl font-bold tracking-tight">
              {currentTask.title}
            </h1>
            <Pencil className="h-4 w-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
          </div>
        )}
      </div>

      <Separator />

      {/* Two-column layout */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        {/* Left panel: 2/3 */}
        <div className="space-y-6 lg:col-span-2">
          {/* Description */}
          <div>
            <h2 className="mb-2 text-sm font-semibold text-muted-foreground">
              Description
            </h2>
            <div className="rounded-lg border border-border p-4 text-sm">
              {currentTask.description ? (
                <div className="whitespace-pre-wrap">{currentTask.description}</div>
              ) : (
                <span className="text-muted-foreground">
                  No description provided.
                </span>
              )}
            </div>
          </div>

          {/* Tabs */}
          <div>
            <div className="flex border-b border-border">
              {tabs.map((tab) => {
                const Icon = tab.icon;
                return (
                  <button
                    key={tab.id}
                    type="button"
                    className={cn(
                      "flex items-center gap-1.5 border-b-2 px-4 py-2 text-sm font-medium transition-colors",
                      activeTab === tab.id
                        ? "border-primary text-foreground"
                        : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
                    )}
                    onClick={() => setActiveTab(tab.id)}
                  >
                    <Icon className="h-4 w-4" />
                    {tab.label}
                  </button>
                );
              })}
            </div>

            <div className="pt-4">
              {activeTab === "comments" && (
                <CommentList taskId={currentTask.id} />
              )}
              {activeTab === "activity" && (
                <ActivityLog taskId={currentTask.id} />
              )}
              {activeTab === "subtasks" && (
                <SubtaskList taskId={currentTask.id} />
              )}
              {activeTab === "artifacts" && (
                <ArtifactList taskId={currentTask.id} />
              )}
            </div>
          </div>
        </div>

        {/* Right side panel: 1/3 */}
        <div>
          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="text-sm">Task Properties</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              {/* Status */}
              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">
                  Status
                </label>
                <div className="flex items-center gap-2">
                  {currentStatus && (
                    <span
                      className="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
                      style={{ backgroundColor: currentStatus.color }}
                    />
                  )}
                  <Select
                    value={currentTask.status_id}
                    onChange={(e) => void handleStatusChange(e.target.value)}
                    className="h-8 text-xs"
                  >
                    {statuses
                      .sort((a, b) => a.position - b.position)
                      .map((s) => (
                        <option key={s.id} value={s.id}>
                          {s.name}
                        </option>
                      ))}
                  </Select>
                </div>
                {category && (
                  <Badge variant="secondary" className="text-[10px]">
                    {category.label}
                  </Badge>
                )}
              </div>

              <Separator />

              {/* Priority */}
              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">
                  Priority
                </label>
                <Select
                  value={currentTask.priority}
                  onChange={(e) =>
                    void handlePriorityChange(e.target.value as Priority)
                  }
                  className="h-8 text-xs"
                >
                  {priorities.map((p) => {
                    const cfg = priorityConfig[p];
                    return (
                      <option key={p} value={p}>
                        {cfg.label}
                      </option>
                    );
                  })}
                </Select>
                <Badge
                  variant="outline"
                  className={cn("text-[10px]", pConfig.color)}
                >
                  {pConfig.label}
                </Badge>
              </div>

              <Separator />

              {/* Assignee */}
              <div className="space-y-1.5">
                <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <User className="h-3.5 w-3.5" />
                  Assignee
                </label>
                <div className="flex items-center gap-2">
                  {currentTask.assignee_id && (
                    currentTask.assignee_type === "agent" ? (
                      <Bot className="h-4 w-4 shrink-0 text-violet-500" />
                    ) : (
                      <User className="h-4 w-4 shrink-0 text-sky-500" />
                    )
                  )}
                  <Select
                    value={
                      currentTask.assignee_id
                        ? `${currentTask.assignee_type}:${currentTask.assignee_id}`
                        : "unassigned"
                    }
                    onChange={(e) => void handleAssigneeChange(e.target.value)}
                    className="h-8 text-xs"
                  >
                    <option value="unassigned">Unassigned</option>
                    {user && (
                      <option value={`user:${user.id}`}>
                        {user.name} (you)
                      </option>
                    )}
                    {agents.map((agent) => (
                      <option key={agent.id} value={`agent:${agent.id}`}>
                        {agent.name} (agent)
                      </option>
                    ))}
                  </Select>
                </div>
              </div>

              <Separator />

              {/* Due date */}
              <div className="space-y-1.5">
                <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  Due Date
                </label>
                <DatePickerPopover
                  value={
                    currentTask.due_date
                      ? toDateTimeLocal(currentTask.due_date)
                      : null
                  }
                  onChange={(val) => void handleDueDateChange(val ?? "")}
                  includeTime
                  placeholder="Set due date"
                />
              </div>

              <Separator />

              {/* Labels */}
              <div className="space-y-1.5">
                <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <Tag className="h-3.5 w-3.5" />
                  Labels
                </label>
                {(currentTask.labels ?? []).length > 0 ? (
                  <div className="flex flex-wrap gap-1">
                    {(currentTask.labels ?? []).map((label) => (
                      <Badge key={label} variant="secondary" className="text-xs">
                        {label}
                      </Badge>
                    ))}
                  </div>
                ) : (
                  <span className="text-sm text-muted-foreground">
                    No labels
                  </span>
                )}
              </div>

              {/* Custom Fields */}
              {customFieldDefs.length > 0 && (
                <>
                  <Separator />
                  <div className="space-y-3">
                    <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                      <SlidersHorizontal className="h-3.5 w-3.5" />
                      Custom Fields
                    </label>
                    {[...customFieldDefs]
                      .sort((a, b) => a.position - b.position)
                      .map((field) => (
                        <div key={field.id} className="space-y-1">
                          <label className="text-xs text-muted-foreground">
                            {field.name}
                            {field.is_required && (
                              <span className="ml-0.5 text-destructive">*</span>
                            )}
                          </label>
                          <CustomFieldRenderer
                            field={field}
                            value={currentTask.custom_fields?.[field.slug]}
                            onChange={(val) =>
                              void handleCustomFieldChange(field.slug, val)
                            }
                          />
                        </div>
                      ))}
                  </div>
                </>
              )}

              {/* Estimated hours */}
              {currentTask.estimated_hours != null && (
                <>
                  <Separator />
                  <div className="space-y-1.5">
                    <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                      <Hourglass className="h-3.5 w-3.5" />
                      Estimated Hours
                    </label>
                    <span className="text-sm">
                      {currentTask.estimated_hours}h
                    </span>
                  </div>
                </>
              )}

              <Separator />

              {/* VCS Links */}
              <VCSLinks taskId={currentTask.id} />

              <Separator />

              {/* Created / Updated */}
              <div className="space-y-1.5">
                <label className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <Clock className="h-3.5 w-3.5" />
                  Created
                </label>
                <span className="text-sm">
                  {formatDate(currentTask.created_at)}
                </span>
              </div>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">
                  Updated
                </label>
                <span className="text-sm">
                  {formatRelative(currentTask.updated_at)}
                </span>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
