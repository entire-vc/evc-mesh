import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { OUT_DIR, SetupFail, cfg } from "./lib/env";
import {
  type ChatSettingsView, assertChatOff, assertEmptyAgentsRejected, assertLimitsReadback, assertNoTab, assertSettingsReadback, assertTabAfter,
} from "./lib/detect";
import { type Actor, type Ctx, chatBase, chatUrl, openActor, resolveCtx, snap, step } from "./lib/harness";

/**
 * e2e/agent-chat S01: admin C switches Chat with Agent on by clicks, first in the
 * workspace (Extensions → Installed), then in project P (Settings → Extensions:
 * on, both harness agents, roles, Save, reload). The API only observes, except
 * for setting the Given (the chat off in W and P) and the rollback at the end,
 * which puts back exactly what was found and reads it back.
 *
 * Then user A: the Chat tab stands right after Docs in P; in the chat-off project
 * Q there is no tab, the direct route shows the off state and the API refuses to
 * create a thread. Negative control: switching on with no agent is not possible
 * from the card and the API refuses it, so the tab never appears for empty agents.
 */
test.describe.configure({ mode: "serial" });

const EXT = "agent-chat";
const ROLES = ["admin", "member"];
const offSlug = () => process.env.E2E_CHAT_OFF_PROJECT_SLUG || "e2e-fixtures";

interface ReadbackLine { at: string; what: string; status: number; body: unknown }
const readbacks: ReadbackLine[] = [];

/** Every GET of the settings goes to the log and to s01-readback.json: AC2 is read from there. */
async function readSettings(a: Actor, what: string, path: string): Promise<{ status: number; body: Record<string, unknown> }> {
  const r = await a.api("GET", path);
  let body: Record<string, unknown> = {};
  try { body = JSON.parse(r.text) as Record<string, unknown>; } catch { body = { raw: r.text.slice(0, 200) }; }
  readbacks.push({ at: new Date().toISOString(), what, status: r.status, body });
  console.log(`[S01 readback] ${what}: ${r.status} ${JSON.stringify(body)}`);
  writeFileSync(join(OUT_DIR, "s01-readback.json"), JSON.stringify(readbacks, null, 1));
  return { status: r.status, body };
}

async function wsExtensionEnabled(a: Actor, x: Ctx, what: string): Promise<boolean> {
  const r = await a.api("GET", `/api/v1/workspaces/${x.workspaceId}/extensions`);
  if (r.status !== 200) throw new Error(`${what}: workspace extensions answered ${r.status}`);
  const e = ((r.json() as { extensions?: Array<{ id: string; status?: { enabled?: boolean }; connection?: unknown }> }).extensions ?? []).find((v) => v.id === EXT);
  if (!e) throw new SetupFail(`${EXT} is not in the workspace extension list`);
  const on = Boolean(e.status?.enabled);
  readbacks.push({ at: new Date().toISOString(), what, status: r.status, body: { extension: EXT, enabled: on, connected: e.connection != null } });
  console.log(`[S01 readback] ${what}: workspace ${EXT} enabled=${on} connected=${e.connection != null}`);
  writeFileSync(join(OUT_DIR, "s01-readback.json"), JSON.stringify(readbacks, null, 1));
  return on;
}

const view = (b: Record<string, unknown>): ChatSettingsView => ({
  enabled: Boolean(b.enabled), agents: (b.agents as string[] | undefined) ?? [], allowed_roles: b.allowed_roles as string[] | undefined,
});

/** The full order of the project strip, overflow included: the hidden measuring copy holds every tab. */
async function tabOrder(page: Page): Promise<string[]> {
  const strip = page.getByTestId("project-tabs-order").first();
  await strip.locator("[data-tab-id]").first().waitFor({ state: "attached", timeout: 30_000 });
  return strip.locator("[data-tab-id]").evaluateAll((els) => els.map((el) => el.getAttribute("data-tab-id") ?? ""));
}

/** The strip of a project as A sees it after a fresh load; the chat flag is fetched once per page load. */
async function projectStrip(a: Actor, ws: string, project: string): Promise<string[]> {
  await a.page.goto(`/w/${ws}/p/${project}`);
  await a.page.waitForLoadState("networkidle").catch(() => undefined);
  return tabOrder(a.page);
}

test.describe("agent-chat S01 enable", () => {
  let A: Actor;
  let C: Actor;
  let x: Ctx;
  let off: { id: string; slug: string };
  let baseline: { ws: boolean; project: Record<string, unknown>; limits: Record<string, unknown> };
  let givenSet = false;
  let addedToOff = false;

  test.beforeAll(async ({ browser }) => {
    C = await openActor(browser, "C");
    A = await openActor(browser, "A");
    x = await resolveCtx(C);
    // A is a plain member and lists only its own projects; C (workspace admin) sees all of them.
    // When A is not in Q, C adds A for this run and afterAll takes it back out.
    const pr = (await C.api("GET", `/api/v1/workspaces/${x.workspaceId}/projects?page_size=100`)).json() as { items?: Array<{ id: string; slug: string }> };
    const q = (pr.items ?? []).find((p) => p.slug === offSlug());
    if (!q) throw new SetupFail(`workspace admin C does not see the chat-off project ${offSlug()} (E2E_CHAT_OFF_PROJECT_SLUG)`);
    off = q;
    const ap = (await A.api("GET", `/api/v1/workspaces/${x.workspaceId}/projects?page_size=100`)).json() as { items?: Array<{ id: string; slug: string }> };
    if (!(ap.items ?? []).some((p) => p.id === off.id)) {
      const add = await C.api("POST", `/api/v1/projects/${off.id}/members`, { user_id: A.userId, role: "member" });
      if (add.status !== 201) throw new SetupFail(`adding A to ${off.slug} answered ${add.status}: ${add.text.slice(0, 200)}`);
      addedToOff = true;
      console.log(`[S01 setup] A added to ${off.slug} as member for this run`);
    }
    const qs = await readSettings(C, "Q settings (chat-off project)", `/api/v1/projects/${off.id}/chat/settings`);
    if (qs.status !== 200 || (qs.body.enabled === true && ((qs.body.agents as string[] | undefined) ?? []).length > 0)) {
      throw new SetupFail(`project ${off.slug} must have the chat off for S01, GET answered ${qs.status} enabled=${String(qs.body.enabled)}`);
    }
    // What the run found (the window global-setup opened): the rollback puts back exactly this.
    const p = await readSettings(C, "P settings before S01", `${chatBase(x)}/settings`);
    const l = await readSettings(C, "W limits before S01", `/api/v1/workspaces/${x.workspaceId}/chat/settings`);
    if (p.status !== 200 || l.status !== 200) throw new SetupFail(`settings GET answered P=${p.status} W=${l.status}`);
    baseline = { ws: await wsExtensionEnabled(C, x, "W extension before S01"), project: p.body, limits: l.body };
  });

  test.afterAll(async () => {
    try {
      if (addedToOff) {
        await step("S01-off-member-rollback", "C", async () => {
          const rm = await C.api("DELETE", `/api/v1/projects/${off.id}/members/${A.userId}`);
          console.log(`[S01 rollback] A removed from ${off.slug}: ${rm.status}`);
          expect(rm.status, `removing A from ${off.slug}: ${rm.text.slice(0, 200)}`).toBeLessThan(300);
          const ap = (await A.api("GET", `/api/v1/workspaces/${x.workspaceId}/projects?page_size=100`)).json() as { items?: Array<{ id: string }> };
          expect((ap.items ?? []).some((p) => p.id === off.id), `A is out of ${off.slug} again`).toBe(false);
        });
      }
      if (!baseline || !givenSet) return;
      // Rollback, whatever the test did: the project settings first, then the workspace switch.
      const b = view(baseline.project);
      const put = await C.api("PUT", `${chatBase(x)}/settings`, { enabled: b.enabled, agents: b.agents, allowed_roles: b.allowed_roles });
      const ws = await C.api("POST", `/api/v1/workspaces/${x.workspaceId}/extensions/${EXT}/${baseline.ws ? "enable" : "disable"}`, baseline.ws ? undefined : { confirm: true });
      console.log(`[S01 rollback] PUT P settings ${put.status}, workspace ${baseline.ws ? "enable" : "disable"} ${ws.status}`);
      await step("S01-rollback", "C", async () => {
        const back = await readSettings(C, "P settings after rollback", `${chatBase(x)}/settings`);
        expect(back.status).toBe(200);
        assertSettingsReadback("S01 rollback P", b, view(back.body));
        expect(await wsExtensionEnabled(C, x, "W extension after rollback"), "workspace switch is back").toBe(baseline.ws);
        const lim = await readSettings(C, "W limits after rollback", `/api/v1/workspaces/${x.workspaceId}/chat/settings`);
        assertLimitsReadback("S01 rollback W limits", baseline.limits, lim.body);
      });
    } finally {
      await Promise.allSettled([A?.close(), C?.close()]);
    }
  });

  test("S01: admin switches the chat on in W and P by clicks; Chat stands right after Docs; a chat-off project has no tab and no API", async () => {
    const c = cfg();
    const agents = [c.agents.claude, c.agents.codex];

    await step("S01-given", "C", async () => {
      // Given: the extension is off in W and P (fixture setup through the API, read back).
      givenSet = true;
      const b = view(baseline.project);
      const put = await C.api("PUT", `${chatBase(x)}/settings`, { enabled: false, agents: [], allowed_roles: b.allowed_roles });
      expect(put.status, `PUT P off: ${put.text.slice(0, 200)}`).toBe(200);
      const dis = await C.api("POST", `/api/v1/workspaces/${x.workspaceId}/extensions/${EXT}/disable`, { confirm: true });
      expect(dis.status, `workspace disable: ${dis.text.slice(0, 200)}`).toBeLessThan(300);
      expect(await wsExtensionEnabled(C, x, "W extension after Given"), "the workspace switch is off").toBe(false);
      const p = await readSettings(C, "P settings after Given", `${chatBase(x)}/settings`);
      expect(view(p.body).enabled).toBe(false);
      assertNoTab(await projectStrip(A, x.wsSlug, x.projectSlug), "chat");
      const t = await A.api("POST", `${chatBase(x)}/threads`, { agent_id: c.agents.claude, title: "S01-given" });
      assertChatOff("S01 create thread with the chat off in P", t.status, t.text);
    });

    await step("S01-workspace", "C", async () => {
      // Workspace settings → Extensions → Installed → Chat with Agent → on.
      await C.page.goto(`/w/${x.wsSlug}`);
      await C.page.locator(`a[href="/w/${x.wsSlug}/extensions"]`).first().click({ timeout: 30_000 });
      await C.page.locator("#extensions-tab-installed").click({ timeout: 30_000 });
      const sw = C.page.locator(`li[data-ext="${EXT}"] [role="switch"]`);
      await expect(sw, "the chat is off in the workspace list").toHaveAttribute("aria-checked", "false", { timeout: 30_000 });
      const enabled = C.page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith(`/extensions/${EXT}/enable`), { timeout: 30_000 });
      await sw.click();
      const res = await enabled;
      expect(res.status(), "workspace enable answers 2xx").toBeLessThan(300);
      await expect(sw).toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
      await C.page.reload();
      await expect(C.page.locator(`li[data-ext="${EXT}"] [role="switch"]`), "on after a reload").toHaveAttribute("aria-checked", "true", { timeout: 30_000 });
      expect(await wsExtensionEnabled(C, x, "W extension after the click"), "the server has it on").toBe(true);
    });

    await step("S01-project", "C", async () => {
      // Project settings → Extensions → Chat with Agent: on, both agents, roles, Save.
      const before = await readSettings(C, "P settings before Save", `${chatBase(x)}/settings`);
      await C.page.goto(`/w/${x.wsSlug}/p/${x.projectSlug}/settings`);
      await C.page.getByRole("button", { name: /^(Extensions|Расширения)$/ }).click({ timeout: 30_000 });
      await C.page.getByTestId(`ext-tab-${EXT}`).click({ timeout: 30_000 });
      const card = C.page.getByTestId("chat-settings");
      await expect(card).toBeVisible({ timeout: 30_000 });
      const enable = card.getByTestId("chat-settings-enabled");
      await expect(enable).not.toBeChecked();
      await enable.check();

      // Negative control: on with no agent cannot be saved here, and the API refuses it without storing.
      for (const box of await card.locator('[data-testid^="chat-settings-agent-"]').all()) await box.uncheck();
      await expect(card.getByTestId("chat-settings-save"), "Save is off while no agent is picked").toBeDisabled();
      await expect(card.getByRole("status")).toBeVisible();
      const empty = await C.api("PUT", `${chatBase(x)}/settings`, { enabled: true, agents: [], allowed_roles: ROLES });
      const afterEmpty = await readSettings(C, "P settings after the empty-agents PUT", `${chatBase(x)}/settings`);
      assertEmptyAgentsRejected(empty.status, view(before.body), view(afterEmpty.body));
      assertNoTab(await projectStrip(A, x.wsSlug, x.projectSlug), "chat");

      for (const id of agents) await card.getByTestId(`chat-settings-agent-${id}`).check({ timeout: 30_000 });
      for (const r of ["admin", "member", "viewer"]) {
        const box = card.getByTestId(`chat-settings-role-${r}`);
        if (ROLES.includes(r)) await box.check();
        else await box.uncheck();
      }
      const saved = C.page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith(`/projects/${x.projectId}/chat/settings`), { timeout: 30_000 });
      await card.getByTestId("chat-settings-save").click();
      const res = await saved;
      expect(res.status(), `Save answers 200: ${(await res.text()).slice(0, 200)}`).toBe(200);
      C.f1.verify("C/S01-save");

      await C.page.reload();
      const again = C.page.getByTestId("chat-settings");
      await expect(again).toBeVisible({ timeout: 30_000 });
      await expect(again.getByTestId("chat-settings-enabled"), "on after a reload").toBeChecked();
      for (const id of agents) await expect(again.getByTestId(`chat-settings-agent-${id}`)).toBeChecked();
      await expect(again.getByTestId(`chat-settings-agent-${c.agents.offline}`)).not.toBeChecked();
      await expect(again.getByTestId("chat-settings-role-viewer")).not.toBeChecked();

      const back = await readSettings(C, "P settings after Save", `${chatBase(x)}/settings`);
      assertSettingsReadback("S01 P readback", { enabled: true, agents, allowed_roles: ROLES }, view(back.body));
      const lim = await readSettings(C, "W limits after Save", `/api/v1/workspaces/${x.workspaceId}/chat/settings`);
      assertLimitsReadback("S01 W limits", baseline.limits, lim.body);
    });

    await step("S01-tab", "A", async () => {
      const order = await projectStrip(A, x.wsSlug, x.projectSlug);
      console.log(`[S01] strip of ${x.projectSlug} for A: ${order.join(",")}`);
      assertTabAfter(order, "docs", "chat");
      await expect(A.page.locator('button[data-tab-id="chat"]').filter({ visible: true }).first(), "Chat is in the strip at 1440").toBeVisible();
      await A.page.locator('button[data-tab-id="chat"]').click();
      await expect(A.page).toHaveURL(new RegExp(`${chatUrl(x)}$`));
      // Frames of the strip with Chat after Docs at 1440 and 393; at 393 Chat may sit in the "…" menu, opened for the shot.
      const menuIfHidden = async () => {
        if (await A.page.locator('button[data-tab-id="chat"]').isVisible()) return;
        await A.page.locator(".mesh-project-tabs button[aria-haspopup]").filter({ visible: true }).first().click();
        await expect(A.page.locator('[role="menuitem"][data-tab-id="chat"]')).toBeVisible();
      };
      await snap(A, "s01-chat-tab", menuIfHidden, async () => { await A.page.keyboard.press("Escape"); }, [], async (where) => {
        assertTabAfter(await tabOrder(A.page), "docs", "chat");
        const shown = A.page.locator('button[data-tab-id="chat"], [role="menuitem"][data-tab-id="chat"]').filter({ visible: true });
        if ((await shown.count()) === 0) throw new Error(`${where}: the Chat tab is not visible in the strip or the menu`);
      });
    });

    await step("S01-off-project", "A", async () => {
      assertNoTab(await projectStrip(A, x.wsSlug, off.slug), "chat");
      await A.page.goto(`/w/${x.wsSlug}/p/${off.slug}/chat`);
      await expect(A.page.getByTestId("chat-disabled"), "the direct route shows the chat as off").toBeVisible({ timeout: 30_000 });
      await expect(A.page.getByTestId("chat-composer")).toHaveCount(0);
      const t = await A.api("POST", `/api/v1/projects/${off.id}/chat/threads`, { agent_id: c.agents.claude, title: "S01-off" });
      assertChatOff("S01 create thread in the chat-off project", t.status, t.text);
      const list = await A.api("GET", `/api/v1/projects/${off.id}/chat/threads`);
      assertChatOff("S01 list threads in the chat-off project", list.status, list.text);
      A.f1.verify("A/S01");
    });
  });
});
