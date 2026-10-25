import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "@playwright/test";
import { OUT_DIR, missingEnv } from "./lib/env";
import { chatBase, openActor, resolveCtx, row, trackedThreads } from "./lib/harness";

/**
 * Closes the window whatever the tests did: delete this run's threads (the ids the
 * specs recorded, through each author's own session), then enabled=false agents=[]
 * with a readback. A leftover is its own FAIL with the exact remainder.
 */
export default async function globalTeardown() {
  // Local run of the pure detector controls only (project "controls"); CI never sets this.
  if (process.env.AGENT_CHAT_E2E_CONTROLS_ONLY === "1") return;
  if (missingEnv().length > 0) return; // setup already failed loudly
  const browser = await chromium.launch();
  const leftovers: string[] = [];
  try {
    const actors = [] as Awaited<ReturnType<typeof openActor>>[];
    try {
      for (const id of ["A", "B", "C", "V"] as const) actors.push(await openActor(browser, id));
      const ctx = await resolveCtx(actors[2]!);
      const base = chatBase(ctx);
      for (const t of trackedThreads()) {
        const a = actors.find((x) => x.id === t.actor)!;
        const del = await a.api("DELETE", `${base}/threads/${t.threadId}`);
        // 404 = already deleted through the UI by the spec itself.
        if (del.status >= 300 && del.status !== 404) leftovers.push(`thread ${t.threadId} (${t.actor}) delete=${del.status}`);
      }
      const baseline = existsSync(join(OUT_DIR, ".settings-baseline.json"))
        ? (JSON.parse(readFileSync(join(OUT_DIR, ".settings-baseline.json"), "utf8")) as { allowed_roles?: string[] })
        : {};
      // Close the window: the sandbox goes back to enabled=false, no agents.
      await actors[2]!.api("PUT", `${base}/settings`, { enabled: false, agents: [], allowed_roles: baseline.allowed_roles });
      const back = (await actors[2]!.api("GET", `${base}/settings`)).json() as { enabled: boolean; agents: string[] };
      if (back.enabled || back.agents.length > 0) leftovers.push(`settings still enabled=${back.enabled} agents=${back.agents.length}`);
    } finally {
      await Promise.allSettled(actors.map((a) => a.close()));
    }
  } catch (e) {
    leftovers.push(`teardown crashed: ${(e as Error).message}`);
  } finally {
    await browser.close();
  }
  row("CLEANUP", "window", leftovers.length === 0 ? "PASS" : "FAIL", leftovers.join(" | "));
  writeFileSync(join(OUT_DIR, "cleanup.json"), JSON.stringify({ closed_at: new Date().toISOString(), leftovers }, null, 1));
  if (leftovers.length > 0) throw new Error(`cleanup left: ${leftovers.join(" | ")}`);
}
