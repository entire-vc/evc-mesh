import { test, expect, type APIRequestContext, type Page } from "@playwright/test";

// Mesh Doc e2e/appearance-first-login. Real API + Postgres on the job's own stack.
const endpoint = "/api/v1/me/ui-state/appearance";
const password = "AppearanceFixture1"; // pragma: allowlist secret

async function seed(request: APIRequestContext) {
  const email = `appearance-${crypto.randomUUID()}@example.test`;
  const registered = await request.post("http://localhost:8095/api/v1/auth/register", {
    data: { email, password, name: "Appearance fixture" },
  });
  expect(registered.status()).toBe(201);
  const auth = await registered.json();
  const headers = { Authorization: `Bearer ${auth.tokens.access_token}` };
  const workspaces = await request.get("http://localhost:8095/api/v1/workspaces", { headers });
  expect(workspaces.status()).toBe(200);
  const ws = (await workspaces.json())[0];
  expect(ws.slug).toBeTruthy();
  return { email, headers, profile: `/w/${ws.slug}/me`, dashboard: `/w/${ws.slug}/dashboard` };
}

function observe(page: Page) {
  const errors: string[] = [];
  page.on("console", m => { if (m.type() === "error") errors.push(`console: ${m.text()}`); });
  page.on("pageerror", e => errors.push(`pageerror: ${e.message}`));
  page.on("response", r => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url()}`); });
  page.on("requestfailed", r => errors.push(`requestfailed: ${r.url()}`));
  return errors;
}

async function loginForm(page: Page, email: string, profile: string, local?: string) {
  // The scope is errors AFTER login. The anonymous bootstrap refresh happens
  // before the form is submitted and is outside this appearance regression.
  if (local) await page.addInitScript(value => localStorage.setItem("theme", value), local);
  const bootstrap = page.waitForResponse(r => r.url().includes("/api/v1/auth/refresh"));
  await page.goto(`/login?redirect=${encodeURIComponent(profile)}`);
  await bootstrap;
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
}

for (const status of [404, 500]) {
  test(`RED control: Chromium exposes appearance GET ${status}`, async ({ page, request }) => {
    const user = await seed(request);
    await loginForm(page, user.email, user.profile);
    // mock: HTTP boundary — intentionally replay the bad response only to
    // prove the console detector. All green scenarios use the real endpoint.
    await page.route(`**${endpoint}`, route => route.fulfill({ status, json: { error: "control" } }));
    const errors = observe(page);
    const response = page.waitForResponse(r => new URL(r.url()).pathname === endpoint);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    expect((await response).status()).toBe(status);
    await expect.poll(() => errors.some(e => e.startsWith("console:") && e.includes(String(status)))).toBe(true);
    if (status === 500) expect(errors.some(e => e.startsWith("HTTP 500"))).toBe(true);
    console.log(`RED control ${status}: strict zero-error gate rejects ${JSON.stringify(errors)}`);
  });
}

for (const width of [1440, 393]) {
  for (const saved of [false, true]) {
    test(`${width}: ${saved ? "saved dark/compact" : "unset system"} survives login, navigation and reload`, async ({ page, request }, info) => {
      const user = await seed(request);
      let original: unknown;
      if (saved) {
        const put = await request.put(`http://localhost:8095${endpoint}`, {
          headers: user.headers, data: { state: { mode: "dark", density: "compact" } },
        });
        expect(put.status()).toBe(200);
        original = await put.json();
      }
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ colorScheme: saved ? "light" : "dark" });
      await loginForm(page, user.email, user.profile);
      const errors = observe(page);
      const appearance = page.waitForResponse(r => new URL(r.url()).pathname === endpoint);
      await page.getByRole("button", { name: "Sign in", exact: true }).click();
      expect((await appearance).status()).toBe(200);
      const check = async () => {
        await expect(page.getByRole("radio", { name: saved ? "Dark" : "System", exact: true })).toHaveAttribute("aria-checked", "true");
        await expect(page.locator("html")).toHaveClass(/dark/);
        if (saved) await expect(page.locator("html")).toHaveAttribute("data-density", "compact");
        const get = await request.get(`http://localhost:8095${endpoint}`, { headers: user.headers });
        expect(get.status()).toBe(200);
        expect(await get.json()).toEqual(saved ? original : { state: {} });
        expect(errors).toEqual([]);
      };
      await check();
      await page.screenshot({ path: info.outputPath(`${width}-${saved ? "saved" : "system"}.png`), fullPage: true });
      await page.goto(user.dashboard);
      await expect(page.locator("main")).toBeVisible();
      await page.goto(user.profile);
      await check();
      await page.reload();
      await check();
      if (!saved) {
        await page.emulateMedia({ colorScheme: "light" });
        await expect(page.locator("html")).not.toHaveClass(/dark/);
      }
      expect(errors).toEqual([]);
      console.log(`GREEN ${width} ${saved ? "saved dark/compact" : "unset system"}: login/navigation/reload; console/pageerror/5xx/requestfailed=0`);
    });
  }
}

test("an explicit local choice uploads when appearance is unset", async ({ page, request }) => {
  const user = await seed(request);
  await page.emulateMedia({ colorScheme: "dark" });
  await loginForm(page, user.email, user.profile, "light");
  const errors = observe(page);
  const saved = page.waitForResponse(r => new URL(r.url()).pathname === endpoint && r.request().method() === "PUT");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  expect((await saved).status()).toBe(200);
  await expect(page.getByRole("radio", { name: "Light", exact: true })).toHaveAttribute("aria-checked", "true");
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  const get = await request.get(`http://localhost:8095${endpoint}`, { headers: user.headers });
  expect(get.status()).toBe(200);
  expect(await get.json()).toMatchObject({ state: { mode: "light" } });
  expect(errors).toEqual([]);
});
