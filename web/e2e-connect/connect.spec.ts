import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";

// Mesh an internal ticket: the normal Connect wizard for a catalog external extension
// configured only at instance level. Real API + Postgres on the job's own stack,
// built with -tags e2eloopback so the SSRF guard lets the synthetic extension
// service (the external boundary) on loopback in. Connect, storage and the
// resolver are the real ones. Before the fix "Next" skipped POST /connect, no
// workspace connection was created and the check failed with "not connected".
const api = "http://localhost:8095";
const password = "ConnectFixture1"; // pragma: allowlist secret
const shots = "e2e-connect/results/shots";

async function seed(request: APIRequestContext) {
  const email = `connect-${crypto.randomUUID()}@example.test`;
  const registered = await request.post(`${api}/api/v1/auth/register`, { data: { email, password, name: "Connect fixture" } });
  expect(registered.status()).toBe(201);
  const headers = { Authorization: `Bearer ${(await registered.json()).tokens.access_token}` };
  const workspaces = await request.get(`${api}/api/v1/workspaces`, { headers });
  expect(workspaces.status()).toBe(200);
  const ws = (await workspaces.json())[0];
  return { email, headers, ws: { id: ws.id as string, slug: ws.slug as string } };
}

// success-path observer: anything unexpected after the form is submitted. The
// intentional refusal of the connect call is collected apart, never mixed in.
function observe(page: Page) {
  const unexpected: string[] = [];
  const refusals: string[] = [];
  // The wizard asks GET /extensions/instance/overview and tolerates 403 for a
  // user who is not an instance admin (every fixture user). Existing behaviour,
  // named here so it never hides another error.
  const probes: string[] = [];
  const isConnect = (url: string) => /\/extensions\/[^/]+\/connect$/.test(new URL(url).pathname);
  page.on("console", m => {
    if (m.type() !== "error") return;
    // the browser logs a refused request as a bare "Failed to load resource";
    // the 4xx itself is classified by its own response event below
    const bucket = /Failed to load resource.*status of 400/.test(m.text()) ? refusals : /Failed to load resource.*status of 403/.test(m.text()) ? probes : unexpected;
    bucket.push(`console: ${m.text()}`);
  });
  page.on("pageerror", e => unexpected.push(`pageerror: ${e.message}`));
  page.on("response", r => {
    if (r.status() >= 500) unexpected.push(`HTTP ${r.status()} ${r.url()}`);
    else if (r.status() >= 400 && isConnect(r.url())) refusals.push(`HTTP ${r.status()} ${r.url()}`);
    else if (r.status() === 403 && new URL(r.url()).pathname === "/api/v1/extensions/instance/overview") probes.push(`HTTP 403 ${r.url()}`);
    else if (r.status() >= 400) unexpected.push(`HTTP ${r.status()} ${r.url()}`);
  });
  page.on("requestfailed", r => unexpected.push(`requestfailed: ${r.url()}`));
  return { unexpected, refusals, probes };
}

async function signIn(page: Page, email: string, path: string, dark: boolean) {
  await page.addInitScript(v => localStorage.setItem("theme", v), dark ? "dark" : "light");
  await page.emulateMedia({ colorScheme: dark ? "dark" : "light" });
  const bootstrap = page.waitForResponse(r => r.url().includes("/api/v1/auth/refresh"));
  await page.goto(`/login?redirect=${encodeURIComponent(path)}`);
  await bootstrap;
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
}

async function shot(page: Page, name: string) {
  mkdirSync(shots, { recursive: true });
  await page.screenshot({ path: `${shots}/${name}.png`, fullPage: true });
}

async function workspaceItem(request: APIRequestContext, headers: Record<string, string>, wsId: string, ext: string) {
  const res = await request.get(`${api}/api/v1/workspaces/${wsId}/extensions`, { headers });
  expect(res.status()).toBe(200);
  const list = ((await res.json()) as { extensions: Array<{ id: string; connection: { consented_capabilities: string[] } | null; status: { enabled: boolean; source: string; last_error?: string; health?: { ok: boolean; message: string } } }> }).extensions;
  return list.find(e => e.id === ext);
}

const forms = [
  { id: "keep", name: "Keep", form: "workspace fields present" },
  { id: "keep-rotator", name: "Keep rotator", form: "no workspace fields" },
];

for (const f of forms) {
  for (const width of [1440, 393]) {
    for (const dark of [false, true]) {
      const tag = `${f.id}-${width}-${dark ? "dark" : "light"}`;
      test(`connect ${f.id} (${f.form}) ${width} ${dark ? "dark" : "light"}: consent, active connection, healthy`, async ({ page, request }) => {
        const user = await seed(request);
        const path = `/w/${user.ws.slug}/extensions/${f.id}/connect`;
        await page.setViewportSize({ width, height: 900 });
        await signIn(page, user.email, path, dark);
        const seen = observe(page);
        await page.getByRole("button", { name: "Sign in", exact: true }).click();

        // before: instance-configured, no workspace connection yet
        await expect(page.getByRole("heading", { name: `Connect ${f.name}` })).toBeVisible();
        expect((await workspaceItem(request, user.headers, user.ws.id, f.id))?.connection ?? null, "no connection before").toBeNull();
        await expect(page.locator("html")).toHaveClass(dark ? /dark/ : /^(?!.*dark)/);

        // consent comes before submit
        const nextBtn = page.getByRole("button", { name: "Next", exact: true });
        await expect(nextBtn).toBeDisabled();
        await shot(page, `${tag}-1-before-consent`);
        await page.getByRole("checkbox").check();
        await expect(nextBtn).toBeEnabled();
        await shot(page, `${tag}-2-consent-given`);
        await nextBtn.click();
        await expect(page.getByText("Configured by the instance administrator")).toBeVisible();
        await shot(page, `${tag}-3-settings-inherited`);

        // Next on the inherited settings performs the connect, once
        const connect = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname === `/api/v1/workspaces/${user.ws.id}/extensions/${f.id}/connect`);
        await page.getByRole("button", { name: "Next", exact: true }).click();
        const res = await connect;
        expect(res.status(), "connect status").toBeLessThan(300);
        expect(res.request().postDataJSON().consent.capabilities).toEqual(["api:tasks:read"]);
        await expect(page.getByRole("heading", { name: "Check the connection" })).toBeVisible();

        // the right workspace and provider now have an active connection
        const item = await workspaceItem(request, user.headers, user.ws.id, f.id);
        expect(item?.connection, "workspace connection exists").not.toBeNull();
        expect(item?.connection?.consented_capabilities).toEqual(["api:tasks:read"]);
        expect(item?.status).toMatchObject({ enabled: true, source: "workspace" });

        // install handshake + health through the UI: healthy, nothing in last_error
        await page.getByRole("button", { name: "Check connection" }).click();
        await expect(page.getByText("Connection works.")).toBeVisible();
        const health = await request.post(`${api}/api/v1/workspaces/${user.ws.id}/extensions/${f.id}/test`, { headers: user.headers });
        expect(await health.json()).toMatchObject({ ok: true });
        const after = await workspaceItem(request, user.headers, user.ws.id, f.id);
        expect(after?.status.health?.ok, "health.ok").toBe(true);
        expect(after?.status.last_error ?? "", "last_error").toBe("");
        await shot(page, `${tag}-4-success-checked`);
        await page.getByRole("button", { name: "Next", exact: true }).click();
        await expect(page.getByText(`${f.name} is connected`)).toBeVisible();
        await shot(page, `${tag}-5-done`);

        expect(seen.unexpected, "no console.error / pageerror / 5xx on the success path").toEqual([]);
        expect(seen.refusals).toEqual([]);
        expect(seen.probes.every(p => p.startsWith("console:") || p.includes("/extensions/instance/overview"))).toBe(true);
        console.log(`assert ${tag}: connect ${res.status()}, connection active, health.ok=true, last_error="", unexpected=0`);
      });
    }
  }
}

for (const width of [1440, 393]) {
  for (const dark of [false, true]) {
    const tag = `keep-refused-${width}-${dark ? "dark" : "light"}`;
    test(`refusal ${width} ${dark ? "dark" : "light"}: an invalid instance address is refused, the wizard stays on the step`, async ({ page, request }) => {
      const user = await seed(request);
      const path = `/w/${user.ws.slug}/extensions/keep-refused/connect`;
      await page.setViewportSize({ width, height: 900 });
      await signIn(page, user.email, path, dark);
      const seen = observe(page);
      await page.getByRole("button", { name: "Sign in", exact: true }).click();
      await expect(page.getByRole("heading", { name: "Connect Keep refused" })).toBeVisible();
      await page.getByRole("checkbox").check();
      await page.getByRole("button", { name: "Next", exact: true }).click();
      await expect(page.getByText("Configured by the instance administrator")).toBeVisible();

      const connect = page.waitForResponse(r => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/extensions/keep-refused/connect"));
      await page.getByRole("button", { name: "Next", exact: true }).click();
      const res = await connect;
      expect(res.status(), "connect refusal status").toBe(400);

      // stays on the settings step with an error; no connection, no check step
      await expect(page.getByRole("alert")).toBeVisible();
      await expect(page.getByText("Configured by the instance administrator")).toBeVisible();
      await expect(page.getByRole("heading", { name: "Check the connection" })).toHaveCount(0);
      expect((await workspaceItem(request, user.headers, user.ws.id, "keep-refused"))?.connection ?? null, "no connection after refusal").toBeNull();
      await shot(page, `${tag}-refusal`);

      expect(seen.unexpected, "only the intentional refusal is an error").toEqual([]);
      expect(seen.refusals.some(r => r.startsWith("HTTP 400"))).toBe(true);
      console.log(`assert ${tag}: connect 400, stays on settings, no connection; intentional refusal=${JSON.stringify(seen.refusals)}`);
    });
  }
}
