import * as i18nFmt from "@/i18n/format";
import { agentLabel } from "@/lib/agent-label";
import { AgentShortTag } from "@/components/agent-short-tag";
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import {
  AlertTriangle,
  ArrowRight,
  Bot,
  Check,
  Clock,
  Crown,
  Download,
  FileDown,
  FileUp,
  Pencil,
  Plus,
  Save,
  Settings,
  Shield,
  Trash2,
  Upload,
  Users,
  Zap,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar } from "@/components/ui/avatar";
import { Select } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { InviteMemberDialog } from "@/components/invite-member-dialog";
import { InviteAgentDialog } from "@/components/invite-agent-dialog";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import { InviteLinkBox } from "@/components/invite-link-box";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { useWorkspaceStore } from "@/stores/workspace";
import { WorkspaceThemeLink } from "@/components/themes/workspace-theme-link";
import { useAuthStore } from "@/stores/auth";
import { useMemberStore } from "@/stores/member";
import { useAgentStore } from "@/stores/agent";
import { useAgentWorkspaceGrantStore } from "@/stores/agent-workspace-grant";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useRulesStore } from "@/stores/rules";
import { cn } from "@/lib/cn";
import { displayName, inlineLabel, isNamePlaceholder } from "@/lib/user-display";
import { apiErrorMessage } from "@/lib/api-error";
import type {
  AgentWorkspaceGrantWithAgent,
  AssignmentRulesConfig,
  ImportResult,
  InviteDelivery,
  RuleViolation,
  TeamDirectoryAgent,
  TeamImportResult,
  WorkflowRulesConfig,
  WorkspaceMemberWithUser,
  WorkspaceRole,
} from "@/types";

const roleOptions: { value: WorkspaceRole; labelKey: string }[] = [
  { value: "admin", labelKey: "workspace.roles.admin" },
  { value: "member", labelKey: "workspace.roles.member" },
  { value: "viewer", labelKey: "workspace.roles.viewer" },
];

function roleBadgeVariant(role: WorkspaceRole): "default" | "secondary" | "outline" {
  if (role === "owner") return "default";
  if (role === "admin") return "secondary";
  return "outline";
}

function RoleIcon({ role }: { role: WorkspaceRole }) {
  if (role === "owner") return <Crown className="h-3.5 w-3.5 text-tone-warning-500" />;
  if (role === "admin") return <Shield className="h-3.5 w-3.5 text-tone-info-500" />;
  return null;
}

function formatDate(iso: string): string {
  try {
    return i18nFmt.formatDate(iso, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return iso;
  }
}

// ---- Assignment Rules editor ----

interface AssignmentRulesEditorProps {
  initialConfig: AssignmentRulesConfig;
  onSave: (config: AssignmentRulesConfig) => Promise<void>;
  isSaving: boolean;
  agents: TeamDirectoryAgent[];
  members: WorkspaceMemberWithUser[];
}

function WsAssigneeSelect({
  value,
  onChange,
  agents,
  members,
  placeholder,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  agents: TeamDirectoryAgent[];
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
      <option value="">{placeholder ?? t("workspace.assignment.selectAssignee")}</option>
      {agents.length > 0 && (
        <optgroup label={t("workspace.assignment.agents")}>
          {agents.map((a) => (
            <option key={a.id} value={`agent:${a.id}`}>
              {agentLabel(a.name, a.short_tag)}
            </option>
          ))}
        </optgroup>
      )}
      {members.length > 0 && (
        <optgroup label={t("workspace.assignment.members")}>
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

const PRIORITIES = ["urgent", "high", "medium", "low", "none"];
const PRIORITY_LABEL_KEYS: Record<string, string> = {
  urgent: "workspace.priority.urgent",
  high: "workspace.priority.high",
  medium: "workspace.priority.medium",
  low: "workspace.priority.low",
  none: "workspace.priority.none",
};

function AssignmentRulesEditor({
  initialConfig,
  onSave,
  isSaving,
  agents,
  members,
}: AssignmentRulesEditorProps) {
  const { t } = useTranslation("settings");
  const [defaultAssignee, setDefaultAssignee] = useState(
    initialConfig.default_assignee ?? "",
  );
  const [byType, setByType] = useState<{ type: string; assignee: string }[]>(
    Object.entries(initialConfig.by_type ?? {}).map(([type, assignee]) => ({
      type,
      assignee,
    })),
  );
  const [byPriority, setByPriority] = useState<
    { priority: string; assignee: string }[]
  >(
    Object.entries(initialConfig.by_priority ?? {}).map(
      ([priority, assignee]) => ({ priority, assignee }),
    ),
  );
  const [fallbackChain, setFallbackChain] = useState<string[]>(
    initialConfig.fallback_chain ?? [],
  );
  const [feedback, setFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);

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
      setFeedback({ type: "success", message: t("workspace.assignment.saved") });
    } catch (err) {
      setFeedback({
        type: "error",
        message: apiErrorMessage(err, t("workspace.assignment.saveFailed")),
      });
    }
  };

  return (
    <div className="space-y-5">
      {/* Default assignee */}
      <div className="space-y-1.5">
        <label className="text-sm font-medium">{t("workspace.assignment.defaultAssignee")}</label>
        <WsAssigneeSelect
          value={defaultAssignee}
          onChange={setDefaultAssignee}
          agents={agents}
          members={members}
          placeholder={t("workspace.assignment.none")}
        />
      </div>

      {/* By type rules */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">{t("workspace.assignment.byType")}</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setByType((prev) => [...prev, { type: "", assignee: "" }])
            }
          >
            <Plus className="h-3.5 w-3.5" />
            {t("workspace.assignment.addRule")}
          </Button>
        </div>
        {byType.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t("workspace.assignment.noTypeRules")}
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
                  placeholder={t("workspace.assignment.typePlaceholder")}
                  className="flex-1"
                />
                <ArrowRight className="h-4 w-4 text-muted-foreground shrink-0" />
                <WsAssigneeSelect
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
                  placeholder={t("workspace.assignment.selectAssignee")}
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

      {/* By priority rules */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium">{t("workspace.assignment.byPriority")}</label>
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
            {t("workspace.assignment.addRule")}
          </Button>
        </div>
        {byPriority.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t("workspace.assignment.noPriorityRules")}
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
                      {t(PRIORITY_LABEL_KEYS[p]!)}
                    </option>
                  ))}
                </Select>
                <ArrowRight className="h-4 w-4 text-muted-foreground shrink-0" />
                <WsAssigneeSelect
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
                  placeholder={t("workspace.assignment.selectAssignee")}
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
          <label className="text-sm font-medium">{t("workspace.assignment.fallbackChain")}</label>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setFallbackChain((prev) => [...prev, ""])}
          >
            <Plus className="h-3.5 w-3.5" />
            {t("workspace.assignment.add")}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("workspace.assignment.fallbackHint")}
        </p>
        {fallbackChain.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("workspace.assignment.noFallback")}</p>
        ) : (
          <div className="space-y-2">
            {fallbackChain.map((val, i) => (
              <div key={i} className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground w-5 shrink-0">
                  {i + 1}.
                </span>
                <WsAssigneeSelect
                  value={val}
                  onChange={(v) =>
                    setFallbackChain((prev) =>
                      prev.map((old, idx) => (idx === i ? v : old)),
                    )
                  }
                  agents={agents}
                  members={members}
                  placeholder={t("workspace.assignment.selectAssignee")}
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
          {isSaving ? t("workspace.assignment.saving") : t("workspace.assignment.saveRules")}
        </Button>
      </div>
    </div>
  );
}

// ---- Violations list ----

function ViolationRow({ v }: { v: RuleViolation }) {
  const { t } = useTranslation("settings");
  return (
    <div className="flex items-start gap-3 py-2.5 border-b border-border last:border-0">
      <AlertTriangle className="h-4 w-4 text-tone-warning-500 shrink-0 mt-0.5" />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm font-medium capitalize">
            {v.rule_type.replace(/_/g, " ")}
          </span>
          <Badge variant="outline" className="text-xs">
            {v.actor_type}: {v.actor_id.slice(0, 8)}
          </Badge>
          <Badge variant="secondary" className="text-xs">
            {v.action_taken}
          </Badge>
        </div>
        <p className="text-xs text-muted-foreground mt-0.5">
          {formatDate(v.created_at)}
          {v.project_id && (
            <span className="ml-2">{t("workspace.violations.project", { id: v.project_id.slice(0, 8) })}</span>
          )}
        </p>
        {v.violation_detail != null && (
          <p className="text-xs text-muted-foreground mt-0.5 font-mono">
            {typeof v.violation_detail === "string"
              ? v.violation_detail
              : JSON.stringify(v.violation_detail)}
          </p>
        )}
      </div>
    </div>
  );
}

// ---- Main page ----

export function WorkspaceSettingsPage() {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const navigate = useNavigate();
  const { t } = useTranslation("settings");
  const { currentWorkspace, updateWorkspace, uploadIcon, deleteWorkspace } = useWorkspaceStore();
  const { user } = useAuthStore();
    const {
    workspaceMembers,
    myRole,
    myRoleError,
    isLoadingWorkspaceMembers,
    fetchWorkspaceMembers,
    fetchMyRole,
    updateWorkspaceMemberRole,
    updateWorkspaceMemberName,
    removeWorkspaceMember,
    workspaceInvites,
    fetchWorkspaceInvites,
    resendInvite,
    revokeInvite,
  } = useMemberStore();

  const { agents, fetchAgents } = useAgentStore();
  const {
    workspaceAgentGrants,
    isLoadingWorkspaceAgentGrants,
    fetchWorkspaceAgentGrants,
    revokeAgentGrant,
  } = useAgentWorkspaceGrantStore();

  const {
    teamDirectory,
    fetchTeamDirectory,
    wsAssignmentRules,
    isWsRulesLoading,
    fetchWsAssignmentRules,
    saveWsAssignmentRules,
    violations,
    isViolationsLoading,
    fetchViolations,
    importConfig,
    exportConfig,
    importTeam,
    workflowTemplates,
    isTemplatesLoading,
    fetchWorkflowTemplates,
    saveWorkflowTemplates,
  } = useRulesStore();

  // General form state
  const [wsName, setWsName] = useState("");
  const [isSavingGeneral, setIsSavingGeneral] = useState(false);
  const [generalFeedback, setGeneralFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);

  // Branding (icon upload) state
  const [isUploadingIcon, setIsUploadingIcon] = useState(false);
  const [iconFeedback, setIconFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);
  const iconFileInputRef = useRef<HTMLInputElement>(null);

  // Danger zone (delete workspace) state
  const [deleteWorkspaceDialogOpen, setDeleteWorkspaceDialogOpen] = useState(false);
  const [isDeletingWorkspace, setIsDeletingWorkspace] = useState(false);
  const [deleteWorkspaceError, setDeleteWorkspaceError] = useState<string | null>(null);

  // Members state
  const [inviteDialogOpen, setInviteDialogOpen] = useState(false);
  const [memberToRemove, setMemberToRemove] =
    useState<WorkspaceMemberWithUser | null>(null);
  const [isRemoving, setIsRemoving] = useState(false);
  const [removeError, setRemoveError] = useState<string | null>(null);

  // Agent grants state (task U4)
  const [inviteAgentDialogOpen, setInviteAgentDialogOpen] = useState(false);
  const [editAgentId, setEditAgentId] = useState<string | null>(null);
  const [grantToRevoke, setGrantToRevoke] =
    useState<AgentWorkspaceGrantWithAgent | null>(null);
  const [isRevokingGrant, setIsRevokingGrant] = useState(false);
  const [revokeGrantError, setRevokeGrantError] = useState<string | null>(null);
  const [memberToRename, setMemberToRename] =
    useState<WorkspaceMemberWithUser | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [isRenaming, setIsRenaming] = useState(false);
  const [renameError, setRenameError] = useState<string | null>(null);
  // Outcome of the last Resend, scoped to the invite it belongs to.
  const [resendResult, setResendResult] = useState<{
    inviteId: string;
    delivery?: InviteDelivery;
    error?: string;
  } | null>(null);

  // Rules saving state
  const [isSavingRules, setIsSavingRules] = useState(false);

  // Tab state
  const [activeTab, setActiveTab] = useState("general");

  const WS_TABS = [
    { id: "profile", label: t("workspace.tabs.profile") },
    { id: "general", label: t("workspace.tabs.general") },
    { id: "theme", label: t("workspace.tabs.theme") },
    { id: "members", label: t("workspace.tabs.members") },
    { id: "assignment", label: t("workspace.tabs.assignment") },
    { id: "violations", label: t("workspace.tabs.violations") },
    { id: "workflow-templates", label: t("workspace.tabs.workflowTemplates") },
    { id: "config", label: t("workspace.tabs.config") },
  ];

  // Config Import/Export state
  const [isExporting, setIsExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);
  const [importConfigResult, setImportConfigResult] = useState<ImportResult | null>(null);
  const [importConfigError, setImportConfigError] = useState<string | null>(null);
  const [isImportingConfig, setIsImportingConfig] = useState(false);
  const [configYamlText, setConfigYamlText] = useState("");

  // Team Import state
  const [importTeamResult, setImportTeamResult] = useState<TeamImportResult | null>(null);
  const [importTeamError, setImportTeamError] = useState<string | null>(null);
  const [isImportingTeam, setIsImportingTeam] = useState(false);
  const [teamYamlText, setTeamYamlText] = useState("");

  // Workflow Templates state
  const [templatesEditorValue, setTemplatesEditorValue] = useState("");
  const [templatesEditorError, setTemplatesEditorError] = useState<string | null>(null);
  const [isSavingTemplates, setIsSavingTemplates] = useState(false);
  const [templatesSaved, setTemplatesSaved] = useState(false);
  const [expandedTemplate, setExpandedTemplate] = useState<string | null>(null);

  const configFileInputRef = useRef<HTMLInputElement>(null);
  const teamFileInputRef = useRef<HTMLInputElement>(null);

  // Populate form
  useEffect(() => {
    if (currentWorkspace) {
      setWsName(currentWorkspace.name);
    }
  }, [currentWorkspace]);

  // Fetch members, team directory, assignment rules, violations, workflow templates on mount
  useEffect(() => {
    if (currentWorkspace?.id) {
      void fetchWorkspaceMembers(currentWorkspace.id);
      void fetchMyRole(currentWorkspace.id);
      void fetchTeamDirectory(currentWorkspace.id);
      void fetchWsAssignmentRules(currentWorkspace.id);
      void fetchViolations(currentWorkspace.id);
      void fetchWorkflowTemplates(currentWorkspace.id);
      void fetchWorkspaceInvites(currentWorkspace.id);
      void fetchAgents(currentWorkspace.id);
      void fetchWorkspaceAgentGrants(currentWorkspace.id);
    }
  }, [
    currentWorkspace?.id,
    fetchWorkspaceMembers,
    fetchMyRole,
    fetchTeamDirectory,
    fetchWsAssignmentRules,
    fetchViolations,
    fetchWorkflowTemplates,
    fetchWorkspaceInvites,
    fetchAgents,
    fetchWorkspaceAgentGrants,
  ]);

  // Sync templates editor when store data arrives
  useEffect(() => {
    if (Object.keys(workflowTemplates).length > 0 && !templatesEditorValue) {
      setTemplatesEditorValue(JSON.stringify(workflowTemplates, null, 2));
    }
  }, [workflowTemplates, templatesEditorValue]);

  const canManageMembers = myRole === "owner" || myRole === "admin";
  const canManageRules = myRole === "owner" || myRole === "admin";
  const canDeleteWorkspace = myRole === "owner" || myRole === "admin";

  // Count owners to disable remove on last owner
  const ownerCount = workspaceMembers.filter((m) => m.role === "owner").length;

  // Agent grants (task U4). The list endpoint returns every active
  // connection including the workspace's own home agents (migration U1
  // backfilled one grant per pre-existing agent) — home vs guest is decided
  // client-side by intersecting with `agents`, the SAME
  // GET /workspaces/:ws_id/agents this page already loads.
  const homeAgentIds = new Set(agents.map((a) => a.id));
  const connectedAgentIds = new Set(
    workspaceAgentGrants.map((g) => g.agent_id),
  );

  const handleSaveGeneral = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!currentWorkspace) return;
    if (!wsName.trim()) {
      setGeneralFeedback({ type: "error", message: t("workspace.general.nameRequired") });
      return;
    }
    setIsSavingGeneral(true);
    setGeneralFeedback(null);
    try {
      await updateWorkspace(currentWorkspace.id, { name: wsName.trim() });
      setGeneralFeedback({ type: "success", message: t("workspace.general.saved") });
    } catch (err) {
      setGeneralFeedback({
        type: "error",
        message: apiErrorMessage(err, t("workspace.general.saveFailed")),
      });
    } finally {
      setIsSavingGeneral(false);
    }
  };

  const handleIconUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    if (!currentWorkspace) return;
    const file = e.target.files?.[0];
    if (!file) return;

    if (file.type !== "image/png") {
      setIconFeedback({ type: "error", message: t("workspace.general.iconPngOnly") });
      return;
    }
    if (file.size > 500 * 1024) {
      setIconFeedback({ type: "error", message: t("workspace.general.iconTooLarge") });
      return;
    }

    setIsUploadingIcon(true);
    setIconFeedback(null);
    try {
      await uploadIcon(currentWorkspace.id, file);
      setIconFeedback({ type: "success", message: t("workspace.general.iconUpdated") });
    } catch (err) {
      setIconFeedback({
        type: "error",
        message: apiErrorMessage(err, t("workspace.general.uploadFailed")),
      });
    } finally {
      setIsUploadingIcon(false);
      if (iconFileInputRef.current) iconFileInputRef.current.value = "";
    }
  };

  // Deleting cascades server-side (workspace, its projects, and their tasks
  // are all soft-deleted together) — see WorkspaceRepo.Delete. The store's
  // deleteWorkspace already drops the workspace from the local list and
  // clears currentWorkspace if it was the one removed; navigating to "/"
  // with no workspace in the URL is what hands control back to AppLayout,
  // which redirects to another workspace's activity page, or to the
  // create-workspace screen if none are left — the same logic that already
  // runs on first load, reused rather than duplicated here.
  const handleDeleteWorkspace = async () => {
    if (!currentWorkspace) return;
    setIsDeletingWorkspace(true);
    setDeleteWorkspaceError(null);
    try {
      await deleteWorkspace(currentWorkspace.id);
      navigate("/", { replace: true });
    } catch (err) {
      setDeleteWorkspaceError(
        apiErrorMessage(err, t("workspace.danger.deleteFailed")),
      );
      setIsDeletingWorkspace(false);
    }
  };

  const handleRoleChange = async (member: WorkspaceMemberWithUser, newRole: WorkspaceRole) => {
    if (!currentWorkspace) return;
    try {
      await updateWorkspaceMemberRole(currentWorkspace.id, member.user_id, newRole);
    } catch {
      // Silently fail — role will revert in UI since store didn't update
    }
  };

  const handleOpenRename = (member: WorkspaceMemberWithUser) => {
    setMemberToRename(member);
    // Prefilling with a placeholder would just make the operator delete the
    // address before typing, so start empty when there is no real name.
    setRenameValue(isNamePlaceholder(member.user) ? "" : member.user.name);
    setRenameError(null);
  };

  const handleConfirmRename = async () => {
    if (!currentWorkspace || !memberToRename) return;
    const trimmed = renameValue.trim();
    if (!trimmed) {
      setRenameError(t("workspace.general.nameRequired"));
      return;
    }
    setIsRenaming(true);
    setRenameError(null);
    try {
      await updateWorkspaceMemberName(currentWorkspace.id, memberToRename.user_id, trimmed);
      setMemberToRename(null);
    } catch (err) {
      setRenameError(apiErrorMessage(err, t("workspace.members.renameFailed")));
    } finally {
      setIsRenaming(false);
    }
  };

  const handleOpenRemove = (member: WorkspaceMemberWithUser) => {
    setMemberToRemove(member);
    setRemoveError(null);
  };

  const handleConfirmRemove = async () => {
    if (!currentWorkspace || !memberToRemove) return;
    setIsRemoving(true);
    setRemoveError(null);
    try {
      await removeWorkspaceMember(currentWorkspace.id, memberToRemove.user_id);
      setMemberToRemove(null);
    } catch (err) {
      setRemoveError(apiErrorMessage(err, t("workspace.members.removeFailed")));
    } finally {
      setIsRemoving(false);
    }
  };

  // Revoking a HOME connection is never offered in this UI (see the
  // homeAgentIds guard around the revoke button below) — it would take the
  // agent out of service entirely rather than just changing this
  // workspace's membership, since agentService.Authenticate treats a
  // revoked grant as an immediate 401 with no fallback. This handler only
  // ever runs against a guest row.
  const handleOpenRevokeGrant = (grant: AgentWorkspaceGrantWithAgent) => {
    setGrantToRevoke(grant);
    setRevokeGrantError(null);
  };

  const handleConfirmRevokeGrant = async () => {
    if (!currentWorkspace || !grantToRevoke) return;
    setIsRevokingGrant(true);
    setRevokeGrantError(null);
    try {
      await revokeAgentGrant(currentWorkspace.id, grantToRevoke.id);
      setGrantToRevoke(null);
    } catch (err) {
      setRevokeGrantError(
        apiErrorMessage(err, t("workspace.agents.revokeFailed")),
      );
    } finally {
      setIsRevokingGrant(false);
    }
  };

  const handleSaveWsRules = async (config: AssignmentRulesConfig) => {
    if (!currentWorkspace) return;
    setIsSavingRules(true);
    try {
      await saveWsAssignmentRules(currentWorkspace.id, config);
    } finally {
      setIsSavingRules(false);
    }
  };

  const handleExportConfig = async () => {
    if (!currentWorkspace) return;
    setIsExporting(true);
    setExportError(null);
    try {
      const yaml = await exportConfig(currentWorkspace.id);
      const blob = new Blob([yaml], { type: "text/yaml" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `mesh-config-${currentWorkspace.slug}.yaml`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      setExportError(apiErrorMessage(err, t("workspace.config.exportFailed")));
    } finally {
      setIsExporting(false);
    }
  };

  const handleImportConfigFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (ev) => {
      setConfigYamlText(ev.target?.result as string ?? "");
    };
    reader.readAsText(file);
    // Reset input so same file can be re-selected
    e.target.value = "";
  };

  const handleImportConfig = async () => {
    if (!currentWorkspace || !configYamlText.trim()) return;
    setIsImportingConfig(true);
    setImportConfigResult(null);
    setImportConfigError(null);
    try {
      const result = await importConfig(currentWorkspace.id, configYamlText);
      setImportConfigResult(result);
      setConfigYamlText("");
    } catch (err) {
      setImportConfigError(apiErrorMessage(err, t("workspace.config.importFailed")));
    } finally {
      setIsImportingConfig(false);
    }
  };

  const handleImportTeamFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (ev) => {
      setTeamYamlText(ev.target?.result as string ?? "");
    };
    reader.readAsText(file);
    e.target.value = "";
  };

  const handleImportTeam = async () => {
    if (!currentWorkspace || !teamYamlText.trim()) return;
    setIsImportingTeam(true);
    setImportTeamResult(null);
    setImportTeamError(null);
    try {
      const result = await importTeam(currentWorkspace.id, teamYamlText);
      setImportTeamResult(result);
      setTeamYamlText("");
    } catch (err) {
      setImportTeamError(apiErrorMessage(err, t("workspace.config.teamImportFailed")));
    } finally {
      setIsImportingTeam(false);
    }
  };

  const handleSaveTemplates = async () => {
    if (!currentWorkspace) return;
    setTemplatesEditorError(null);
    let parsed: Record<string, WorkflowRulesConfig>;
    try {
      parsed = JSON.parse(templatesEditorValue) as Record<string, WorkflowRulesConfig>;
    } catch {
      setTemplatesEditorError(t("workspace.templates.invalidJson"));
      return;
    }
    setIsSavingTemplates(true);
    setTemplatesSaved(false);
    try {
      await saveWorkflowTemplates(currentWorkspace.id, parsed);
      setTemplatesSaved(true);
      setTimeout(() => setTemplatesSaved(false), 3000);
    } catch (err) {
      setTemplatesEditorError(apiErrorMessage(err, t("workspace.templates.saveFailed")));
    } finally {
      setIsSavingTemplates(false);
    }
  };

  if (!currentWorkspace) {
    return (
      <div className="mx-auto max-w-2xl space-y-6">
        <div className="flex items-center gap-3">
          <Settings className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">{t("workspace.title")}</h1>
        </div>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            {t("workspace.notFound", { slug: wsSlug })}
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="mesh-mobile-settings space-y-6">
      {/* Tab navigation */}
      <div className="flex flex-wrap gap-1 border-b border-border md:flex-nowrap">
        {WS_TABS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            onClick={() => (tab.id === "profile" ? navigate(`/w/${wsSlug}/me`) : setActiveTab(tab.id))}
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
      {/* Role fetch failed (not "you have no role" — that's a real 404 and
          stays silent, see fetchMyRole in stores/member.ts). Shown on every
          tab: admin controls across Members/General/Workflow Templates/Config
          all key off the same myRole, so a fetch failure hides all of them
          at once — the reader needs to know why, not just see fewer buttons. */}
      {myRoleError && (
        <div className="flex items-start gap-3 rounded-lg border border-destructive/50 bg-destructive/5 p-4">
          <AlertTriangle className="h-4 w-4 text-destructive shrink-0 mt-0.5" />
          <div className="flex-1 min-w-0 space-y-1">
            <p className="text-sm font-medium text-destructive">
              {t("workspace.roleError.title")}
            </p>
            <p className="text-sm text-muted-foreground">
              {t("workspace.roleError.detail", { error: myRoleError })}
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => currentWorkspace && void fetchMyRole(currentWorkspace.id)}
          >
            {t("workspace.roleError.retry")}
          </Button>
        </div>
      )}
      {/* Section 1: General */}
      {activeTab === "general" && (
      <Card>
        <CardHeader>
          <CardTitle>{t("workspace.tabs.general")}</CardTitle>
          <CardDescription>{t("workspace.general.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveGeneral} className="space-y-4">
            <div className="space-y-1.5">
              <label htmlFor="ws-name" className="text-sm font-medium">
                {t("workspace.general.name")} <span className="text-destructive">*</span>
              </label>
              <Input
                id="ws-name"
                value={wsName}
                onChange={(e) => setWsName(e.target.value)}
                placeholder={t("workspace.general.namePlaceholder")}
              />
            </div>
            <div className="space-y-1.5">
              <label htmlFor="ws-slug" className="text-sm font-medium">
                {t("workspace.general.slug")}
              </label>
              <Input
                id="ws-slug"
                value={currentWorkspace.slug}
                disabled
                className="bg-muted text-muted-foreground"
              />
              <p className="text-xs text-muted-foreground">
                {t("workspace.general.slugHint")}
              </p>
            </div>

            {generalFeedback && (
              <p
                className={
                  generalFeedback.type === "success"
                    ? "text-sm text-tone-success-600"
                    : "text-sm text-destructive"
                }
              >
                {generalFeedback.message}
              </p>
            )}

            <div className="flex justify-end">
              <Button type="submit" disabled={isSavingGeneral}>
                {isSavingGeneral ? t("workspace.assignment.saving") : t("workspace.general.saveChanges")}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
      )}

      {/* Theme: branding (workspace icon) + theme choice and its settings */}
      {activeTab === "theme" && (
      <Card className="mt-4">
        <CardHeader>
          <CardTitle>{t("workspace.branding.title")}</CardTitle>
          <CardDescription>{t("workspace.branding.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-center gap-4">
            <div className="flex h-16 w-16 shrink-0 items-center justify-center overflow-hidden rounded-lg border bg-muted text-2xl font-bold text-muted-foreground">
              {currentWorkspace.icon_url ? (
                <img
                  src={currentWorkspace.icon_url}
                  alt={currentWorkspace.name}
                  className="h-full w-full object-cover"
                />
              ) : (
                currentWorkspace.name.charAt(0).toUpperCase()
              )}
            </div>
            <div className="space-y-2">
              <input
                ref={iconFileInputRef}
                type="file"
                accept="image/png"
                className="hidden"
                onChange={handleIconUpload}
              />
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={isUploadingIcon}
                onClick={() => iconFileInputRef.current?.click()}
              >
                <Upload className="mr-2 h-4 w-4" />
                {isUploadingIcon ? t("workspace.branding.uploading") : t("workspace.branding.upload")}
              </Button>
              <p className="text-xs text-muted-foreground">{t("workspace.branding.hint")}</p>
              {iconFeedback && (
                <p
                  className={
                    iconFeedback.type === "success"
                      ? "text-sm text-tone-success-600"
                      : "text-sm text-destructive"
                  }
                >
                  {iconFeedback.message}
                </p>
              )}
            </div>
          </div>
        </CardContent>
      </Card>
      )}

      {activeTab === "theme" && currentWorkspace && (
      <Card className="mt-4">
        <CardHeader>
          <CardTitle>{t("themes.workspace.title")}</CardTitle>
          <CardDescription>{t("themes.workspace.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* the theme is chosen and set up in Extensions, like any extension (П8) */}
          <WorkspaceThemeLink wsSlug={currentWorkspace.slug} />
          <Link to={`/w/${currentWorkspace.slug}/instance?tab=themes`} className="inline-block text-sm text-primary hover:underline">
            {t("themes.workspace.instanceLink")}
          </Link>
        </CardContent>
      </Card>
      )}

      {/* Danger Zone — owner/admin only. Member/viewer never see this section,
          even though the server would also refuse the request (PermDeleteWorkspace):
          a button that is guaranteed to 403 is a bad interface, not a second guard. */}
      {activeTab === "general" && canDeleteWorkspace && (
      <Card className="mt-4 border-destructive/50">
        <CardHeader>
          <CardTitle className="text-destructive">{t("workspace.danger.title")}</CardTitle>
          <CardDescription>{t("workspace.danger.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center justify-between rounded-lg border border-destructive/30 p-4">
            <div>
              <p className="text-sm font-medium">{t("workspace.danger.deleteTitle")}</p>
              <p className="text-sm text-muted-foreground">
                {t("workspace.danger.deleteDescription")}
              </p>
            </div>
            <Button
              variant="destructive"
              onClick={() => setDeleteWorkspaceDialogOpen(true)}
            >
              {t("workspace.danger.delete")}
            </Button>
          </div>
          {deleteWorkspaceError && (
            <p className="text-sm text-destructive">{deleteWorkspaceError}</p>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 3: Members */}
      {activeTab === "members" && (
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="min-w-[200px] space-y-1.5">
              <CardTitle>{t("workspace.tabs.members")}</CardTitle>
              <CardDescription>
                {t("workspace.members.description")}
              </CardDescription>
            </div>
            {canManageMembers && (
              <div className="flex items-center gap-2">
                <Button size="sm" onClick={() => setInviteDialogOpen(true)}>
                  <Users className="h-4 w-4" />
                  {t("workspace.members.inviteMember")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setInviteAgentDialogOpen(true)}
                >
                  <Bot className="h-4 w-4" />
                  {t("workspace.members.inviteAgent")}
                </Button>
              </div>
            )}
          </div>
        </CardHeader>
        <CardContent>
          {isLoadingWorkspaceMembers ? (
            <div className="space-y-3">
              {[1, 2, 3].map((i) => (
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
          ) : workspaceMembers.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {t("workspace.members.empty")}
            </p>
          ) : (
            <div className="divide-y divide-border">
              {workspaceMembers.map((member) => {
                const isMe = member.user_id === user?.id;
                const isLastOwner = member.role === "owner" && ownerCount <= 1;
                const canEditRole =
                  canManageMembers && member.role !== "owner";
                const canRemove =
                  canManageMembers && !isLastOwner;

                return (
                  <div
                    key={member.id}
                    className="flex flex-wrap items-center gap-3 py-3 first:pt-0 last:pb-0"
                  >
                    {/* Avatar */}
                    <Avatar
                      src={member.user.avatar_url || undefined}
                      name={displayName(member.user)}
                      size="md"
                    />

                    {/* Info. Name first, address only when it disambiguates —
                        for an account that never got a name the two are the
                        same string, and printing it twice reads as a bug.
                        min-w floor (not min-w-0) so the action controls wrap
                        to their own line on narrow screens instead of
                        crushing this column to an unreadable sliver. */}
                    <div className="min-w-[140px] flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-medium">
                          {displayName(member.user)}
                        </span>
                        {isMe && (
                          <span className="text-xs text-muted-foreground">{t("workspace.members.you")}</span>
                        )}
                        <RoleIcon role={member.role} />
                      </div>
                      {isNamePlaceholder(member.user) ? (
                        <p className="truncate text-xs text-muted-foreground italic">
                          {t("workspace.members.noName")}
                          {canManageMembers && !isMe && t("workspace.members.noNameHint")}
                        </p>
                      ) : (
                        <p className="truncate text-xs text-muted-foreground">
                          {member.user.email}
                        </p>
                      )}
                      <p className="truncate text-xs text-muted-foreground">
                        {t("workspace.members.joined", { date: formatDate(member.created_at) })}
                      </p>
                    </div>

                    {/* Action controls as one group so they wrap together onto
                        their own line on narrow screens, rather than each
                        squeezing the info column independently. gap-3 (not
                        gap-1) matches the original outer row's spacing between
                        these same three controls — this wrapper must not
                        change their relative spacing at 1440, only group them
                        for wrapping. */}
                    <div className="flex items-center gap-3">
                      {/* Edit name. Own name is edited on the Profile tab, which
                          is also the only place it can be changed once its owner
                          has chosen it. */}
                      {canManageMembers && !isMe && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 text-muted-foreground hover:text-foreground"
                          onClick={() => handleOpenRename(member)}
                          title={t("workspace.members.editName")}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                      )}

                      {/* Role badge or dropdown */}
                      {canEditRole ? (
                        <Select
                          value={member.role}
                          onChange={(e) =>
                            void handleRoleChange(
                              member,
                              e.target.value as WorkspaceRole,
                            )
                          }
                          className="w-28 text-sm"
                        >
                          {roleOptions.map((opt) => (
                            <option key={opt.value} value={opt.value}>
                              {t(opt.labelKey)}
                            </option>
                          ))}
                        </Select>
                      ) : (
                        <Badge
                          variant={roleBadgeVariant(member.role)}
                          className="capitalize"
                        >
                          {member.role}
                        </Badge>
                      )}

                      {/* Remove button */}
                      {canRemove && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 text-muted-foreground hover:text-destructive"
                          onClick={() => handleOpenRemove(member)}
                          title={t("workspace.members.remove")}
                          disabled={isMe && isLastOwner}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          )}

          {/* Connected agents — same card as members, per §U4: the invite
              button starts on the workspace side, so the list it fills
              belongs next to the people it mirrors, not on a separate tab. */}
          <div className="mt-6 border-t border-border pt-4">
            <p className="mb-3 text-sm font-medium">
              {t("workspace.team.agentsCount", { n: workspaceAgentGrants.length })}
            </p>
            {isLoadingWorkspaceAgentGrants ? (
              <div className="space-y-3">
                {[1, 2].map((i) => (
                  <div key={i} className="flex items-center gap-3">
                    <Skeleton className="h-8 w-8 rounded-full" />
                    <div className="flex-1 space-y-1.5">
                      <Skeleton className="h-4 w-32" />
                    </div>
                    <Skeleton className="h-8 w-20" />
                  </div>
                ))}
              </div>
            ) : workspaceAgentGrants.length === 0 ? (
              <p className="py-4 text-center text-sm text-muted-foreground">
                {t("workspace.agents.empty")}
              </p>
            ) : (
              <div className="divide-y divide-border">
                {workspaceAgentGrants.map((grant) => {
                  const isHome = homeAgentIds.has(grant.agent_id);
                  return (
                    <div
                      key={grant.id}
                      className="flex items-center gap-3 py-3 first:pt-0 last:pb-0"
                    >
                      <Avatar name={grant.agent.name} size="md" />
                      <div className="flex-1 min-w-0">
                        <span className="block truncate text-sm font-medium">
                          {grant.agent.name}<AgentShortTag tag={grant.agent.short_tag} />
                        </span>
                      </div>

                      {/* A home connection has no revoke button by design —
                          revoking it would take the agent out of service
                          entirely (agentService.Authenticate 401s the home
                          key too, immediately, with no fallback), not just
                          change this workspace's membership. Remove or
                          transfer the agent instead. */}
                      {isHome && (
                        <Badge
                          variant="outline"
                          className="shrink-0 text-xs"
                          title={t("workspace.agents.homeTitle")}
                        >
                          {t("workspace.agents.home")}
                        </Badge>
                      )}
                      <Badge
                        variant={roleBadgeVariant(grant.role)}
                        className="shrink-0 capitalize"
                      >
                        {grant.role}
                      </Badge>

                      {/* Edit opens the same agent card as the org chart. Only
                          for home agents: a guest's card belongs to its home
                          workspace, which this workspace's admin can't edit. */}
                      {canManageMembers && isHome && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 shrink-0 text-muted-foreground"
                          onClick={() => setEditAgentId(grant.agent_id)}
                          title={t("workspace.agents.edit")}
                          aria-label={t("workspace.agents.editAria", { name: grant.agent.name })}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                      )}
                      {canManageMembers && !isHome && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 shrink-0 text-muted-foreground hover:text-destructive"
                          onClick={() => handleOpenRevokeGrant(grant)}
                          title={t("workspace.agents.revoke")}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </CardContent>
      </Card>
      )}

      {/* Pending Invites — shown below the members card when on the members tab */}
      {activeTab === "members" && workspaceInvites.length > 0 && (
      <Card className="mt-4">
        <CardHeader>
          <div className="flex items-center gap-2">
            <Clock className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>{t("workspace.invites.title")}</CardTitle>
              <CardDescription>
                {t("workspace.invites.description")}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <div className="divide-y divide-border">
            {workspaceInvites.map((invite) => (
              <div key={invite.id} className="py-3">
                <div className="flex items-center gap-3">
                <div className="flex-1 min-w-0">
                  <p className="truncate text-sm font-medium">{invite.email}</p>
                  <p className="text-xs text-muted-foreground capitalize">
                    {invite.role} · {t("workspace.invites.expires", { date: i18nFmt.formatDate(invite.expires_at) })}
                  </p>
                </div>
                {canManageMembers && (
                  <div className="flex items-center gap-1 shrink-0">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 text-xs"
                      onClick={async () => {
                        if (!currentWorkspace) return;
                        // Report what the resend actually did. This used to
                        // swallow every outcome in an empty catch, so a resend
                        // that sent nothing looked exactly like one that worked.
                        try {
                          const delivery = await resendInvite(currentWorkspace.id, invite.id);
                          setResendResult({ inviteId: invite.id, delivery });
                        } catch (err) {
                          setResendResult({
                            inviteId: invite.id,
                            error: apiErrorMessage(err, t("workspace.invites.resendFailed")),
                          });
                        }
                      }}
                    >
                      {t("workspace.invites.resend")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive"
                      title={t("workspace.invites.revoke")}
                      onClick={async () => {
                        if (currentWorkspace) {
                          try { await revokeInvite(currentWorkspace.id, invite.id); }
                          catch { /* ignore */ }
                        }
                      }}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                )}
                </div>

                {/* Resend outcome for this invite, plus the link whenever no
                    email went out — otherwise the operator is left guessing. */}
                {resendResult?.inviteId === invite.id && (
                  <div className="mt-2 space-y-1.5">
                    {resendResult.error ? (
                      <p className="text-xs text-destructive">{resendResult.error}</p>
                    ) : resendResult.delivery?.email_sent ? (
                      <p className="text-xs text-tone-success-600 dark:text-tone-success-400">
                        {t("workspace.invites.emailSent", { email: invite.email })}
                      </p>
                    ) : (
                      <>
                        <p className="text-xs text-muted-foreground">
                          {resendResult.delivery?.delivery_status === "not_configured"
                            ? t("workspace.invites.notConfigured")
                            : t("workspace.invites.sendFailed")}
                        </p>
                        {resendResult.delivery && (
                          <InviteLinkBox url={resendResult.delivery.invite_url} />
                        )}
                      </>
                    )}
                  </div>
                )}
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
      )}

      {/* Section 4: Assignment Rules */}
      {activeTab === "assignment" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Zap className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>{t("workspace.tabs.assignment")}</CardTitle>
              <CardDescription>
                {t("workspace.assignment.description")}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {isWsRulesLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-3/4" />
            </div>
          ) : !canManageRules ? (
            <p className="text-sm text-muted-foreground">
              {t("workspace.assignment.adminOnly")}
            </p>
          ) : (
            <AssignmentRulesEditor
              initialConfig={wsAssignmentRules ?? {}}
              onSave={handleSaveWsRules}
              isSaving={isSavingRules}
              agents={teamDirectory?.agents ?? []}
              members={workspaceMembers}
            />
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 5: Rule Violations */}
      {activeTab === "violations" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <AlertTriangle className="h-4 w-4 text-tone-warning-500" />
            <div className="space-y-1.5">
              <CardTitle>{t("workspace.violations.title")}</CardTitle>
              <CardDescription>
                {t("workspace.violations.description")}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {isViolationsLoading ? (
            <div className="space-y-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : violations.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {t("workspace.violations.empty")}
            </p>
          ) : (
            <div>
              {violations.slice(0, 20).map((v) => (
                <ViolationRow key={v.id} v={v} />
              ))}
            </div>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 6: Workflow Templates */}
      {activeTab === "workflow-templates" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Zap className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>{t("workspace.tabs.workflowTemplates")}</CardTitle>
              <CardDescription>
                {t("workspace.templates.description")}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {isTemplatesLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : (
            <div className="space-y-4">
              {/* Template list (read view) */}
              {Object.keys(workflowTemplates).length > 0 && (
                <div className="space-y-2">
                  {Object.entries(workflowTemplates).map(([name, tpl]) => (
                    <div key={name} className="rounded-lg border border-border">
                      <button
                        type="button"
                        className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-muted/50 transition-colors"
                        onClick={() =>
                          setExpandedTemplate((prev) => (prev === name ? null : name))
                        }
                      >
                        <span className="flex-1 text-sm font-medium truncate">{name}</span>
                        {tpl.enforcement_mode && (
                          <Badge variant="secondary" className="text-xs capitalize shrink-0">
                            {tpl.enforcement_mode}
                          </Badge>
                        )}
                        {tpl.statuses && (
                          <span className="text-xs text-muted-foreground shrink-0">
                            {t("workspace.templates.statusesCount", { n: tpl.statuses.length })}
                          </span>
                        )}
                        {tpl.transitions && (
                          <span className="text-xs text-muted-foreground shrink-0">
                            {t("workspace.templates.transitionsCount", { n: Object.keys(tpl.transitions).length })}
                          </span>
                        )}
                      </button>
                      {expandedTemplate === name && (
                        <div className="border-t border-border px-4 py-3 bg-muted/30 space-y-2 text-sm">
                          {tpl.statuses && tpl.statuses.length > 0 && (
                            <div className="flex gap-2 flex-wrap">
                              <span className="text-muted-foreground w-28 shrink-0 text-xs">{t("workspace.templates.statuses")}</span>
                              <div className="flex flex-wrap gap-1">
                                {tpl.statuses.map((s) => (
                                  <Badge key={s} variant="outline" className="text-xs">
                                    {s}
                                  </Badge>
                                ))}
                              </div>
                            </div>
                          )}
                          {tpl.transitions && Object.keys(tpl.transitions).length > 0 && (
                            <div className="space-y-1">
                              <span className="text-muted-foreground text-xs">{t("workspace.templates.transitions")}</span>
                              {Object.entries(tpl.transitions).map(([from, rule]) => (
                                <div key={from} className="flex items-center gap-2 text-xs pl-1">
                                  <span className="font-mono bg-muted px-1.5 py-0.5 rounded text-xs">
                                    {from}
                                  </span>
                                  <ArrowRight className="h-3 w-3 text-muted-foreground shrink-0" />
                                  <span className="text-muted-foreground">
                                    {rule.allowed.join(", ")}
                                  </span>
                                </div>
                              ))}
                            </div>
                          )}
                          {tpl.policies && Object.keys(tpl.policies).length > 0 && (
                            <div className="space-y-1">
                              <span className="text-muted-foreground text-xs">{t("workspace.templates.policies")}</span>
                              {Object.entries(tpl.policies).map(([role, pol]) => (
                                <div key={role} className="flex items-center gap-2 text-xs pl-1">
                                  <span className="font-mono bg-muted px-1.5 py-0.5 rounded text-xs">
                                    {role}
                                  </span>
                                  <ArrowRight className="h-3 w-3 text-muted-foreground shrink-0" />
                                  <span className="text-muted-foreground">
                                    {pol.allowed.join(", ")}
                                  </span>
                                </div>
                              ))}
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}

              {/* JSON editor for advanced users */}
              {canManageRules && (
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <label className="text-sm font-medium">
                      {t("workspace.templates.editJson")}
                    </label>
                    <span className="text-xs text-muted-foreground">
                      {t("workspace.templates.editHint")}
                    </span>
                  </div>
                  <textarea
                    value={templatesEditorValue}
                    onChange={(e) => {
                      setTemplatesEditorValue(e.target.value);
                      setTemplatesEditorError(null);
                      setTemplatesSaved(false);
                    }}
                    placeholder={`{\n  "software-dev": {\n    "statuses": ["backlog","todo","in_progress","review","done"],\n    "enforcement_mode": "strict"\n  }\n}`}
                    rows={10}
                    className="w-full rounded-md border border-input bg-muted/30 px-3 py-2 text-xs font-mono resize-y focus:outline-none focus:ring-2 focus:ring-ring"
                    spellCheck={false}
                  />
                  {templatesEditorError && (
                    <p className="text-xs text-destructive flex items-center gap-1">
                      <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                      {templatesEditorError}
                    </p>
                  )}
                  <div className="flex items-center justify-between">
                    {templatesSaved && (
                      <span className="text-xs text-tone-success-600 flex items-center gap-1">
                        <Check className="h-3.5 w-3.5" />
                        {t("workspace.templates.saved")}
                      </span>
                    )}
                    <div className="ml-auto">
                      <Button
                        type="button"
                        onClick={handleSaveTemplates}
                        disabled={isSavingTemplates || !templatesEditorValue.trim()}
                        size="sm"
                      >
                        <Save className="h-3.5 w-3.5" />
                        {isSavingTemplates ? t("workspace.assignment.saving") : t("workspace.templates.save")}
                      </Button>
                    </div>
                  </div>
                </div>
              )}

              {Object.keys(workflowTemplates).length === 0 && !canManageRules && (
                <p className="py-4 text-center text-sm text-muted-foreground">
                  {t("workspace.templates.empty")}
                </p>
              )}
            </div>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 7: Config Import/Export */}
      {activeTab === "config" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <FileDown className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>{t("workspace.config.title")}</CardTitle>
              <CardDescription>
                {t("workspace.config.description")}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-6">
          {/* Export */}
          <div className="space-y-2">
            <h3 className="text-sm font-medium flex items-center gap-1.5">
              <Download className="h-3.5 w-3.5 text-muted-foreground" />
              {t("workspace.config.exportTitle")}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t("workspace.config.exportDescription")}
            </p>
            {exportError && (
              <p className="text-xs text-destructive flex items-center gap-1">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                {exportError}
              </p>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleExportConfig}
              disabled={isExporting}
            >
              <FileDown className="h-3.5 w-3.5" />
              {isExporting ? t("workspace.config.exporting") : t("workspace.config.download")}
            </Button>
          </div>

          <Separator />

          {/* Import Config */}
          {canManageRules && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium flex items-center gap-1.5">
                <Upload className="h-3.5 w-3.5 text-muted-foreground" />
                {t("workspace.config.import")}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t("workspace.config.importDescription")}
              </p>

              {/* File upload */}
              <div className="flex items-center gap-2">
                <input
                  ref={configFileInputRef}
                  type="file"
                  accept=".yaml,.yml"
                  className="hidden"
                  onChange={handleImportConfigFile}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => configFileInputRef.current?.click()}
                >
                  <FileUp className="h-3.5 w-3.5" />
                  {t("workspace.config.chooseFile")}
                </Button>
                {configYamlText && (
                  <span className="text-xs text-muted-foreground">
                    {t("workspace.config.fileLoaded", { n: configYamlText.length })}
                  </span>
                )}
              </div>

              {/* Textarea for pasting */}
              <textarea
                value={configYamlText}
                onChange={(e) => {
                  setConfigYamlText(e.target.value);
                  setImportConfigResult(null);
                  setImportConfigError(null);
                }}
                placeholder={t("workspace.config.pasteConfig")}
                rows={6}
                className="w-full rounded-md border border-input bg-muted/30 px-3 py-2 text-xs font-mono resize-y focus:outline-none focus:ring-2 focus:ring-ring"
                spellCheck={false}
              />

              {importConfigError && (
                <p className="text-xs text-destructive flex items-center gap-1">
                  <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                  {importConfigError}
                </p>
              )}

              {importConfigResult && (
                <div className="rounded-lg border border-border bg-muted/30 p-3 space-y-1.5 text-xs">
                  <p className="font-medium text-tone-success-700 dark:text-tone-success-400 flex items-center gap-1">
                    <Check className="h-3.5 w-3.5" />
                    {t("workspace.config.importSuccess")}
                  </p>
                  {importConfigResult.team && (
                    <p className="text-muted-foreground">
                      {t("workspace.config.teamUpdated", { agents: importConfigResult.team.agents_updated, humans: importConfigResult.team.humans_updated })}
                      {importConfigResult.team.errors && importConfigResult.team.errors.length > 0 && (
                        <span className="text-tone-warning-600"> {t("workspace.config.errorsCount", { n: importConfigResult.team.errors.length })}</span>
                      )}
                    </p>
                  )}
                  {importConfigResult.assignment_rules && (
                    <p className="text-muted-foreground">
                      {t("workspace.config.assignmentRulesResult", { result: importConfigResult.assignment_rules.updated ? t("workspace.config.updated") : t("workspace.config.noChanges") })}
                    </p>
                  )}
                  {importConfigResult.workflow_templates && (
                    <p className="text-muted-foreground">
                      {t("workspace.config.templatesResult", { created: importConfigResult.workflow_templates.created, updated: importConfigResult.workflow_templates.updated })}
                    </p>
                  )}
                  {importConfigResult.warnings.length > 0 && (
                    <div className="space-y-0.5">
                      <p className="text-tone-warning-600 flex items-center gap-1">
                        <AlertTriangle className="h-3 w-3 shrink-0" />
                        {t("workspace.config.warnings", { n: importConfigResult.warnings.length })}
                      </p>
                      {importConfigResult.warnings.map((w, i) => (
                        <p key={i} className="pl-4 text-muted-foreground">{w}</p>
                      ))}
                    </div>
                  )}
                </div>
              )}

              <div className="flex justify-end">
                <Button
                  type="button"
                  size="sm"
                  onClick={handleImportConfig}
                  disabled={isImportingConfig || !configYamlText.trim()}
                >
                  <Upload className="h-3.5 w-3.5" />
                  {isImportingConfig ? t("workspace.config.importing") : t("workspace.config.import")}
                </Button>
              </div>
            </div>
          )}

          <Separator />

          {/* Import Team */}
          {canManageRules && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium flex items-center gap-1.5">
                <Users className="h-3.5 w-3.5 text-muted-foreground" />
                {t("workspace.config.importTeam")}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t("workspace.config.importTeamDescription")}
              </p>

              <div className="flex items-center gap-2">
                <input
                  ref={teamFileInputRef}
                  type="file"
                  accept=".yaml,.yml"
                  className="hidden"
                  onChange={handleImportTeamFile}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => teamFileInputRef.current?.click()}
                >
                  <FileUp className="h-3.5 w-3.5" />
                  {t("workspace.config.chooseFile")}
                </Button>
                {teamYamlText && (
                  <span className="text-xs text-muted-foreground">
                    {t("workspace.config.fileLoaded", { n: teamYamlText.length })}
                  </span>
                )}
              </div>

              <textarea
                value={teamYamlText}
                onChange={(e) => {
                  setTeamYamlText(e.target.value);
                  setImportTeamResult(null);
                  setImportTeamError(null);
                }}
                placeholder={t("workspace.config.pasteTeam")}
                rows={5}
                className="w-full rounded-md border border-input bg-muted/30 px-3 py-2 text-xs font-mono resize-y focus:outline-none focus:ring-2 focus:ring-ring"
                spellCheck={false}
              />

              {importTeamError && (
                <p className="text-xs text-destructive flex items-center gap-1">
                  <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                  {importTeamError}
                </p>
              )}

              {importTeamResult && (
                <div className="rounded-lg border border-border bg-muted/30 p-3 space-y-1 text-xs">
                  <p className="font-medium text-tone-success-700 dark:text-tone-success-400 flex items-center gap-1">
                    <Check className="h-3.5 w-3.5" />
                    {t("workspace.config.teamImportSuccess")}
                  </p>
                  <p className="text-muted-foreground">
                    {t("workspace.config.agentsHumansUpdated", { agents: importTeamResult.agents_updated, humans: importTeamResult.humans_updated })}
                  </p>
                  {importTeamResult.errors && importTeamResult.errors.length > 0 && (
                    <div className="space-y-0.5">
                      <p className="text-tone-warning-600">{t("workspace.config.errors")}</p>
                      {importTeamResult.errors.map((e, i) => (
                        <p key={i} className="pl-4 text-muted-foreground">{e}</p>
                      ))}
                    </div>
                  )}
                </div>
              )}

              <div className="flex justify-end">
                <Button
                  type="button"
                  size="sm"
                  onClick={handleImportTeam}
                  disabled={isImportingTeam || !teamYamlText.trim()}
                >
                  <Users className="h-3.5 w-3.5" />
                  {isImportingTeam ? t("workspace.config.importing") : t("workspace.config.importTeam")}
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>
      )}

      {/* Invite Member Dialog */}
      <InviteMemberDialog
        open={inviteDialogOpen}
        onClose={() => setInviteDialogOpen(false)}
        workspaceId={currentWorkspace.id}
      />

      {/* Remove Member Confirmation */}
      <ConfirmDialog
        open={!!memberToRemove}
        onClose={() => {
          setMemberToRemove(null);
          setRemoveError(null);
        }}
        onConfirm={handleConfirmRemove}
        title={t("workspace.members.removeTitle")}
        description={
          memberToRemove
            ? t("workspace.members.removeConfirm", { name: displayName(memberToRemove.user), email: memberToRemove.user.email })
            : ""
        }
        confirmText={t("workspace.members.removeTitle")}
        variant="destructive"
        isLoading={isRemoving}
      />

      {/* Invite Agent Dialog */}
      <AgentDetailDialog
        open={editAgentId !== null}
        onOpenChange={(open) => {
          if (!open) setEditAgentId(null);
        }}
        agent={agents.find((a) => a.id === editAgentId) ?? null}
      />

      <InviteAgentDialog
        open={inviteAgentDialogOpen}
        onClose={() => setInviteAgentDialogOpen(false)}
        workspaceId={currentWorkspace.id}
        connectedAgentIds={connectedAgentIds}
      />

      {/* Revoke Agent Connection Confirmation */}
      <ConfirmDialog
        open={!!grantToRevoke}
        onClose={() => {
          setGrantToRevoke(null);
          setRevokeGrantError(null);
        }}
        onConfirm={() => void handleConfirmRevokeGrant()}
        title={t("workspace.agents.revokeTitle")}
        description={
          grantToRevoke
            ? t("workspace.agents.revokeConfirm", { agent: agentLabel(grantToRevoke.agent.name, grantToRevoke.agent.short_tag) })
            : ""
        }
        confirmText={t("workspace.agents.revokeConfirmText")}
        variant="destructive"
        isLoading={isRevokingGrant}
      />
      {revokeGrantError && (
        <p className="text-sm text-destructive">{revokeGrantError}</p>
      )}

      {/* Rename member. Scoped to a display name that nobody has claimed: the
          API answers 403 once its owner has set it themselves, because the name
          is account-wide and would change how they appear in every other
          workspace too. */}
      <Dialog
        open={!!memberToRename}
        onOpenChange={(next) => { if (!next) setMemberToRename(null); }}
      >
        <DialogContent onClose={() => setMemberToRename(null)}>
          <DialogHeader>
            <DialogTitle>{t("workspace.members.editName")}</DialogTitle>
            <DialogDescription>
              {memberToRename
                ? t("workspace.members.renameDescription", { email: memberToRename.user.email })
                : ""}
            </DialogDescription>
          </DialogHeader>

          <form
            className="mt-4 space-y-4"
            onSubmit={(e) => { e.preventDefault(); void handleConfirmRename(); }}
          >
            <div className="space-y-1.5">
              <label htmlFor="ws-rename" className="text-sm font-medium">
                {t("workspace.members.nameLabel")} <span className="text-destructive">*</span>
              </label>
              <Input
                id="ws-rename"
                value={renameValue}
                onChange={(e) => setRenameValue(e.target.value)}
                placeholder={t("workspace.members.namePlaceholder")}
                autoFocus
                maxLength={100}
              />
              <p className="text-xs text-muted-foreground">
                {t("workspace.members.renameHint")}
              </p>
            </div>

            {renameError && <p className="text-sm text-destructive">{renameError}</p>}

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setMemberToRename(null)}
                disabled={isRenaming}
              >
                {t("workspace.cancel")}
              </Button>
              <Button type="submit" disabled={isRenaming}>
                {isRenaming ? t("workspace.members.savingName") : t("workspace.members.saveName")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {removeError && (
        <p className="text-sm text-destructive">{removeError}</p>
      )}

      {/* Delete Workspace Confirmation */}
      <ConfirmDialog
        open={deleteWorkspaceDialogOpen}
        onClose={() => {
          setDeleteWorkspaceDialogOpen(false);
          setDeleteWorkspaceError(null);
        }}
        onConfirm={() => void handleDeleteWorkspace()}
        title={t("workspace.danger.deleteTitle")}
        description={t("workspace.danger.deleteConfirm", { name: currentWorkspace.name })}
        confirmText={t("workspace.danger.deleteTitle")}
        variant="destructive"
        requireText={currentWorkspace.name}
        isLoading={isDeletingWorkspace}
      />
      </div>
    </div>
  );
}
