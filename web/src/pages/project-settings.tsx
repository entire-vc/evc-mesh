import { type FormEvent, useCallback, useEffect, useState } from "react";
import { toast } from "@/components/ui/toast";
import { useNavigate, useParams } from "react-router";
import {
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Bot,
  Eye,
  GitBranch,
  GripVertical,
  History,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Save,
  Settings,
  Shield,
  Trash2,
  Users,
  X,
  Zap,
} from "lucide-react";
import { useProjectStore } from "@/stores/project";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import { useRulesStore } from "@/stores/rules";
import { useAgentStore } from "@/stores/agent";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Avatar } from "@/components/ui/avatar";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { StatusDialog } from "@/components/status-dialog";
import { CustomFieldDialog } from "@/components/custom-field-dialog";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { AddProjectMemberDialog } from "@/components/add-project-member-dialog";
import { CreateRecurringDialog } from "@/components/create-recurring-dialog";
import { RecurringHistoryPanel } from "@/components/recurring-history-panel";
import { useRecurringStore } from "@/stores/recurring";
import { useTemplateStore } from "@/stores/template";
import { statusCategoryConfig } from "@/lib/utils";
import { cn } from "@/lib/cn";
import type {
  Agent,
  AssignmentRulesConfig,
  CustomFieldDefinition,
  EffectiveAssignmentRules,
  Priority,
  ProjectMemberWithUser,
  ProjectRole,
  RecurringSchedule,
  TaskStatus,
  TaskTemplate,
  TransitionRule,
  WorkflowRulesConfig,
  WorkflowRulesResponse,
  WorkspaceMemberWithUser,
} from "@/types";

const fieldTypeLabels: Record<string, string> = {
  text: "Text",
  number: "Number",
  date: "Date",
  datetime: "Date & Time",
  select: "Select",
  multiselect: "Multi-Select",
  url: "URL",
  email: "Email",
  checkbox: "Checkbox",
  user_ref: "User Ref",
  agent_ref: "Agent Ref",
  json: "JSON",
};

const PRIORITIES = ["urgent", "high", "medium", "low", "none"];

// ---- Workflow Rules section component ----

const ACTOR_ROLES = ["owner", "admin", "member", "viewer", "agent"] as const;

interface WorkflowRulesSectionProps {
  workflowRules: WorkflowRulesResponse | null;
  isLoading: boolean;
  onSave: (config: WorkflowRulesConfig) => Promise<void>;
  isSaving: boolean;
  statuses: TaskStatus[];
  agents: Agent[];
  members: WorkspaceMemberWithUser[];
}

// Parse an allowed array into typed buckets
function parseAllowed(allowed: string[]): {
  roles: string[];
  agents: string[];
  users: string[];
} {
  const roles: string[] = [];
  const agentIds: string[] = [];
  const userIds: string[] = [];
  for (const a of allowed) {
    if (a.startsWith("agent:")) agentIds.push(a.slice(6));
    else if (a.startsWith("user:")) userIds.push(a.slice(5));
    else if (a.startsWith("role:")) roles.push(a.slice(5));
    else roles.push(a); // legacy: plain role name without prefix
  }
  return { roles, agents: agentIds, users: userIds };
}

// Re-build allowed array from typed buckets
function buildAllowed(
  roles: string[],
  agentIds: string[],
  userIds: string[],
): string[] {
  return [
    ...roles.map((r) => `role:${r}`),
    ...agentIds.map((id) => `agent:${id}`),
    ...userIds.map((id) => `user:${id}`),
  ];
}

function WorkflowRulesSection({
  workflowRules,
  isLoading,
  onSave,
  isSaving,
  statuses,
  agents,
  members,
}: WorkflowRulesSectionProps) {
  const [enforcementMode, setEnforcementMode] = useState(
    workflowRules?.enforcement_mode ?? "advisory",
  );
  const [transitions, setTransitions] = useState<
    { from: string; to: string; allowed: string[]; description: string }[]
  >(
    Object.entries(workflowRules?.transitions ?? {}).map(([key, rule]) => {
      const [from, to] = key.split("->").map((s) => s.trim());
      return {
        from: from ?? key,
        to: to ?? "",
        allowed: (rule as TransitionRule).allowed,
        description: (rule as TransitionRule).description ?? "",
      };
    }),
  );
  const [feedback, setFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);
  const [confirmStrictOpen, setConfirmStrictOpen] = useState(false);

  // Sync if workflowRules changes from fetch
  useEffect(() => {
    setEnforcementMode(workflowRules?.enforcement_mode ?? "advisory");
    setTransitions(
      Object.entries(workflowRules?.transitions ?? {}).map(([key, rule]) => {
        const [from, to] = key.split("->").map((s) => s.trim());
        return {
          from: from ?? key,
          to: to ?? "",
          allowed: (rule as TransitionRule).allowed,
          description: (rule as TransitionRule).description ?? "",
        };
      }),
    );
  }, [workflowRules]);

  const handleSave = async () => {
    setFeedback(null);
    const transitionsMap: Record<string, TransitionRule> = {};
    for (const t of transitions) {
      if (!t.from.trim() || !t.to.trim()) continue;
      const key = `${t.from.trim()} -> ${t.to.trim()}`;
      transitionsMap[key] = {
        allowed: t.allowed.filter(Boolean),
        description: t.description.trim() || undefined,
      };
    }
    const config: WorkflowRulesConfig = {
      enforcement_mode: enforcementMode,
      transitions: transitionsMap,
    };
    try {
      await onSave(config);
      setFeedback({ type: "success", message: "Workflow rules saved." });
      toast.success("Workflow rules saved.");
    } catch (err) {
      setFeedback({
        type: "error",
        message: err instanceof Error ? err.message : "Failed to save rules",
      });
      toast.error(err instanceof Error ? err.message : "Failed to save rules");
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-3/4" />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      {/* Enforcement mode */}
      <div className="space-y-1.5">
        <label className="text-sm font-medium">Enforcement Mode</label>
        <div className="flex gap-2">
          <Button
            type="button"
            variant={enforcementMode === "advisory" ? "default" : "outline"}
            size="sm"
            onClick={() => setEnforcementMode("advisory")}
          >
            Advisory
          </Button>
          <Button
            type="button"
            variant={enforcementMode === "strict" ? "default" : "outline"}
            size="sm"
            onClick={() => setConfirmStrictOpen(true)}
          >
            Strict
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {enforcementMode === "strict"
            ? "Strict mode blocks task actions that violate workflow rules."
            : "Advisory mode warns on violations but does not block actions."}
        </p>
      </div>

      <ConfirmDialog
        open={confirmStrictOpen}
        onClose={() => setConfirmStrictOpen(false)}
        onConfirm={() => {
          setEnforcementMode("strict");
          setConfirmStrictOpen(false);
        }}
        title="Enable Strict Enforcement?"
        description="Strict mode will block task actions that violate workflow rules. Are you sure you want to enable strict enforcement?"
        confirmText="Enable Strict"
      />

      {/* Transitions table */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">Allowed Transitions</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setTransitions((prev) => [
                ...prev,
                { from: "", to: "", allowed: [], description: "" },
              ])
            }
          >
            <Plus className="h-3.5 w-3.5" />
            Add Transition
          </Button>
        </div>
        {transitions.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            No transitions configured. All transitions are permitted when no rules are set.
          </p>
        ) : (
          <div className="space-y-3">
            {transitions.map((t, i) => (
              <div
                key={i}
                className="rounded-lg border border-border p-3 space-y-2"
              >
                {/* From → To row */}
                <div className="flex items-center gap-2">
                  <Select
                    value={t.from}
                    onChange={(e) =>
                      setTransitions((prev) =>
                        prev.map((r, idx) =>
                          idx === i ? { ...r, from: e.target.value } : r,
                        ),
                      )
                    }
                    className="flex-1 text-sm"
                  >
                    <option value="">Select status...</option>
                    {statuses.map((s) => (
                      <option key={s.id} value={s.name}>
                        {s.name}
                      </option>
                    ))}
                  </Select>
                  <ArrowRight className="h-4 w-4 text-muted-foreground shrink-0" />
                  <Select
                    value={t.to}
                    onChange={(e) =>
                      setTransitions((prev) =>
                        prev.map((r, idx) =>
                          idx === i ? { ...r, to: e.target.value } : r,
                        ),
                      )
                    }
                    className="flex-1 text-sm"
                  >
                    <option value="">Select status...</option>
                    {statuses.map((s) => (
                      <option key={s.id} value={s.name}>
                        {s.name}
                      </option>
                    ))}
                  </Select>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-muted-foreground hover:text-destructive shrink-0"
                    onClick={() =>
                      setTransitions((prev) => prev.filter((_, idx) => idx !== i))
                    }
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
                {/* Allowed actors */}
                <div className="space-y-2">
                  <span className="text-xs text-muted-foreground font-medium">
                    Allowed actors
                  </span>
                  {/* Role pills */}
                  <div className="space-y-1">
                    <span className="text-xs text-muted-foreground">
                      Roles
                    </span>
                    <div className="flex flex-wrap gap-1.5">
                      {ACTOR_ROLES.map((role) => {
                        const parsed = parseAllowed(t.allowed);
                        const selected = parsed.roles.includes(role);
                        return (
                          <Badge
                            key={role}
                            variant={selected ? "default" : "outline"}
                            className="cursor-pointer select-none capitalize"
                            onClick={() =>
                              setTransitions((prev) =>
                                prev.map((r, idx) => {
                                  if (idx !== i) return r;
                                  const p = parseAllowed(r.allowed);
                                  const nextRoles = selected
                                    ? p.roles.filter((x) => x !== role)
                                    : [...p.roles, role];
                                  return {
                                    ...r,
                                    allowed: buildAllowed(
                                      nextRoles,
                                      p.agents,
                                      p.users,
                                    ),
                                  };
                                }),
                              )
                            }
                          >
                            {role}
                          </Badge>
                        );
                      })}
                    </div>
                  </div>
                  {/* Specific actors */}
                  <div className="space-y-1.5">
                    <span className="text-xs text-muted-foreground">
                      Specific actors
                    </span>
                    {(() => {
                      const parsed = parseAllowed(t.allowed);
                      const hasActors =
                        parsed.agents.length > 0 || parsed.users.length > 0;
                      return (
                        <>
                          {hasActors && (
                            <div className="flex flex-wrap gap-1.5">
                              {parsed.agents.map((agentId) => {
                                const agent = agents.find(
                                  (a) => a.id === agentId,
                                );
                                return (
                                  <Badge
                                    key={agentId}
                                    variant="secondary"
                                    className="gap-1 pr-1"
                                  >
                                    <Bot className="h-3 w-3" />
                                    {agent?.name ?? agentId.slice(0, 8)}
                                    <button
                                      type="button"
                                      className="ml-0.5 rounded-sm hover:bg-muted-foreground/20"
                                      onClick={() =>
                                        setTransitions((prev) =>
                                          prev.map((r, idx) => {
                                            if (idx !== i) return r;
                                            const p = parseAllowed(r.allowed);
                                            return {
                                              ...r,
                                              allowed: buildAllowed(
                                                p.roles,
                                                p.agents.filter(
                                                  (id) => id !== agentId,
                                                ),
                                                p.users,
                                              ),
                                            };
                                          }),
                                        )
                                      }
                                    >
                                      <X className="h-3 w-3" />
                                    </button>
                                  </Badge>
                                );
                              })}
                              {parsed.users.map((userId) => {
                                const member = members.find(
                                  (m) => m.user_id === userId,
                                );
                                return (
                                  <Badge
                                    key={userId}
                                    variant="secondary"
                                    className="gap-1 pr-1"
                                  >
                                    <Users className="h-3 w-3" />
                                    {member?.user?.name ?? userId.slice(0, 8)}
                                    <button
                                      type="button"
                                      className="ml-0.5 rounded-sm hover:bg-muted-foreground/20"
                                      onClick={() =>
                                        setTransitions((prev) =>
                                          prev.map((r, idx) => {
                                            if (idx !== i) return r;
                                            const p = parseAllowed(r.allowed);
                                            return {
                                              ...r,
                                              allowed: buildAllowed(
                                                p.roles,
                                                p.agents,
                                                p.users.filter(
                                                  (id) => id !== userId,
                                                ),
                                              ),
                                            };
                                          }),
                                        )
                                      }
                                    >
                                      <X className="h-3 w-3" />
                                    </button>
                                  </Badge>
                                );
                              })}
                            </div>
                          )}
                          {/* Add actor dropdown */}
                          <Select
                            value=""
                            onChange={(e) => {
                              const val = e.target.value;
                              if (!val) return;
                              setTransitions((prev) =>
                                prev.map((r, idx) => {
                                  if (idx !== i) return r;
                                  const p = parseAllowed(r.allowed);
                                  if (val.startsWith("agent:")) {
                                    const id = val.slice(6);
                                    if (p.agents.includes(id)) return r;
                                    return {
                                      ...r,
                                      allowed: buildAllowed(
                                        p.roles,
                                        [...p.agents, id],
                                        p.users,
                                      ),
                                    };
                                  } else if (val.startsWith("user:")) {
                                    const id = val.slice(5);
                                    if (p.users.includes(id)) return r;
                                    return {
                                      ...r,
                                      allowed: buildAllowed(
                                        p.roles,
                                        p.agents,
                                        [...p.users, id],
                                      ),
                                    };
                                  }
                                  return r;
                                }),
                              );
                            }}
                            className="text-sm"
                          >
                            <option value="">
                              + Add agent or member...
                            </option>
                            {agents.length > 0 && (
                              <optgroup label="Agents">
                                {agents
                                  .filter(
                                    (a) =>
                                      !parseAllowed(t.allowed).agents.includes(
                                        a.id,
                                      ),
                                  )
                                  .map((a) => (
                                    <option
                                      key={a.id}
                                      value={`agent:${a.id}`}
                                    >
                                      {a.name}
                                    </option>
                                  ))}
                              </optgroup>
                            )}
                            {members.length > 0 && (
                              <optgroup label="Members">
                                {members
                                  .filter(
                                    (m) =>
                                      !parseAllowed(t.allowed).users.includes(
                                        m.user_id,
                                      ),
                                  )
                                  .map((m) => (
                                    <option
                                      key={m.user_id}
                                      value={`user:${m.user_id}`}
                                    >
                                      {m.user.name} ({m.user.email})
                                    </option>
                                  ))}
                              </optgroup>
                            )}
                          </Select>
                        </>
                      );
                    })()}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* My permissions (read-only, from backend) */}
      {workflowRules?.my_permissions && (
        <div className="rounded-lg border border-border bg-muted/30 p-3 space-y-2">
          <div className="flex items-center gap-2">
            <Shield className="h-4 w-4 text-muted-foreground" />
            <span className="text-sm font-medium">
              My permissions ({workflowRules.my_permissions.my_role})
            </span>
          </div>
          <div className="flex flex-wrap gap-2 text-xs">
            {workflowRules.my_permissions.can_create_tasks && (
              <Badge variant="outline">Create tasks</Badge>
            )}
            {workflowRules.my_permissions.can_delete_tasks && (
              <Badge variant="outline">Delete tasks</Badge>
            )}
            {workflowRules.my_permissions.can_reassign && (
              <Badge variant="outline">Reassign</Badge>
            )}
            {Object.entries(
              workflowRules.my_permissions.can_transition,
            ).map(([key, allowed]) => (
              <Badge
                key={key}
                variant={allowed ? "secondary" : "outline"}
                className={cn(!allowed && "opacity-50")}
              >
                {allowed ? "" : "no "}{key.replace("->", "→")}
              </Badge>
            ))}
          </div>
        </div>
      )}

      {feedback && (
        <p
          className={cn(
            "text-sm",
            feedback.type === "success" ? "text-green-600" : "text-destructive",
          )}
        >
          {feedback.message}
        </p>
      )}

      <div className="flex justify-end">
        <Button type="button" onClick={handleSave} disabled={isSaving}>
          <Save className="h-4 w-4" />
          {isSaving ? "Saving..." : "Save Rules"}
        </Button>
      </div>
    </div>
  );
}

// ---- Project Assignment Rules section ----

// ---- AssigneeSelect helper ----

function AssigneeSelect({
  value,
  onChange,
  agents,
  members,
  placeholder = "Select assignee...",
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  agents: Agent[];
  members: WorkspaceMemberWithUser[];
  placeholder?: string;
  className?: string;
}) {
  return (
    <Select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className={className}
    >
      <option value="">{placeholder}</option>
      {agents.length > 0 && (
        <optgroup label="Agents">
          {agents.map((a) => (
            <option key={a.id} value={`agent:${a.id}`}>
              {a.name}
            </option>
          ))}
        </optgroup>
      )}
      {members.length > 0 && (
        <optgroup label="Members">
          {members.map((m) => (
            <option key={m.user_id} value={`user:${m.user_id}`}>
              {m.user.name} ({m.user.email})
            </option>
          ))}
        </optgroup>
      )}
    </Select>
  );
}

// ---- Project Assignment Rules section ----

interface ProjectAssignmentRulesSectionProps {
  effectiveRules: EffectiveAssignmentRules | null;
  isLoading: boolean;
  onSave: (config: AssignmentRulesConfig) => Promise<void>;
  isSaving: boolean;
  agents: Agent[];
  members: WorkspaceMemberWithUser[];
}

function ProjectAssignmentRulesSection({
  effectiveRules,
  isLoading,
  onSave,
  isSaving,
  agents,
  members,
}: ProjectAssignmentRulesSectionProps) {
  const [defaultAssignee, setDefaultAssignee] = useState(
    effectiveRules?.default_assignee?.value ?? "",
  );
  const [byType, setByType] = useState<{ type: string; assignee: string }[]>(
    Object.entries(effectiveRules?.by_type ?? {}).map(([type, rule]) => ({
      type,
      assignee: rule.value,
    })),
  );
  const [byPriority, setByPriority] = useState<
    { priority: string; assignee: string }[]
  >(
    Object.entries(effectiveRules?.by_priority ?? {}).map(
      ([priority, rule]) => ({ priority, assignee: rule.value }),
    ),
  );
  const [fallbackChain, setFallbackChain] = useState<string[]>(
    effectiveRules?.fallback_chain ?? [],
  );
  const [feedback, setFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);

  // Sync when effectiveRules loads
  useEffect(() => {
    setDefaultAssignee(effectiveRules?.default_assignee?.value ?? "");
    setByType(
      Object.entries(effectiveRules?.by_type ?? {}).map(([type, rule]) => ({
        type,
        assignee: rule.value,
      })),
    );
    setByPriority(
      Object.entries(effectiveRules?.by_priority ?? {}).map(
        ([priority, rule]) => ({ priority, assignee: rule.value }),
      ),
    );
    setFallbackChain(effectiveRules?.fallback_chain ?? []);
  }, [effectiveRules]);

  const handleSave = async () => {
    setFeedback(null);
    // Only save values that are project-level overrides, not inherited workspace
    // values. This prevents accidental workspace inheritance breakage when the
    // user opens project settings and clicks Save without changing anything.
    const config: AssignmentRulesConfig = {};
    const daVal = defaultAssignee.trim();
    if (daVal) {
      const wsSource = effectiveRules?.default_assignee?.source;
      const wsVal = effectiveRules?.default_assignee?.value ?? "";
      if (wsSource === "project" || daVal !== wsVal) {
        config.default_assignee = daVal;
      }
    }
    const filteredByType = byType.filter((r) => r.type.trim() && r.assignee.trim());
    if (filteredByType.length > 0) {
      const projEntries = filteredByType.filter((r) => {
        const eff = effectiveRules?.by_type?.[r.type.trim()];
        return !eff || eff.source === "project" || eff.value !== r.assignee.trim();
      });
      if (projEntries.length > 0) {
        config.by_type = Object.fromEntries(
          projEntries.map((r) => [r.type.trim(), r.assignee.trim()]),
        );
      }
    }
    const filteredByPriority = byPriority.filter((r) => r.priority && r.assignee.trim());
    if (filteredByPriority.length > 0) {
      const projEntries = filteredByPriority.filter((r) => {
        const eff = effectiveRules?.by_priority?.[r.priority];
        return !eff || eff.source === "project" || eff.value !== r.assignee.trim();
      });
      if (projEntries.length > 0) {
        config.by_priority = Object.fromEntries(
          projEntries.map((r) => [r.priority, r.assignee.trim()]),
        );
      }
    }
    if (fallbackChain.some((v) => v.trim())) {
      config.fallback_chain = fallbackChain.filter((v) => v.trim());
    }
    try {
      await onSave(config);
      setFeedback({ type: "success", message: "Project assignment rules saved." });
    } catch (err) {
      setFeedback({
        type: "error",
        message: err instanceof Error ? err.message : "Failed to save rules",
      });
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-3/4" />
      </div>
    );
  }

  // Source badge helper
  const SourceBadge = ({ source }: { source?: string }) => {
    if (!source || source === "project") return null;
    return (
      <Badge variant="outline" className="text-xs ml-1.5">
        Inherited from {source}
      </Badge>
    );
  };

  return (
    <div className="space-y-5">
      {/* Default assignee */}
      <div className="space-y-1.5">
        <div className="flex items-center">
          <label className="text-sm font-medium">Default Assignee</label>
          <SourceBadge source={effectiveRules?.default_assignee?.source} />
        </div>
        <AssigneeSelect
          value={defaultAssignee}
          onChange={setDefaultAssignee}
          agents={agents}
          members={members}
          placeholder="None (inherit from workspace)"
        />
        <p className="text-xs text-muted-foreground">
          Overrides workspace-level default assignee for this project.
        </p>
      </div>

      {/* By type rules */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">By Task Type</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setByType((prev) => [...prev, { type: "", assignee: "" }])
            }
          >
            <Plus className="h-3.5 w-3.5" />
            Add Rule
          </Button>
        </div>
        {byType.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            No type-based rules for this project.
          </p>
        ) : (
          <div className="space-y-2">
            {byType.map((rule, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  value={rule.type}
                  onChange={(e) =>
                    setByType((prev) =>
                      prev.map((r, idx) =>
                        idx === i ? { ...r, type: e.target.value } : r,
                      ),
                    )
                  }
                  placeholder="Task type"
                  className="flex-1"
                />
                <ArrowRight className="h-4 w-4 text-muted-foreground shrink-0" />
                <AssigneeSelect
                  value={rule.assignee}
                  onChange={(val) =>
                    setByType((prev) =>
                      prev.map((r, idx) =>
                        idx === i ? { ...r, assignee: val } : r,
                      ),
                    )
                  }
                  agents={agents}
                  members={members}
                  className="flex-1"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-muted-foreground hover:text-destructive shrink-0"
                  onClick={() =>
                    setByType((prev) => prev.filter((_, idx) => idx !== i))
                  }
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* By priority */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">By Priority</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setByPriority((prev) => [
                ...prev,
                { priority: "medium", assignee: "" },
              ])
            }
          >
            <Plus className="h-3.5 w-3.5" />
            Add Rule
          </Button>
        </div>
        {byPriority.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            No priority-based rules for this project.
          </p>
        ) : (
          <div className="space-y-2">
            {byPriority.map((rule, i) => (
              <div key={i} className="flex items-center gap-2">
                <Select
                  value={rule.priority}
                  onChange={(e) =>
                    setByPriority((prev) =>
                      prev.map((r, idx) =>
                        idx === i ? { ...r, priority: e.target.value } : r,
                      ),
                    )
                  }
                  className="flex-1"
                >
                  {PRIORITIES.map((p) => (
                    <option key={p} value={p} className="capitalize">
                      {p.charAt(0).toUpperCase() + p.slice(1)}
                    </option>
                  ))}
                </Select>
                <ArrowRight className="h-4 w-4 text-muted-foreground shrink-0" />
                <AssigneeSelect
                  value={rule.assignee}
                  onChange={(val) =>
                    setByPriority((prev) =>
                      prev.map((r, idx) =>
                        idx === i ? { ...r, assignee: val } : r,
                      ),
                    )
                  }
                  agents={agents}
                  members={members}
                  className="flex-1"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-muted-foreground hover:text-destructive shrink-0"
                  onClick={() =>
                    setByPriority((prev) => prev.filter((_, idx) => idx !== i))
                  }
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Fallback chain */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">Fallback Chain</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setFallbackChain((prev) => [...prev, ""])}
          >
            <Plus className="h-3.5 w-3.5" />
            Add
          </Button>
        </div>
        {fallbackChain.length === 0 ? (
          <p className="text-xs text-muted-foreground">No fallback chain for this project.</p>
        ) : (
          <div className="space-y-2">
            {fallbackChain.map((val, i) => (
              <div key={i} className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground w-5 shrink-0">
                  {i + 1}.
                </span>
                <AssigneeSelect
                  value={val}
                  onChange={(newVal) =>
                    setFallbackChain((prev) =>
                      prev.map((v, idx) => (idx === i ? newVal : v)),
                    )
                  }
                  agents={agents}
                  members={members}
                  className="flex-1"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-muted-foreground hover:text-destructive shrink-0"
                  onClick={() =>
                    setFallbackChain((prev) => prev.filter((_, idx) => idx !== i))
                  }
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      {feedback && (
        <p
          className={cn(
            "text-sm",
            feedback.type === "success" ? "text-green-600" : "text-destructive",
          )}
        >
          {feedback.message}
        </p>
      )}

      <div className="flex justify-end">
        <Button type="button" onClick={handleSave} disabled={isSaving}>
          <Save className="h-4 w-4" />
          {isSaving ? "Saving..." : "Save Rules"}
        </Button>
      </div>
    </div>
  );
}

// ---- Main page ----

export function ProjectSettingsPage() {
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const {
    currentProject,
    statuses,
    fetchStatuses,
    updateProject,
    deleteProject,
    reorderStatuses,
    deleteStatus,
  } = useProjectStore();

  const {
    fields: customFields,
    fetchFields,
    deleteField,
    reorderFields,
  } = useCustomFieldStore();

  const { currentWorkspace } = useWorkspaceStore();
  const { user } = useAuthStore();
  const {
    workspaceMembers,
    projectMembers,
    isLoadingProjectMembers,
    fetchWorkspaceMembers,
    fetchProjectMembers,
    updateProjectMemberRole,
    removeProjectMember,
    addProjectAgentMember,
    removeProjectAgentMember,
  } = useMemberStore();

  const {
    workflowRules,
    isWorkflowLoading,
    fetchWorkflowRules,
    saveWorkflowRules,
    effectiveAssignmentRules,
    isProjRulesLoading,
    fetchEffectiveAssignmentRules,
    saveProjectAssignmentRules,
  } = useRulesStore();

  const { agents, fetchAgents } = useAgentStore();

  // --- General info form state ---
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [icon, setIcon] = useState("");
  const [isSavingGeneral, setIsSavingGeneral] = useState(false);
  const [generalFeedback, setGeneralFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);

  // --- Status management state ---
  const [statusDialogOpen, setStatusDialogOpen] = useState(false);
  const [editingStatus, setEditingStatus] = useState<TaskStatus | undefined>(
    undefined,
  );
  const [deleteStatusDialogOpen, setDeleteStatusDialogOpen] = useState(false);
  const [statusToDelete, setStatusToDelete] = useState<TaskStatus | null>(null);
  const [isDeletingStatus, setIsDeletingStatus] = useState(false);
  const [statusError, setStatusError] = useState<string | null>(null);

  // --- Custom field management state ---
  const [fieldDialogOpen, setFieldDialogOpen] = useState(false);
  const [editingField, setEditingField] = useState<
    CustomFieldDefinition | undefined
  >(undefined);
  const [deleteFieldDialogOpen, setDeleteFieldDialogOpen] = useState(false);
  const [fieldToDelete, setFieldToDelete] =
    useState<CustomFieldDefinition | null>(null);
  const [isDeletingField, setIsDeletingField] = useState(false);
  const [fieldError, setFieldError] = useState<string | null>(null);

  // --- Project members state ---
  const [addMemberDialogOpen, setAddMemberDialogOpen] = useState(false);
  const [memberToRemove, setMemberToRemove] =
    useState<ProjectMemberWithUser | null>(null);
  const [isRemovingMember, setIsRemovingMember] = useState(false);
  const [memberError, setMemberError] = useState<string | null>(null);

  // --- Danger zone state ---
  const [archiveDialogOpen, setArchiveDialogOpen] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [isArchiving, setIsArchiving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  // --- Rules saving state ---
  const [isSavingWorkflow, setIsSavingWorkflow] = useState(false);
  const [isSavingAssignment, setIsSavingAssignment] = useState(false);

  // --- Tab state ---
  const [activeTab, setActiveTab] = useState("general");

  const PROJECT_TABS = [
    { id: "general", label: "General" },
    { id: "statuses", label: "Statuses" },
    { id: "custom-fields", label: "Custom Fields" },
    { id: "members", label: "Members" },
    { id: "workflow", label: "Workflow Rules" },
    { id: "assignment", label: "Assignment Rules" },
    { id: "recurring", label: "Recurring" },
    { id: "templates", label: "Templates" },
  ];

  // --- Recurring state ---
  const {
    schedules: recurringSchedules,
    fetchSchedules: fetchRecurringSchedules,
    updateSchedule: updateRecurringSchedule,
    deleteSchedule: deleteRecurringSchedule,
    triggerNow: triggerRecurringNow,
  } = useRecurringStore();
  const [recurringDialogOpen, setRecurringDialogOpen] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState<RecurringSchedule | null>(null);
  const [historySchedule, setHistorySchedule] = useState<RecurringSchedule | null>(null);
  const [recurringActionLoading, setRecurringActionLoading] = useState<string | null>(null);

  // --- Templates state ---
  const {
    templates,
    fetchTemplates,
    createTemplate,
    updateTemplate,
    deleteTemplate,
  } = useTemplateStore();
  const [templateDialogOpen, setTemplateDialogOpen] = useState(false);
  const [editingTemplate, setEditingTemplate] = useState<TaskTemplate | null>(null);
  const [templateActionLoading, setTemplateActionLoading] = useState<string | null>(null);
  const [templateForm, setTemplateForm] = useState({
    name: "",
    description: "",
    title_template: "",
    description_template: "",
    priority: "medium" as Priority,
    labels: "",
  });
  const [templateFormError, setTemplateFormError] = useState<string | null>(null);

  // Populate general form when project changes
  useEffect(() => {
    if (currentProject) {
      setName(currentProject.name);
      setSlug(currentProject.slug);
      setDescription(currentProject.description || "");
      setIcon(currentProject.icon || "");
    }
  }, [currentProject]);

  // Fetch statuses, custom fields, members, and rules on mount
  useEffect(() => {
    if (currentProject) {
      fetchStatuses(currentProject.id);
      fetchFields(currentProject.id);
      fetchProjectMembers(currentProject.id);
      void fetchWorkflowRules(currentProject.id);
      void fetchEffectiveAssignmentRules(currentProject.id);
      void fetchRecurringSchedules(currentProject.id);
      void fetchTemplates(currentProject.id);
    }
  }, [
    currentProject,
    fetchStatuses,
    fetchFields,
    fetchProjectMembers,
    fetchWorkflowRules,
    fetchEffectiveAssignmentRules,
    fetchRecurringSchedules,
    fetchTemplates,
  ]);

  // Fetch workspace members and agents for assignment selects
  useEffect(() => {
    if (currentWorkspace?.id) {
      void fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace?.id, fetchAgents]);

  // Fetch workspace members for the add-member dialog
  useEffect(() => {
    if (currentWorkspace?.id) {
      fetchWorkspaceMembers(currentWorkspace.id);
    }
  }, [currentWorkspace?.id, fetchWorkspaceMembers]);

  // --- General info handlers ---
  const handleSaveGeneral = async (e: FormEvent) => {
    e.preventDefault();
    if (!currentProject) return;

    if (!name.trim()) {
      setGeneralFeedback({ type: "error", message: "Name is required" });
      return;
    }

    setIsSavingGeneral(true);
    setGeneralFeedback(null);

    try {
      await updateProject(currentProject.id, {
        name: name.trim(),
        slug: slug.trim(),
        description: description.trim() || undefined,
        icon: icon.trim() || undefined,
      });
      setGeneralFeedback({
        type: "success",
        message: "Project settings saved successfully.",
      });
    } catch (err) {
      setGeneralFeedback({
        type: "error",
        message:
          err instanceof Error ? err.message : "Failed to save settings",
      });
    } finally {
      setIsSavingGeneral(false);
    }
  };

  // --- Rules handlers ---
  const handleSaveWorkflow = async (config: WorkflowRulesConfig) => {
    if (!currentProject) return;
    setIsSavingWorkflow(true);
    try {
      await saveWorkflowRules(currentProject.id, config);
      await fetchWorkflowRules(currentProject.id);
    } finally {
      setIsSavingWorkflow(false);
    }
  };

  const handleSaveAssignment = async (config: AssignmentRulesConfig) => {
    if (!currentProject) return;
    setIsSavingAssignment(true);
    try {
      // PUT already returns effective (merged) rules and updates the store —
      // no need for a separate GET which causes double form reinit.
      await saveProjectAssignmentRules(currentProject.id, config);
    } finally {
      setIsSavingAssignment(false);
    }
  };

  // --- Status handlers ---
  const sortedStatuses = [...statuses].sort(
    (a, b) => a.position - b.position,
  );

  const handleOpenCreateStatus = () => {
    setEditingStatus(undefined);
    setStatusDialogOpen(true);
  };

  const handleOpenEditStatus = (status: TaskStatus) => {
    setEditingStatus(status);
    setStatusDialogOpen(true);
  };

  const handleCloseStatusDialog = () => {
    setStatusDialogOpen(false);
    setEditingStatus(undefined);
  };

  const handleOpenDeleteStatus = (status: TaskStatus) => {
    setStatusToDelete(status);
    setDeleteStatusDialogOpen(true);
    setStatusError(null);
  };

  const handleConfirmDeleteStatus = async () => {
    if (!currentProject || !statusToDelete) return;

    setIsDeletingStatus(true);
    setStatusError(null);

    try {
      await deleteStatus(currentProject.id, statusToDelete.id);
      setDeleteStatusDialogOpen(false);
      setStatusToDelete(null);
    } catch (err) {
      setStatusError(
        err instanceof Error
          ? err.message
          : "Failed to delete status. It may still have tasks assigned.",
      );
    } finally {
      setIsDeletingStatus(false);
    }
  };

  const handleMoveStatus = useCallback(
    async (index: number, direction: "up" | "down") => {
      if (!currentProject) return;

      const newStatuses = [...sortedStatuses];
      const swapIndex = direction === "up" ? index - 1 : index + 1;
      if (swapIndex < 0 || swapIndex >= newStatuses.length) return;

      const temp = newStatuses[index];
      const swapItem = newStatuses[swapIndex];
      if (!temp || !swapItem) return;

      newStatuses[index] = swapItem;
      newStatuses[swapIndex] = temp;

      const newOrderedIds = newStatuses.map((s) => s.id);
      await reorderStatuses(currentProject.id, newOrderedIds);
    },
    [currentProject, sortedStatuses, reorderStatuses],
  );

  // --- Custom field handlers ---
  const sortedFields = [...customFields].sort(
    (a, b) => a.position - b.position,
  );

  const handleOpenCreateField = () => {
    setEditingField(undefined);
    setFieldDialogOpen(true);
  };

  const handleOpenEditField = (field: CustomFieldDefinition) => {
    setEditingField(field);
    setFieldDialogOpen(true);
  };

  const handleCloseFieldDialog = () => {
    setFieldDialogOpen(false);
    setEditingField(undefined);
    // Refresh fields after dialog closes (in case of create/edit)
    if (currentProject) {
      fetchFields(currentProject.id);
    }
  };

  const handleOpenDeleteField = (field: CustomFieldDefinition) => {
    setFieldToDelete(field);
    setDeleteFieldDialogOpen(true);
    setFieldError(null);
  };

  const handleConfirmDeleteField = async () => {
    if (!fieldToDelete) return;

    setIsDeletingField(true);
    setFieldError(null);

    try {
      await deleteField(fieldToDelete.id);
      setDeleteFieldDialogOpen(false);
      setFieldToDelete(null);
    } catch (err) {
      setFieldError(
        err instanceof Error ? err.message : "Failed to delete custom field.",
      );
    } finally {
      setIsDeletingField(false);
    }
  };

  const handleMoveField = useCallback(
    async (index: number, direction: "up" | "down") => {
      if (!currentProject) return;

      const newFields = [...sortedFields];
      const swapIndex = direction === "up" ? index - 1 : index + 1;
      if (swapIndex < 0 || swapIndex >= newFields.length) return;

      const temp = newFields[index];
      const swapItem = newFields[swapIndex];
      if (!temp || !swapItem) return;

      newFields[index] = swapItem;
      newFields[swapIndex] = temp;

      const newOrderedIds = newFields.map((f) => f.id);
      await reorderFields(currentProject.id, newOrderedIds);
    },
    [currentProject, sortedFields, reorderFields],
  );

  // --- Project member handlers ---
  const userMembers = projectMembers.filter((m) => m.user_id);
  const agentMembers = projectMembers.filter((m) => m.agent_id);

  const handleProjectMemberRoleChange = async (
    member: ProjectMemberWithUser,
    newRole: ProjectRole,
  ) => {
    if (!currentProject || !member.user_id) return;
    try {
      await updateProjectMemberRole(currentProject.id, member.user_id, newRole);
    } catch {
      // Silently fail — role will revert since store didn't update
    }
  };

  const handleOpenRemoveMember = (member: ProjectMemberWithUser) => {
    setMemberToRemove(member);
    setMemberError(null);
  };

  const handleConfirmRemoveMember = async () => {
    if (!currentProject || !memberToRemove) return;
    setIsRemovingMember(true);
    setMemberError(null);
    try {
      if (memberToRemove.agent_id) {
        await removeProjectAgentMember(currentProject.id, memberToRemove.agent_id);
      } else if (memberToRemove.user_id) {
        await removeProjectMember(currentProject.id, memberToRemove.user_id);
      }
      setMemberToRemove(null);
    } catch (err) {
      setMemberError(
        err instanceof Error ? err.message : "Failed to remove member",
      );
    } finally {
      setIsRemovingMember(false);
    }
  };

  const [addAgentMemberOpen, setAddAgentMemberOpen] = useState(false);
  const [selectedAgentId, setSelectedAgentId] = useState("");
  const [isAddingAgent, setIsAddingAgent] = useState(false);

  const handleAddAgentMember = async () => {
    if (!currentProject || !selectedAgentId) return;
    setIsAddingAgent(true);
    setMemberError(null);
    try {
      await addProjectAgentMember(currentProject.id, selectedAgentId, "member");
      setSelectedAgentId("");
      setAddAgentMemberOpen(false);
    } catch (err) {
      setMemberError(
        err instanceof Error ? err.message : "Failed to add agent",
      );
    } finally {
      setIsAddingAgent(false);
    }
  };

  // --- Danger zone handlers ---
  const handleArchiveProject = async () => {
    if (!currentProject) return;

    setIsArchiving(true);
    try {
      await updateProject(currentProject.id, { is_archived: true } as Parameters<typeof updateProject>[1]);
      setArchiveDialogOpen(false);
      navigate(`/w/${wsSlug}`);
    } catch {
      // Error is handled by keeping the dialog open
      setIsArchiving(false);
    }
  };

  const handleDeleteProject = async () => {
    if (!currentProject) return;

    setIsDeleting(true);
    try {
      await deleteProject(currentProject.id);
      setDeleteDialogOpen(false);
      navigate(`/w/${wsSlug}`);
    } catch {
      // Error is handled by keeping the dialog open
      setIsDeleting(false);
    }
  };

  if (!currentProject) {
    return (
      <div className="mx-auto max-w-2xl space-y-6">
        <div className="flex items-center gap-3">
          <Settings className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">
            Project Settings
          </h1>
        </div>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            Project &quot;{projectSlug}&quot; not found
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      {/* Tab navigation */}
      <div className="flex gap-1 border-b border-border">
        {PROJECT_TABS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "px-3 py-2 text-sm font-medium border-b-2 -mb-px transition-colors",
              activeTab === tab.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {/* Section 1: General Information */}
      {activeTab === "general" && (
      <Card>
        <CardHeader>
          <CardTitle>General Information</CardTitle>
          <CardDescription>
            Basic project information and configuration
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveGeneral} className="space-y-4">
            <div className="space-y-1.5">
              <label htmlFor="ps-name" className="text-sm font-medium">
                Name <span className="text-destructive">*</span>
              </label>
              <Input
                id="ps-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Project name"
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-slug" className="text-sm font-medium">
                Slug
              </label>
              <Input
                id="ps-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="project-slug"
              />
              <p className="text-xs text-muted-foreground">
                Used in URLs. Changing this will affect existing links.
              </p>
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-description" className="text-sm font-medium">
                Description
              </label>
              <Textarea
                id="ps-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional project description..."
                rows={3}
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-icon" className="text-sm font-medium">
                Icon
              </label>
              <Input
                id="ps-icon"
                value={icon}
                onChange={(e) => setIcon(e.target.value)}
                placeholder="Emoji or short text (e.g. \uD83D\uDE80)"
                maxLength={8}
              />
            </div>

            {generalFeedback && (
              <p
                className={cn(
                  "text-sm",
                  generalFeedback.type === "success"
                    ? "text-green-600"
                    : "text-destructive",
                )}
              >
                {generalFeedback.message}
              </p>
            )}

            <div className="flex justify-end">
              <Button type="submit" disabled={isSavingGeneral}>
                {isSavingGeneral ? "Saving..." : "Save Changes"}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
      )}

      {/* Section 2: Task Statuses */}
      {activeTab === "statuses" && (
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Task Statuses</CardTitle>
              <CardDescription>
                Configure status columns for your project board
              </CardDescription>
            </div>
            <Button size="sm" onClick={handleOpenCreateStatus}>
              <Plus className="h-4 w-4" />
              Add Status
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {sortedStatuses.length === 0 ? (
            <div className="py-8 text-center">
              <p className="text-sm text-muted-foreground">
                No statuses configured yet. Add your first status to get
                started.
              </p>
            </div>
          ) : (
            <div className="space-y-1">
              {sortedStatuses.map((status, index) => {
                const categoryConf =
                  statusCategoryConfig[status.category];
                return (
                  <div
                    key={status.id}
                    className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5"
                  >
                    {/* Grip icon (visual only) */}
                    <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground" />

                    {/* Color dot */}
                    <div
                      className="h-3 w-3 shrink-0 rounded-full"
                      style={{ backgroundColor: status.color }}
                    />

                    {/* Name */}
                    <span className="flex-1 text-sm font-medium">
                      {status.name}
                    </span>

                    {/* Category badge */}
                    {categoryConf && (
                      <Badge variant="secondary" className="text-xs">
                        <span
                          className={cn(
                            "mr-1.5 inline-block h-2 w-2 rounded-full",
                            categoryConf.color,
                          )}
                        />
                        {categoryConf.label}
                      </Badge>
                    )}

                    {/* Default badge */}
                    {status.is_default && (
                      <Badge variant="outline" className="text-xs">
                        Default
                      </Badge>
                    )}

                    {/* Move up/down buttons */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={index === 0}
                      onClick={() => handleMoveStatus(index, "up")}
                      title="Move up"
                    >
                      <ArrowUp className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={index === sortedStatuses.length - 1}
                      onClick={() => handleMoveStatus(index, "down")}
                      title="Move down"
                    >
                      <ArrowDown className="h-3.5 w-3.5" />
                    </Button>

                    {/* Edit button */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => handleOpenEditStatus(status)}
                      title="Edit status"
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>

                    {/* Delete button */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive"
                      onClick={() => handleOpenDeleteStatus(status)}
                      title="Delete status"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                );
              })}
            </div>
          )}

          {statusError && (
            <p className="mt-3 text-sm text-destructive">{statusError}</p>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 3: Custom Fields */}
      {activeTab === "custom-fields" && (
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Custom Fields</CardTitle>
              <CardDescription>
                Define custom data fields for tasks in this project
              </CardDescription>
            </div>
            <Button size="sm" onClick={handleOpenCreateField}>
              <Plus className="h-4 w-4" />
              Add Field
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {sortedFields.length === 0 ? (
            <div className="py-8 text-center">
              <p className="text-sm text-muted-foreground">
                No custom fields configured yet. Add fields to capture
                additional data on tasks.
              </p>
            </div>
          ) : (
            <div className="space-y-1">
              {sortedFields.map((field, index) => (
                <div
                  key={field.id}
                  className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5"
                >
                  {/* Grip icon (visual only) */}
                  <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground" />

                  {/* Name and description */}
                  <div className="flex-1 min-w-0">
                    <span className="text-sm font-medium">{field.name}</span>
                    {field.description && (
                      <p className="truncate text-xs text-muted-foreground">
                        {field.description}
                      </p>
                    )}
                  </div>

                  {/* Field type badge */}
                  <Badge variant="secondary" className="text-xs">
                    {fieldTypeLabels[field.field_type] || field.field_type}
                  </Badge>

                  {/* Required badge */}
                  {field.is_required && (
                    <Badge variant="outline" className="text-xs">
                      Required
                    </Badge>
                  )}

                  {/* Agent-visible badge */}
                  {field.is_visible_to_agents && (
                    <Badge variant="outline" className="text-xs">
                      <Eye className="mr-1 h-3 w-3" />
                      Agents
                    </Badge>
                  )}

                  {/* Move up/down buttons */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    disabled={index === 0}
                    onClick={() => handleMoveField(index, "up")}
                    title="Move up"
                  >
                    <ArrowUp className="h-3.5 w-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    disabled={index === sortedFields.length - 1}
                    onClick={() => handleMoveField(index, "down")}
                    title="Move down"
                  >
                    <ArrowDown className="h-3.5 w-3.5" />
                  </Button>

                  {/* Edit button */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleOpenEditField(field)}
                    title="Edit field"
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>

                  {/* Delete button */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    onClick={() => handleOpenDeleteField(field)}
                    title="Delete field"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              ))}
            </div>
          )}

          {fieldError && (
            <p className="mt-3 text-sm text-destructive">{fieldError}</p>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 4: Workflow Rules */}
      {activeTab === "workflow" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <GitBranch className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>Workflow Rules</CardTitle>
              <CardDescription>
                Configure allowed status transitions and enforcement mode for this project
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <WorkflowRulesSection
            workflowRules={workflowRules}
            isLoading={isWorkflowLoading}
            onSave={handleSaveWorkflow}
            isSaving={isSavingWorkflow}
            statuses={statuses}
            agents={agents}
            members={workspaceMembers}
          />
        </CardContent>
      </Card>
      )}

      {/* Section 5: Assignment Rules */}
      {activeTab === "assignment" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Zap className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>Assignment Rules</CardTitle>
              <CardDescription>
                Project-level assignment overrides. Rules marked "Inherited from workspace" come from workspace settings.
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <ProjectAssignmentRulesSection
            effectiveRules={effectiveAssignmentRules}
            isLoading={isProjRulesLoading}
            onSave={handleSaveAssignment}
            isSaving={isSavingAssignment}
            agents={agents}
            members={workspaceMembers}
          />
        </CardContent>
      </Card>
      )}

      {/* Section 6: Members */}
      {activeTab === "members" && (
      <>
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>
                <Users className="inline h-4 w-4 mr-1.5" />
                User Members
              </CardTitle>
              <CardDescription>
                Users with access to this project. Workspace owners and admins always have access.
              </CardDescription>
            </div>
            <Button size="sm" onClick={() => setAddMemberDialogOpen(true)}>
              <Plus className="h-4 w-4" />
              Add User
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {isLoadingProjectMembers ? (
            <div className="space-y-3">
              {[1, 2].map((i) => (
                <div key={i} className="flex items-center gap-3">
                  <Skeleton className="h-8 w-8 rounded-full" />
                  <div className="flex-1 space-y-1.5">
                    <Skeleton className="h-4 w-32" />
                    <Skeleton className="h-3 w-48" />
                  </div>
                  <Skeleton className="h-8 w-24" />
                </div>
              ))}
            </div>
          ) : userMembers.length === 0 ? (
            <div className="py-6 text-center">
              <Users className="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                No user members added yet.
              </p>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {userMembers.map((member) => {
                const isMe = member.user_id === user?.id;
                return (
                  <div
                    key={member.id}
                    className="flex items-center gap-3 py-3 first:pt-0 last:pb-0"
                  >
                    <Avatar
                      src={member.user?.avatar_url || undefined}
                      name={member.user?.name || member.user?.email || ""}
                      size="md"
                    />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-medium">
                          {member.user?.name}
                        </span>
                        {isMe && (
                          <span className="text-xs text-muted-foreground">(you)</span>
                        )}
                      </div>
                      <p className="truncate text-xs text-muted-foreground">
                        {member.user?.email}
                      </p>
                    </div>
                    <Select
                      value={member.role}
                      onChange={(e) =>
                        void handleProjectMemberRoleChange(
                          member,
                          e.target.value as ProjectRole,
                        )
                      }
                      className="w-28 text-sm"
                    >
                      <option value="admin">Admin</option>
                      <option value="member">Member</option>
                      <option value="viewer">Viewer</option>
                    </Select>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-destructive"
                      onClick={() => handleOpenRemoveMember(member)}
                      title="Remove from project"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>
                <Bot className="inline h-4 w-4 mr-1.5" />
                Agent Members
              </CardTitle>
              <CardDescription>
                AI agents with access to this project via API/MCP.
              </CardDescription>
            </div>
            <Button size="sm" onClick={() => setAddAgentMemberOpen(true)}>
              <Plus className="h-4 w-4" />
              Add Agent
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {agentMembers.length === 0 ? (
            <div className="py-6 text-center">
              <Bot className="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                No agent members added yet.
              </p>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {agentMembers.map((member) => (
                <div
                  key={member.id}
                  className="flex items-center gap-3 py-3 first:pt-0 last:pb-0"
                >
                  <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted">
                    <Bot className="h-4 w-4 text-muted-foreground" />
                  </div>
                  <div className="flex-1 min-w-0">
                    <span className="truncate text-sm font-medium">
                      {member.agent_name || member.agent_id?.slice(0, 8)}
                    </span>
                    <p className="text-xs text-muted-foreground">Agent</p>
                  </div>
                  <Badge variant="secondary" className="text-xs">{member.role}</Badge>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-muted-foreground hover:text-destructive"
                    onClick={() => handleOpenRemoveMember(member)}
                    title="Remove agent from project"
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
            </div>
          )}

          {/* Add Agent Dialog - inline */}
          {addAgentMemberOpen && (
            <div className="mt-4 rounded-md border border-border p-4 space-y-3">
              <p className="text-sm font-medium">Add Agent to Project</p>
              <Select
                value={selectedAgentId}
                onChange={(e) => setSelectedAgentId(e.target.value)}
                className="w-full text-sm"
              >
                <option value="">Select agent...</option>
                {agents
                  .filter((a) => !agentMembers.some((m) => m.agent_id === a.id))
                  .map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
              </Select>
              <div className="flex gap-2 justify-end">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setAddAgentMemberOpen(false);
                    setSelectedAgentId("");
                  }}
                >
                  Cancel
                </Button>
                <Button
                  size="sm"
                  disabled={!selectedAgentId || isAddingAgent}
                  onClick={() => void handleAddAgentMember()}
                >
                  {isAddingAgent ? "Adding..." : "Add"}
                </Button>
              </div>
            </div>
          )}

          {memberError && (
            <p className="mt-3 text-sm text-destructive">{memberError}</p>
          )}
        </CardContent>
      </Card>
      </>
      )}

      {/* Section 7: Recurring Schedules */}
      {activeTab === "recurring" && (
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Recurring Schedules</CardTitle>
              <CardDescription>
                Automatically create task instances on a schedule. Recurring tasks can carry context from previous runs.
              </CardDescription>
            </div>
            <Button
              size="sm"
              onClick={() => {
                setEditingSchedule(null);
                setRecurringDialogOpen(true);
              }}
            >
              <Plus className="h-4 w-4" />
              New Schedule
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {recurringSchedules.length === 0 ? (
            <div className="py-10 text-center">
              <RefreshCw className="mx-auto mb-3 h-8 w-8 text-muted-foreground opacity-50" />
              <p className="text-sm text-muted-foreground">
                No recurring schedules yet. Create one to automatically generate tasks on a schedule.
              </p>
              <Button
                size="sm"
                variant="outline"
                className="mt-4"
                onClick={() => {
                  setEditingSchedule(null);
                  setRecurringDialogOpen(true);
                }}
              >
                <Plus className="h-4 w-4" />
                New Schedule
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {recurringSchedules.map((schedule) => (
                <div key={schedule.id} className="py-4 first:pt-0 last:pb-0">
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex items-start gap-2 min-w-0">
                      <RefreshCw
                        className={cn(
                          "mt-0.5 h-4 w-4 shrink-0",
                          schedule.is_active
                            ? "text-primary"
                            : "text-muted-foreground",
                        )}
                      />
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="text-sm font-medium truncate">
                            {schedule.title_template}
                          </span>
                          <Badge
                            variant={schedule.is_active ? "default" : "secondary"}
                            className="text-[10px] shrink-0"
                          >
                            {schedule.is_active ? "Active" : "Paused"}
                          </Badge>
                          <Badge variant="outline" className="text-[10px] capitalize shrink-0">
                            {schedule.frequency === "custom"
                              ? `Custom (${schedule.cron_expr})`
                              : schedule.frequency}
                          </Badge>
                        </div>
                        <div className="mt-1 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
                          {schedule.assignee_type !== "unassigned" && schedule.assignee_id && (
                            <span className="flex items-center gap-1">
                              {schedule.assignee_type === "agent" ? (
                                <Bot className="h-3 w-3" />
                              ) : (
                                <Users className="h-3 w-3" />
                              )}
                              {schedule.assignee_id.slice(0, 8)}...
                            </span>
                          )}
                          <span className="capitalize">Priority: {schedule.priority}</span>
                          <span>{schedule.instance_count} instance{schedule.instance_count !== 1 ? "s" : ""}</span>
                          {schedule.next_run_at && schedule.is_active && (
                            <span>
                              Next:{" "}
                              {new Date(schedule.next_run_at).toLocaleString(undefined, {
                                dateStyle: "short",
                                timeStyle: "short",
                              })}
                            </span>
                          )}
                        </div>
                      </div>
                    </div>

                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title="View history"
                        onClick={() => setHistorySchedule(schedule)}
                      >
                        <History className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title="Edit schedule"
                        onClick={() => {
                          setEditingSchedule(schedule);
                          setRecurringDialogOpen(true);
                        }}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title={schedule.is_active ? "Pause" : "Resume"}
                        disabled={recurringActionLoading === `toggle-${schedule.id}`}
                        onClick={async () => {
                          setRecurringActionLoading(`toggle-${schedule.id}`);
                          try {
                            await updateRecurringSchedule(schedule.id, {
                              is_active: !schedule.is_active,
                            });
                          } finally {
                            setRecurringActionLoading(null);
                          }
                        }}
                      >
                        {schedule.is_active ? (
                          <Pause className="h-4 w-4" />
                        ) : (
                          <Play className="h-4 w-4" />
                        )}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title="Trigger now"
                        disabled={recurringActionLoading === `trigger-${schedule.id}`}
                        onClick={async () => {
                          setRecurringActionLoading(`trigger-${schedule.id}`);
                          try {
                            await triggerRecurringNow(schedule.id);
                          } finally {
                            setRecurringActionLoading(null);
                          }
                        }}
                      >
                        <Zap className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground hover:text-destructive"
                        title="Delete schedule"
                        disabled={recurringActionLoading === `delete-${schedule.id}`}
                        onClick={async () => {
                          if (!confirm(`Delete schedule "${schedule.title_template}"? Existing task instances will not be deleted.`)) return;
                          setRecurringActionLoading(`delete-${schedule.id}`);
                          try {
                            await deleteRecurringSchedule(schedule.id);
                          } finally {
                            setRecurringActionLoading(null);
                          }
                        }}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
      )}

      {/* Recurring dialogs */}
      {currentProject && (
        <>
          <CreateRecurringDialog
            open={recurringDialogOpen}
            onOpenChange={setRecurringDialogOpen}
            projectId={currentProject.id}
            editSchedule={editingSchedule ?? undefined}
          />
          {historySchedule && (
            <RecurringHistoryPanel
              open={Boolean(historySchedule)}
              onOpenChange={(open) => {
                if (!open) setHistorySchedule(null);
              }}
              schedule={historySchedule}
            />
          )}
        </>
      )}

      {/* Section 8: Task Templates */}
      {activeTab === "templates" && (
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Task Templates</CardTitle>
              <CardDescription>
                Define reusable templates to quickly create pre-filled tasks. Templates store default values for title, description, priority, assignee, and labels.
              </CardDescription>
            </div>
            <Button
              size="sm"
              onClick={() => {
                setEditingTemplate(null);
                setTemplateForm({ name: "", description: "", title_template: "", description_template: "", priority: "medium", labels: "" });
                setTemplateFormError(null);
                setTemplateDialogOpen(true);
              }}
            >
              <Plus className="h-4 w-4" />
              New Template
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {templates.length === 0 ? (
            <div className="py-10 text-center">
              <Save className="mx-auto mb-3 h-8 w-8 text-muted-foreground opacity-50" />
              <p className="text-sm text-muted-foreground">
                No templates yet. Create one to speed up task creation.
              </p>
              <Button
                size="sm"
                variant="outline"
                className="mt-4"
                onClick={() => {
                  setEditingTemplate(null);
                  setTemplateForm({ name: "", description: "", title_template: "", description_template: "", priority: "medium", labels: "" });
                  setTemplateFormError(null);
                  setTemplateDialogOpen(true);
                }}
              >
                <Plus className="h-4 w-4" />
                New Template
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {templates.map((tmpl) => (
                <div key={tmpl.id} className="py-4 first:pt-0 last:pb-0">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-sm font-medium truncate">{tmpl.name}</span>
                        <Badge variant="outline" className="text-[10px] capitalize shrink-0">
                          {tmpl.priority}
                        </Badge>
                      </div>
                      {tmpl.description && (
                        <p className="mt-0.5 text-xs text-muted-foreground truncate max-w-md">
                          {tmpl.description}
                        </p>
                      )}
                      <div className="mt-1 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
                        {tmpl.title_template && (
                          <span className="truncate max-w-xs">Title: {tmpl.title_template}</span>
                        )}
                        {tmpl.labels && tmpl.labels.length > 0 && (
                          <span>Labels: {tmpl.labels.join(", ")}</span>
                        )}
                        {tmpl.estimated_hours != null && (
                          <span>{tmpl.estimated_hours}h estimated</span>
                        )}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title="Edit template"
                        onClick={() => {
                          setEditingTemplate(tmpl);
                          setTemplateForm({
                            name: tmpl.name,
                            description: tmpl.description ?? "",
                            title_template: tmpl.title_template ?? "",
                            description_template: tmpl.description_template ?? "",
                            priority: tmpl.priority,
                            labels: tmpl.labels ? tmpl.labels.join(", ") : "",
                          });
                          setTemplateFormError(null);
                          setTemplateDialogOpen(true);
                        }}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground hover:text-destructive"
                        title="Delete template"
                        disabled={templateActionLoading === `delete-${tmpl.id}`}
                        onClick={async () => {
                          if (!confirm(`Delete template "${tmpl.name}"?`)) return;
                          setTemplateActionLoading(`delete-${tmpl.id}`);
                          try {
                            await deleteTemplate(tmpl.id);
                          } finally {
                            setTemplateActionLoading(null);
                          }
                        }}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
      )}

      {/* Template create/edit dialog */}
      {currentProject && templateDialogOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="w-full max-w-lg rounded-lg bg-card border border-border shadow-xl p-6 space-y-4">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold">
                {editingTemplate ? "Edit Template" : "New Template"}
              </h2>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={() => setTemplateDialogOpen(false)}
              >
                <X className="h-4 w-4" />
              </Button>
            </div>

            <div className="space-y-3">
              <div className="space-y-1.5">
                <label className="text-sm font-medium">
                  Name <span className="text-destructive">*</span>
                </label>
                <Input
                  placeholder="e.g. Bug Report"
                  value={templateForm.name}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, name: e.target.value }))}
                  autoFocus
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-sm font-medium">Description</label>
                <Input
                  placeholder="Short description of when to use this template"
                  value={templateForm.description}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, description: e.target.value }))}
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-sm font-medium">Default Title</label>
                <Input
                  placeholder="Default task title"
                  value={templateForm.title_template}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, title_template: e.target.value }))}
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-sm font-medium">Default Description</label>
                <Textarea
                  placeholder="Default task description"
                  rows={3}
                  value={templateForm.description_template}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, description_template: e.target.value }))}
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-sm font-medium">Priority</label>
                  <Select
                    value={templateForm.priority}
                    onChange={(e) => setTemplateForm((f) => ({ ...f, priority: e.target.value as Priority }))}
                  >
                    {["none", "low", "medium", "high", "urgent"].map((p) => (
                      <option key={p} value={p} className="capitalize">{p}</option>
                    ))}
                  </Select>
                </div>
                <div className="space-y-1.5">
                  <label className="text-sm font-medium">Labels</label>
                  <Input
                    placeholder="Comma-separated"
                    value={templateForm.labels}
                    onChange={(e) => setTemplateForm((f) => ({ ...f, labels: e.target.value }))}
                  />
                </div>
              </div>
            </div>

            {templateFormError && (
              <p className="text-sm text-destructive">{templateFormError}</p>
            )}

            <div className="flex justify-end gap-2 pt-2">
              <Button variant="outline" onClick={() => setTemplateDialogOpen(false)}>
                Cancel
              </Button>
              <Button
                disabled={templateActionLoading === "save"}
                onClick={async () => {
                  if (!templateForm.name.trim()) {
                    setTemplateFormError("Name is required");
                    return;
                  }
                  setTemplateFormError(null);
                  setTemplateActionLoading("save");
                  try {
                    const labels = templateForm.labels
                      .split(",")
                      .map((l) => l.trim())
                      .filter(Boolean);
                    if (editingTemplate) {
                      await updateTemplate(editingTemplate.id, {
                        name: templateForm.name.trim(),
                        description: templateForm.description.trim() || undefined,
                        title_template: templateForm.title_template.trim() || undefined,
                        description_template: templateForm.description_template.trim() || undefined,
                        priority: templateForm.priority,
                        labels,
                      });
                    } else {
                      await createTemplate(currentProject.id, {
                        name: templateForm.name.trim(),
                        description: templateForm.description.trim() || undefined,
                        title_template: templateForm.title_template.trim() || undefined,
                        description_template: templateForm.description_template.trim() || undefined,
                        priority: templateForm.priority,
                        labels,
                      });
                    }
                    setTemplateDialogOpen(false);
                  } catch (err) {
                    setTemplateFormError(err instanceof Error ? err.message : "Failed to save template");
                  } finally {
                    setTemplateActionLoading(null);
                  }
                }}
              >
                {templateActionLoading === "save" ? "Saving..." : "Save Template"}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* Section 9: Danger Zone */}
      {activeTab === "general" && (
      <Card className="border-destructive/50">
        <CardHeader>
          <CardTitle className="text-destructive">Danger Zone</CardTitle>
          <CardDescription>
            Irreversible actions for this project
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between rounded-lg border border-border p-4">
            <div>
              <p className="text-sm font-medium">Archive Project</p>
              <p className="text-sm text-muted-foreground">
                Archive this project. It will be hidden from the sidebar and
                board.
              </p>
            </div>
            <Button
              variant="outline"
              className="border-destructive/50 text-destructive hover:bg-destructive/10"
              onClick={() => setArchiveDialogOpen(true)}
            >
              Archive
            </Button>
          </div>

          <Separator />

          <div className="flex items-center justify-between rounded-lg border border-destructive/30 p-4">
            <div>
              <p className="text-sm font-medium">Delete Project</p>
              <p className="text-sm text-muted-foreground">
                Permanently delete this project and all its data. This action
                cannot be undone.
              </p>
            </div>
            <Button
              variant="destructive"
              onClick={() => setDeleteDialogOpen(true)}
            >
              Delete
            </Button>
          </div>
        </CardContent>
      </Card>
      )}

      {/* --- Dialogs --- */}

      {/* Add Project Member Dialog */}
      <AddProjectMemberDialog
        open={addMemberDialogOpen}
        onClose={() => setAddMemberDialogOpen(false)}
        projectId={currentProject.id}
        workspaceMembers={workspaceMembers}
      />

      {/* Remove Project Member Confirmation */}
      <ConfirmDialog
        open={!!memberToRemove}
        onClose={() => {
          setMemberToRemove(null);
          setMemberError(null);
        }}
        onConfirm={handleConfirmRemoveMember}
        title="Remove Member"
        description={
          memberToRemove
            ? `Are you sure you want to remove ${memberToRemove.agent_id ? (memberToRemove.agent_name || "this agent") : (memberToRemove.user?.name || "this member")} from this project?`
            : ""
        }
        confirmText="Remove Member"
        variant="destructive"
        isLoading={isRemovingMember}
      />

      {/* Status Create/Edit Dialog */}
      <StatusDialog
        open={statusDialogOpen}
        onClose={handleCloseStatusDialog}
        projectId={currentProject.id}
        status={editingStatus}
      />

      {/* Custom Field Create/Edit Dialog */}
      <CustomFieldDialog
        open={fieldDialogOpen}
        onClose={handleCloseFieldDialog}
        projectId={currentProject.id}
        field={editingField}
      />

      {/* Delete Status Confirmation */}
      <ConfirmDialog
        open={deleteStatusDialogOpen}
        onClose={() => {
          setDeleteStatusDialogOpen(false);
          setStatusToDelete(null);
        }}
        onConfirm={handleConfirmDeleteStatus}
        title="Delete Status"
        description={`Are you sure you want to delete the "${statusToDelete?.name ?? ""}" status? This will fail if any tasks are currently using this status.`}
        confirmText="Delete Status"
        variant="destructive"
        isLoading={isDeletingStatus}
      />

      {/* Delete Custom Field Confirmation */}
      <ConfirmDialog
        open={deleteFieldDialogOpen}
        onClose={() => {
          setDeleteFieldDialogOpen(false);
          setFieldToDelete(null);
        }}
        onConfirm={handleConfirmDeleteField}
        title="Delete Custom Field"
        description={`Are you sure you want to delete the "${fieldToDelete?.name ?? ""}" custom field? Any values stored in this field on tasks will be lost.`}
        confirmText="Delete Field"
        variant="destructive"
        isLoading={isDeletingField}
      />

      {/* Archive Project Confirmation */}
      <ConfirmDialog
        open={archiveDialogOpen}
        onClose={() => setArchiveDialogOpen(false)}
        onConfirm={handleArchiveProject}
        title="Archive Project"
        description={`Are you sure you want to archive "${currentProject.name}"? The project will be hidden but can be restored later.`}
        confirmText="Archive Project"
        variant="destructive"
        isLoading={isArchiving}
      />

      {/* Delete Project Confirmation */}
      <ConfirmDialog
        open={deleteDialogOpen}
        onClose={() => setDeleteDialogOpen(false)}
        onConfirm={handleDeleteProject}
        title="Delete Project"
        description={`This will permanently delete "${currentProject.name}" and all of its tasks, statuses, and data. This action cannot be undone.`}
        confirmText="Delete Project"
        variant="destructive"
        requireText={currentProject.name}
        isLoading={isDeleting}
      />
    </div>
  );
}
