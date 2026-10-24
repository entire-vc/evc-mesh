// nav.topbar contract v1.0.0 (spec «Каталог слотов тем v1», §4 of
// slots-task-nav-doc-auth). Fields marked "(+)" extend the spec so that the
// default view reproduces today's header with zero visual difference.
import type { FC } from "react";
import type { Id, SlotView } from "../types";

export interface TopbarCrumb {
  label: string;
  href?: string;
  /** (+) what the crumb names; the default view sizes and truncates each kind its own way. */
  kind: "workspace" | "project" | "page";
  /** (+) lucide icon name drawn before the label (workspace pages). */
  icon?: string;
  /** (+) drawn in the foreground tone; the last crumb of a project or page. */
  strong?: boolean;
}

export interface TopbarData {
  breadcrumbs: TopbarCrumb[];
  user: { id: Id; name: string; avatarUrl?: string };
  notifications: { unread: number };
  installable: boolean;
  isMobile: boolean;
  components: {
    NotificationBell: FC;
    UserMenu: FC;
    ModeSwitch?: FC;
    /** (+) project view tabs / org-chart tabs; draw nothing on other pages. */
    ViewTabs: FC;
    TeamTabs: FC;
    /** (+) global task search. */
    Search: FC;
  };
}

export interface TopbarActions {
  toggleSidebar(): void;
  install?(): void;
  openSearch?(): void;
}

export interface TopbarSettings {
  show_breadcrumbs?: boolean;
  [k: string]: unknown;
}

export type NavTopbarView = SlotView<TopbarData, TopbarActions, TopbarSettings>;
