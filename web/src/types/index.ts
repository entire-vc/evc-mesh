// Enums

export type AssigneeType = "user" | "agent" | "unassigned";
export type Priority = "urgent" | "high" | "medium" | "low" | "none";
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
  | "custom";
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

export interface Task {
  id: string;
  project_id: string;
  status_id: string;
  title: string;
  description: string;
  assignee_id: string | null;
  assignee_type: AssigneeType;
  assignee_name?: string | null;
  priority: Priority;
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
}

export interface TaskDependency {
  id: string;
  task_id: string;
  depends_on_task_id: string;
  dependency_type: DependencyType;
  created_at: string;
}

export interface Agent {
  id: string;
  workspace_id: string;
  name: string;
  agent_type: AgentType;
  status: AgentStatus;
  api_key_hash: string;
  capabilities: string[];
  metadata: Record<string, unknown>;
  last_heartbeat: string | null;
  created_at: string;
  updated_at: string;
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

export interface TokenPair {
  access_token: string;
  refresh_token: string;
}

export interface AuthResponse {
  user: User;
  tokens: TokenPair;
}

export interface RefreshResponse {
  tokens: TokenPair;
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
  assignee_id?: string;
  assignee_type?: AssigneeType;
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
  assignee_id?: string | null;
  assignee_type?: AssigneeType;
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

export type ViewType = "board" | "list" | "timeline";

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

export type IntegrationProvider = "slack" | "github" | "spark" | "mcp";

export interface IntegrationConfig {
  id: string;
  workspace_id: string;
  provider: IntegrationProvider;
  config: Record<string, unknown>;
  is_active: boolean;
  created_at: string;
  updated_at: string;
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
}
