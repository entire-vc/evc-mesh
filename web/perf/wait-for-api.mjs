#!/usr/bin/env node
// Fail-closed readiness wait for the EPHEMERAL API of the perf-counters job.
//
// The old inline probe gave /health 90 x 1s. On a cold Postgres under a loaded
// shared runner the boot-time migrations alone ran 88s (job 106538, MR !1056),
// so the probe gave up while the API was still migrating and seed/counters
// never started. The wait now has ONE overall deadline sized for that case, a
// per-request timeout (a hung socket must not eat the deadline in one fetch),
// early exit when the API process is gone (no point waiting 5 minutes for a
// corpse), and a semantic check of the body — HTTP 200 alone proves nothing.
//
//   node perf/wait-for-api.mjs --url http://localhost:8095/health \
//        [--pid <api pid>] [--deadline 300] [--request-timeout 3] [--log perf-api.log]
//   node perf/wait-for-api.mjs --selftest
//
// Exit 0 = ready; 1 = deadline passed / process exited / body wrong.
import { readFileSync, existsSync } from "node:fs";
import http from "node:http";
import { spawn } from "node:child_process";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export function pidAlive(pid) {
  try {
    process.kill(pid, 0);
  } catch {
    return false;
  }
  // A dead child nobody has reaped still answers kill(0): read its state.
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    const state = stat.slice(stat.lastIndexOf(")") + 2, stat.lastIndexOf(")") + 3);
    if (state === "Z" || state === "X") return false;
  } catch {
    /* no /proc (macOS): kill(0) is all we have */
  }
  return true;
}

// Healthy = 200 AND the body is this service's own "ok" — not any 200 from
// whatever else happens to listen on the port.
function semanticallyHealthy(status, body) {
  if (status !== 200) return false;
  try {
    const j = JSON.parse(body);
    return j.status === "ok" && j.service === "evc-mesh-api";
  } catch {
    return false;
  }
}

export async function waitForApi({ url, pid, deadlineS, requestTimeoutS, log = console.log, intervalMs = 1000 }) {
  const start = Date.now();
  let last = "no attempt made";
  while ((Date.now() - start) / 1000 < deadlineS) {
    if (pid !== undefined && !pidAlive(pid)) {
      return { ok: false, reason: `API process ${pid} exited before becoming healthy (last probe: ${last})` };
    }
    try {
      const res = await fetch(url, { signal: AbortSignal.timeout(requestTimeoutS * 1000) });
      const body = await res.text();
      if ((Date.now() - start) / 1000 >= deadlineS) {
        last = `response arrived after the ${deadlineS}s deadline`;
        break;
      }
      if (semanticallyHealthy(res.status, body)) {
        const s = Math.round((Date.now() - start) / 1000);
        log(`ephemeral API up after ${s}s`);
        return { ok: true, seconds: s };
      }
      last = `HTTP ${res.status}, body ${JSON.stringify(body.slice(0, 120))}`;
    } catch (e) {
      last = String(e?.cause?.code || e?.name || e);
    }
    await sleep(intervalMs);
  }
  return { ok: false, reason: `no healthy /health within ${deadlineS}s (last probe: ${last})` };
}

function parseArgs(argv) {
  const a = { deadlineS: 300, requestTimeoutS: 3 };
  for (let i = 0; i < argv.length; i++) {
    const v = argv[i + 1];
    if (argv[i] === "--url") a.url = argv[++i];
    else if (argv[i] === "--pid") a.pid = Number(argv[++i]);
    else if (argv[i] === "--deadline") a.deadlineS = Number(v), i++;
    else if (argv[i] === "--request-timeout") a.requestTimeoutS = Number(v), i++;
    else if (argv[i] === "--log") a.logFile = argv[++i];
    else if (argv[i] === "--selftest") a.selftest = true;
    else throw new Error(`unknown argument ${argv[i]}`);
  }
  return a;
}

// ── selftest ────────────────────────────────────────────────────────────────
// The real failure needs >90s, so the same logic is exercised scaled down:
// a server that starts listening late, standing in for a slow migration.
const OK_BODY = JSON.stringify({ status: "ok", service: "evc-mesh-api" });

function serve(handler, delayMs = 0) {
  return new Promise((resolve) => {
    const srv = http.createServer(handler);
    const open = () => srv.listen(0, "127.0.0.1", () => resolve(srv));
    delayMs ? setTimeout(open, delayMs) : open();
  });
}

async function selftest() {
  const failures = [];
  const check = (name, cond, detail) => {
    console.log(`${cond ? "PASS" : "FAIL"}  ${name}${cond ? "" : ` — ${detail}`}`);
    if (!cond) failures.push(name);
  };
  const quiet = () => {};
  const fast = { requestTimeoutS: 1, log: quiet, intervalMs: 100 };

  // Late-booting API: the port is closed for 3s, then answers healthy.
  // RED control = a budget shorter than the boot (what 90s was); GREEN = deadline that covers it.
  const lateUrl = await new Promise((resolve) => {
    const srv = http.createServer((_, res) => res.end(OK_BODY));
    const probe = http.createServer().listen(0, "127.0.0.1", () => {
      const port = probe.address().port;
      probe.close(() => {
        setTimeout(() => srv.listen(port, "127.0.0.1"), 3000);
        resolve({ url: `http://127.0.0.1:${port}/health`, srv });
      });
    });
  });
  const red = await waitForApi({ url: lateUrl.url, deadlineS: 1, ...fast });
  check("RED: budget shorter than boot gives up (old behaviour)", !red.ok, "returned ok");
  const green = await waitForApi({ url: lateUrl.url, deadlineS: 15, ...fast });
  check("GREEN: deadline covering the boot waits for real readiness", green.ok, green.reason);
  lateUrl.srv.close();

  // Never comes up -> nonzero at the deadline, not before and not never.
  const probe = http.createServer().listen(0, "127.0.0.1");
  await sleep(50);
  const deadPort = probe.address().port;
  await new Promise((r) => probe.close(r));
  const t0 = Date.now();
  const never = await waitForApi({ url: `http://127.0.0.1:${deadPort}/health`, deadlineS: 2, ...fast });
  const took = (Date.now() - t0) / 1000;
  check("NEG: API never up fails at the deadline", !never.ok && took >= 2 && took < 5, `ok=${never.ok} took=${took}s`);

  // HTTP 200 with the wrong body must not be green.
  const wrong = await serve((_, res) => res.end("<html>hello from something else</html>"));
  const w = await waitForApi({ url: `http://127.0.0.1:${wrong.address().port}/health`, deadlineS: 1, ...fast });
  check("NEG: HTTP 200 with a foreign body is not ready", !w.ok, "returned ok");
  wrong.close();
  const notOk = await serve((_, res) => res.end(JSON.stringify({ status: "degraded", service: "evc-mesh-api" })));
  const n = await waitForApi({ url: `http://127.0.0.1:${notOk.address().port}/health`, deadlineS: 1, ...fast });
  check('NEG: status != "ok" is not ready', !n.ok, "returned ok");
  notOk.close();

  // A hung socket must not swallow the deadline in a single fetch.
  const hung = await serve(() => {});
  const t1 = Date.now();
  const h = await waitForApi({ url: `http://127.0.0.1:${hung.address().port}/health`, deadlineS: 3, ...fast });
  const hTook = (Date.now() - t1) / 1000;
  check("NEG: hanging server fails near the deadline (per-request timeout)", !h.ok && hTook < 6, `ok=${h.ok} took=${hTook}s`);
  hung.closeAllConnections?.();
  hung.close();

  // A healthy answer that lands after the deadline must not turn the wait green.
  const slow = await serve((_, res) => setTimeout(() => res.end(OK_BODY), 2500));
  const sl = await waitForApi({ url: `http://127.0.0.1:${slow.address().port}/health`, deadlineS: 2, ...fast, requestTimeoutS: 5 });
  check("NEG: healthy response arriving after the deadline is not ready", !sl.ok, "returned ok");
  slow.closeAllConnections?.();
  slow.close();

  // Process died -> fail fast with a named reason, long before the deadline.
  const child = spawn(process.execPath, ["-e", "process.exit(3)"], { stdio: "ignore" });
  await new Promise((r) => child.once("exit", r));
  const t2 = Date.now();
  const gone = await waitForApi({ url: `http://127.0.0.1:${deadPort}/health`, pid: child.pid, deadlineS: 60, ...fast });
  const gTook = (Date.now() - t2) / 1000;
  check("NEG: exited API process fails fast, not at the deadline", !gone.ok && gTook < 5 && /exited/.test(gone.reason ?? ""), `ok=${gone.ok} took=${gTook}s reason=${gone.reason}`);

  // Live pid + healthy server still green (the pid check must not false-red).
  const live = spawn(process.execPath, ["-e", "setTimeout(()=>{},20000)"], { stdio: "ignore" });
  const good = await serve((_, res) => res.end(OK_BODY));
  const g = await waitForApi({ url: `http://127.0.0.1:${good.address().port}/health`, pid: live.pid, deadlineS: 5, ...fast });
  check("POS: live process + healthy body is ready", g.ok, g.reason);
  live.kill();
  good.close();

  // The CLI path itself (flag parsing -> exit code), with every flag the CI
  // job passes. The first CI run of this script false-failed against a healthy
  // API because --log leaked into the options and shadowed the logger.
  const cliRun = (url, extra = []) =>
    new Promise((resolve) => {
      const c = spawn(process.execPath, [new URL(import.meta.url).pathname, "--url", url, "--deadline", "4",
        "--request-timeout", "1", "--log", "/nonexistent.log", ...extra], { stdio: "ignore" });
      c.once("exit", (code) => resolve(code));
    });
  const cliGood = await serve((_, res) => res.end(OK_BODY));
  const cliOk = await cliRun(`http://127.0.0.1:${cliGood.address().port}/health`, ["--pid", String(process.pid)]);
  check("CLI: healthy API with the CI flag set exits 0", cliOk === 0, `exit=${cliOk}`);
  cliGood.close();
  const cliBad = await cliRun(`http://127.0.0.1:${deadPort}/health`);
  check("CLI: unreachable API exits nonzero", cliBad === 1, `exit=${cliBad}`);

  if (failures.length) {
    console.error(`selftest FAILED: ${failures.join("; ")}`);
    process.exit(1);
  }
  console.log("selftest ok");
  process.exit(0);
}

const args = parseArgs(process.argv.slice(2));
if (args.selftest) {
  await selftest();
} else {
  if (!args.url) throw new Error("--url is required");
  const r = await waitForApi({ url: args.url, pid: args.pid, deadlineS: args.deadlineS, requestTimeoutS: args.requestTimeoutS });
  if (!r.ok) {
    console.error(`ephemeral API did not come up: ${r.reason}`);
    if (args.logFile && existsSync(args.logFile)) {
      console.error(`--- tail of ${args.logFile} ---`);
      console.error(readFileSync(args.logFile, "utf8").split("\n").slice(-50).join("\n"));
    }
    process.exit(1);
  }
}
