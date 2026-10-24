// Contract: Mesh Doc "slots-task-nav-doc-auth" §3 (nav.sidebar) and §4 (nav.topbar), v1.0.0.
import type { FC } from "react";
import type { Async, SlotView } from "./slots";
import type { Id } from "./primitives";

export interface NavItem {
  /** Stable key: "dashboard" | "triage" | "activity" | … | "ext:<id>". */
  id: string;
  label: string;
  /** Icon name from the core's set (lucide); a view picks the component itself or takes it as is. */
  icon: string;
  href: string;
  active: boolean;
  badge?: { count: number; tone: "info" | "attention" } | null;
  origin: "core" | "extension";
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

export interface NavSidebarData {
  workspace: { id: Id; slug: string; name: string; logoUrl?: string };
  workspaces: { id: Id; slug: string; name: string; logoUrl?: string; href: string; current: boolean }[];
  /** Workspace-level items, permissions already applied. */
  items: NavItem[];
  projects: Async<NavProject[]>;
  collapsed: boolean;
  /** The mobile panel slides in; the core drives opening. */
  isMobile: boolean;
}

export interface NavSidebarActions {
  toggleCollapsed(): void;
  switchWorkspace(slug: string): void;
  /** Omitted without the permission to create a project. */
  createProject?(): void;
  closeMobile?(): void;
  retry(): void;
}

export interface NavSidebarSettings {
  collapsed_default?: boolean;
  show_project_icons?: boolean;
  item_order?: string[];
  [k: string]: unknown;
}

export type NavSidebarView = SlotView<NavSidebarData, NavSidebarActions, NavSidebarSettings>;

export interface TopbarData {
  breadcrumbs: { label: string; href?: string }[];
  user: { id: Id; name: string; avatarUrl?: string };
  notifications: { unread: number };
  /** The PWA install can be offered. */
  installable: boolean;
  isMobile: boolean;
  components: {
    /** Core block: the list and mark-as-read. */
    NotificationBell: FC;
    /** Core block: profile, light/dark/system mode, sign out. */
    UserMenu: FC;
    ModeSwitch?: FC;
  };
}

export interface TopbarActions {
  toggleSidebar(): void;
  install?(): void;
  /** When search is available. */
  openSearch?(): void;
}

export interface TopbarSettings {
  show_breadcrumbs?: boolean;
  [k: string]: unknown;
}

export type NavTopbarView = SlotView<TopbarData, TopbarActions, TopbarSettings>;
