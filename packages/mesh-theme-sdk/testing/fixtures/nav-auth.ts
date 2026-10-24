// Fixtures for nav.sidebar, nav.topbar and auth.screen (Mesh Doc slots-task-nav-doc-auth §3, §4, §7).
// The data is the contract's shape plus the fields the core container fills on top of it (grouping of the
// sidebar items, crumb kinds, auth heading/footer): a view written against the contract ignores them, the
// core's default view needs them to draw what the page draws today.
import type { AuthField, AuthScreenData, NavItem, NavProject, NavSidebarData, TopbarData } from "../../src/index";

/** The workspace `ctx.workspace` names in the harness (slug "ws"). */
export const WS = { id: "w1", slug: "ws", name: "Workspace" } as const;

export type NavItemFixture = NavItem & { group: "top" | "inbox" | "tools" | "footer"; inRail: boolean };

export function navItem(id: string, over: Partial<NavItemFixture> = {}): NavItemFixture {
  return {
    id, label: id[0]!.toUpperCase() + id.slice(1), icon: "LayoutDashboard", href: `/ws/${id}`, active: false,
    origin: "core", badge: null, group: "top", inRail: true, ...over,
  };
}

export function navProject(slug: string, over: Partial<NavProject> = {}): NavProject {
  return { id: `p-${slug}`, slug, name: slug[0]!.toUpperCase() + slug.slice(1), icon: "", active: false, href: `/ws/p/${slug}`, items: [], ...over };
}

export type SidebarFixture = Omit<NavSidebarData, "items"> & {
  items: NavItemFixture[];
  workspaceResolved: boolean;
  archivedProjects: NavProject[];
  archivedError: string | null;
};

/** Four workspace items in list order (one active, one with an unread badge), one project, two workspaces. */
export function sidebar(over: Partial<SidebarFixture> = {}): SidebarFixture {
  return {
    workspace: { id: WS.id, slug: WS.slug, name: WS.name },
    workspaces: [
      { id: WS.id, slug: WS.slug, name: WS.name, href: `/${WS.slug}`, current: true },
      { id: "w2", slug: "beta", name: "Beta", href: "/beta", current: false },
    ],
    items: [
      navItem("dashboard", { active: true }),
      navItem("triage", { group: "inbox", icon: "Inbox" }),
      navItem("activity", { group: "inbox", icon: "Bell", badge: { count: 3, tone: "attention" } }),
      navItem("analytics", { group: "tools", icon: "BarChart2" }),
    ],
    projects: { state: "ready", value: [navProject("lab")] },
    collapsed: false,
    isMobile: false,
    workspaceResolved: true,
    archivedProjects: [],
    archivedError: null,
    ...over,
  };
}

/** An item an extension contributed: drawn like a core one. */
export const extensionItem = () =>
  navItem("ext:gitlab", { label: "GitLab", icon: "Puzzle", group: "tools", origin: "extension", href: "/ws/x/gitlab" });

export type CrumbFixture = TopbarData["breadcrumbs"][number] & { kind: "workspace" | "project" | "page"; strong?: boolean };

/** `components` are injected by the mounter (stand-ins of the core blocks). */
export type TopbarFixture = Omit<TopbarData, "components" | "breadcrumbs"> & { breadcrumbs: CrumbFixture[] };

export function topbar(over: Partial<TopbarFixture> = {}): TopbarFixture {
  return {
    breadcrumbs: [
      { kind: "workspace", label: WS.name, href: `/${WS.slug}` },
      { kind: "project", label: "Lab", href: `/${WS.slug}/lab` },
      { kind: "page", label: "Board", strong: true },
    ],
    user: { id: "u1", name: "Ann Lee" },
    notifications: { unread: 0 },
    installable: false,
    isMobile: false,
    ...over,
  };
}

export const authField = (name: AuthField["name"], over: Partial<AuthField> = {}): AuthField => ({
  name, type: "text", label: name, required: true, autoComplete: name, value: "", ...over,
});

/** `components` are injected by the mounter. */
export type AuthFixture = Omit<AuthScreenData, "components">;

/** The sign-in screen: email + password, registration open. */
export function authLogin(over: Partial<AuthFixture> = {}): AuthFixture {
  return {
    mode: "login",
    status: "form",
    heading: { title: "Welcome back", subtitle: "Sign in to your account" },
    fields: [
      authField("email", { type: "email", label: "Email", autoComplete: "email" }),
      authField("password", { type: "password", label: "Password", autoComplete: "current-password" }),
    ],
    submitLabel: "Sign in",
    submitting: false,
    formError: null,
    registrationEnabled: true,
    invite: null,
    links: { login: "/login", register: "/register", forgot: null },
    footer: { text: "No account?", linkLabel: "Register", to: "/register" },
    ...over,
  };
}

/** Registration closed: the core drops the register link and the footer that points to it. */
export const authRegistrationClosed = (): AuthFixture =>
  authLogin({ registrationEnabled: false, links: { login: "/login", register: null, forgot: null }, footer: null });

/** Accepting an invite to "Acme" from "Ann Lee". */
export function authInvite(over: Partial<AuthFixture> = {}): AuthFixture {
  const invite = { workspaceName: "Acme", inviterName: "Ann Lee" };
  return authLogin({
    mode: "accept-invite",
    heading: { title: "Accept the invitation", subtitle: `Join ${invite.workspaceName}`, detail: `Invited by ${invite.inviterName}` },
    invite,
    fields: [
      authField("name", { label: "Name", autoComplete: "name" }),
      authField("password", { type: "password", label: "Password", autoComplete: "new-password" }),
    ],
    submitLabel: "Join",
    footer: null,
    ...over,
  });
}
