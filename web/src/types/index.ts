// Enums

export type AssigneeType = "user" | "agent" | "unassigned";
export type Priority = "urgent" | "high" | "medium" | "low" | "none";
export type DelegationLevel = "review" | "auto" | "supervised";
export type ActorType = "user" | "agent" | "system";
export type StatusCategory =
  | "backlog"
  | "triage"
  | "todo"
  | "in_progress"
  | "review"
  | "done"
  | "cancelled";
export type AgentType =
  | "claude_code"
  | "openclaw"
  | "cline"
  | "aider"
  | "custom"
  | "hermes";
export type AgentStatus = "online" | "offline" | "busy" | "error";
export type DependencyType = "blocks" | "relates_to" | "is_child_of";
export type ArtifactType =
  | "file"
  | "code"
  | "log"
  | "report"
  | "link"
  | "image"
  | "data";
export type EventType =
  | "summary"
  | "status_change"
  | "context_update"
  | "error"
  | "dependency_resolved"
  | "custom";
export type WorkspaceRole = "owner" | "admin" | "member" | "viewer";
export type ProjectRole = "admin" | "member" | "viewer";
export type DefaultAssigneeType = "user" | "agent" | "none";

// Custom field types
export type FieldType =
  | "text"
  | "number"
  | "date"
  | "datetime"
  | "select"
  | "multiselect"
  | "url"
  | "email"
  | "checkbox"
  | "user_ref"
  | "agent_ref"
  | "json";

export interface CustomFieldDefinition {
  id: string;
  project_id: string;
  name: string;
  slug: string;
  field_type: FieldType;
  description: string;
  options: Record<string, unknown>;
  default_value: unknown;
  is_required: boolean;
  is_visible_to_agents: boolean;
  position: number;
  created_at: string;
}

export interface CreateCustomFieldRequest {
  name: string;
  slug: string;
  field_type: FieldType;
  description?: string;
  options?: Record<string, unknown>;
  default_value?: unknown;
  is_required?: boolean;
  is_visible_to_agents?: boolean;
}

// Core domain types

export interface User {
  id: string;
  email: string;
  name: string;
  username?: string;
  avatar_url: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface Workspace {
  id: string;
  name: string;
  slug: string;
  owner_id: string;
  settings: Record<string, unknown>;
  billing_plan_id: string;
  billing_customer_id: string;
  icon_url: string | null;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceMember {
  id: string;
  workspace_id: string;
  user_id: string;
  role: WorkspaceRole;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceMemberWithUser {
  id: string;
  workspace_id: string;
  user_id: string;
  role: WorkspaceRole;
  invited_by: string | null;
  created_at: string;
  updated_at: string;
  user: {
    id: string;
    email: string;
    name: string;
    /** Stable secondary identifier, so telling two people apart does not
     *  require printing an address. */
    username?: string;
    avatar_url: string;
  };
}

export interface ProjectMember {
  id: string;
  project_id: string;
  user_id?: string;
  agent_id?: string;
  role: ProjectRole;
  created_at: string;
  updated_at: string;
}

export interface ProjectMemberWithUser extends ProjectMember {
  user?: {
    id: string;
    email: string;
    name: string;
    avatar_url: string;
  };
  agent_name?: string;
  agent_role?: string;
  agent_description?: string;
}

export interface UserSearchResult {
  id: string;
  email: string;
  name: string;
  username?: string;
  avatar_url: string;
  is_member: boolean;
}

export interface WorkspaceInvite {
  id: string;
  workspace_id: string;
  email: string;
  role: WorkspaceRole;
  token: string;
  invited_by: string | null;
  expires_at: string;
  accepted_at: string | null;
  created_at: string;
}

/**
 * What became of the invitation email.
 *
 * "not_configured" is the normal state of a self-hosted instance with no SMTP
 * server: the invite is valid and its link works, there is simply nothing to
 * send it with. Treat it as a handover — show the link — not as an error.
 */
export type InviteDeliveryStatus = "sent" | "not_configured" | "failed";

export interface InviteDelivery {
  /** True only when an email actually went out. Never assume it from the 201. */
  email_sent: boolean;
  delivery_status: InviteDeliveryStatus;
  /** The accept link. Always present, whatever happened to the email. */
  invite_url: string;
  /** Present only when delivery_status is "failed". */
  delivery_error?: string;
}

export type CreateInviteResponse = WorkspaceInvite & InviteDelivery;

export interface Project {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  slug: string;
  icon: string;
  settings: Record<string, unknown>;
  default_assignee_type: DefaultAssigneeType;
  is_archived: boolean;
  created_at: string;
  updated_at: string;
}

export interface TaskStatus {
  id: string;
  project_id: string;
  name: string;
  slug: string;
  color: string;
  position: number;
  category: StatusCategory;
  is_default: boolean;
  auto_transition: Record<string, unknown>;
}

// DodCheckStatus is the state of a single Definition-of-Done gate check.
export type DodCheckStatus = "pending" | "pass" | "fail";

// DodCheck holds the state of a single named DoD gate as reported by an external caller.
export interface DodCheck {
  status: DodCheckStatus;
  updated_at: string;
  reporter?: string;
}

// DodGateConfig defines a named gate entry in a project's dod_gates settings.
export interface DodGateConfig {
  name: string;
  required: boolean;
}

export interface Task {
  id: string;
  project_id: string;
  status_id: string;
  title: string;
  /**
   * Optional because list responses can be asked to omit it. The board fetches
   * tasks with include_description=false — descriptions were 77% of that payload
   * and no card renders one. `undefined` here means "not sent", not "empty";
   * read has_description for the flag, and GET /tasks/:id for the text.
   */
  description?: string;
  /** Always sent on list responses. True when the task has non-blank description text. */
  has_description?: boolean;
  assignee_id: string | null;
  assignee_type: AssigneeType;
  assignee_name?: string | null;
  reviewer_id?: string | null;
  reviewer_type?: AssigneeType | null;
  reviewer_name?: string | null;
  created_by_name?: string | null;
  priority: Priority;
  delegation_level?: DelegationLevel;
  parent_task_id: string | null;
  position: number;
  due_date: string | null;
  estimated_hours: number | null;
  custom_fields: Record<string, unknown> | null;
  labels: string[] | null;
  created_by: string;
  created_by_type: ActorType;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
  subtask_count?: number;
  artifact_count?: number;
  vcs_link_count?: number;
  url?: string;
  recurring_schedule_id?: string | null;
  recurring_instance_number?: number | null;
  dod_checks?: Record<string, DodCheck>;
  /**
   * Who currently holds the exclusive work-lock, and until when. Both are
   * omitted by the server when the task is not checked out. A checkout is what
   * "an agent has this in their hands right now" actually means — the status
   * column alone cannot say it, which is why the card renders it separately.
   */
  checked_out_by?: string | null;
  checkout_expires?: string | null;
  /**
   * Sticky flag set when a human sign-off is required — always sent (never
   * omitted) by both list and detail responses. See human_gate_class for
   * hard/soft, and human_gate_info for who/since (detail responses only).
   */
  human_gate: boolean;
  human_gate_class?: "hard" | "soft";
  human_gate_armed_at?: string | null;
  /** Populated only by GET /tasks/:id when human_gate is true — not on list rows. */
  human_gate_info?: HumanGateInfo | null;
  /**
   * The "false-open" graph signal (task #c80fe88f): sent on both list and
   * detail responses for any parent task (subtask_count > 0) whose own
   * status is still open, regardless of whether either flag has actually
   * fired yet — the board decides what to render, the server always reports
   * the raw state. Absent for leaf tasks and for tasks whose own status is
   * already done/cancelled.
   */
  false_open?: FalseOpenSignal | null;
}

export interface HumanGateInfo {
  gated: boolean;
  owner_agent_id?: string | null;
  owner_name?: string;
  marker_comment_id?: string | null;
  marker_created_at?: string | null;
  clearable_by_owner: boolean;
  reason_if_not?: string;
}

/**
 * See internal/domain/task.go FalseOpenSignal. Two mutually exclusive flags
 * on purpose, not one merged bool — #65dc5949 (one open BACKLOG child) must
 * light up only_parked_children_left, never all_children_closed, or the
 * signal equally mislabels umbrellas correctly blocked on a live parked
 * dependency.
 */
export interface FalseOpenSignal {
  all_children_closed: boolean;
  only_parked_children_left: boolean;
  open_children_count: number;
  stale_days: number;
}

export interface Comment {
  id: string;
  task_id: string;
  parent_comment_id: string | null;
  author_id: string;
  author_type: ActorType;
  author_name?: string;
  body: string;
  metadata: Record<string, unknown>;
  is_internal: boolean;
  created_at: string;
  updated_at: string;
  /**
   * What became of each @-addressed handle on this comment. Absent when the
   * comment addressed nobody — which is why it is optional rather than an
   * empty array: "no handles" and "handles that all failed" must not render
   * the same way.
   */
  delivery?: CommentDeliveryOutcome[];
}

/**
 * One verdict per @-addressed handle. `reason` is never empty, including on
 * delivered rows, where it names which path carried the comment.
 */
export interface CommentDeliveryOutcome {
  comment_id: string;
  recipient_slug: string;
  recipient_id?: string;
  recipient_kind: "agent" | "user" | "unknown";
  outcome: "delivered" | "skipped" | "failed";
  reason: string;
  channel: string;
  recipient_presence: string;
  decided_at: string;
}

export interface CommentView {
  comment_id: string;
  task_id: string;
  task_title: string;
  project_id: string;
  project_name: string;
  comment_body: string;
  author_id: string;
  author_name: string;
  author_kind: string;
  is_internal: boolean;
  created_at: string;
  updated_at: string;
}

export interface CommentViewPage {
  items: CommentView[];
  next_cursor: string | null;
  // Tie-breaker paired with next_cursor: created_at alone is not unique (a
  // page boundary landing inside a group of same-timestamp comments would
  // otherwise silently drop the rest of the group — #c6dc694e). Echo both
  // back as before/before_id on the next "Load more" request.
  next_cursor_id: string | null;
}

export interface TaskDependency {
  id: string;
  task_id: string;
  depends_on_task_id: string;
  dependency_type: DependencyType;
  created_at: string;
  related_task_title?: string;
  related_task_status_id?: string;
}

export interface TaskDependencyList {
  outgoing: TaskDependency[];
  incoming: TaskDependency[];
}

export interface Agent {
  id: string;
  workspace_id: string;
  parent_agent_id?: string | null;
  supervisor_user_id?: string | null;
  name: string;
  agent_type: AgentType;
  status: AgentStatus;
  role?: string;
  capabilities: string[];
  metadata: Record<string, unknown>;
  last_heartbeat: string | null;
  heartbeat_status?: string;
  heartbeat_message?: string;
  heartbeat_metadata?: Record<string, unknown>;
  current_task_id?: string | null;
  total_tasks_completed?: number;
  total_errors?: number;
  profile_description?: string;
  callback_url?: string;
  created_at: string;
  updated_at: string;
}

// A markdown page inside a project. Named ProjectDocument rather than Document
// because `Document` is a DOM global: a type by that name shadows it inside any
// file that imports it, and `document.querySelector` then type-errors somewhere
// unrelated.
//
// `body` is absent from the list response — the API fills it in only for the
// single-document read (GET /documents/:id), so treat it as optional everywhere.
export interface ProjectDocument {
  id: string;
  project_id: string;
  parent_id: string | null;
  slug: string;
  title: string;
  storage_key: string;
  position: number;
  created_by: string;
  created_by_type: ActorType;
  // Actor names and the last editor are being added to the document responses
  // by a separate change. Optional here so the page that shows them renders
  // against today's API too, and starts naming the editor the moment the field
  // appears — same treatment Task gives created_by_name.
  created_by_name?: string | null;
  updated_by?: string | null;
  updated_by_name?: string | null;
  created_at: string;
  updated_at: string;
  deleted_at?: string | null;
  body?: string;
  // Monotonic counter bumped by every write to title or body. The value a
  // caller read is what it sends back as base_version on the next write —
  // see UpdateDocumentRequest.base_version.
  version: number;
}

export interface CreateDocumentRequest {
  title: string;
  slug?: string;
  parent_id?: string | null;
  position?: number;
  body?: string;
}

export interface UpdateDocumentRequest {
  title?: string;
  parent_id?: string;
  // The API cannot read "move to the root" from parent_id: null — a null in the
  // JSON is indistinguishable from an omitted field once bound into *uuid.UUID.
  // clear_parent is the backend's explicit spelling for it.
  clear_parent?: boolean;
  position?: number;
  body?: string;
  // The version last read from the server. Omitted, the API writes
  // unconditionally; sent and stale, it 409s with document_version_conflict
  // instead of silently overwriting a change that landed in between.
  base_version?: number;
}

// A comment anchored to a run of a document's text — the W3C Web Annotation
// selector pair, the same shape Hypothesis stores.
//
// `start`/`end` are **byte** offsets into the document's markdown, half-open,
// and are NOT JavaScript string indices. Convert with lib/doc-comments/offsets.
//
// Three states are legal, and the server enforces them:
//   - no quote            -> not anchored (a page-level comment, or a reply)
//   - quote + offsets     -> anchored
//   - quote, offsets null -> orphaned: we know what it was about, not where
//
// `orphaned` is computed by the server from whether the offsets are present. It
// is never sent on a write.
export interface DocumentCommentAnchor {
  exact: string;
  prefix: string;
  suffix: string;
  start: number | null;
  end: number | null;
  orphaned?: boolean;
}

export interface DocumentComment {
  id: string;
  document_id: string;
  parent_comment_id: string | null;
  author_id: string;
  author_type: ActorType;
  author_name?: string | null;
  body: string;
  /** Null when the comment was never anchored. A reply never carries one. */
  anchor: DocumentCommentAnchor | null;
  resolved_at: string | null;
  resolved_by: string | null;
  resolved_by_type: ActorType | null;
  resolved_by_name?: string | null;
  created_at: string;
  updated_at: string;
}

export interface CreateDocumentCommentRequest {
  body: string;
  /** Set only on a reply. The server refuses a reply to a reply. */
  parent_comment_id?: string;
  /**
   * Omitted for a page-level comment, and forbidden on a reply — a reply
   * inherits its parent's anchor and the server answers 400 if it carries one.
   */
  anchor?: Omit<DocumentCommentAnchor, "orphaned">;
}

export interface Artifact {
  id: string;
  task_id: string;
  name: string;
  artifact_type: ArtifactType;
  mime_type: string;
  storage_key: string;
  storage_url: string;
  size_bytes: number;
  checksum_sha256: string;
  metadata: Record<string, unknown>;
  uploaded_by: string;
  uploaded_by_type: "user" | "agent";
  created_at: string;
}

export interface ActivityLog {
  id: string;
  workspace_id: string;
  entity_type: string;
  entity_id: string;
  action: string;
  actor_id: string;
  actor_type: ActorType;
  changes: Record<string, unknown>;
  created_at: string;
}

// API response types

// The refresh token never appears here — it travels only in the httpOnly
// cookie the server sets alongside this response.
export interface TokenPair {
  access_token: string;
  expires_in: number;
}

export interface AuthResponse {
  user: User;
  tokens: TokenPair;
}

export interface RefreshResponse {
  tokens: TokenPair;
}

export interface AuthConfig {
  registration_enabled: boolean;
}

export interface PaginatedResponse<T> {
  items: T[];
  total: number;
  total_count: number;
  page: number;
  per_page: number;
  page_size: number;
  has_more: boolean;
}

export interface Mention {
  comment_id: string;
  mentioned_id: string;
  mentioned_kind: string;
  mentioned_slug: string;
  extracted_at: string;
  seen_at: string | null;
  task_id: string;
  task_title: string;
  project_id: string;
  comment_body: string;
  author_id: string;
  author_name: string;
}

/**
 * An @-mention inside a document comment — the shape of GET /me/document-mentions.
 *
 * A sibling of Mention rather than a widened version of it: this one names a
 * document and no task, and folding the two into one struct with a nullable
 * `task_id` would push "which of the two am I holding" onto every reader.
 * The union that lets a screen show both is MentionInboxItem
 * (`@/lib/mentions/inbox`).
 */
export interface DocumentMention {
  comment_id: string;
  mentioned_id: string;
  mentioned_kind: string;
  mentioned_slug: string;
  extracted_at: string;
  seen_at: string | null;
  document_id: string;
  document_title: string;
  document_slug: string;
  project_id: string;
  comment_body: string;
  author_id: string;
  author_name: string;
}

export interface UnseenCountResponse {
  count: number;
}

export interface Mentionable {
  id: string;
  kind: "agent" | "user";
  slug: string;
  display_name: string;
  avatar_url: string | null;
}

// API request types

export interface LoginRequest {
  email: string;
  password: string;
}

export interface RegisterRequest {
  email: string;
  password: string;
  name: string;
}

export interface CreateWorkspaceRequest {
  name: string;
  slug: string;
  settings?: Record<string, unknown>;
}

export interface CreateProjectRequest {
  name: string;
  slug: string;
  description?: string;
  icon?: string;
  settings?: Record<string, unknown>;
}

export interface CreateTaskRequest {
  title: string;
  description?: string;
  priority?: Priority;
  delegation_level?: DelegationLevel;
  assignee_id?: string;
  assignee_type?: AssigneeType;
  reviewer_id?: string;
  reviewer_type?: AssigneeType;
  labels?: string[];
  custom_fields?: Record<string, unknown>;
  due_date?: string | null;
  estimated_hours?: number | null;
  status_id?: string;
}

export interface UpdateTaskRequest {
  title?: string;
  description?: string;
  priority?: Priority;
  delegation_level?: DelegationLevel;
  assignee_id?: string | null;
  assignee_type?: AssigneeType;
  reviewer_id?: string | null;
  reviewer_type?: AssigneeType | null;
  // clear_reviewer explicitly clears the reviewer — reviewer_id:null alone is
  // indistinguishable from "not provided" once JSON-decoded on the Go side.
  clear_reviewer?: boolean;
  labels?: string[];
  status_id?: string;
  due_date?: string | null;
  estimated_hours?: number | null;
  custom_fields?: Record<string, unknown>;
}

export interface MoveTaskRequest {
  status_id?: string;
  position?: number;
}

export interface CreateStatusRequest {
  name: string;
  slug: string;
  color: string;
  category: StatusCategory;
  position?: number;
  is_default?: boolean;
}

export interface CreateCommentRequest {
  body: string;
  parent_comment_id?: string;
  is_internal?: boolean;
}

export interface RegisterAgentRequest {
  name: string;
  agent_type: AgentType;
  capabilities?: Record<string, unknown>;
}

export interface RegisterAgentResponse {
  agent: Agent;
  api_key: string; // Only returned once at registration
}

// Spark catalog types

export interface SparkAgentManifest {
  id: string;
  slug: string;
  name: string;
  description: string;
  agent_type: AgentType | string;
  version: string;
  author: string;
  capabilities: Record<string, unknown>;
  config: Record<string, unknown>;
  tags: string[];
  downloads: number;
  rating: number;
  created_at: string;
}

export interface SparkInstallResponse {
  agent: Agent;
  api_key: string;
  spark: {
    id: string;
    version: string;
    author: string;
  };
}

// Saved view types

export type ViewType = "board" | "list" | "timeline" | "calendar";

/**
 * Tabs reachable from the project view strip.
 *
 * Superset of ViewType on purpose: Docs is a navigation destination, not a
 * savable view. The server's saved-view enum (internal/service/
 * saved_view_service.go) only accepts board/list/timeline/calendar, so widening
 * ViewType itself would let the UI POST a view_type the API rejects.
 */
export type ProjectViewTab = ViewType | "docs";

export interface SavedView {
  id: string;
  project_id: string;
  name: string;
  view_type: ViewType;
  filters: Record<string, unknown>;
  sort_by: string | null;
  sort_order: string | null;
  columns: string[] | null;
  is_shared: boolean;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface CreateSavedViewRequest {
  name: string;
  view_type?: ViewType;
  filters?: Record<string, unknown>;
  sort_by?: string;
  sort_order?: string;
  columns?: string[];
  is_shared?: boolean;
}

export interface UpdateSavedViewRequest {
  name?: string;
  view_type?: ViewType;
  filters?: Record<string, unknown>;
  sort_by?: string;
  sort_order?: string;
  columns?: string[];
  is_shared?: boolean;
}

// Project update types

export type UpdateStatus = "on_track" | "at_risk" | "off_track" | "completed";

export interface TextItem {
  text: string;
}

export interface ProjectUpdateMetrics {
  tasks_completed: number;
  tasks_total: number;
  tasks_in_progress: number;
}

export interface ProjectUpdate {
  id: string;
  project_id: string;
  title: string;
  status: UpdateStatus;
  summary: string;
  highlights: TextItem[];
  blockers: TextItem[];
  next_steps: TextItem[];
  metrics: ProjectUpdateMetrics;
  created_by: string;
  created_at: string;
}

export interface CreateProjectUpdateRequest {
  title: string;
  status?: UpdateStatus;
  summary: string;
  highlights?: TextItem[];
  blockers?: TextItem[];
  next_steps?: TextItem[];
}

// Initiative types

export type InitiativeStatus = "active" | "completed" | "archived";

export interface Initiative {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  status: InitiativeStatus;
  target_date: string | null;
  created_by: string;
  created_at: string;
  updated_at: string;
  total_tasks?: number;
  completed_tasks?: number;
  linked_projects?: Project[];
}

export interface CreateInitiativeRequest {
  name: string;
  description?: string;
  status?: InitiativeStatus;
  target_date?: string | null;
}

export interface UpdateInitiativeRequest {
  name?: string;
  description?: string;
  status?: InitiativeStatus;
  target_date?: string | null;
}

// Team Directory
export interface TeamDirectoryAgent {
  id: string;
  name: string;
  slug: string;
  status: string;
  agent_type: string;
  parent_agent_id?: string | null;
  supervisor_user_id?: string | null;
  role: string;
  capabilities: string[];
  responsibility_zone: string;
  escalation_to: unknown;
  accepts_from: string[];
  max_concurrent_tasks: number;
  working_hours: string;
  profile_description: string;
  current_tasks: number;
  projects: string[];
  last_heartbeat?: string | null;
  heartbeat_status?: string;
  heartbeat_message?: string;
  is_stale?: boolean;
}

export interface TeamDirectoryHuman {
  id: string;
  name: string;
  /** The @-handle. What the mention renderers match an `@word` against. */
  username: string;
  email: string;
  avatar_url: string;
  role: string;
  capabilities: string[];
  responsibility_zone: string;
  availability: string;
  projects: string[];
}

export interface TeamDirectory {
  workspace: string;
  agents: TeamDirectoryAgent[];
  humans: TeamDirectoryHuman[];
}

// Org Chart tree types
export interface OrgChartAgentNode extends TeamDirectoryAgent {
  children: OrgChartAgentNode[];
}

export interface OrgChartData {
  workspace: string;
  agent_tree: OrgChartAgentNode[];
  humans: TeamDirectoryHuman[];
}

// Rules
export interface AssignmentRulesConfig {
  default_assignee?: string;
  by_type?: Record<string, string>;
  by_priority?: Record<string, string>;
  fallback_chain?: string[];
}

export interface EffectiveAssignmentRule {
  value: string;
  source: string;
}

export interface EffectiveAssignmentRules {
  default_assignee?: EffectiveAssignmentRule;
  by_type?: Record<string, EffectiveAssignmentRule>;
  by_priority?: Record<string, EffectiveAssignmentRule>;
  fallback_chain?: string[];
}

export interface TransitionRule {
  allowed: string[];
  description?: string;
  on_transition?: { auto_assign?: boolean; set_reviewer?: string; notify?: string[] };
  requires?: { approval?: boolean };
}

export interface WorkflowRulesConfig {
  statuses?: string[];
  transitions?: Record<string, TransitionRule>;
  enforcement_mode?: string;
  policies?: Record<string, { allowed: string[] }>;
}

export interface WorkflowRulesResponse extends WorkflowRulesConfig {
  my_permissions?: {
    my_role: string;
    my_name: string;
    can_transition: Record<string, boolean>;
    can_create_tasks: boolean;
    can_delete_tasks: boolean;
    can_reassign: boolean;
  };
}

export interface RuleViolation {
  id: string;
  workspace_id: string;
  project_id?: string;
  actor_id: string;
  actor_type: string;
  rule_type: string;
  violation_detail: unknown;
  action_taken: string;
  created_at: string;
}

// Config Import/Export types

export interface ImportResult {
  team?: { agents_updated: number; humans_updated: number; errors?: string[] };
  assignment_rules?: { updated: boolean };
  workflow_templates?: { created: number; updated: number };
  warnings: string[];
}

export interface TeamImportResult {
  agents_updated: number;
  humans_updated: number;
  errors?: string[];
}

// Notification types

export interface Notification {
  id: string;
  workspace_id: string;
  user_id: string | null;
  event_type: string;
  title: string;
  body: string;
  metadata: Record<string, unknown>;
  is_read: boolean;
  created_at: string;
}

export interface NotificationPreference {
  id: string;
  workspace_id: string;
  user_id: string | null;
  agent_id: string | null;
  channel: string;
  events: string[];
  is_enabled: boolean;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface NotificationListResponse {
  items: Notification[];
  unread_count: number;
}

export interface UpdateNotificationPreferencesRequest {
  workspace_id: string;
  channel?: string;
  events?: string[];
  is_enabled?: boolean;
  config?: Record<string, string>;
}

// API error type
export interface ApiError {
  error?: string;
  message?: string;
  code: string | number;
  details?: Record<string, string>;
  validation?: Record<string, string>;
}

// WebSocket / EventBus types

export interface EventBusMessage {
  id: string;
  workspace_id: string;
  project_id: string;
  task_id: string | null;
  agent_id: string | null;
  event_type: EventType;
  subject: string;
  payload: Record<string, unknown>;
  tags: string[];
  ttl: string;
  created_at: string;
  expires_at: string | null;
  // Enriched fields — populated by the list endpoint via LEFT JOINs.
  task_title?: string | null;
  project_name?: string | null;
  actor_name?: string | null;
}

export interface WSMessage {
  type: string;
  channel: string;
  data: Record<string, unknown>;
  timestamp: string;
}

// VCS Link types

export type VCSProvider = "github" | "gitlab";
export type VCSLinkType = "pr" | "commit" | "branch";
export type VCSLinkStatus = "open" | "merged" | "closed";

export interface VCSLink {
  id: string;
  task_id: string;
  provider: VCSProvider;
  link_type: VCSLinkType;
  external_id: string;
  url: string;
  title: string;
  status: VCSLinkStatus | "";
  metadata: Record<string, unknown>;
  created_at: string;
}

export interface CreateVCSLinkRequest {
  provider?: VCSProvider;
  link_type: VCSLinkType;
  external_id: string;
  url: string;
  title?: string;
  status?: VCSLinkStatus;
}

// Integration types

export type IntegrationProvider = "slack" | "github" | "spark" | "mcp" | "telegram";

export interface IntegrationConfig {
  id: string;
  workspace_id: string;
  provider: IntegrationProvider;
  config: Record<string, unknown>;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

// A telegram IntegrationConfig's `config` is always this shape — the API
// never returns bot_token, encrypted or not, only whether one is set.
export interface TelegramIntegrationConfig {
  bot_username?: string;
  bot_token_set?: boolean;
}

// A telegram-channel NotificationPreference's `config` is always this shape.
export interface TelegramPreferenceConfig {
  telegram_username?: string;
  telegram_chat_id?: number;
  telegram_bind_token?: string;
  telegram_bind_expires_at?: string;
}

export interface TelegramBotInfo {
  /** A bot is configured for this workspace and its token decrypts. */
  available: boolean;
  bot_username: string;
  /**
   * This server could actually reach the Telegram Bot API just now. Distinct
   * from `available`, which only says a bot is on file: a configured bot on a
   * host with no outbound route to api.telegram.org looks perfectly healthy
   * and delivers nothing.
   */
  reachable: boolean;
  /** Why the channel cannot deliver, ready to show. Empty when it can. */
  unavailable_reason: string;
}

// Recurring tasks types

export type RecurringFrequency = "daily" | "weekly" | "monthly" | "custom";

export interface RecurringSchedule {
  id: string;
  workspace_id: string;
  project_id: string;
  title_template: string;
  description_template: string;
  frequency: RecurringFrequency;
  cron_expr: string;
  timezone: string;
  assignee_id: string | null;
  assignee_type: AssigneeType;
  priority: Priority;
  labels: string[];
  status_id: string | null;
  is_active: boolean;
  starts_at: string;
  ends_at: string | null;
  max_instances: number | null;
  next_run_at: string | null;
  last_triggered_at: string | null;
  instance_count: number;
  created_by: string;
  created_by_type: ActorType;
  created_at: string;
  updated_at: string;
}

export interface RecurringInstanceSummary {
  task_id: string;
  instance_number: number;
  title: string;
  status_category: StatusCategory;
  completed_at: string | null;
  created_at: string;
  last_comment: string | null;
  artifact_count: number;
}

export interface CreateRecurringRequest {
  title_template: string;
  description_template?: string;
  frequency: RecurringFrequency;
  cron_expr?: string;
  timezone?: string;
  assignee_id?: string;
  assignee_type?: AssigneeType;
  priority?: Priority;
  labels?: string[];
  is_active?: boolean;
  starts_at?: string;
  ends_at?: string;
  max_instances?: number;
}

export interface UpdateRecurringRequest {
  title_template?: string;
  description_template?: string;
  frequency?: RecurringFrequency;
  cron_expr?: string;
  timezone?: string;
  assignee_id?: string | null;
  assignee_type?: AssigneeType;
  priority?: Priority;
  labels?: string[];
  is_active?: boolean;
  starts_at?: string;
  ends_at?: string | null;
  max_instances?: number | null;
}

export interface TriggerRecurringResponse {
  task: Task;
  instance_number: number;
}

// Task template types

export interface TaskTemplate {
  id: string;
  project_id: string;
  name: string;
  description: string;
  title_template: string;
  description_template: string;
  priority: Priority;
  labels: string[];
  estimated_hours: number | null;
  custom_fields: Record<string, unknown> | null;
  assignee_id: string | null;
  assignee_type: AssigneeType | null;
  status_id: string | null;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface CreateTemplateRequest {
  name: string;
  description?: string;
  title_template?: string;
  description_template?: string;
  priority?: Priority;
  labels?: string[];
  estimated_hours?: number | null;
  custom_fields?: Record<string, unknown>;
  assignee_id?: string | null;
  assignee_type?: AssigneeType | null;
  status_id?: string | null;
}

export interface UpdateTemplateRequest {
  name?: string;
  description?: string;
  title_template?: string;
  description_template?: string;
  priority?: Priority;
  labels?: string[];
  estimated_hours?: number | null;
  custom_fields?: Record<string, unknown>;
  assignee_id?: string | null;
  assignee_type?: AssigneeType | null;
  status_id?: string | null;
}

// Memory types

export interface Memory {
  id: string;
  workspace_id: string;
  project_id?: string | null;
  agent_id?: string | null;
  key: string;
  content: string;
  scope: "workspace" | "project" | "agent";
  tags: string[];
  source_type: "agent" | "human" | "system";
  relevance: number;
  created_at: string;
  updated_at: string;
  expires_at?: string | null;
  last_accessed_at?: string | null;
  archived: boolean;
}

export interface ScoredMemory extends Memory {
  score: number;
}

export type MemorySourceType = "agent" | "human" | "system";

export type MemoryOrderBy =
  | "created_at:desc"
  | "relevance:desc"
  | "decayed_relevance:desc";

// MemoryFilter is the combined (AND) filter set applied to the memory list/search.
// All fields are optional; an empty filter returns the unfiltered list.
export interface MemoryFilter {
  q?: string;
  scope?: string; // "all" | "workspace" | "project" | "agent"
  tags?: string[];
  tagsMode?: "all" | "any"; // maps to tags= (AND) vs tags_any= (OR)
  sourceType?: MemorySourceType | "";
  createdBy?: string; // agent id (only meaningful when sourceType === "agent")
  since?: string; // YYYY-MM-DD (local date)
  until?: string; // YYYY-MM-DD (local date)
  relevanceMin?: number; // 0.0–1.0
  includeExpired?: boolean;
  includeArchived?: boolean;
  orderBy?: MemoryOrderBy;
}

// Analytics types

export interface AnalyticsMetrics {
  task_metrics: {
    total: number;
    by_status_category: Record<string, number>;
    by_priority: Record<string, number>;
    created_this_period: number;
    completed_this_period: number;
  };
  agent_metrics: {
    total_agents: number;
    active_agents: number;
    tasks_by_agent: Array<{
      agent_id: string;
      agent_name: string;
      completed: number;
    }>;
  };
  event_metrics: {
    total_events: number;
    by_type: Record<string, number>;
  };
  timeline: Array<{
    date: string;
    created: number;
    completed: number;
  }>;
  cost_metrics: {
    total_cost: number;
    total_tokens_in: number;
    total_tokens_out: number;
    session_count: number;
    by_agent: Array<{
      agent_id: string;
      agent_name: string;
      cost: number;
      tokens_in: number;
      tokens_out: number;
    }>;
    by_project: Array<{
      project_id: string;
      project_name: string;
      cost: number;
      tokens_in: number;
      tokens_out: number;
    }>;
    by_day: Array<{
      date: string;
      cost: number;
      tokens_in: number;
      tokens_out: number;
    }>;
    top_tasks: Array<{
      task_id: string;
      task_title: string;
      cost: number;
      tokens_in: number;
      tokens_out: number;
      session_count: number;
    }>;
  };
}
