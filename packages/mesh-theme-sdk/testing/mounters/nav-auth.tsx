// Containers for nav.sidebar, nav.topbar and auth.screen. These views are leaves: the container hands them data
// and actions; the core blocks the topbar and the auth screen embed (bell, user menu, OAuth buttons, ...) are
// stand-ins that draw a `data-mesh-block="<name>"` marker, so a test can count them. Links resolve through the
// router the host Provider supplies.
import type { ComponentType, FC } from "react";
import type { SlotContext } from "../../src/index";
import { fixtures } from "../fixtures";
import type { AuthFixture, SidebarFixture, TopbarFixture } from "../fixtures/nav-auth";
import type { RenderEnv, Scenario, Spies } from "../types";
import { registerMounter } from "./registry";

type Leaf<D> = ComponentType<{ data: D; actions: unknown; themeSettings: Record<string, unknown>; ctx: SlotContext }>;

/** A core block stand-in: one marker element, nothing else. */
const block = (name: string): FC => {
  const B: FC = () => <span data-mesh-block={name}>{name}</span>;
  B.displayName = `CoreBlock(${name})`;
  return B;
};

function renderLeaf<D>(env: RenderEnv<D>, data: unknown) {
  const View = env.view as unknown as Leaf<unknown>;
  return <View data={data} actions={env.actions} themeSettings={env.themeSettings} ctx={env.ctx} />;
}

// -- nav.sidebar ---------------------------------------------------------------------------------------

function sidebarDefaults(s: Scenario<SidebarFixture>): SidebarFixture {
  const base = fixtures.sidebar({ isMobile: s.isMobile ?? false });
  // An extension filling `sidebar.item` reaches the view as an item with origin "extension" (spec §3).
  if (s.extensions?.includes("sidebar.item")) base.items = [...base.items, fixtures.extensionItem()];
  return base;
}

function sidebarAsync(data: SidebarFixture, state: string, spies: Spies): SidebarFixture {
  const retry = () => void spies.retry?.();
  if (state === "loading") return { ...data, projects: { state: "loading" } };
  if (state === "error") return { ...data, projects: { state: "error", error: { code: "x", message: "Could not load projects" }, retry } };
  const value = data.projects.state === "ready" ? data.projects.value : [];
  return { ...data, projects: { state: "refreshing", value } };
}

registerMounter<SidebarFixture>({
  slot: "nav.sidebar",
  defaults: sidebarDefaults,
  actions: [
    { name: "toggleCollapsed" },
    { name: "switchWorkspace" },
    { name: "createProject", needs: "create" },
    { name: "closeMobile" },
    { name: "retry" },
  ],
  asyncData: sidebarAsync,
  render: (env) => renderLeaf(env, env.data),
});

// -- nav.topbar ----------------------------------------------------------------------------------------

/** Core blocks of the topbar: the contract's three plus the ones the core header also embeds. */
const TOPBAR_BLOCKS = {
  NotificationBell: block("NotificationBell"),
  UserMenu: block("UserMenu"),
  ViewTabs: block("ViewTabs"),
  TeamTabs: block("TeamTabs"),
  Search: block("Search"),
};

registerMounter<TopbarFixture>({
  slot: "nav.topbar",
  defaults: (s) => fixtures.topbar({ isMobile: s.isMobile ?? false }),
  actions: [{ name: "toggleSidebar" }, { name: "install" }, { name: "openSearch" }],
  render: (env) => {
    const ModeSwitch = env.scenario.options?.modeSwitch ? { ModeSwitch: block("ModeSwitch") } : {};
    return renderLeaf(env, { ...env.data, components: { ...TOPBAR_BLOCKS, ...ModeSwitch } });
  },
});

// -- auth.screen ---------------------------------------------------------------------------------------

registerMounter<AuthFixture>({
  slot: "auth.screen",
  defaults: (s) => {
    const mode = s.options?.mode;
    return mode === "accept-invite" ? fixtures.authInvite() : fixtures.authLogin();
  },
  // No role gating before sign-in: both are always there.
  actions: [{ name: "setValue" }, { name: "submit", async: true }, { name: "navigate" }],
  render: (env) => {
    const OAuth = env.scenario.options?.oauth ? { OAuthButtons: block("OAuthButtons") } : {};
    return renderLeaf(env, { ...env.data, components: { ...OAuth } });
  },
});
