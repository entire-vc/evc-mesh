import * as i18nFmt from "@/i18n/format";
import { useTranslation } from "react-i18next";
import i18n from "@/i18n";
import { useAgentShortTag } from "@/hooks/use-agent-short-tag";
import { agentLabel } from "@/lib/agent-label";
import { AgentShortTag } from "@/components/agent-short-tag";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { toast } from "@/components/ui/toast";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Bot,
  Eye,
  GitBranch,
  GripVertical,
  History,
  Link as LinkIcon,
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
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { ProjectExtensionsTab } from "@/components/extensions/project-extensions-tab";
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
import { apiErrorMessage } from "@/lib/api-error";
import { projectSettingsTab, scheduleHref } from "@/lib/entity-deep-links";
import { EntityAnchor } from "@/components/entity-anchor";
import { displayName, inlineLabel, isNamePlaceholder } from "@/lib/user-display";
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

const FIELD_TYPE_KEYS = [
  "text",
  "number",
  "date",
  "datetime",
  "select",
  "multiselect",
  "url",
  "email",
  "checkbox",
  "user_ref",
  "agent_ref",
  "json",
] as const;
const fieldTypeLabel = (type: string): string =>
  (FIELD_TYPE_KEYS as readonly string[]).includes(type)
    ? i18n.t(`settings:project.fieldType.${type}`)
    : type;

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
  const { t } = useTranslation("settings");
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
    for (const tr of transitions) {
      if (!tr.from.trim() || !tr.to.trim()) continue;
      const key = `${tr.from.trim()} -> ${tr.to.trim()}`;
      transitionsMap[key] = {
        allowed: tr.allowed.filter(Boolean),
        description: tr.description.trim() || undefined,
      };
    }
    const config: WorkflowRulesConfig = {
      enforcement_mode: enforcementMode,
      transitions: transitionsMap,
    };
    try {
      await onSave(config);
      setFeedback({ type: "success", message: t("project.workflow.saved") });
      toast.success(t("project.workflow.saved"));
    } catch (err) {
      setFeedback({
        type: "error",
        message: apiErrorMessage(err, t("project.rules.saveFailed")),
      });
      toast.error(apiErrorMessage(err, t("project.rules.saveFailed")));
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
        <label className="text-sm font-medium">{t("project.workflow.enforcementMode")}</label>
        <div className="flex gap-2">
          <Button
            type="button"
            variant={enforcementMode === "advisory" ? "default" : "outline"}
            size="sm"
            onClick={() => setEnforcementMode("advisory")}
          >
            {t("project.workflow.advisory")}
          </Button>
          <Button
            type="button"
            variant={enforcementMode === "strict" ? "default" : "outline"}
            size="sm"
            onClick={() => setConfirmStrictOpen(true)}
          >
            {t("project.workflow.strict")}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {enforcementMode === "strict"
            ? t("project.workflow.strictHint")
            : t("project.workflow.advisoryHint")}
        </p>
      </div>

      <ConfirmDialog
        open={confirmStrictOpen}
        onClose={() => setConfirmStrictOpen(false)}
        onConfirm={() => {
          setEnforcementMode("strict");
          setConfirmStrictOpen(false);
        }}
        title={t("project.workflow.strictConfirmTitle")}
        description={t("project.workflow.strictConfirmDesc")}
        confirmText={t("project.workflow.strictConfirmOk")}
      />

      {/* Transitions table */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">{t("project.workflow.allowedTransitions")}</label>
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
            {t("project.workflow.addTransition")}
          </Button>
        </div>
        {transitions.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t("project.workflow.noTransitions")}
          </p>
        ) : (
          <div className="space-y-3">
            {transitions.map((tr, i) => (
              <div
                key={i}
                className="rounded-lg border border-border p-3 space-y-2"
              >
                {/* From → To row */}
                <div className="flex items-center gap-2">
                  <Select
                    value={tr.from}
                    onChange={(e) =>
                      setTransitions((prev) =>
                        prev.map((r, idx) =>
                          idx === i ? { ...r, from: e.target.value } : r,
                        ),
                      )
                    }
                    className="flex-1 text-sm"
                  >
                    <option value="">{t("project.workflow.selectStatus")}</option>
                    {statuses.map((s) => (
                      <option key={s.id} value={s.name}>
                        {s.name}
                      </option>
                    ))}
                  </Select>
                  <ArrowRight className="h-4 w-4 text-muted-foreground shrink-0" />
                  <Select
                    value={tr.to}
                    onChange={(e) =>
                      setTransitions((prev) =>
                        prev.map((r, idx) =>
                          idx === i ? { ...r, to: e.target.value } : r,
                        ),
                      )
                    }
                    className="flex-1 text-sm"
                  >
                    <option value="">{t("project.workflow.selectStatus")}</option>
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
                    {t("project.workflow.allowedActors")}
                  </span>
                  {/* Role pills */}
                  <div className="space-y-1">
                    <span className="text-xs text-muted-foreground">
                      {t("project.workflow.roles")}
                    </span>
                    <div className="flex flex-wrap gap-1.5">
                      {ACTOR_ROLES.map((role) => {
                        const parsed = parseAllowed(tr.allowed);
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
                            {t(`project.role.${role}`)}
                          </Badge>
                        );
                      })}
                    </div>
                  </div>
                  {/* Specific actors */}
                  <div className="space-y-1.5">
                    <span className="text-xs text-muted-foreground">
                      {t("project.workflow.specificActors")}
                    </span>
                    {(() => {
                      const parsed = parseAllowed(tr.allowed);
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
                                    <span>{agent?.name ?? agentId.slice(0, 8)}<AgentShortTag id={agentId} /></span>
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
                              {t("project.workflow.addActor")}
                            </option>
                            {agents.length > 0 && (
                              <optgroup label={t("project.common.agents")}>
                                {agents
                                  .filter(
                                    (a) =>
                                      !parseAllowed(tr.allowed).agents.includes(
                                        a.id,
                                      ),
                                  )
                                  .map((a) => (
                                    <option
                                      key={a.id}
                                      value={`agent:${a.id}`}
                                    >
                                      {agentLabel(a.name, a.short_tag)}
                                    </option>
                                  ))}
                              </optgroup>
                            )}
                            {members.length > 0 && (
                              <optgroup label={t("project.common.members")}>
                                {members
                                  .filter(
                                    (m) =>
                                      !parseAllowed(tr.allowed).users.includes(
                                        m.user_id,
                                      ),
                                  )
                                  .map((m) => (
                                    <option
                                      key={m.user_id}
                                      value={`user:${m.user_id}`}
                                    >
                                      {inlineLabel(m.user)}
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
              {t("project.workflow.myPermissions", { role: workflowRules.my_permissions.my_role })}
            </span>
          </div>
          <div className="flex flex-wrap gap-2 text-xs">
            {workflowRules.my_permissions.can_create_tasks && (
              <Badge variant="outline">{t("project.workflow.createTasks")}</Badge>
            )}
            {workflowRules.my_permissions.can_delete_tasks && (
              <Badge variant="outline">{t("project.workflow.deleteTasks")}</Badge>
            )}
            {workflowRules.my_permissions.can_reassign && (
              <Badge variant="outline">{t("project.workflow.reassign")}</Badge>
            )}
            {Object.entries(
              workflowRules.my_permissions.can_transition,
            ).map(([key, allowed]) => (
              <Badge
                key={key}
                variant={allowed ? "secondary" : "outline"}
                className={cn(!allowed && "opacity-50")}
              >
                {allowed ? "" : t("project.workflow.no")}{key.replace("->", "→")}
              </Badge>
            ))}
          </div>
        </div>
      )}

      {feedback && (
        <p
          className={cn(
            "text-sm",
            feedback.type === "success" ? "text-tone-success-600" : "text-destructive",
          )}
        >
          {feedback.message}
        </p>
      )}

      <div className="flex justify-end">
        <Button type="button" onClick={handleSave} disabled={isSaving}>
          <Save className="h-4 w-4" />
          {isSaving ? t("project.rules.saving") : t("project.rules.save")}
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
  placeholder,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  agents: Agent[];
  members: WorkspaceMemberWithUser[];
  placeholder?: string;
  className?: string;
}) {
  const { t } = useTranslation("settings");
  return (
    <Select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className={className}
    >
      <option value="">{placeholder ?? t("project.assign.selectAssignee")}</option>
      {agents.length > 0 && (
        <optgroup label={t("project.common.agents")}>
          {agents.map((a) => (
            <option key={a.id} value={`agent:${a.id}`}>
              {agentLabel(a.name, a.short_tag)}
            </option>
          ))}
        </optgroup>
      )}
      {members.length > 0 && (
        <optgroup label={t("project.common.members")}>
          {members.map((m) => (
            <option key={m.user_id} value={`user:${m.user_id}`}>
              {inlineLabel(m.user)}
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
  const { t } = useTranslation("settings");
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
    const config: AssignmentRulesConfig = {};
    if (defaultAssignee.trim()) {
      config.default_assignee = defaultAssignee.trim();
    }
    if (byType.length > 0) {
      config.by_type = Object.fromEntries(
        byType
          .filter((r) => r.type.trim() && r.assignee.trim())
          .map((r) => [r.type.trim(), r.assignee.trim()]),
      );
    }
    if (byPriority.length > 0) {
      config.by_priority = Object.fromEntries(
        byPriority
          .filter((r) => r.priority && r.assignee.trim())
          .map((r) => [r.priority, r.assignee.trim()]),
      );
    }
    if (fallbackChain.some((v) => v.trim())) {
      config.fallback_chain = fallbackChain.filter((v) => v.trim());
    }
    try {
      await onSave(config);
      setFeedback({ type: "success", message: t("project.assign.saved") });
    } catch (err) {
      setFeedback({
        type: "error",
        message: apiErrorMessage(err, t("project.rules.saveFailed")),
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
        {t("project.assign.inheritedFrom", { source })}
      </Badge>
    );
  };

  return (
    <div className="space-y-5">
      {/* Default assignee */}
      <div className="space-y-1.5">
        <div className="flex items-center">
          <label className="text-sm font-medium">{t("project.assign.defaultAssignee")}</label>
          <SourceBadge source={effectiveRules?.default_assignee?.source} />
        </div>
        <AssigneeSelect
          value={defaultAssignee}
          onChange={setDefaultAssignee}
          agents={agents}
          members={members}
          placeholder={t("project.assign.noneInherit")}
        />
        <p className="text-xs text-muted-foreground">
          {t("project.assign.overrideHint")}
        </p>
      </div>

      {/* By type rules */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">{t("project.assign.byTaskType")}</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setByType((prev) => [...prev, { type: "", assignee: "" }])
            }
          >
            <Plus className="h-3.5 w-3.5" />
            {t("project.assign.addRule")}
          </Button>
        </div>
        {byType.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t("project.assign.noTypeRules")}
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
                  placeholder={t("project.assign.taskType")}
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
          <label className="text-sm font-medium">{t("project.assign.byPriority")}</label>
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
            {t("project.assign.addRule")}
          </Button>
        </div>
        {byPriority.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t("project.assign.noPriorityRules")}
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
                      {t(`project.priority.${p}`)}
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
          <label className="text-sm font-medium">{t("project.assign.fallbackChain")}</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setFallbackChain((prev) => [...prev, ""])}
          >
            <Plus className="h-3.5 w-3.5" />
            {t("project.assign.add")}
          </Button>
        </div>
        {fallbackChain.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("project.assign.noFallback")}</p>
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
            feedback.type === "success" ? "text-tone-success-600" : "text-destructive",
          )}
        >
          {feedback.message}
        </p>
      )}

      <div className="flex justify-end">
        <Button type="button" onClick={handleSave} disabled={isSaving}>
          <Save className="h-4 w-4" />
          {isSaving ? t("project.rules.saving") : t("project.rules.save")}
        </Button>
      </div>
    </div>
  );
}

// ---- Main page ----

export function ProjectSettingsPage() {
  const { t } = useTranslation("settings");
  const { wsSlug, projectSlug } = useParams();
  const navigate = useNavigate();
  const {
    currentProject,
    statuses,
    fetchStatuses,
    updateProject,
    deleteProject,
    archiveProject,
    unarchiveProject,
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
  // Home+guest candidates for the "Add Agent to Project" dropdown below —
  // deliberately NOT `agents` (useAgentStore.fetchAgents), which lists only
  // agents whose HOME workspace is this one (GET /workspaces/:ws_id/agents,
  // internal/repository/postgres/agent_repo.go). A guest agent invited into
  // this workspace via agent_workspace_grants (task U1/U3) never appears
  // there — U4 built exactly this endpoint for the Members-tab "Agents (N)"
  // list, and the project-level Add-Agent dropdown needs the same source
  //.
  const { workspaceAgentGrants, fetchWorkspaceAgentGrants } =
    useAgentWorkspaceGrantStore();

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
  const removeMemberTag = useAgentShortTag(memberToRemove?.agent_id, "agent");
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
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = projectSettingsTab(searchParams);
  const focusScheduleId = searchParams.get("schedule");
  const setActiveTab = (tab: string) => {
    setSearchParams(prev => {
      const next = new URLSearchParams(prev);
      next.set("tab", tab);
      if (tab !== "recurring") next.delete("schedule");
      return next;
    });
  };

  const PROJECT_TABS = [
    { id: "general", label: t("project.tabs.general") },
    { id: "statuses", label: t("project.tabs.statuses") },
    { id: "custom-fields", label: t("project.tabs.custom-fields") },
    { id: "members", label: t("project.tabs.members") },
    { id: "workflow", label: t("project.tabs.workflow") },
    { id: "assignment", label: t("project.tabs.assignment") },
    { id: "recurring", label: t("project.tabs.recurring") },
    { id: "templates", label: t("project.tabs.templates") },
    { id: "integrations", label: t("project.tabs.integrations") },
  ];

  // --- Recurring state ---
  const {
    schedules: recurringSchedules,
    fetchSchedules: fetchRecurringSchedules,
    updateSchedule: updateRecurringSchedule,
    deleteSchedule: deleteRecurringSchedule,
    triggerNow: triggerRecurringNow,
    schedulesLoading,
    schedulesError,
    schedulesProjectId,
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
      void fetchTemplates(currentProject.id);
    }
  }, [
    currentProject,
    fetchStatuses,
    fetchFields,
    fetchProjectMembers,
    fetchWorkflowRules,
    fetchEffectiveAssignmentRules,
    fetchTemplates,
  ]);

  useEffect(() => {
    if (currentProject?.id) void fetchRecurringSchedules(currentProject.id, focusScheduleId);
  }, [currentProject?.id, focusScheduleId, fetchRecurringSchedules]);

  // Fetch workspace members and agents for assignment selects
  useEffect(() => {
    if (currentWorkspace?.id) {
      void fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace?.id, fetchAgents]);

  // Fetch home+guest agent-grants for the "Add Agent to Project" dropdown —
  // separate from fetchAgents above, see the useAgentWorkspaceGrantStore
  // destructure comment.
  useEffect(() => {
    if (currentWorkspace?.id) {
      void fetchWorkspaceAgentGrants(currentWorkspace.id);
    }
  }, [currentWorkspace?.id, fetchWorkspaceAgentGrants]);

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
      setGeneralFeedback({ type: "error", message: t("project.general.nameRequired") });
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
        message: t("project.general.saved"),
      });
    } catch (err) {
      setGeneralFeedback({
        type: "error",
        message:
          apiErrorMessage(err, t("project.general.saveFailed")),
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
      await saveProjectAssignmentRules(currentProject.id, config);
      await fetchEffectiveAssignmentRules(currentProject.id);
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
        apiErrorMessage(err, t("project.statuses.deleteFailed")),
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
        apiErrorMessage(err, t("project.fields.deleteFailed")),
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
        apiErrorMessage(err, t("project.members.removeFailed")),
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
        apiErrorMessage(err, t("project.members.addAgentFailed")),
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
      if (currentProject.is_archived) {
        await unarchiveProject(currentProject.id);
        setArchiveDialogOpen(false);
        setIsArchiving(false);
      } else {
        await archiveProject(currentProject.id);
        setArchiveDialogOpen(false);
        navigate(`/w/${wsSlug}`);
      }
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
            {t("project.title")}
          </h1>
        </div>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            {t("project.notFound", { slug: projectSlug })}
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Tab navigation */}
      <div className="flex gap-1 border-b border-border">
        {PROJECT_TABS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "px-3 py-2 text-sm font-medium border-b-2 -mb-px transition-colors whitespace-nowrap",
              activeTab === tab.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {tab.label}
          </button>
        ))}
      </div>

      <div className="mx-auto max-w-2xl space-y-6">
      {/* Section 1: General Information */}
      {activeTab === "general" && (
      <Card>
        <CardHeader>
          <CardTitle>{t("project.general.title")}</CardTitle>
          <CardDescription>
            {t("project.general.desc")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveGeneral} className="space-y-4">
            <div className="space-y-1.5">
              <label htmlFor="ps-name" className="text-sm font-medium">
                {t("project.general.name")} <span className="text-destructive">*</span>
              </label>
              <Input
                id="ps-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("project.general.namePlaceholder")}
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-slug" className="text-sm font-medium">
                {t("project.general.slug")}
              </label>
              <Input
                id="ps-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="project-slug"
              />
              <p className="text-xs text-muted-foreground">
                {t("project.general.slugHint")}
              </p>
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-description" className="text-sm font-medium">
                {t("project.general.description")}
              </label>
              <Textarea
                id="ps-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t("project.general.descriptionPlaceholder")}
                rows={3}
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor="ps-icon" className="text-sm font-medium">
                {t("project.general.icon")}
              </label>
              <Input
                id="ps-icon"
                value={icon}
                onChange={(e) => setIcon(e.target.value)}
                placeholder={t("project.general.iconPlaceholder")}
                maxLength={8}
              />
            </div>

            {generalFeedback && (
              <p
                className={cn(
                  "text-sm",
                  generalFeedback.type === "success"
                    ? "text-tone-success-600"
                    : "text-destructive",
                )}
              >
                {generalFeedback.message}
              </p>
            )}

            <div className="flex justify-end">
              <Button type="submit" disabled={isSavingGeneral}>
                {isSavingGeneral ? t("project.rules.saving") : t("project.general.saveChanges")}
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
              <CardTitle>{t("project.statuses.title")}</CardTitle>
              <CardDescription>
                {t("project.statuses.desc")}
              </CardDescription>
            </div>
            <Button size="sm" onClick={handleOpenCreateStatus}>
              <Plus className="h-4 w-4" />
              {t("project.statuses.add")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {sortedStatuses.length === 0 ? (
            <div className="py-8 text-center">
              <p className="text-sm text-muted-foreground">
                {t("project.statuses.empty")}
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
                        {t(`project.statusCategory.${status.category}`, { defaultValue: categoryConf.label })}
                      </Badge>
                    )}

                    {/* Default badge */}
                    {status.is_default && (
                      <Badge variant="outline" className="text-xs">
                        {t("project.statuses.default")}
                      </Badge>
                    )}

                    {/* Move up/down buttons */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={index === 0}
                      onClick={() => handleMoveStatus(index, "up")}
                      title={t("project.common.moveUp")}
                    >
                      <ArrowUp className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      disabled={index === sortedStatuses.length - 1}
                      onClick={() => handleMoveStatus(index, "down")}
                      title={t("project.common.moveDown")}
                    >
                      <ArrowDown className="h-3.5 w-3.5" />
                    </Button>

                    {/* Edit button */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      onClick={() => handleOpenEditStatus(status)}
                      title={t("project.statuses.edit")}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>

                    {/* Delete button */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive"
                      onClick={() => handleOpenDeleteStatus(status)}
                      title={t("project.statuses.delete")}
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
              <CardTitle>{t("project.fields.title")}</CardTitle>
              <CardDescription>
                {t("project.fields.desc")}
              </CardDescription>
            </div>
            <Button size="sm" onClick={handleOpenCreateField}>
              <Plus className="h-4 w-4" />
              {t("project.fields.add")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {sortedFields.length === 0 ? (
            <div className="py-8 text-center">
              <p className="text-sm text-muted-foreground">
                {t("project.fields.empty")}
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
                    {fieldTypeLabel(field.field_type)}
                  </Badge>

                  {/* Required badge */}
                  {field.is_required && (
                    <Badge variant="outline" className="text-xs">
                      {t("project.fields.required")}
                    </Badge>
                  )}

                  {/* Agent-visible badge */}
                  {field.is_visible_to_agents && (
                    <Badge variant="outline" className="text-xs">
                      <Eye className="mr-1 h-3 w-3" />
                      {t("project.common.agents")}
                    </Badge>
                  )}

                  {/* Move up/down buttons */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    disabled={index === 0}
                    onClick={() => handleMoveField(index, "up")}
                    title={t("project.common.moveUp")}
                  >
                    <ArrowUp className="h-3.5 w-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    disabled={index === sortedFields.length - 1}
                    onClick={() => handleMoveField(index, "down")}
                    title={t("project.common.moveDown")}
                  >
                    <ArrowDown className="h-3.5 w-3.5" />
                  </Button>

                  {/* Edit button */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleOpenEditField(field)}
                    title={t("project.fields.edit")}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>

                  {/* Delete button */}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    onClick={() => handleOpenDeleteField(field)}
                    title={t("project.fields.delete")}
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
              <CardTitle>{t("project.tabs.workflow")}</CardTitle>
              <CardDescription>
                {t("project.workflow.desc")}
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
              <CardTitle>{t("project.tabs.assignment")}</CardTitle>
              <CardDescription>
                {t("project.assign.desc")}
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
                {t("project.members.userMembers")}
              </CardTitle>
              <CardDescription>
                {t("project.members.userMembersDesc")}
              </CardDescription>
            </div>
            <Button size="sm" onClick={() => setAddMemberDialogOpen(true)}>
              <Plus className="h-4 w-4" />
              {t("project.members.addUser")}
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
                {t("project.members.noUsers")}
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
                      name={displayName(member.user)}
                      size="md"
                    />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-medium">
                          {displayName(member.user)}
                        </span>
                        {isMe && (
                          <span className="text-xs text-muted-foreground">{t("project.members.you")}</span>
                        )}
                      </div>
                      {isNamePlaceholder(member.user) ? (
                        <p className="truncate text-xs text-muted-foreground italic">
                          {t("project.members.noName")}
                        </p>
                      ) : (
                        <p className="truncate text-xs text-muted-foreground">
                          {member.user?.email}
                        </p>
                      )}
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
                      <option value="admin">{t("project.members.roleAdmin")}</option>
                      <option value="member">{t("project.members.roleMember")}</option>
                      <option value="viewer">{t("project.members.roleViewer")}</option>
                    </Select>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-destructive"
                      onClick={() => handleOpenRemoveMember(member)}
                      title={t("project.members.removeFromProject")}
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
                {t("project.members.agentMembers")}
              </CardTitle>
              <CardDescription>
                {t("project.members.agentMembersDesc")}
              </CardDescription>
            </div>
            <Button size="sm" onClick={() => setAddAgentMemberOpen(true)}>
              <Plus className="h-4 w-4" />
              {t("project.members.addAgent")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {agentMembers.length === 0 ? (
            <div className="py-6 text-center">
              <Bot className="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                {t("project.members.noAgents")}
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
                      {member.agent_name || member.agent_id?.slice(0, 8)}<AgentShortTag id={member.agent_id} />
                    </span>
                    <p className="text-xs text-muted-foreground">{t("project.members.agent")}</p>
                  </div>
                  <Badge variant="secondary" className="text-xs">{t(`project.role.${member.role}`, { defaultValue: member.role })}</Badge>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 text-muted-foreground hover:text-destructive"
                    onClick={() => handleOpenRemoveMember(member)}
                    title={t("project.members.removeAgent")}
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
              <p className="text-sm font-medium">{t("project.members.addAgentToProject")}</p>
              <Select
                value={selectedAgentId}
                onChange={(e) => setSelectedAgentId(e.target.value)}
                className="w-full text-sm"
              >
                <option value="">{t("project.members.selectAgent")}</option>
                {workspaceAgentGrants
                  .filter(
                    (g) => !agentMembers.some((m) => m.agent_id === g.agent.id),
                  )
                  .map((g) => (
                    <option key={g.agent.id} value={g.agent.id}>
                      {agentLabel(g.agent.name, g.agent.short_tag)}
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
                  {t("project.common.cancel")}
                </Button>
                <Button
                  size="sm"
                  disabled={!selectedAgentId || isAddingAgent}
                  onClick={() => void handleAddAgentMember()}
                >
                  {isAddingAgent ? t("project.members.adding") : t("project.assign.add")}
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
              <CardTitle>{t("project.recurring.title")}</CardTitle>
              <CardDescription>
                {t("project.recurring.desc")}
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
              {t("project.recurring.newSchedule")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {schedulesLoading || schedulesProjectId !== currentProject.id ? (
            <div role="status" className="py-8 text-sm text-muted-foreground">{t("project.recurring.loading")}</div>
          ) : schedulesError ? (
            <div role="alert" className="py-4 text-sm">
              {t("project.recurring.loadFailed")}
              <Button variant="ghost" onClick={() => void fetchRecurringSchedules(currentProject.id, focusScheduleId)}>{t("project.recurring.retry")}</Button>
            </div>
          ) : <>
          {focusScheduleId && !recurringSchedules.some(schedule => schedule.id === focusScheduleId) && (
            <p role="alert" className="mb-4 text-sm text-muted-foreground">{t("project.recurring.notFound")}</p>
          )}
          {recurringSchedules.length === 0 ? (
            <div className="py-10 text-center">
              <RefreshCw className="mx-auto mb-3 h-8 w-8 text-muted-foreground opacity-50" />
              <p className="text-sm text-muted-foreground">
                {t("project.recurring.empty")}
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
                {t("project.recurring.newSchedule")}
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {recurringSchedules.map((schedule) => (
                <EntityAnchor key={schedule.id} id={schedule.id} kind="schedule"
                  focused={focusScheduleId === schedule.id} className="py-4 first:pt-0 last:pb-0">
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
                            {schedule.is_active ? t("project.recurring.active") : t("project.recurring.paused")}
                          </Badge>
                          <Badge variant="outline" className="text-[10px] capitalize shrink-0">
                            {schedule.frequency === "custom"
                              ? t("project.recurring.customFreq", { cron: schedule.cron_expr })
                              : t(`project.recurring.frequency.${schedule.frequency}`, { defaultValue: schedule.frequency })}
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
                          <span className="capitalize">{t("project.recurring.priority", { priority: t(`project.priorityLc.${schedule.priority}`, { defaultValue: schedule.priority }) })}</span>
                          <span>{t("project.recurring.instances", { count: schedule.instance_count })}</span>
                          {schedule.next_run_at && schedule.is_active && (
                            <span>
                              {t("project.recurring.next")}{" "}
                              {i18nFmt.formatDate(schedule.next_run_at, {
                                dateStyle: "short",
                                timeStyle: "short",
                              })}
                            </span>
                          )}
                        </div>
                      </div>
                    </div>

                    <div className="flex shrink-0 items-center gap-1">
                      <a href={scheduleHref(wsSlug ?? "", projectSlug ?? "", schedule.id)}
                        title={t("project.recurring.link")} aria-label={t("project.recurring.link")}
                        className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted">
                        <LinkIcon className="h-4 w-4" />
                      </a>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title={t("project.recurring.history")}
                        onClick={() => setHistorySchedule(schedule)}
                      >
                        <History className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title={t("project.recurring.edit")}
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
                        title={schedule.is_active ? t("project.recurring.pause") : t("project.recurring.resume")}
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
                        title={t("project.recurring.trigger")}
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
                        title={t("project.recurring.delete")}
                        disabled={recurringActionLoading === `delete-${schedule.id}`}
                        onClick={async () => {
                          if (!confirm(t("project.recurring.deleteConfirm", { title: schedule.title_template }))) return;
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
                </EntityAnchor>
              ))}
            </div>
          )}
          </>}
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
              <CardTitle>{t("project.templates.title")}</CardTitle>
              <CardDescription>
                {t("project.templates.desc")}
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
              {t("project.templates.new")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {templates.length === 0 ? (
            <div className="py-10 text-center">
              <Save className="mx-auto mb-3 h-8 w-8 text-muted-foreground opacity-50" />
              <p className="text-sm text-muted-foreground">
                {t("project.templates.empty")}
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
                {t("project.templates.new")}
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
                          {t(`project.priorityLc.${tmpl.priority}`, { defaultValue: tmpl.priority })}
                        </Badge>
                      </div>
                      {tmpl.description && (
                        <p className="mt-0.5 text-xs text-muted-foreground truncate max-w-md">
                          {tmpl.description}
                        </p>
                      )}
                      <div className="mt-1 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
                        {tmpl.title_template && (
                          <span className="truncate max-w-xs">{t("project.templates.titleLine", { title: tmpl.title_template })}</span>
                        )}
                        {tmpl.labels && tmpl.labels.length > 0 && (
                          <span>{t("project.templates.labelsLine", { labels: tmpl.labels.join(", ") })}</span>
                        )}
                        {tmpl.estimated_hours != null && (
                          <span>{t("project.templates.estimated", { hours: tmpl.estimated_hours })}</span>
                        )}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground"
                        title={t("project.templates.edit")}
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
                        title={t("project.templates.delete")}
                        disabled={templateActionLoading === `delete-${tmpl.id}`}
                        onClick={async () => {
                          if (!confirm(t("project.templates.deleteConfirm", { name: tmpl.name }))) return;
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
                {editingTemplate ? t("project.templates.editTitle") : t("project.templates.new")}
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
                  {t("project.general.name")} <span className="text-destructive">*</span>
                </label>
                <Input
                  placeholder={t("project.templates.namePlaceholder")}
                  value={templateForm.name}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, name: e.target.value }))}
                  autoFocus
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t("project.general.description")}</label>
                <Input
                  placeholder={t("project.templates.descPlaceholder")}
                  value={templateForm.description}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, description: e.target.value }))}
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t("project.templates.defaultTitle")}</label>
                <Input
                  placeholder={t("project.templates.defaultTitlePlaceholder")}
                  value={templateForm.title_template}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, title_template: e.target.value }))}
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t("project.templates.defaultDesc")}</label>
                <Textarea
                  placeholder={t("project.templates.defaultDescPlaceholder")}
                  rows={3}
                  value={templateForm.description_template}
                  onChange={(e) => setTemplateForm((f) => ({ ...f, description_template: e.target.value }))}
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-sm font-medium">{t("project.templates.priority")}</label>
                  <Select
                    value={templateForm.priority}
                    onChange={(e) => setTemplateForm((f) => ({ ...f, priority: e.target.value as Priority }))}
                  >
                    {["none", "low", "medium", "high", "urgent"].map((p) => (
                      <option key={p} value={p} className="capitalize">{t(`project.priorityLc.${p}`)}</option>
                    ))}
                  </Select>
                </div>
                <div className="space-y-1.5">
                  <label className="text-sm font-medium">{t("project.templates.labels")}</label>
                  <Input
                    placeholder={t("project.templates.commaSeparated")}
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
                {t("project.common.cancel")}
              </Button>
              <Button
                disabled={templateActionLoading === "save"}
                onClick={async () => {
                  if (!templateForm.name.trim()) {
                    setTemplateFormError(t("project.general.nameRequired"));
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
                    setTemplateFormError(apiErrorMessage(err, t("project.templates.saveFailed")));
                  } finally {
                    setTemplateActionLoading(null);
                  }
                }}
              >
                {templateActionLoading === "save" ? t("project.rules.saving") : t("project.templates.save")}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* Section 10: Extensions (spec 04 §9) */}
      {activeTab === "integrations" && currentProject?.id && (
        <ProjectExtensionsTab projectId={currentProject.id} />
      )}

      {/* Section 9: Danger Zone */}
      {activeTab === "general" && (
      <Card className="border-destructive/50">
        <CardHeader>
          <CardTitle className="text-destructive">{t("project.danger.title")}</CardTitle>
          <CardDescription>
            {t("project.danger.desc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center justify-between rounded-lg border border-border p-4">
            <div>
              <p className="text-sm font-medium">
                {currentProject.is_archived ? t("project.danger.unarchiveProject") : t("project.danger.archiveProject")}
              </p>
              <p className="text-sm text-muted-foreground">
                {currentProject.is_archived
                  ? t("project.danger.archivedHint")
                  : t("project.danger.archiveHint")}
              </p>
            </div>
            <Button
              variant="outline"
              className="border-destructive/50 text-destructive hover:bg-destructive/10"
              onClick={() => setArchiveDialogOpen(true)}
            >
              {currentProject.is_archived ? t("project.danger.unarchive") : t("project.danger.archive")}
            </Button>
          </div>

          <Separator />

          <div className="flex items-center justify-between rounded-lg border border-destructive/30 p-4">
            <div>
              <p className="text-sm font-medium">{t("project.danger.deleteProject")}</p>
              <p className="text-sm text-muted-foreground">
                {t("project.danger.deleteHint")}
              </p>
            </div>
            <Button
              variant="destructive"
              onClick={() => setDeleteDialogOpen(true)}
            >
              {t("project.common.delete")}
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
        title={t("project.members.removeTitle")}
        description={
          memberToRemove
            ? t("project.members.removeConfirm", {
                who: memberToRemove.agent_id
                  ? agentLabel(memberToRemove.agent_name || t("project.members.thisAgent"), removeMemberTag)
                  : (memberToRemove.user?.name || t("project.members.thisMember")),
              })
            : ""
        }
        confirmText={t("project.members.removeTitle")}
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
        title={t("project.statuses.deleteTitle")}
        description={t("project.statuses.deleteConfirm", { name: statusToDelete?.name ?? "" })}
        confirmText={t("project.statuses.deleteTitle")}
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
        title={t("project.fields.deleteTitle")}
        description={t("project.fields.deleteConfirm", { name: fieldToDelete?.name ?? "" })}
        confirmText={t("project.fields.deleteOk")}
        variant="destructive"
        isLoading={isDeletingField}
      />

      {/* Archive Project Confirmation */}
      <ConfirmDialog
        open={archiveDialogOpen}
        onClose={() => setArchiveDialogOpen(false)}
        onConfirm={handleArchiveProject}
        title={currentProject.is_archived ? t("project.danger.unarchiveProject") : t("project.danger.archiveProject")}
        description={
          currentProject.is_archived
            ? t("project.danger.unarchiveConfirm", { name: currentProject.name })
            : t("project.danger.archiveConfirm", { name: currentProject.name })
        }
        confirmText={currentProject.is_archived ? t("project.danger.unarchiveProject") : t("project.danger.archiveProject")}
        variant="destructive"
        isLoading={isArchiving}
      />

      {/* Delete Project Confirmation */}
      <ConfirmDialog
        open={deleteDialogOpen}
        onClose={() => setDeleteDialogOpen(false)}
        onConfirm={handleDeleteProject}
        title={t("project.danger.deleteProject")}
        description={t("project.danger.deleteConfirm", { name: currentProject.name })}
        confirmText={t("project.danger.deleteProject")}
        variant="destructive"
        requireText={currentProject.name}
        isLoading={isDeleting}
      />
      </div>
    </div>
  );
}
