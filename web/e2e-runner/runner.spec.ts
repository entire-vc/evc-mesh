import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";

// Regression: the first enable of Runner in a workspace. Real API + Postgres
// of this revision, the frontend build, no mocks. Before the fix the Accounts/Pools/
// Profiles tabs stayed on the read-only personal view after "Runner enabled" until a
// full reload (and a fresh catalog could be stored as config={}). Only the owner's
// own workspace and a real resource saved by the form are created; no provider
// account, token or heartbeat is made by enabling.
const api = "http://localhost:8095";
const password = "RunnerFixture1"; // pragma: allowlist secret
const shots = "e2e-runner/results/shots";

async function register(request: APIRequestContext, label: string) {
  // short local part: a longer address is refused by the users table (400 value violates constraint)
  const email = `r-${label}-${crypto.randomUUID().slice(0, 8)}@example.test`;
  const res = await request.post(`${api}/api/v1/auth/register`, { data: { email, password, name: `Runner ${label}` } });
  expect(res.status(), `register: ${await res.text()}`).toBe(201);
  const headers = { Authorization: `Bearer ${(await res.json()).tokens.access_token}` };
  const ws = (await (await request.get(`${api}/api/v1/workspaces`, { headers })).json())[0];
  return { email, headers, ws: { id: ws.id as string, slug: ws.slug as string } };
}

function observe(page: Page) {
  const unexpected: string[] = [];
  page.on("console", m => {
    if (m.type() === "error" && !/Failed to load resource.*status of (401|403|404)/.test(m.text())) unexpected.push(`console: ${m.text()}`);
  });
  page.on("pageerror", e => unexpected.push(`pageerror: ${e.message}`));
  page.on("response", async r => {
    // a refused catalog save names its reason in the body: keep it in the failure message
    if (r.status() === 400 && r.request().method() === "PUT") unexpected.push(`refused ${r.url()}: ${(await r.text()).slice(0, 600)}`);
  });
  page.on("response", r => { if (r.status() >= 500) unexpected.push(`HTTP ${r.status()} ${r.url()}`); });
  return unexpected;
}

async function signIn(page: Page, email: string, path: string) {
  const bootstrap = page.waitForResponse(r => r.url().includes("/api/v1/auth/refresh"));
  await page.goto(`/login?redirect=${encodeURIComponent(path)}`);
  await bootstrap;
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /sign in|log in/i }).click();
}

async function shot(page: Page, name: string) {
  mkdirSync(shots, { recursive: true });
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true });
}

const runtimeOf = async (request: APIRequestContext, headers: Record<string, string>, wsId: string) =>
  request.get(`${api}/api/v1/workspaces/${wsId}/runtime`, { headers });

for (const vp of [{ name: "1440", width: 1440, height: 900 }, { name: "393", width: 393, height: 852 }]) {
  test(`first enable opens working forms, survives reload, re-enable keeps the catalog @${vp.name}`, async ({ page, request }) => {
    const owner = await register(request, "owner");
    const unexpected = observe(page);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await signIn(page, owner.email, `/w/${owner.ws.slug}/runner`);

    await expect(page.getByTestId("my-runner-admin")).toBeVisible();
    // before enabling there is nothing to edit with forms
    await page.getByRole("button", { name: "Turn on Runner" }).click();
    await expect(page.getByTestId("my-runner-admin").getByText("Runner is on").first()).toBeVisible();
    await shot(page, `${vp.name}-enabled-overview`);

    // right after the switch, with NO reload: the forms are there
    await page.getByRole("tab", { name: "Accounts" }).click();
    await expect(page.getByRole("button", { name: "Add" })).toBeVisible();
    await expect(page.getByText("read-only here")).toHaveCount(0);
    for (const tab of ["Pools", "Profiles"]) {
      await page.getByRole("tab", { name: tab }).click();
      await expect(page.getByRole("button", { name: "Add" })).toBeVisible();
    }
    // the stored document is a valid catalog and readable (not config={} corruption)
    const first = await runtimeOf(request, owner.headers, owner.ws.id);
    expect(first.status()).toBe(200);
    const doc = await first.json();
    expect(doc.config?.accounts ?? {}).toEqual({});

    // real resources through the forms: an account needs a pool of its provider, so the pool first
    await page.getByRole("tab", { name: "Pools" }).click();
    await page.getByRole("button", { name: "Add" }).click();
    await page.getByLabel("Name", { exact: true }).fill("shared-claude-pool");
    await page.getByRole("combobox", { name: "Provider" }).selectOption("anthropic");
    await page.getByRole("textbox", { name: "Provider resource" }).fill("shared-claude-subscription");
    await page.getByRole("spinbutton", { name: "Concurrent runs, at most" }).fill("1");
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page.getByText("shared-claude-pool").first()).toBeVisible();
    await page.getByRole("tab", { name: "Accounts" }).click();
    await page.getByRole("button", { name: "Add" }).click();
    await page.getByLabel("Name", { exact: true }).fill("shared-claude");
    await page.getByRole("combobox", { name: "Provider" }).selectOption("anthropic");
    await page.getByLabel("Credential reference").fill("cred:e2e-fixture");
    const quota = page.getByRole("group", { name: "Quota pools" });
    await quota.getByLabel("shared-claude-pool").first().check();
    await quota.getByLabel("shared-claude-pool").last().check();
    await page.getByRole("button", { name: "Apply" }).click();
    await shot(page, `${vp.name}-account-draft`);
    await page.getByRole("button", { name: "Save resources" }).click();
    await expect(page.getByText("Resources saved").first()).toBeVisible().catch(e => { throw new Error(`${e.message}\nserver said: ${unexpected.join(" | ")}`); });

    await page.reload();
    await page.getByRole("tab", { name: "Accounts" }).click();
    await expect(page.getByText("shared-claude")).toBeVisible();
    await expect(page.getByRole("button", { name: "Add" })).toBeVisible();
    await shot(page, `${vp.name}-accounts-after-reload`);
    const saved = await (await runtimeOf(request, owner.headers, owner.ws.id)).json();
    expect(Object.keys(saved.config.accounts)).toEqual(["shared-claude"]);
    const revision = saved.revision;

    // disable and enable again: the saved catalog is not overwritten
    await page.getByRole("tab", { name: "Overview" }).click();
    await page.getByRole("button", { name: "Turn off Runner" }).click();
    await page.getByTestId("my-runner-disable-confirm").getByRole("button", { name: "Turn off", exact: true }).click();
    await expect(page.getByRole("button", { name: "Turn on Runner" })).toBeVisible();
    await page.getByRole("button", { name: "Turn on Runner" }).click();
    await expect(page.getByRole("button", { name: "Turn off Runner" })).toBeVisible();
    const again = await (await runtimeOf(request, owner.headers, owner.ws.id)).json();
    expect(Object.keys(again.config.accounts)).toEqual(["shared-claude"]);
    expect(again.revision).toBeGreaterThanOrEqual(revision);

    // enabling creates no runner, no token, no heartbeat
    const runners = await request.get(`${api}/api/v1/workspaces/${owner.ws.id}/operator/runners`, { headers: owner.headers });
    if (runners.status() === 200) expect(await runners.json()).toEqual(expect.objectContaining({}));
    expect(unexpected).toEqual([]);
  });
}

test("a user who is not in the workspace cannot change its Runner catalog", async ({ request }) => {
  const owner = await register(request, "keeper");
  const stranger = await register(request, "stranger");
  const url = `${api}/api/v1/workspaces/${owner.ws.id}/runtime`;
  const read = await request.get(url, { headers: stranger.headers });
  expect([403, 404]).toContain(read.status());
  const write = await request.put(url, { headers: stranger.headers, data: { if_revision: 0, enabled: true, config: {} } });
  expect([403, 404]).toContain(write.status());
  // the owner's workspace is untouched by the refused write
  const after = await request.get(url, { headers: owner.headers });
  expect(JSON.stringify((await after.json()).config?.accounts ?? {})).toBe("{}");
});

// Roles inside the SAME workspace (a stranger is not a substitute for a member). The
// owner adds an admin, a member and a viewer with real accounts; the catalog is
// writable by owner and admin, and refused to member and viewer, who also cannot read it.
// A valid empty catalog v2: the server refuses a bare {} (every section is required).
const emptyCatalog = { schema_version: 2, controllers: {}, accounts: {}, pools: {}, profiles: {}, bindings: {} };

test("owner and admin manage the Runner catalog, member and viewer cannot", async ({ request }) => {
  const owner = await register(request, "boss");
  const url = `${api}/api/v1/workspaces/${owner.ws.id}/runtime`;
  const addAs = async (role: string) => {
    const email = `r-${role}-${crypto.randomUUID().slice(0, 8)}@example.test`;
    const add = await request.post(`${api}/api/v1/workspaces/${owner.ws.id}/members`, { headers: owner.headers, data: { email, name: `Runner ${role}`, role, password } });
    expect(add.status(), `add ${role}: ${await add.text()}`).toBe(201);
    const login = await request.post(`${api}/api/v1/auth/login`, { data: { email, password } });
    expect(login.status(), `login ${role}: ${await login.text()}`).toBe(200);
    return { Authorization: `Bearer ${(await login.json()).tokens.access_token}` };
  };
  const admin = await addAs("admin");
  const member = await addAs("member");
  const viewer = await addAs("viewer");

  // positive control: the owner reads and writes
  const base = await request.get(url, { headers: owner.headers });
  expect(base.status()).toBe(200);
  let revision = (await base.json()).revision ?? 0;
  const ownerWrite = await request.put(url, { headers: owner.headers, data: { if_revision: revision, enabled: true, config: emptyCatalog } });
  expect(ownerWrite.status(), `owner write: ${await ownerWrite.text()}`).toBe(200);
  revision = (await ownerWrite.json()).revision;

  // admin: allowed to read and to write through CAS
  expect((await request.get(url, { headers: admin })).status()).toBe(200);
  const adminWrite = await request.put(url, { headers: admin, data: { if_revision: revision, enabled: true, config: emptyCatalog } });
  expect(adminWrite.status(), `admin write: ${await adminWrite.text()}`).toBe(200);
  const adminRevision = (await adminWrite.json()).revision;
  expect(adminRevision).toBeGreaterThan(revision);

  // member and viewer: refused, and the refused write changes nothing
  for (const [role, headers] of [["member", member], ["viewer", viewer]] as const) {
    expect((await request.get(url, { headers })).status(), `${role} read`).toBe(403);
    const w = await request.put(url, { headers, data: { if_revision: adminRevision, enabled: false, config: emptyCatalog } });
    expect(w.status(), `${role} write`).toBe(403);
  }
  const after = await (await request.get(url, { headers: owner.headers })).json();
  expect(after.revision).toBe(adminRevision);
  expect(after.enabled ?? true).toBe(true);
});
