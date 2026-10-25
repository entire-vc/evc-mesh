// Domain types a theme's contract refers to (Task, Priority, ...). Moved here from the core's
// `types/index.ts`, which re-exports them: one definition, no copies.

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

// DodCheckStatus is the state of a single Definition-of-Done gate check.
export type DodCheckStatus = "pending" | "pass" | "fail";

// DodCheck holds the state of a single named DoD gate as reported by an external caller.
export interface DodCheck {
  status: DodCheckStatus;
  updated_at: string;
  reporter?: string;
}

export interface HumanGateInfo {
  gated: boolean;
  owner_agent_id?: string | null;
  owner_name?: string;
  marker_comment_id?: string | null;
  marker_created_at?: string | null;
  clearable_by_owner: boolean;
  reason_if_not?: string;
  /**
   * Which door is open to owner_agent_id — see domain.HumanGateClearPath.
   * "withdraw_marker": the ask lives in a "Blocking @" comment and comes down
   * by a withdrawal comment. "clear_endpoint": armed through the API with no
   * marker, released by DELETE /tasks/:id/human-gate. Absent when
   * clearable_by_owner is false: there is no door to name.
   *
   * Read-only here — this SPA has no agent session, so neither path is one it
   * can take. Mirrored so the type does not silently omit a field the API
   * returns, which is how a type stops being a description of the API.
   */
  clear_path?: "withdraw_marker" | "clear_endpoint";
}

/**
 * See internal/domain/task.go FalseOpenSignal. Two mutually exclusive flags
 * on purpose, not one merged bool — an internal ticket (one open BACKLOG child) must
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

export type RuntimeStateKind = "working" | "waiting" | "queued" | "stopped";

export interface RuntimeState {
  kind: RuntimeStateKind;
  /** When the state began (ISO). */
  since?: string | null;
  /** When a waiting state is expected to end (ISO). */
  until?: string | null;
  /** What is awaited: `card:#8hex`, `pipeline:g/p#id`, `mr:g/p!iid`, `decision:@name`. */
  ref?: string | null;
  /** Lane holding / queueing the card. */
  lane?: string | null;
  /** Free text; for `queued` may carry the queue position, for `stopped` the cause. */
  reason?: string | null;
  /** Machine-readable reason from the server's closed list; the badge translates it. Wins over `reason`. */
  reason_code?: string | null;
  /** Placeholder values for `reason_code` (e.g. `{ minutes: "4" }`). */
  reason_params?: Record<string, string> | null;
  /** Queue position (queued only), 1-based. */
  position?: number | null;
  /** The publisher has gone quiet — the value may be out of date. */
  stale?: boolean;
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
  /** Subtasks not in a done/cancelled status category. */
  open_subtask_count?: number;
  /** `blocks` edges where this task is the dependent, and how many blockers are not done/cancelled. */
  dependency_count?: number;
  open_dependency_count?: number;
  artifact_count?: number;
  vcs_link_count?: number;
  /** Distinct providers of the task's VCS links (`github`, `gitlab`); lets a card badge be drawn per provider. */
  vcs_providers?: string[];
  /** Latest GitLab pipeline state across the task's MR links (`success`, `failed`, …); null/absent = none reported. */
  vcs_pipeline_status?: string | null;
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
  /** Why the card waits on a human (list and detail rows; omitted when unset). */
  gate_reason?: string | null;
  /** Populated only by GET /tasks/:id when human_gate is true — not on list rows. */
  human_gate_info?: HumanGateInfo | null;
  /**
   * The "false-open" graph signal: sent on both list and
   * detail responses for any parent task (subtask_count > 0) whose own
   * status is still open, regardless of whether either flag has actually
   * fired yet — the board decides what to render, the server always reports
   * the raw state. Absent for leaf tasks and for tasks whose own status is
   * already done/cancelled.
   */
  false_open?: FalseOpenSignal | null;
  /**
   * Live state of the work behind the card (an internal ticket, parent an internal ticket).
   * Separate from the Mesh status on purpose: a todo card can be worked on right
   * now and an in_progress card can be waiting. Absent/null until the server
   * (or fiddler) has a value.
   */
  runtime_state?: RuntimeState | null;
}

// Team Directory
export interface TeamDirectoryAgent {
  id: string;
  name: string;
  slug: string;
  status: string;
  agent_type: string;
  model?: string | null;
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
  short_tag?: string | null;
  /** false = invited guest (home workspace elsewhere, agent_workspace_grants). */
  is_home?: boolean;
}
/** Which inbox a mention row came from. Also the field a reader branches on. */
export type MentionSource = "task" | "document";
