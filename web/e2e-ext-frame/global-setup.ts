import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { request } from "@playwright/test";
import { OUT_DIR, SetupFail, cfg, missingEnv } from "./env";

/** Preflight. Everything unavailable is FAIL SETUP: a green run over a missing fixture is what this suite stops. */
export default async function globalSetup() {
  mkdirSync(OUT_DIR, { recursive: true });
  const missing = missingEnv();
  if (missing.length > 0) {
    throw new SetupFail(`missing CI variables: ${missing.join(", ")} (isolated pilot receiver and QA user: an internal ticket)`);
  }
  const c = cfg();
  const anon = await request.newContext({ baseURL: c.baseURL });
  const res = await anon.get("/api/v1/version");
  if (res.status() !== 200) throw new SetupFail(`/api/v1/version answered ${res.status()}`);
  const v = (await res.json()) as { commit?: string; version?: string; build_time?: string };
  await anon.dispose();
  if (!v.commit) throw new SetupFail("/api/v1/version carries no commit");
  console.log(`[ext-frame-e2e] target=${c.baseURL} commit=${v.commit} version=${v.version} built=${v.build_time}`);
  // The receipt must be about a known commit: in CI the pipeline's own SHA unless a pipeline variable pins another.
  if (!c.expectSha) throw new SetupFail("no expected SHA: set E2E_EXT_EXPECT_SHA (CI uses CI_COMMIT_SHA) so the run is bound to a commit");
  // /api/v1/version reports the full commit: the published build must equal the expected one exactly.
  if (!/^[0-9a-f]{40}$/.test(c.expectSha)) throw new SetupFail("the expected SHA must be a full lowercase 40-hex commit");
  if (v.commit !== c.expectSha) {
    throw new SetupFail(`target runs ${v.commit}, the run expects ${c.expectSha}: the change is not published`);
  }
  writeFileSync(
    join(OUT_DIR, "manifest.json"),
    JSON.stringify({ target: c.baseURL, commit: v.commit, version: v.version, build_time: v.build_time, ci_pipeline: process.env.CI_PIPELINE_ID ?? null, ci_commit: process.env.CI_COMMIT_SHA ?? null }, null, 1),
  );
}
