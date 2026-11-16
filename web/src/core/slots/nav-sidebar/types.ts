// nav.sidebar contract v1.0.0 (spec «Каталог слотов тем v1», §3 of
// slots-task-nav-doc-auth). Fields marked "(+)" extend the spec so that the
// default view reproduces today's sidebar with zero visual difference.
import type { Async, Id, SlotView } from "../types";

export interface NavItem {
  id: string; // "dashboard" | "triage" | "activity" | … | "ext:<id>"
  label: string;
  icon: string; // lucide icon name from the core set; see NAV_ICON_NAMES
  href: string;
  active: boolean;
  badge?: { count: number; tone: "info" | "attention" } | null;
  origin: "core" | "extension";
  /**
   * (+) which block of the list the item belongs to; blocks are separated by a rule.
   * `dashboards-top` / `dashboards-bottom` (contract 1.1.0): the user's dashboards above and below the projects.
   * A view that does not know them gets them in `top` (see `normalizeNavGroups`).
   */
  group: "top" | "inbox" | "tools" | "footer" | "dashboards-top" | "dashboards-bottom";
  /** (+) false: the collapsed rail does not draw this item (today: «Runner» and the settings links). */
  inRail: boolean;
  /** (+) set on dashboard entries only. */
  dashboard?: NavDashboard;
}

/** (+) What the menu of a dashboard entry needs; the emoji replaces the icon, 👥 marks a shared one. */
export interface NavDashboard {
  id: Id;
  /** "" = the default icon. */
  emoji: string;
  shared: boolean;
  isDefault: boolean;
  /** Not saved yet: «Reset» makes no sense, and a first change materializes it. */
  virtual: boolean;
  /** rename, change icon, reset. */
  canEdit: boolean;
  /** delete, sharing. */
  canManage: boolean;
  /** The last dashboard cannot be deleted. */
  canDelete: boolean;
}

export interface NavProject {
  id: Id;
  slug: string;
  name: string;
  icon: string;
  active: boolean;
  href: string;
  items: NavItem[];
}

export interface NavWorkspace {
  id: Id;
  slug: string;
  name: string;
  logoUrl?: string;
  href: string;
  current: boolean;
}

export interface NavSidebarData {
  workspace: { id: Id; slug: string; name: string; logoUrl?: string };
  workspaces: NavWorkspace[];
  items: NavItem[];
  projects: Async<NavProject[]>;
  collapsed: boolean;
  isMobile: boolean;
  // (+) the workspace is resolved; until then the project list draws nothing (no «empty» hint).
  workspaceResolved: boolean;
  // (+) archived projects live under a fold, not in `projects`.
  archivedProjects: NavProject[];
  // (+) text of the last failed «unarchive», shown under the archived list.
  archivedError: string | null;
  // (+) dashboards: the list is still loading / failed (the groups are then empty and the view says so).
  dashboardsState?: "loading" | "ready" | "error";
  /** (+) «All dashboards» screen. */
  allDashboardsHref?: string;
}

export interface NavSidebarActions {
  toggleCollapsed(): void;
  switchWorkspace(slug: string): void;
  /** Omitted without the right to create a project. */
  createProject?(): void;
  /** (+) opens the «new workspace» dialog (owned by the container). */
  createWorkspace?(): void;
  /** (+) puts an archived project back; failure text arrives in `archivedError`. */
  unarchiveProject?(id: Id): Promise<void>;
  closeMobile?(): void;
  /** (+) whole menu order in one call (drag, keyboard, or the buttons of the item menu); resolves when saved. */
  reorderDashboards?(order: { top: Id[]; bottom: Id[] }): Promise<void>;
  /** (+) opens the «new dashboard» dialog with the group preselected (owned by the container). */
  createDashboard?(group: "top" | "bottom"): void;
  /** (+) item-menu actions; the dialogs / confirmations are the container's. */
  renameDashboard?(id: Id): void;
  changeDashboardIcon?(id: Id): void;
  duplicateDashboard?(id: Id): void;
  shareDashboard?(id: Id): void;
  resetDashboard?(id: Id): void;
  deleteDashboard?(id: Id): void;
  /** (+) hides a shared dashboard from MY menu. */
  hideDashboard?(id: Id): void;
}

export interface NavSidebarSettings {
  collapsed_default?: boolean;
  show_project_icons?: boolean;
  item_order?: string[];
  [k: string]: unknown;
}

export type NavSidebarView = SlotView<NavSidebarData, NavSidebarActions, NavSidebarSettings>;
