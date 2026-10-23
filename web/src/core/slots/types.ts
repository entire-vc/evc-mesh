// Contract: Mesh Doc "theme-slot-catalog-v1" (general types), "slots-board" (board.layout / board.column /
// board.card) and "slots-task-nav-doc-auth" (task.row, task.view), v1.0.0.
import type { FC, ReactElement } from "react";
import type { Id, IsoDate } from "@/core/primitives";
import type { CustomFieldDefinition, Priority, RuntimeState, StatusCategory } from "@/types";

export type { Id, IsoDate, Priority, StatusCategory };

export interface SlotError {
  code: string;
  message: string;
}

/** Load state a view must tell apart: loading / ready / refreshing (old data kept) / error. */
export type Async<T> =
  | { state: "loading" }
  | { state: "ready"; value: T }
  | { state: "refreshing"; value: T }
  | { state: "error"; error: SlotError; value?: T; retry(): void };

export interface SlotContext {
  slot: string;
  contractVersion: string;
  locale: string;
  mode: "light" | "dark";
  isMobile: boolean;
  // null only for `auth.screen`: the workspace is unknown before sign-in, so
  // the slot takes the instance theme only (ADR D3).
  workspace: { id: Id; slug: string; name: string } | null;
}

export interface SlotProps<D, A, S = Record<string, never>> {
  data: D;
  actions: A;
  themeSettings: S;
  ctx: SlotContext;
}

export type SlotView<D, A, S = Record<string, never>> = (props: SlotProps<D, A, S>) => ReactElement | null;

export interface ActorRef {
  id: Id;
  type: "user" | "agent";
  name: string;
  avatarUrl?: string;
  /** Agent short tag, resolved by the container; the view never looks it up itself. */
  shortTag?: string | null;
}

export interface TaskData {
  id: Id;
  shortId: string;
  projectId: Id;
  title: string;
  hasDescription: boolean;
  statusId: Id;
  statusCategory: StatusCategory;
  priority: Priority;
  assignee: ActorRef | null;
  reviewer: ActorRef | null;
  /** Set only when the holder differs from the assignee; the core decides. */
  checkedOutBy: ActorRef | null;
  labels: string[];
  dueDate: IsoDate | null;
  overdue: boolean;
  estimatedHours: number | null;
  customFields: Record<string, unknown>;
  counts: { subtasks: number; artifacts: number; vcsLinks: number };
  vcsProviders: ("github" | "gitlab")[];
  pipelineStatus: string | null;
  gate: { class: "hard" | "soft"; reason: string } | null;
  /** Readable runtime state (lane already resolved to a name); the catalog says {kind,label}, this is a superset. */
  runtimeState: RuntimeState | null;
  delegationLevel: string | null;
  updatedAt: IsoDate;
  // Additive to the catalog's TaskData (minor, mesh-default needs them to draw today's card):
  /** Provider list is known (older API sends none: the generic VCS icon stays). */
  vcsProvidersKnown: boolean;
  /** Providers whose extension draws its own `task.badge`; the generic VCS icon skips them (the container reads the extension state). */
  vcsOwnedProviders: readonly ("github" | "gitlab")[];
  checkout: { expiresAt: string; lapsed: boolean } | null;
  recurring: { instanceNumber: number | null } | null;
  falseOpen: {
    allChildrenClosed: boolean;
    onlyParkedChildrenLeft: boolean;
    openChildrenCount: number;
    staleDays: number;
  } | null;
}

export interface StatusData {
  id: Id;
  name: string;
  slug: string;
  color: string;
  category: StatusCategory;
  position: number;
}

export interface FieldDescriptor {
  key: string;
  label: string;
  kind: "text" | "number" | "date" | "select" | "multiselect" | "person" | "flag" | "badge";
  source: "builtin" | "custom";
  projectId?: Id;
}

// ---------------------------------------------------------------------------
// board.*
// ---------------------------------------------------------------------------

export type BoardGroupBy = "status" | "priority" | "assignee";
export type BoardSortBy = "manual" | "updated" | "priority" | "due_date" | "created" | "title";

export interface BoardColumnData {
  id: Id;
  kind: "status" | "triage" | "group";
  title: string;
  color?: string;
  status?: StatusData;
  tasks: TaskData[];
  totalCount: number;
  wipLimit: number | null;
  isClosed: boolean;
  hasMore: boolean;
  loadingMore: boolean;
  gateStatusNameByTask?: Record<Id, string>;
  /** Additive: header marker of a group column (priority flag / assignee). */
  group?: { priority?: Priority; assignee?: { id: Id; name?: string; type: "user" | "agent" | "unassigned"; shortTag?: string | null } };
  /** Additive: header count string; a partial closed column shows "N+" under a filter. */
  countLabel: string;
  /** Additive: what a partial closed column knows about the cards it does not hold. */
  closedSlice?: { loaded: number; total: number; filtersActive: boolean };
}

export interface BoardDndState {
  enabled: boolean;
  reason?: "group_by" | "no_permission" | "mobile_readonly";
  nonDroppableColumnIds: Id[];
}

export interface BoardLayoutData {
  project: { id: Id; slug: string; name: string };
  groupBy: BoardGroupBy;
  sortBy: BoardSortBy;
  columns: Async<BoardColumnData[]>;
  dnd: BoardDndState;
  availableFields: FieldDescriptor[];
  cardFields: string[];
  /** Additive: why there are no columns (the layout draws the matching empty state). */
  emptyReason?: "no_statuses" | "no_matches";
  /** Additive: note under the toolbar for non-status groupings with a partial closed slice. */
  closedPartial?: { loaded: number; total: number } | null;
}

export type QuickEditPatch =
  | { field: "title"; value: string }
  | { field: "priority"; value: Priority }
  | { field: "due_date"; value: IsoDate | null }
  | { field: "assignee"; value: { id: Id; type: "user" | "agent" } | null }
  | { field: "labels"; value: string[] }
  | { field: `cf:${string}`; value: unknown };

export interface BoardLayoutActions {
  moveTask(taskId: Id, toColumnId: Id, position: { index: number }): Promise<void>;
  openTask(taskId: Id, opts?: { newTab?: boolean }): void;
  quickEdit(taskId: Id, patch: QuickEditPatch): Promise<void>;
  /** Omitted without the `task.create` permission. */
  createTask?(columnId: Id): void;
  loadMore(columnId: Id): Promise<void>;
  retry(): void;
}

export interface BoardLayoutSettings {
  card_fields?: string[];
  density?: "comfortable" | "compact";
  layout_variant?: string;
  collapse_empty_columns?: boolean;
  [themeKey: string]: unknown;
}

export interface BoardCardViewData {
  task: TaskData;
  column: { id: Id; kind: BoardColumnData["kind"]; statusCategory?: StatusCategory };
  gateStatusName?: string;
  fields: FieldDescriptor[];
  selected: boolean;
  isOverlay: boolean;
}

export interface BoardCardActions {
  openTask(opts?: { newTab?: boolean }): void;
  quickEdit?(patch: QuickEditPatch): Promise<void>;
  assign?(assignee: { id: Id; type: "user" | "agent" } | null): Promise<void>;
}

export interface BoardCardSettings extends BoardLayoutSettings {
  show_cover?: boolean;
}

export type BoardCardView = SlotView<BoardCardViewData, BoardCardActions, BoardCardSettings>;

export interface BoardColumnViewData {
  column: BoardColumnData;
  index: number;
  total: number;
  dnd: BoardDndState;
  /** Additive: fields chosen for the cards (the column hands them to every card). */
  fields: FieldDescriptor[];
  components: { Card: BoardCardView };
}

export interface BoardColumnActions {
  createTask?(): void;
  loadMore(): Promise<void>;
  openTask(taskId: Id, opts?: { newTab?: boolean }): void;
  quickEdit?(taskId: Id, patch: QuickEditPatch): Promise<void>;
  moveTask(taskId: Id, position: { index: number }): Promise<void>;
}

export type BoardColumnView = SlotView<BoardColumnViewData, BoardColumnActions, BoardLayoutSettings>;

export interface BoardLayoutComponents {
  Column: BoardColumnView;
  Card: BoardCardView;
}

export type BoardLayoutView = SlotView<
  BoardLayoutData & { components: BoardLayoutComponents },
  BoardLayoutActions,
  BoardLayoutSettings
>;

export const BOARD_CONTRACT_VERSION = "1.0.0";

// ---------------------------------------------------------------------------
// task.row
// ---------------------------------------------------------------------------

export interface TaskRowColumn {
  key: string;
  label: string;
  field: FieldDescriptor;
  width?: number;
  /** Additive: the definition behind a `cf:<slug>` column (the renderer and the editor need its type and options). */
  customField?: CustomFieldDefinition;
}

export interface TaskRowData {
  task: TaskData;
  status: StatusData;
  projectName?: string;
  columns: TaskRowColumn[];
  selected: boolean;
  focused: boolean;
  depth: number;
  expandable: boolean;
  expanded: boolean;
  /** Additive: statuses the status cell offers (same order as the project's). */
  statuses: StatusData[];
  /** Additive: the children of an expanded row are being fetched. */
  expanding: boolean;
  /** Additive: the list API does not carry `created_at` in TaskData. */
  createdAt: IsoDate;
  /** Additive: ready core blocks (extension badges of the title cell). */
  components: { Badges: () => ReactElement | null };
}

export interface TaskRowActions {
  openTask(opts?: { newTab?: boolean }): void;
  toggleSelected(): void;
  toggleExpanded?(): void;
  quickEdit?(patch: QuickEditPatch): Promise<void>;
  changeStatus?(statusId: Id): Promise<void>;
}

export interface TaskRowSettings {
  row_fields?: string[];
  density?: "comfortable" | "compact";
  [k: string]: unknown;
}

export type TaskRowView = SlotView<TaskRowData, TaskRowActions, TaskRowSettings>;

// ---------------------------------------------------------------------------
// task.view
// ---------------------------------------------------------------------------

export type TaskViewBlockId =
  | "header" | "description" | "properties" | "subtasks" | "dependencies"
  | "comments" | "artifacts" | "vcs" | "activity";

export interface TaskViewData {
  task: Async<TaskData & { descriptionMarkdown: string }>;
  /** Null until the task is loaded. */
  status: StatusData | null;
  statuses: StatusData[];
  presentation: "page" | "slide-over";
  /**
   * Blocks the core offers, in the default order. `vcs` is not offered yet: the VCS links are drawn
   * inside `properties`, where they are today (a theme that wants them apart is a minor version).
   */
  blocks: TaskViewBlockId[];
  components: Record<TaskViewBlockId, FC> & {
    /** Additive: the panel's top bar (back, open in a new tab, copy link, extension `task.actions`, close). `openPanel` brings an extension panel to the front. */
    TopBar: FC<{ openPanel(extensionId: string): void }>;
    /** Additive: the open/blocked dependency counter a tab can carry. */
    DependencyBadge: FC;
    /** Additive: an extension's `task.panel` tab body, bound to the open task (the extension takes the whole task, not TaskData). */
    ExtensionPanel: FC<{ extensionId: string; name: string; external?: boolean }>;
  };
  /** Additive: the core asks the view to bring a block to the front (arrival from a mention, an opened subtask, a document inserted into the description). */
  requestedBlock: { block: TaskViewBlockId; nonce: number } | null;
}

export interface TaskViewActions {
  close?(): void;
  back?(): void;
  quickEdit?(patch: QuickEditPatch): Promise<void>;
  changeStatus?(statusId: Id): Promise<void>;
  assign?(assignee: { id: Id; type: "user" | "agent" } | null): Promise<void>;
  openTask(taskId: Id, opts?: { newTab?: boolean }): void;
  retry(): void;
}

export interface TaskViewSettings {
  block_order?: string[];
  layout_variant?: "single-column" | "tabs" | "two-column" | string;
  hidden_blocks?: TaskViewBlockId[];
  [k: string]: unknown;
}

export type TaskViewView = SlotView<TaskViewData, TaskViewActions, TaskViewSettings>;


// ---------------------------------------------------------------------------
// doc.tree / doc.view  (Mesh Doc "slots-task-nav-doc-auth", sections 5 and 6, v1.0.0)
// ---------------------------------------------------------------------------

export interface DocNodeData {
  id: Id;
  title: string;
  slug: string;
  depth: number;
  hasChildren: boolean;
  expanded: boolean;
  selected: boolean;
  updatedAt: IsoDate;
  /** Additive: the document's real address, so a node is a link (new tab, copy link). */
  href: string;
}

export interface DocTreeData {
  projectId: Id;
  /** Visible nodes only, in display order (collapsed branches are not in the list). */
  nodes: Async<DocNodeData[]>;
}

export interface DocTreeActions {
  select(docId: Id, opts?: { newTab?: boolean }): void;
  toggleExpanded(docId: Id): void;
  createChild?(parentId: Id | null): void;
  rename?(docId: Id): void;
  move?(docId: Id): void;
  remove?(docId: Id): void;
  retry(): void;
}

export interface DocTreeSettings {
  show_icons?: boolean;
  density?: "comfortable" | "compact";
  [k: string]: unknown;
}

export type DocTreeView = SlotView<DocTreeData, DocTreeActions, DocTreeSettings>;

export type DocSaveState = "idle" | "saved" | "saving" | "dirty" | "conflict" | "error";

export interface DocViewDoc {
  id: Id;
  title: string;
  slug: string;
  path: { label: string; href: string }[];
  updatedAt: IsoDate;
  updatedBy: ActorRef | null;
  version: number;
}

export interface DocViewData {
  doc: Async<DocViewDoc>;
  /** `idle` (additive): nothing edited yet, no indicator. */
  save: { state: DocSaveState; message?: string };
  components: {
    /** Core block: the editor with save and version-conflict handling. Exactly one per view. */
    Editor: React.FC;
    /** Properties are drawn by the editor block today (frontmatter); this block is empty until they move out. */
    Properties: React.FC;
    /** Core block: the discussion under the article. */
    Comments: React.FC;
    WatchToggle: React.FC;
    Breadcrumbs: React.FC;
    /** Additive core blocks of today's page frame. */
    CommentsRail: React.FC;
    CommentsToggle: React.FC;
    CommentsEntry: React.FC;
    Meta: React.FC;
  };
  layout: { commentsOpen: boolean };
  /** Additive: the docs root (the leading crumb, and the way out of an error on a phone). */
  rootHref: string;
  /** Additive: whether the editor is open for typing (the page draws Edit / Done from it). */
  editing: boolean;
  /** Additive: why the paragraph link did not land exactly, said out loud above the article. */
  anchorNotice: "edited" | "lost" | null;
}

export interface DocViewActions {
  toggleComments(): void;
  retry(): void;
  exportDoc?(format?: "pdf" | "md" | "docx"): void;
  /**
   * Offered by a container whose copy has a recovery flow. mesh-default's approved conflict copy
   * (approved copy) carries no buttons, so the Mesh container does not supply it.
   */
  resolveConflict?(choice: "reload" | "keep-mine"): void;
  /** Additive: absent when the viewer cannot edit. */
  startEditing?(): void;
  finishEditing?(): void;
  rename?(): void;
  move?(): void;
  remove?(): void;
  copyUrl(): void;
  share?(): void;
  /** Additive: where the breadcrumb root ("Documents") leads and how a crumb navigates on a plain click. */
  navigate(href: string): void;
}

export interface DocViewSettings {
  content_width?: "narrow" | "wide" | "full";
  show_properties?: boolean;
  [k: string]: unknown;
}

export type DocViewView = SlotView<DocViewData, DocViewActions, DocViewSettings>;

export const DOC_CONTRACT_VERSION = "1.0.0";
