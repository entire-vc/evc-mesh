import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { request, chromium } from "@playwright/test";
import { OUT_DIR, SetupFail, cfg, missingEnv } from "./lib/env";
import { chatBase, openActor, resolveCtx } from "./lib/harness";

/**
 * Preflight and the settings window. Everything unavailable is FAIL SETUP:
 * a green run over a missing fixture is the failure this suite exists to stop.
 */
export default async function globalSetup() {
  // Local run of the pure detector controls only (project "controls"); CI never sets this.
  if (process.env.AGENT_CHAT_E2E_CONTROLS_ONLY === "1") return;
  mkdirSync(OUT_DIR, { recursive: true });
  const missing = missingEnv();
  if (missing.length > 0) {
    throw new SetupFail(`missing CI variables: ${missing.join(", ")} (fixtures A/B/C/V and agents: an internal ticket, variables: Robin)`);
  }
  const c = cfg();

  // Target manifest: the published SHA the run is about.
  const anon = await request.newContext({ baseURL: c.baseURL });
  const vres = await anon.get("/api/v1/version");
  if (vres.status() !== 200) throw new SetupFail(`/api/v1/version answered ${vres.status()}`);
  const version = (await vres.json()) as { commit?: string; version?: string; build_time?: string };
  await anon.dispose();
  if (!version.commit) throw new SetupFail("/api/v1/version carries no commit");
  console.log(`[agent-chat-e2e] target=${c.baseURL} commit=${version.commit} version=${version.version} built=${version.build_time}`);
  if (c.expectSha && !version.commit.startsWith(c.expectSha) && !c.expectSha.startsWith(version.commit)) {
    throw new SetupFail(`target runs ${version.commit}, the run expects ${c.expectSha}: the change is not published`);
  }

  const browser = await chromium.launch();
  try {
    const C = await openActor(browser, "C");
    try {
      const ctx = await resolveCtx(C);
      const base = chatBase(ctx);
      const before = await C.api("GET", `${base}/settings`);
      if (before.status !== 200) throw new SetupFail(`GET chat settings answered ${before.status}`);
      // The original settings go to a file the teardown reads: it restores the baseline, not a guess.
      writeFileSync(join(OUT_DIR, ".settings-baseline.json"), before.text);
      const want = { enabled: true, agents: [c.agents.claude, c.agents.codex, c.agents.offline], allowed_roles: (before.json() as { allowed_roles?: string[] }).allowed_roles };
      // From the PUT on, any failure closes the window again: global-teardown does not run when setup throws.
      try {
        const put = await C.api("PUT", `${base}/settings`, want);
        // The body names the rejected agent (error_code + details): without it a 422 is not actionable.
        if (put.status !== 200) throw new SetupFail(`PUT chat settings answered ${put.status}: ${put.text.slice(0, 300)}`);
        const back = (await C.api("GET", `${base}/settings`)).json() as { enabled: boolean; agents: string[] };
        if (!back.enabled || [...back.agents].sort().join() !== [...want.agents].sort().join()) throw new SetupFail("settings readback differs from what was saved");
        // Every fixture agent must exist and be what its role says: the two harnesses answer, the offline one is offline.
        const offered = ((await C.api("GET", `${base}/agents`)).json() as { items: Array<{ agent_id: string; presence: string }> }).items;
        const presence = (id: string) => offered.find((i) => i.agent_id === id)?.presence;
        for (const [role, id] of [["claude", c.agents.claude], ["codex", c.agents.codex], ["offline", c.agents.offline]] as const) {
          if (!presence(id)) throw new SetupFail(`agent fixture "${role}" is not offered by /chat/agents after the settings were saved`);
        }
        for (const [role, id] of [["claude", c.agents.claude], ["codex", c.agents.codex]] as const) {
          if (presence(id) === "offline") throw new SetupFail(`agent fixture "${role}" is offline: the streaming scenarios need a live runner`);
        }
        if (presence(c.agents.offline) !== "offline") throw new SetupFail("the offline agent fixture is online: S07 would not exercise the queue");
        const ws = await C.api("GET", `/api/v1/workspaces/${ctx.workspaceId}/chat/settings`);
        if (ws.status !== 200) throw new SetupFail(`GET workspace chat settings answered ${ws.status}`);
        writeFileSync(join(OUT_DIR, "manifest.json"), JSON.stringify({
          target: c.baseURL, commit: version.commit, version: version.version, build_time: version.build_time,
          ci_pipeline: process.env.CI_PIPELINE_ID ?? null, ci_commit: process.env.CI_COMMIT_SHA ?? null,
          workspace: c.wsSlug, project: c.projectSlug,
          workspace_chat_settings: JSON.parse(ws.text), window_opened_at: new Date().toISOString(),
          docs: { scenario: "d0f7eac6 v2", privacy: "ef0a93c6 v1", plan: "31d67a0c v1" },
        }, null, 1));
      } catch (e) {
        const closed = { enabled: false, agents: [] as string[], allowed_roles: want.allowed_roles };
        const rollback = await C.api("PUT", `${base}/settings`, closed).catch(() => undefined);
        const readback = rollback?.status === 200 ? await C.api("GET", `${base}/settings`).catch(() => undefined) : undefined;
        let state: { enabled?: boolean; agents?: unknown[] } | undefined;
        try { state = readback?.status === 200 ? JSON.parse(readback.text) : undefined; } catch { state = undefined; }
        const closedOk = state?.enabled === false && (state.agents ?? []).length === 0;
        console.error(`[agent-chat-e2e] setup failed after opening the settings window; rollback PUT ${rollback?.status ?? "no response"}, readback ${closedOk ? "closed" : "NOT CLOSED"}`);
        if (!closedOk) throw new SetupFail(`setup failed (${e instanceof Error ? e.message : String(e)}) and the settings window could NOT be confirmed closed: close it by hand (enabled=false agents=[])`);
        throw e;
      }
    } finally {
      await C.close();
    }
  } finally {
    await browser.close();
  }
}
