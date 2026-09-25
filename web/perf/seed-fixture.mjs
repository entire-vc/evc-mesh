#!/usr/bin/env node
/**
 * Idempotent fixture for the per-action perf counters (web/perf/README.md,
 * part 2). Run it any number of times against the same API: the first run
 * creates the user, the project and 200 tasks; every later run finds them and
 * creates nothing. `--selftest` proves that claim by running twice and
 * failing if the second pass created anything.
 *
 * The data is fixed on purpose — titles, statuses, priorities and labels are
 * a pure function of the task number — because the counters are only
 * comparable run to run if the board renders the same thing every time.
 *
 * Target is an EPHEMERAL API (the perf-counters CI job starts its own
 * cmd/api over a throwaway Postgres; locally, scripts/local-stack.sh). It
 * refuses a URL that is not localhost unless PERF_ALLOW_REMOTE=1, so it can't
 * be pointed at a real deployment by a stray env var.
 *
 * Env:
 *   PERF_API_URL   API base, e.g. http://localhost:8095
 * Writes web/perf/.fixture.json (git-ignored) for the spec to read.
 */
import { writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

export const FIXTURE = {
  email: "perf-fixture@example.test",
  password: "PerfFixture1",
  name: "Perf Fixture",
  projectName: "Perf fixture",
  projectSlug: "perf-fixture",
  taskCount: 200,
};

const PRIORITIES = ["urgent", "high", "medium", "low", "none"];
const LABELS = ["frontend", "backend", "infra", "docs"];

export const taskTitle = (n) => `Perf fixture task ${String(n).padStart(3, "0")}`;
/** Opened by task.open. */
export const OPEN_TASK_TITLE = taskTitle(1);
/** Receives comment.send; its comments are wiped before each measurement. */
export const COMMENT_TASK_TITLE = taskTitle(2);
/** Dragged by board.drag; moved back to its seeded status before each repeat. */
export const DRAG_TASK_TITLE = taskTitle(3);

function apiBase() {
  const base = process.env.PERF_API_URL;
  if (!base) throw new Error("PERF_API_URL is not set");
  const host = new URL(base).hostname;
  const local = ["localhost", "127.0.0.1", "api", "mesh-api"].includes(host);
  if (!local && process.env.PERF_ALLOW_REMOTE !== "1") {
    throw new Error(
      `refusing to seed ${base}: the perf fixture writes 200 tasks and is meant ` +
        `for an ephemeral API only (set PERF_ALLOW_REMOTE=1 if you really mean it)`
    );
  }
  return base.replace(/\/$/, "");
}

async function call(base, token, method, path, body) {
  const res = await fetch(base + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = null;
  }
  return { status: res.status, json, text };
}

async function must(base, token, method, path, body) {
  const r = await call(base, token, method, path, body);
  if (r.status < 200 || r.status >= 300) {
    throw new Error(`${method} ${path} → HTTP ${r.status}: ${r.text.slice(0, 300)}`);
  }
  return r.json;
}

async function authenticate(base) {
  const login = await call(base, null, "POST", "/api/v1/auth/login", {
    email: FIXTURE.email,
    password: FIXTURE.password,
  });
  if (login.status === 200) return { token: login.json.tokens.access_token, created: false };
  const reg = await must(base, null, "POST", "/api/v1/auth/register", {
    email: FIXTURE.email,
    password: FIXTURE.password,
    name: FIXTURE.name,
  });
  return { token: reg.tokens.access_token, created: true };
}

async function listAllTasks(base, token, projectId) {
  const out = [];
  for (let page = 1; ; page++) {
    const r = await must(
      base,
      token,
      "GET",
      `/api/v1/projects/${projectId}/tasks?page=${page}&page_size=200`
    );
    out.push(...(r.items ?? []));
    if (!r.has_more && page >= (r.total_pages ?? 1)) break;
  }
  return out;
}

export async function seed({ quiet = false } = {}) {
  const base = apiBase();
  const log = (...a) => quiet || console.log("[perf-seed]", ...a);
  let created = 0;

  const auth = await authenticate(base);
  if (auth.created) created++;
  const { token } = auth;

  const workspaces = await must(base, token, "GET", "/api/v1/workspaces");
  const ws = workspaces[0];
  if (!ws) throw new Error("the fixture user has no workspace");

  const projects = await must(base, token, "GET", `/api/v1/workspaces/${ws.id}/projects`);
  const projectList = Array.isArray(projects) ? projects : projects.items ?? [];
  let project = projectList.find((p) => p.slug === FIXTURE.projectSlug);
  if (!project) {
    project = await must(base, token, "POST", `/api/v1/workspaces/${ws.id}/projects`, {
      name: FIXTURE.projectName,
      slug: FIXTURE.projectSlug,
    });
    created++;
  }

  const statuses = await must(base, token, "GET", `/api/v1/projects/${project.id}/statuses`);
  // Every column except review: the API refuses to create a task straight into
  // review (it means "work done, awaiting check"), and moving 40 tasks there
  // would only exercise the review gate, not the board.
  const statusList = (Array.isArray(statuses) ? statuses : statuses.items ?? [])
    .filter((s) => s.category !== "review")
    .slice()
    .sort((a, b) => (a.position ?? 0) - (b.position ?? 0));
  if (statusList.length === 0) throw new Error("the fixture project has no statuses");

  const existing = await listAllTasks(base, token, project.id);
  const byTitle = new Map(existing.map((t) => [t.title, t]));

  for (let n = 1; n <= FIXTURE.taskCount; n++) {
    const title = taskTitle(n);
    if (byTitle.has(title)) continue;
    const task = await must(base, token, "POST", `/api/v1/projects/${project.id}/tasks`, {
      title,
      description: n % 3 === 0 ? `Fixed description for task ${n}.` : "",
      priority: PRIORITIES[n % PRIORITIES.length],
      status_id: statusList[n % statusList.length].id,
      labels: [LABELS[n % LABELS.length]],
    });
    byTitle.set(title, task);
    created++;
  }

  const fixture = {
    ws_slug: ws.slug,
    ws_id: ws.id,
    project_slug: project.slug,
    project_id: project.id,
    open_task_id: byTitle.get(OPEN_TASK_TITLE).id,
    comment_task_id: byTitle.get(COMMENT_TASK_TITLE).id,
    drag_task_id: byTitle.get(DRAG_TASK_TITLE).id,
    task_count: FIXTURE.taskCount,
    email: FIXTURE.email,
    password: FIXTURE.password,
  };
  writeFileSync(resolve(here, ".fixture.json"), JSON.stringify(fixture, null, 2));
  log(`${created === 0 ? "nothing to create" : `created ${created} object(s)`}; project ${ws.slug}/${project.slug}, ${FIXTURE.taskCount} tasks`);
  return { created, fixture };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const selftest = process.argv.includes("--selftest");
  try {
    const first = await seed();
    if (selftest) {
      const second = await seed();
      if (second.created !== 0) {
        console.error(`[perf-seed] NOT idempotent: the second run created ${second.created} object(s)`);
        process.exit(1);
      }
      console.log(`[perf-seed] selftest OK — first run created ${first.created}, second run created 0`);
    }
  } catch (err) {
    console.error(`[perf-seed] ${err.message}`);
    process.exit(1);
  }
}
