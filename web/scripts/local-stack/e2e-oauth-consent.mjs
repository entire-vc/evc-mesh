#!/usr/bin/env node
/**
 * Authed end-to-end run of the OAuth consent flow (`e2e-scenarios/mcp-oauth-consent.md`).
 *
 * Real browser, real API, real database — no mocked internal layer. The only
 * thing stubbed is the *client's* callback listener (a route fulfil on the
 * loopback redirect host), because that listener is the app being connected,
 * not part of Mesh.
 *
 * USAGE
 *   node scripts/local-stack/e2e-oauth-consent.mjs \
 *     --web http://localhost:3007 --api http://localhost:8095 \
 *     --email local-stack@example.test --password LocalStack1 --out-dir ./shots
 *   [--chromium <path>]   use an already-installed Chromium instead of the one
 *                         this Playwright version wants
 *
 * The API must have been started with MESH_BASE_URL equal to --web, so that
 * /oauth/authorize redirects to the consent page on the web origin.
 *
 * Exit code 0 only if every check passed. Every check is unconditional and
 * asserts a RESULT (a code arrived, a token stopped working), not the presence
 * of an element.
 */
import { chromium } from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
import { mkdir } from "node:fs/promises";

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  return i > -1 ? process.argv[i + 1] : fallback;
}
const WEB = arg("web", "http://localhost:3007");
const API = arg("api", "http://localhost:8095");
const EMAIL = arg("email");
const PASSWORD = arg("password");
const OUT = arg("out-dir", "./oauth-consent-shots");
if (!EMAIL || !PASSWORD) {
  console.error("--email and --password are required");
  process.exit(2);
}

const results = [];
function check(name, ok, detail = "") {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? `  — ${detail}` : ""}`);
}

const b64url = (buf) => buf.toString("base64url");
function pkce() {
  const verifier = b64url(randomBytes(32));
  return { verifier, challenge: b64url(createHash("sha256").update(verifier).digest()) };
}

async function dcr(clientName, redirectUri) {
  const res = await fetch(`${API}/oauth/register`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      client_name: clientName,
      redirect_uris: [redirectUri],
      token_endpoint_auth_method: "none",
      grant_types: ["authorization_code", "refresh_token"],
    }),
  });
  const body = await res.json();
  if (res.status !== 201) throw new Error(`DCR failed: ${res.status} ${JSON.stringify(body)}`);
  return body.client_id;
}

function authorizeUrl(clientId, redirectUri, challenge, state) {
  const q = new URLSearchParams({
    client_id: clientId,
    redirect_uri: redirectUri,
    response_type: "code",
    code_challenge: challenge,
    code_challenge_method: "S256",
    scope: "mesh offline_access",
    state,
  });
  return `${API}/oauth/authorize?${q}`;
}

async function tokenRequest(form) {
  const res = await fetch(`${API}/oauth/token`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams(form),
  });
  return { status: res.status, body: await res.json() };
}

async function agentsMe(accessToken) {
  const res = await fetch(`${API}/api/v1/agents/me`, {
    headers: { authorization: `Bearer ${accessToken}` },
  });
  let body = null;
  try {
    body = await res.json();
  } catch {
    /* non-JSON error body */
  }
  return { status: res.status, body };
}

async function setTheme(page, dark) {
  await page.evaluate((d) => {
    localStorage.setItem("theme", d ? "dark" : "light");
    document.documentElement.classList.toggle("dark", d);
  }, dark);
}

async function shoot(page, name) {
  const file = `${OUT}/${name}.png`;
  await page.screenshot({ path: file, fullPage: true });
  console.log(`      shot ${file}`);
}

async function shootAllThemes(page, base) {
  for (const dark of [false, true]) {
    await setTheme(page, dark);
    await shoot(page, `${base}-${dark ? "dark" : "light"}`);
  }
  await setTheme(page, false);
}


// A phone-width shot must come from a page LOADED at that width: AppLayout
// decides rail-vs-drawer on load, so resizing a desktop-loaded page leaves the
// desktop rail overlapping the content and the picture lies about the layout.
async function shootMobile(page, base) {
  await page.setViewportSize({ width: 393, height: 852 });
  await page.reload({ waitUntil: "networkidle" });
  await shootAllThemes(page, base);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.reload({ waitUntil: "networkidle" });
}

(async () => {
  await mkdir(OUT, { recursive: true });
  const browser = await chromium.launch({ executablePath: arg("chromium") });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();

  // F1 fixture: nothing may fail in the background while the flow runs.
  const consoleErrors = [];
  const failedApi = [];
  page.on("pageerror", (e) => consoleErrors.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    // "Failed to load resource" is the browser narrating a non-2xx response;
    // those are judged (with their URL) by the response listener below.
    if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) {
      consoleErrors.push(m.text());
    }
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.origin === new URL(WEB).origin && u.pathname.startsWith("/api/v1/") && r.status() >= 400) {
      // /auth/refresh is probed before anyone is signed in; the negative
      // controls below are direct fetches, not browser traffic.
      if (u.pathname !== "/api/v1/auth/refresh") failedApi.push(`${r.status()} ${u.pathname}`);
    }
  });

  // The connected app's own callback listener: stub it and record what arrives.
  const LOOPBACK_CB = "http://127.0.0.1:5599/callback";
  const HTTPS_CB = "https://client.example.test/cb";
  const arrived = [];
  for (const pattern of ["http://127.0.0.1:5599/**", "https://client.example.test/**"]) {
    await ctx.route(pattern, (route) => {
      arrived.push(route.request().url());
      return route.fulfill({ status: 200, contentType: "text/html", body: "<p>client callback</p>" });
    });
  }

  // ---- 1. signed-out authorize → login → back to consent ----------------------
  const loopbackClient = await dcr("E2E Consent Client", LOOPBACK_CB);
  const flow1 = pkce();
  const state1 = b64url(randomBytes(8));
  await page.goto(authorizeUrl(loopbackClient, LOOPBACK_CB, flow1.challenge, state1));
  await page.waitForURL(/\/login\?redirect=/);
  const redirectParam = new URL(page.url()).searchParams.get("redirect") ?? "";
  check(
    "signed-out user is sent to /login carrying the full authorize query",
    redirectParam.startsWith("/connect/consent?") &&
      redirectParam.includes(`client_id=${encodeURIComponent(loopbackClient)}`) &&
      redirectParam.includes(`state=${state1}`),
    redirectParam.slice(0, 60),
  );

  await page.fill("#email", EMAIL);
  await page.fill("#password", PASSWORD);
  await page.click('button[type="submit"]');
  await page.waitForURL(/\/connect\/consent\?/);
  await page.getByRole("heading", { name: "E2E Consent Client" }).waitFor();

  // ---- 2. what the consent screen shows --------------------------------------
  const host = (await page.getByTestId("redirect-host").textContent())?.trim();
  check("redirect HOST is shown", host === "127.0.0.1:5599", `host=${host}`);
  const loopbackWarning = await page.getByText("This access goes to a program on this computer").count();
  check("loopback warning is shown for an all-loopback client", loopbackWarning === 1);
  const wsOptions = await page.locator("#consent-workspace option").allTextContents();
  check("workspace picker lists the user's workspaces", wsOptions.length >= 1, wsOptions.join(", "));
  await shootAllThemes(page, "consent-loopback-1440");

  await shootMobile(page, "consent-loopback-393");

  // ---- 3. Allow → code in the redirect → token → agent identity --------------
  arrived.length = 0;
  await page.getByRole("button", { name: "Allow" }).click();
  await page.waitForURL(/127\.0\.0\.1:5599\/callback\?/);
  const cb = new URL(arrived.at(-1) ?? page.url());
  const code = cb.searchParams.get("code");
  check("Allow → redirect carries a code", Boolean(code), `code=${code ? code.slice(0, 6) + "…" : "none"}`);
  check("Allow → redirect echoes state", cb.searchParams.get("state") === state1);
  check("Allow → redirect has no error", !cb.searchParams.has("error"));

  const tok = await tokenRequest({
    grant_type: "authorization_code",
    code: code ?? "",
    redirect_uri: LOOPBACK_CB,
    client_id: loopbackClient,
    code_verifier: flow1.verifier,
  });
  const accessToken = tok.body.access_token ?? "";
  const refreshToken = tok.body.refresh_token ?? "";
  check("code exchanges for a mot_ token", tok.status === 200 && accessToken.startsWith("mot_"), `status=${tok.status}`);

  const me = await agentsMe(accessToken);
  check(
    "token authenticates as the connector agent",
    me.status === 200 && /E2E Consent Client/.test(JSON.stringify(me.body)),
    `status=${me.status}`,
  );

  // ---- 4. Deny → access_denied ------------------------------------------------
  const flow2 = pkce();
  const state2 = b64url(randomBytes(8));
  arrived.length = 0;
  await page.goto(authorizeUrl(loopbackClient, LOOPBACK_CB, flow2.challenge, state2));
  await page.waitForURL(/\/connect\/consent\?/);
  await page.getByRole("button", { name: "Deny" }).click();
  await page.waitForURL(/127\.0\.0\.1:5599\/callback\?/);
  const denied = new URL(arrived.at(-1) ?? page.url());
  check("Deny → error=access_denied", denied.searchParams.get("error") === "access_denied");
  check("Deny → state echoed, no code", denied.searchParams.get("state") === state2 && !denied.searchParams.has("code"));

  // ---- 5. a normal (non-loopback) client: host shown, no loopback warning ----
  const httpsClient = await dcr("E2E Web Client", HTTPS_CB);
  const flow3 = pkce();
  await page.goto(authorizeUrl(httpsClient, HTTPS_CB, flow3.challenge, "s3"));
  await page.waitForURL(/\/connect\/consent\?/);
  await page.getByRole("heading", { name: "E2E Web Client" }).waitFor();
  const host2 = (await page.getByTestId("redirect-host").textContent())?.trim();
  check("non-loopback client shows its host", host2 === "client.example.test", `host=${host2}`);
  check(
    "non-loopback client shows NO loopback warning",
    (await page.getByText("This access goes to a program on this computer").count()) === 0,
  );
  await shootAllThemes(page, "consent-web-1440");
  await shootMobile(page, "consent-web-393");

  // ---- 6. invalid request → error page, never a consent form -----------------
  await page.goto(
    `${WEB}/connect/consent?client_id=${encodeURIComponent(loopbackClient)}&redirect_uri=${encodeURIComponent("http://127.0.0.1:5599/other")}&response_type=code&code_challenge=${flow3.challenge}&code_challenge_method=S256`,
  );
  await page.getByText("Can't connect this app").waitFor();
  check("unregistered redirect_uri → error page, no Allow button", (await page.getByRole("button", { name: "Allow" }).count()) === 0);
  await shoot(page, "consent-error-1440-light");
  // Unknown client_id: the API answers 401 invalid_client. That is a bad request,
  // not an expired session; it must NOT bounce the signed-in user to /login.
  await page.goto(
    `${WEB}/connect/consent?client_id=mcpc_does_not_exist&redirect_uri=${encodeURIComponent(LOOPBACK_CB)}&response_type=code&code_challenge=${flow3.challenge}&code_challenge_method=S256`,
  );
  await page.getByText("Can't connect this app").waitFor();
  check(
    "unknown client_id (401 invalid_client) → error page, still on /connect/consent, no Allow",
    new URL(page.url()).pathname === "/connect/consent" &&
      (await page.getByRole("button", { name: "Allow" }).count()) === 0,
    new URL(page.url()).pathname,
  );
  // Those two page loads deliberately provoked one 401 and one 400 from the consent API.
  // Prove the fixture SAW it (so it can go red), then drop it from the tally.
  for (const expected of ["401 /api/v1/oauth/consent", "400 /api/v1/oauth/consent"]) {
    // api() replays a 401 once after a token refresh, so the same provoked
    // request can appear twice; drop every copy but require at least one.
    const seen = failedApi.filter((f) => f === expected).length;
    check(`fixture observed the provoked ${expected.split(" ")[0]} (it is not blind)`, seen > 0, `x${seen}`);
    for (let i = failedApi.indexOf(expected); i > -1; i = failedApi.indexOf(expected)) failedApi.splice(i, 1);
  }

  // ---- 7. Connected apps: listed → revoke → token dies ------------------------
  await page.goto(`${WEB}/`);
  await page.waitForURL(/\/w\/[^/]+\//);
  const slug = new URL(page.url()).pathname.split("/")[2];
  await page.goto(`${WEB}/w/${slug}/connected-apps`);
  await page.getByText("E2E Consent Client").first().waitFor();
  const activeRows = await page.getByRole("button", { name: "Revoke access" }).count();
  check("Connected apps lists the granted app with a Revoke button", activeRows >= 1, `rows=${activeRows}`);
  await shootAllThemes(page, "connected-apps-1440");
  await shootMobile(page, "connected-apps-393");

  await page.getByRole("button", { name: "Revoke access" }).first().click();
  await page.getByText(/will stop working in .* immediately/).waitFor();
  await shoot(page, "connected-apps-revoke-confirm-1440-light");
  // Wait for the DELETE itself, not for the word "Revoked": earlier runs leave
  // revoked rows on this page, so that text is already there before the click.
  const [deleteRes] = await Promise.all([
    page.waitForResponse((r) => r.request().method() === "DELETE" && r.url().includes("/api/v1/oauth/grants/")),
    page.getByRole("button", { name: "Revoke access" }).last().click(),
  ]);
  check("revoke DELETE /api/v1/oauth/grants/:id answers 204", deleteRes.status() === 204, `status=${deleteRes.status()}`);

  const after = await agentsMe(accessToken);
  check("after revoke: the access token gets 401 on GET /api/v1/agents/me", after.status === 401, `status=${after.status}`);
  const refreshed = await tokenRequest({ grant_type: "refresh_token", refresh_token: refreshToken, client_id: loopbackClient });
  check("after revoke: the refresh token is dead too (invalid_grant)", refreshed.status === 400 && refreshed.body.error === "invalid_grant", `status=${refreshed.status} error=${refreshed.body.error}`);
  await shoot(page, "connected-apps-after-revoke-1440-light");

  // ---- F1 fixture -------------------------------------------------------------
  check("zero failed /api/v1 responses in the browser", failedApi.length === 0, failedApi.join("; "));
  check("zero console errors / pageerrors", consoleErrors.length === 0, consoleErrors.slice(0, 3).join(" | "));

  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
  process.exit(failed.length ? 1 : 0);
})().catch((e) => {
  console.error("RUN ABORTED:", e);
  process.exit(1);
});
