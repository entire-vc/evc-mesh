import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { asCapabilityList } from "@/lib/agent-utils";
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
import { InviteLinkBox } from "@/components/invite-link-box";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAuthStore } from "@/stores/auth";
import { useMemberStore } from "@/stores/member";
import { WorkspaceSecrets } from "@/components/workspace-secrets";
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
  AssignmentRulesConfig,
  ImportResult,
  InviteDelivery,
  RuleViolation,
  TeamDirectoryAgent,
  TeamDirectoryHuman,
  TeamImportResult,
  WorkflowRulesConfig,
  WorkspaceMemberWithUser,
  WorkspaceRole,
} from "@/types";

const roleOptions: { value: WorkspaceRole; label: string }[] = [
  { value: "admin", label: "Admin" },
  { value: "member", label: "Member" },
  { value: "viewer", label: "Viewer" },
];

function roleBadgeVariant(role: WorkspaceRole): "default" | "secondary" | "outline" {
  if (role === "owner") return "default";
  if (role === "admin") return "secondary";
  return "outline";
}

function RoleIcon({ role }: { role: WorkspaceRole }) {
  if (role === "owner") return <Crown className="h-3.5 w-3.5 text-amber-500" />;
  if (role === "admin") return <Shield className="h-3.5 w-3.5 text-blue-500" />;
  return null;
}

function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return iso;
  }
}

function agentStatusColor(status: string): string {
  switch (status) {
    case "online":
      return "bg-green-500";
    case "busy":
      return "bg-amber-500";
    case "error":
      return "bg-red-500";
    default:
      return "bg-gray-400";
  }
}

// ---- Team section sub-components ----

function AgentRow({ agent }: { agent: TeamDirectoryAgent }) {
  const caps = asCapabilityList(agent.capabilities);
  const [expanded, setExpanded] = useState(false);
  return (
    <div className="rounded-lg border border-border">
      <button
        type="button"
        className="flex w-full items-center gap-3 px-3 py-2.5 text-left hover:bg-muted/50 transition-colors"
        onClick={() => setExpanded((v) => !v)}
      >
        <span
          className={cn(
            "h-2.5 w-2.5 shrink-0 rounded-full",
            agentStatusColor(agent.status),
          )}
          title={agent.status}
        />
        <span className="flex-1 text-sm font-medium truncate">{agent.name}</span>
        <Badge variant="secondary" className="text-xs capitalize shrink-0">
          {agent.role}
        </Badge>
        <span className="text-xs text-muted-foreground shrink-0">
          {agent.current_tasks}/{agent.max_concurrent_tasks} tasks
        </span>
        {caps.slice(0, 2).map((cap) => (
          <Badge key={cap} variant="outline" className="text-xs shrink-0">
            {cap}
          </Badge>
        ))}
        {caps.length > 2 && (
          <span className="text-xs text-muted-foreground shrink-0">
            +{caps.length - 2}
          </span>
        )}
      </button>
      {expanded && (
        <div className="border-t border-border px-4 py-3 space-y-2 text-sm bg-muted/30">
          {agent.responsibility_zone && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-36 shrink-0">Responsibility zone</span>
              <span>{agent.responsibility_zone}</span>
            </div>
          )}
          {agent.working_hours && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-36 shrink-0">Working hours</span>
              <span className="flex items-center gap-1">
                <Clock className="h-3.5 w-3.5 text-muted-foreground" />
                {agent.working_hours}
              </span>
            </div>
          )}
          {agent.accepts_from.length > 0 && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-36 shrink-0">Accepts from</span>
              <span>{agent.accepts_from.join(", ")}</span>
            </div>
          )}
          {agent.profile_description && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-36 shrink-0">Description</span>
              <span className="text-muted-foreground">{agent.profile_description}</span>
            </div>
          )}
          {caps.length > 0 && (
            <div className="flex gap-2">
              <span className="text-muted-foreground w-36 shrink-0">All capabilities</span>
              <div className="flex flex-wrap gap-1">
                {caps.map((cap) => (
                  <Badge key={cap} variant="outline" className="text-xs">
                    {cap}
                  </Badge>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function HumanRow({ human }: { human: TeamDirectoryHuman }) {
  const caps = asCapabilityList(human.capabilities);
  return (
    <div className="flex items-center gap-3 py-2.5 px-3 rounded-lg border border-border">
      <Avatar
        src={human.avatar_url || undefined}
        name={human.name || human.email}
        size="md"
      />
      <div className="flex-1 min-w-0">
        <span className="text-sm font-medium truncate block">{human.name}</span>
        <p className="text-xs text-muted-foreground truncate">{human.email}</p>
      </div>
      <Badge variant="outline" className="text-xs capitalize shrink-0">
        {human.role}
      </Badge>
      {human.responsibility_zone && (
        <span className="text-xs text-muted-foreground shrink-0 hidden sm:inline">
          {human.responsibility_zone}
        </span>
      )}
      {human.availability && (
        <Badge
          variant="secondary"
          className="text-xs capitalize shrink-0"
        >
          {human.availability}
        </Badge>
      )}
      {caps.length > 0 && (
        <div className="hidden md:flex gap-1">
          {caps.slice(0, 2).map((cap) => (
            <Badge key={cap} variant="outline" className="text-xs">
              {cap}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
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
  placeholder = "Select assignee...",
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  agents: TeamDirectoryAgent[];
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
              {inlineLabel(m.user)}
            </option>
          ))}
        </optgroup>
      )}
    </Select>
  );
}

const PRIORITIES = ["urgent", "high", "medium", "low", "none"];

function AssignmentRulesEditor({
  initialConfig,
  onSave,
  isSaving,
  agents,
  members,
}: AssignmentRulesEditorProps) {
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
      setFeedback({ type: "success", message: "Assignment rules saved." });
    } catch (err) {
      setFeedback({
        type: "error",
        message: apiErrorMessage(err, "Failed to save rules"),
      });
    }
  };

  return (
    <div className="space-y-5">
      {/* Default assignee */}
      <div className="space-y-1.5">
        <label className="text-sm font-medium">Default Assignee</label>
        <WsAssigneeSelect
          value={defaultAssignee}
          onChange={setDefaultAssignee}
          agents={agents}
          members={members}
          placeholder="None"
        />
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
            No type-based rules. Add rules to assign tasks automatically by type.
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
                  placeholder="Task type (e.g. bug)"
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
                  placeholder="Select assignee..."
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
            No priority-based rules configured.
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
                  placeholder="Select assignee..."
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
        <p className="text-xs text-muted-foreground">
          Ordered list of assignees tried if primary assignment fails.
        </p>
        {fallbackChain.length === 0 ? (
          <p className="text-xs text-muted-foreground">No fallback chain configured.</p>
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
                  placeholder="Select assignee..."
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

// ---- Violations list ----

function ViolationRow({ v }: { v: RuleViolation }) {
  return (
    <div className="flex items-start gap-3 py-2.5 border-b border-border last:border-0">
      <AlertTriangle className="h-4 w-4 text-amber-500 shrink-0 mt-0.5" />
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
            <span className="ml-2">Project: {v.project_id.slice(0, 8)}</span>
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
  const { currentWorkspace, updateWorkspace, uploadIcon, deleteWorkspace } = useWorkspaceStore();
  const { user, updateProfile, checkUsername } = useAuthStore();
  const {
    workspaceMembers,
    myRole,
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

  const {
    teamDirectory,
    isTeamLoading,
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

  // Profile form state
  const [profileName, setProfileName] = useState(user?.name ?? "");
  const [isSavingProfile, setIsSavingProfile] = useState(false);
  const [profileFeedback, setProfileFeedback] = useState<{
    type: "success" | "error";
    message: string;
  } | null>(null);

  const [profileUsername, setProfileUsername] = useState(user?.username ?? "");
  const [usernameStatus, setUsernameStatus] = useState<"idle" | "checking" | "available" | "taken" | "invalid">("idle");
  const usernameTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Tab state
  const [activeTab, setActiveTab] = useState("general");

  const WS_TABS = [
    { id: "profile", label: "Profile" },
    { id: "general", label: "General" },
    { id: "members", label: "Members" },
    { id: "team", label: "Team Directory" },
    { id: "assignment", label: "Assignment Rules" },
    { id: "violations", label: "Violations" },
    { id: "workflow-templates", label: "Workflow Templates" },
    { id: "config", label: "Config" },
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
  ]);

  // Sync templates editor when store data arrives
  useEffect(() => {
    if (Object.keys(workflowTemplates).length > 0 && !templatesEditorValue) {
      setTemplatesEditorValue(JSON.stringify(workflowTemplates, null, 2));
    }
  }, [workflowTemplates, templatesEditorValue]);

  useEffect(() => {
    setProfileUsername(user?.username ?? "");
  }, [user?.username]);

  const handleUsernameChange = useCallback(
    (value: string) => {
      setProfileUsername(value);
      if (usernameTimerRef.current) clearTimeout(usernameTimerRef.current);
      if (value === "") {
        setUsernameStatus("idle");
        return;
      }
      const validRe = /^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$|^[a-z0-9]$/;
      if (!validRe.test(value)) {
        setUsernameStatus("invalid");
        return;
      }
      setUsernameStatus("checking");
      usernameTimerRef.current = setTimeout(async () => {
        try {
          const available = await checkUsername(value);
          setUsernameStatus(available ? "available" : "taken");
        } catch {
          setUsernameStatus("idle");
        }
      }, 400);
    },
    [checkUsername],
  );

  const canManageMembers = myRole === "owner" || myRole === "admin";
  const canManageRules = myRole === "owner" || myRole === "admin";
  const canDeleteWorkspace = myRole === "owner" || myRole === "admin";
  const canManageSecrets = myRole === "owner" || myRole === "admin";

  // Count owners to disable remove on last owner
  const ownerCount = workspaceMembers.filter((m) => m.role === "owner").length;

  const handleSaveProfile = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = profileName.trim();
    if (!trimmed) {
      setProfileFeedback({ type: "error", message: "Display name is required" });
      return;
    }
    if ([...trimmed].length > 100) {
      setProfileFeedback({ type: "error", message: "Display name must be at most 100 characters" });
      return;
    }
    setIsSavingProfile(true);
    setProfileFeedback(null);
    try {
      await updateProfile(trimmed, profileUsername.trim() || undefined);
      setProfileFeedback({ type: "success", message: "Profile saved." });
    } catch (err) {
      setProfileFeedback({
        type: "error",
        message: apiErrorMessage(err, "Failed to save profile"),
      });
    } finally {
      setIsSavingProfile(false);
    }
  };

  const handleSaveGeneral = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!currentWorkspace) return;
    if (!wsName.trim()) {
      setGeneralFeedback({ type: "error", message: "Name is required" });
      return;
    }
    setIsSavingGeneral(true);
    setGeneralFeedback(null);
    try {
      await updateWorkspace(currentWorkspace.id, { name: wsName.trim() });
      setGeneralFeedback({ type: "success", message: "Workspace settings saved." });
    } catch (err) {
      setGeneralFeedback({
        type: "error",
        message: apiErrorMessage(err, "Failed to save settings"),
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
      setIconFeedback({ type: "error", message: "Only PNG files are accepted." });
      return;
    }
    if (file.size > 500 * 1024) {
      setIconFeedback({ type: "error", message: "Icon must be ≤ 500 KB." });
      return;
    }

    setIsUploadingIcon(true);
    setIconFeedback(null);
    try {
      await uploadIcon(currentWorkspace.id, file);
      setIconFeedback({ type: "success", message: "Icon updated." });
    } catch (err) {
      setIconFeedback({
        type: "error",
        message: apiErrorMessage(err, "Upload failed."),
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
        apiErrorMessage(err, "Failed to delete workspace."),
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
      setRenameError("Name is required");
      return;
    }
    setIsRenaming(true);
    setRenameError(null);
    try {
      await updateWorkspaceMemberName(currentWorkspace.id, memberToRename.user_id, trimmed);
      setMemberToRename(null);
    } catch (err) {
      setRenameError(apiErrorMessage(err, "Failed to update name"));
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
      setRemoveError(apiErrorMessage(err, "Failed to remove member"));
    } finally {
      setIsRemoving(false);
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
      setExportError(apiErrorMessage(err, "Export failed"));
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
      setImportConfigError(apiErrorMessage(err, "Import failed"));
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
      setImportTeamError(apiErrorMessage(err, "Team import failed"));
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
      setTemplatesEditorError("Invalid JSON. Please fix the syntax and try again.");
      return;
    }
    setIsSavingTemplates(true);
    setTemplatesSaved(false);
    try {
      await saveWorkflowTemplates(currentWorkspace.id, parsed);
      setTemplatesSaved(true);
      setTimeout(() => setTemplatesSaved(false), 3000);
    } catch (err) {
      setTemplatesEditorError(apiErrorMessage(err, "Failed to save templates"));
    } finally {
      setIsSavingTemplates(false);
    }
  };

  if (!currentWorkspace) {
    return (
      <div className="mx-auto max-w-2xl space-y-6">
        <div className="flex items-center gap-3">
          <Settings className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-2xl font-bold tracking-tight">Workspace Settings</h1>
        </div>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            Workspace &quot;{wsSlug}&quot; not found
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Tab navigation */}
      <div className="flex gap-1 border-b border-border">
        {WS_TABS.map((tab) => (
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
      {/* Section 0: Profile */}
      {activeTab === "profile" && (
      <Card>
        <CardHeader>
          <CardTitle>Profile</CardTitle>
          <CardDescription>Your display name shown in comments, activity, and team directory.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveProfile} className="space-y-4">
            <div className="space-y-1.5">
              <label htmlFor="profile-name" className="text-sm font-medium">
                Display name <span className="text-destructive">*</span>
              </label>
              <Input
                id="profile-name"
                value={profileName}
                onChange={(e) => setProfileName(e.target.value)}
                placeholder="Your name"
                maxLength={100}
              />
              <p className="text-xs text-muted-foreground">
                Current email: {user?.email}
              </p>
            </div>
            <div className="space-y-1.5">
              <label htmlFor="profile-username" className="text-sm font-medium">
                Username
              </label>
              <Input
                id="profile-username"
                value={profileUsername}
                onChange={(e) => handleUsernameChange(e.target.value.toLowerCase())}
                placeholder="e.g. john-doe"
                maxLength={40}
                className={
                  usernameStatus === "taken" || usernameStatus === "invalid"
                    ? "border-destructive"
                    : usernameStatus === "available"
                      ? "border-green-500"
                      : ""
                }
              />
              {usernameStatus === "checking" && (
                <p className="text-xs text-muted-foreground">Checking availability…</p>
              )}
              {usernameStatus === "available" && (
                <p className="text-xs text-green-600">Username is available</p>
              )}
              {usernameStatus === "taken" && (
                <p className="text-xs text-destructive">Username is already taken</p>
              )}
              {usernameStatus === "invalid" && (
                <p className="text-xs text-destructive">2–40 characters: lowercase letters, digits, hyphens; no leading/trailing hyphens</p>
              )}
            </div>
            {profileFeedback && (
              <p
                className={cn(
                  "text-sm flex items-center gap-1",
                  profileFeedback.type === "success"
                    ? "text-green-600"
                    : "text-destructive",
                )}
              >
                {profileFeedback.type === "success" ? (
                  <Check className="h-3.5 w-3.5 shrink-0" />
                ) : (
                  <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                )}
                {profileFeedback.message}
              </p>
            )}
            <Button type="submit" size="sm" disabled={isSavingProfile}>
              <Save className="h-3.5 w-3.5" />
              {isSavingProfile ? "Saving..." : "Save profile"}
            </Button>
          </form>
        </CardContent>
      </Card>
      )}

      {/* Section 1: General */}
      {activeTab === "general" && (
      <Card>
        <CardHeader>
          <CardTitle>General</CardTitle>
          <CardDescription>Basic workspace information</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveGeneral} className="space-y-4">
            <div className="space-y-1.5">
              <label htmlFor="ws-name" className="text-sm font-medium">
                Workspace name <span className="text-destructive">*</span>
              </label>
              <Input
                id="ws-name"
                value={wsName}
                onChange={(e) => setWsName(e.target.value)}
                placeholder="My Workspace"
              />
            </div>
            <div className="space-y-1.5">
              <label htmlFor="ws-slug" className="text-sm font-medium">
                Slug
              </label>
              <Input
                id="ws-slug"
                value={currentWorkspace.slug}
                disabled
                className="bg-muted text-muted-foreground"
              />
              <p className="text-xs text-muted-foreground">
                Workspace slug is read-only and used in URLs.
              </p>
            </div>

            {generalFeedback && (
              <p
                className={
                  generalFeedback.type === "success"
                    ? "text-sm text-green-600"
                    : "text-sm text-destructive"
                }
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

      {/* Branding */}
      {activeTab === "general" && (
      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Branding</CardTitle>
          <CardDescription>Custom workspace icon shown in the sidebar and browser tab</CardDescription>
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
                {isUploadingIcon ? "Uploading…" : "Upload PNG icon"}
              </Button>
              <p className="text-xs text-muted-foreground">PNG only · max 500 KB</p>
              {iconFeedback && (
                <p
                  className={
                    iconFeedback.type === "success"
                      ? "text-sm text-green-600"
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

      {/* Danger Zone — owner/admin only. Member/viewer never see this section,
          even though the server would also refuse the request (PermDeleteWorkspace):
          a button that is guaranteed to 403 is a bad interface, not a second guard. */}
      {activeTab === "general" && canDeleteWorkspace && (
      <Card className="mt-4 border-destructive/50">
        <CardHeader>
          <CardTitle className="text-destructive">Danger Zone</CardTitle>
          <CardDescription>Irreversible actions for this workspace</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center justify-between rounded-lg border border-destructive/30 p-4">
            <div>
              <p className="text-sm font-medium">Delete Workspace</p>
              <p className="text-sm text-muted-foreground">
                Permanently delete this workspace, its projects, tasks, and
                comments for everyone in it. This action cannot be undone.
              </p>
            </div>
            <Button
              variant="destructive"
              onClick={() => setDeleteWorkspaceDialogOpen(true)}
            >
              Delete
            </Button>
          </div>
          {deleteWorkspaceError && (
            <p className="text-sm text-destructive">{deleteWorkspaceError}</p>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 2: Team Directory */}
      {activeTab === "team" && (
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Users className="h-4 w-4 text-muted-foreground" />
            <div className="space-y-1.5">
              <CardTitle>Team Directory</CardTitle>
              <CardDescription>
                All agents and humans in this workspace
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {isTeamLoading ? (
            <div className="space-y-3">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-10 w-full rounded-lg" />
              ))}
            </div>
          ) : !teamDirectory ||
            (teamDirectory.agents.length === 0 &&
              teamDirectory.humans.length === 0) ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No team members found. Agents appear here after registering.
            </p>
          ) : (
            <div className="space-y-4">
              {teamDirectory.agents.length > 0 && (
                <div className="space-y-2">
                  <div className="flex items-center gap-2">
                    <Bot className="h-4 w-4 text-muted-foreground" />
                    <h3 className="text-sm font-medium text-muted-foreground">
                      Agents ({teamDirectory.agents.length})
                    </h3>
                  </div>
                  <div className="space-y-1.5">
                    {teamDirectory.agents.map((agent) => (
                      <AgentRow key={agent.id} agent={agent} />
                    ))}
                  </div>
                </div>
              )}
              {teamDirectory.humans.length > 0 && (
                <div className="space-y-2">
                  <div className="flex items-center gap-2">
                    <Users className="h-4 w-4 text-muted-foreground" />
                    <h3 className="text-sm font-medium text-muted-foreground">
                      Humans ({teamDirectory.humans.length})
                    </h3>
                  </div>
                  <div className="space-y-1.5">
                    {teamDirectory.humans.map((human) => (
                      <HumanRow key={human.id} human={human} />
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </CardContent>
      </Card>
      )}

      {/* Section 3: Members */}
      {activeTab === "members" && (
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1.5">
              <CardTitle>Members</CardTitle>
              <CardDescription>
                Manage who has access to this workspace
              </CardDescription>
            </div>
            {canManageMembers && (
              <Button size="sm" onClick={() => setInviteDialogOpen(true)}>
                <Users className="h-4 w-4" />
                Invite Member
              </Button>
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
              No members found.
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
                    className="flex items-center gap-3 py-3 first:pt-0 last:pb-0"
                  >
                    {/* Avatar */}
                    <Avatar
                      src={member.user.avatar_url || undefined}
                      name={displayName(member.user)}
                      size="md"
                    />

                    {/* Info. Name first, address only when it disambiguates —
                        for an account that never got a name the two are the
                        same string, and printing it twice reads as a bug. */}
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-medium">
                          {displayName(member.user)}
                        </span>
                        {isMe && (
                          <span className="text-xs text-muted-foreground">(you)</span>
                        )}
                        <RoleIcon role={member.role} />
                      </div>
                      {isNamePlaceholder(member.user) ? (
                        <p className="truncate text-xs text-muted-foreground italic">
                          No name set
                          {canManageMembers && !isMe && " — click the pencil to add one"}
                        </p>
                      ) : (
                        <p className="truncate text-xs text-muted-foreground">
                          {member.user.email}
                        </p>
                      )}
                      <p className="text-xs text-muted-foreground">
                        Joined {formatDate(member.created_at)}
                      </p>
                    </div>

                    {/* Edit name. Own name is edited on the Profile tab, which
                        is also the only place it can be changed once its owner
                        has chosen it. */}
                    {canManageMembers && !isMe && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground hover:text-foreground"
                        onClick={() => handleOpenRename(member)}
                        title="Edit display name"
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
                            {opt.label}
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
                        title="Remove member"
                        disabled={isMe && isLastOwner}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    )}
                  </div>
                );
              })}
            </div>
          )}
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
              <CardTitle>Pending Invites</CardTitle>
              <CardDescription>
                Invite links sent but not yet accepted
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
                    {invite.role} · Expires {new Date(invite.expires_at).toLocaleDateString()}
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
                            error: apiErrorMessage(err, "Resend failed"),
                          });
                        }
                      }}
                    >
                      Resend
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive"
                      title="Revoke invite"
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
                      <p className="text-xs text-green-600 dark:text-green-400">
                        Invite email sent to {invite.email}
                      </p>
                    ) : (
                      <>
                        <p className="text-xs text-muted-foreground">
                          {resendResult.delivery?.delivery_status === "not_configured"
                            ? "No email server is configured — nothing was sent. Share this link instead:"
                            : "The invitation email could not be sent. Share this link instead:"}
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
              <CardTitle>Assignment Rules</CardTitle>
              <CardDescription>
                Workspace-level rules for automatically assigning tasks. Project-level rules can override these.
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
              Only admins and owners can manage assignment rules.
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
            <AlertTriangle className="h-4 w-4 text-amber-500" />
            <div className="space-y-1.5">
              <CardTitle>Rule Violations</CardTitle>
              <CardDescription>
                Recent rule violations across this workspace (last 20)
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
              No rule violations recorded. This workspace is compliant.
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
              <CardTitle>Workflow Templates</CardTitle>
              <CardDescription>
                Named workflow rule templates that can be applied to projects workspace-wide.
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
                            {tpl.statuses.length} statuses
                          </span>
                        )}
                        {tpl.transitions && (
                          <span className="text-xs text-muted-foreground shrink-0">
                            {Object.keys(tpl.transitions).length} transitions
                          </span>
                        )}
                      </button>
                      {expandedTemplate === name && (
                        <div className="border-t border-border px-4 py-3 bg-muted/30 space-y-2 text-sm">
                          {tpl.statuses && tpl.statuses.length > 0 && (
                            <div className="flex gap-2 flex-wrap">
                              <span className="text-muted-foreground w-28 shrink-0 text-xs">Statuses</span>
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
                              <span className="text-muted-foreground text-xs">Transitions</span>
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
                              <span className="text-muted-foreground text-xs">Policies</span>
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
                      Edit Templates (JSON)
                    </label>
                    <span className="text-xs text-muted-foreground">
                      Define named workflow templates as a JSON object
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
                      <span className="text-xs text-green-600 flex items-center gap-1">
                        <Check className="h-3.5 w-3.5" />
                        Templates saved
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
                        {isSavingTemplates ? "Saving..." : "Save Templates"}
                      </Button>
                    </div>
                  </div>
                </div>
              )}

              {Object.keys(workflowTemplates).length === 0 && !canManageRules && (
                <p className="py-4 text-center text-sm text-muted-foreground">
                  No workflow templates configured for this workspace.
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
              <CardTitle>Config Import / Export</CardTitle>
              <CardDescription>
                Export workspace configuration as YAML or import from a YAML file to bulk-update rules and templates.
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-6">
          {/* Export */}
          <div className="space-y-2">
            <h3 className="text-sm font-medium flex items-center gap-1.5">
              <Download className="h-3.5 w-3.5 text-muted-foreground" />
              Export Config
            </h3>
            <p className="text-xs text-muted-foreground">
              Download the full workspace configuration (assignment rules, workflow templates) as a YAML file.
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
              {isExporting ? "Exporting..." : "Download mesh-config.yaml"}
            </Button>
          </div>

          <Separator />

          {/* Import Config */}
          {canManageRules && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium flex items-center gap-1.5">
                <Upload className="h-3.5 w-3.5 text-muted-foreground" />
                Import Config
              </h3>
              <p className="text-xs text-muted-foreground">
                Upload a YAML config file or paste YAML directly. This will update assignment rules, workflow templates, and optionally team members.
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
                  Choose YAML file
                </Button>
                {configYamlText && (
                  <span className="text-xs text-muted-foreground">
                    File loaded ({configYamlText.length} chars)
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
                placeholder="Or paste YAML config here..."
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
                  <p className="font-medium text-green-700 dark:text-green-400 flex items-center gap-1">
                    <Check className="h-3.5 w-3.5" />
                    Import successful
                  </p>
                  {importConfigResult.team && (
                    <p className="text-muted-foreground">
                      Team: {importConfigResult.team.agents_updated} agents, {importConfigResult.team.humans_updated} humans updated
                      {importConfigResult.team.errors && importConfigResult.team.errors.length > 0 && (
                        <span className="text-amber-600"> ({importConfigResult.team.errors.length} errors)</span>
                      )}
                    </p>
                  )}
                  {importConfigResult.assignment_rules && (
                    <p className="text-muted-foreground">
                      Assignment rules: {importConfigResult.assignment_rules.updated ? "updated" : "no changes"}
                    </p>
                  )}
                  {importConfigResult.workflow_templates && (
                    <p className="text-muted-foreground">
                      Workflow templates: {importConfigResult.workflow_templates.created} created, {importConfigResult.workflow_templates.updated} updated
                    </p>
                  )}
                  {importConfigResult.warnings.length > 0 && (
                    <div className="space-y-0.5">
                      <p className="text-amber-600 flex items-center gap-1">
                        <AlertTriangle className="h-3 w-3 shrink-0" />
                        {importConfigResult.warnings.length} warning(s):
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
                  {isImportingConfig ? "Importing..." : "Import Config"}
                </Button>
              </div>
            </div>
          )}

          <Separator />

          {/* Secrets — write-only store. Rendered only for owner/admin: the API
              gates every one of its four routes on PermManageSecrets, so for
              anyone else this section would be a form whose every submit 403s.
              The check here is a courtesy, not the guard. */}
          {canManageSecrets && currentWorkspace && (
            <>
              <WorkspaceSecrets
                workspaceId={currentWorkspace.id}
                canManage={canManageSecrets}
              />
              <Separator />
            </>
          )}

          {/* Import Team */}
          {canManageRules && (
            <div className="space-y-3">
              <h3 className="text-sm font-medium flex items-center gap-1.5">
                <Users className="h-3.5 w-3.5 text-muted-foreground" />
                Import Team
              </h3>
              <p className="text-xs text-muted-foreground">
                Import team directory only (agents and human members) from a YAML file. Does not affect rules or templates.
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
                  Choose YAML file
                </Button>
                {teamYamlText && (
                  <span className="text-xs text-muted-foreground">
                    File loaded ({teamYamlText.length} chars)
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
                placeholder="Or paste team YAML here..."
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
                  <p className="font-medium text-green-700 dark:text-green-400 flex items-center gap-1">
                    <Check className="h-3.5 w-3.5" />
                    Team import successful
                  </p>
                  <p className="text-muted-foreground">
                    {importTeamResult.agents_updated} agents, {importTeamResult.humans_updated} humans updated
                  </p>
                  {importTeamResult.errors && importTeamResult.errors.length > 0 && (
                    <div className="space-y-0.5">
                      <p className="text-amber-600">Errors:</p>
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
                  {isImportingTeam ? "Importing..." : "Import Team"}
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
        title="Remove Member"
        description={
          memberToRemove
            ? `Are you sure you want to remove ${displayName(memberToRemove.user)} (${memberToRemove.user.email}) from this workspace?`
            : ""
        }
        confirmText="Remove Member"
        variant="destructive"
        isLoading={isRemoving}
      />

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
            <DialogTitle>Edit display name</DialogTitle>
            <DialogDescription>
              {memberToRename
                ? `How ${memberToRename.user.email} appears on tasks, comments and mentions.`
                : ""}
            </DialogDescription>
          </DialogHeader>

          <form
            className="mt-4 space-y-4"
            onSubmit={(e) => { e.preventDefault(); void handleConfirmRename(); }}
          >
            <div className="space-y-1.5">
              <label htmlFor="ws-rename" className="text-sm font-medium">
                Name <span className="text-destructive">*</span>
              </label>
              <Input
                id="ws-rename"
                value={renameValue}
                onChange={(e) => setRenameValue(e.target.value)}
                placeholder="Jane Cooper"
                autoFocus
                maxLength={100}
              />
              <p className="text-xs text-muted-foreground">
                They can change it themselves at any time, and once they do this
                becomes theirs to edit only.
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
                Cancel
              </Button>
              <Button type="submit" disabled={isRenaming}>
                {isRenaming ? "Saving…" : "Save name"}
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
        title="Delete Workspace"
        description={`This will permanently delete "${currentWorkspace.name}" — its projects, tasks, and comments — for everyone in it. This action cannot be undone.`}
        confirmText="Delete Workspace"
        variant="destructive"
        requireText={currentWorkspace.name}
        isLoading={isDeletingWorkspace}
      />
      </div>
    </div>
  );
}
