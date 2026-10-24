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
  /** (+) which block of the list the item belongs to; blocks are separated by a rule. */
  group: "top" | "inbox" | "tools" | "footer";
  /** (+) false: the collapsed rail does not draw this item (today: «My Runner» and the settings links). */
  inRail: boolean;
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
}

export interface NavSidebarSettings {
  collapsed_default?: boolean;
  show_project_icons?: boolean;
  item_order?: string[];
  [k: string]: unknown;
}

export type NavSidebarView = SlotView<NavSidebarData, NavSidebarActions, NavSidebarSettings>;
