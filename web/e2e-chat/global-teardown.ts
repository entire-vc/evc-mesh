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
  // What the server answers after the cleanup, kept as read, so the artifact proves the window is closed.
  const threads: Array<{ actor: string; threadId: string; delete: number; readback: number }> = [];
  let settings: { status: number; enabled?: boolean; agents?: string[] } | null = null;
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
        const back = await a.api("GET", `${base}/threads/${t.threadId}`);
        threads.push({ actor: t.actor, threadId: t.threadId, delete: del.status, readback: back.status });
        if (back.status !== 404) leftovers.push(`thread ${t.threadId} (${t.actor}) still answers ${back.status} after delete`);
      }
      // S07 sets the workspace offline TTL to 1 h and restores it itself; if it died first, restore here.
      const wsFile = join(OUT_DIR, ".ws-chat-settings-baseline.json");
      if (existsSync(wsFile)) {
        const want = JSON.parse(readFileSync(wsFile, "utf8")) as { offline_expiry_hours?: number };
        const url = `/api/v1/workspaces/${ctx.workspaceId}/chat/settings`;
        const now = await actors[2]!.api("GET", url);
        const got = now.status === 200 ? (now.json() as { offline_expiry_hours?: number }).offline_expiry_hours : undefined;
        if (got !== want.offline_expiry_hours) {
          const put = await actors[2]!.api("PUT", url, want);
          const back = await actors[2]!.api("GET", url);
          const again = back.status === 200 ? (back.json() as { offline_expiry_hours?: number }).offline_expiry_hours : undefined;
          if (put.status !== 200 || again !== want.offline_expiry_hours) leftovers.push(`workspace offline_expiry_hours=${again} not restored to ${want.offline_expiry_hours} (PUT ${put.status})`);
        }
      }
      const baseline = existsSync(join(OUT_DIR, ".settings-baseline.json"))
        ? (JSON.parse(readFileSync(join(OUT_DIR, ".settings-baseline.json"), "utf8")) as { allowed_roles?: string[] })
        : {};
      // Close the window: the sandbox goes back to enabled=false, no agents.
      await actors[2]!.api("PUT", `${base}/settings`, { enabled: false, agents: [], allowed_roles: baseline.allowed_roles });
      const read = await actors[2]!.api("GET", `${base}/settings`);
      if (read.status !== 200) {
        settings = { status: read.status };
        leftovers.push(`settings readback answered ${read.status}`);
      } else {
        const back = read.json() as { enabled: boolean; agents: string[] };
        settings = { status: read.status, enabled: back.enabled, agents: back.agents };
        if (back.enabled || back.agents.length > 0) leftovers.push(`settings still enabled=${back.enabled} agents=${back.agents.length}`);
      }
    } finally {
      await Promise.allSettled(actors.map((a) => a.close()));
    }
  } catch (e) {
    leftovers.push(`teardown crashed: ${(e as Error).message}`);
  } finally {
    await browser.close();
  }
  row("CLEANUP", "window", leftovers.length === 0 ? "PASS" : "FAIL", leftovers.join(" | "));
  writeFileSync(join(OUT_DIR, "cleanup.json"), JSON.stringify({ closed_at: new Date().toISOString(), settings, threads, leftovers }, null, 1));
  if (leftovers.length > 0) throw new Error(`cleanup left: ${leftovers.join(" | ")}`);
}
