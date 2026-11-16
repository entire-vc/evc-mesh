import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { test, expect, request as playwrightRequest, type APIRequestContext } from "@playwright/test";
import { rateLimitAware, retryAfterSeconds } from "./api-retry";

/**
 * The 429 helper itself, against a local server: no deployment, no account, no browser. A route answers 429 with
 * Retry-After: 1 for its first `fail` hits and 200 after that; the server counts every hit so the test can tell a
 * retry that happened from one that did not.
 */

let server: Server;
let base = "";
const hits = new Map<string, number>();

test.beforeAll(async () => {
  server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://local");
    const seen = (hits.get(url.pathname) ?? 0) + 1;
    hits.set(url.pathname, seen);
    const fail = Number(url.searchParams.get("fail") ?? "0");
    const status = Number(url.searchParams.get("status") ?? "429");
    if (seen <= fail) {
      res.writeHead(status, status === 429 ? { "Retry-After": "1" } : {});
      res.end("limited");
      return;
    }
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ ok: true, seen }));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

test.afterAll(async () => {
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

async function withApi(fn: (api: APIRequestContext) => Promise<void>): Promise<void> {
  const raw = await playwrightRequest.newContext({ baseURL: base });
  try {
    await fn(rateLimitAware(raw));
  } finally {
    await raw.dispose();
  }
}

test("a 429 waits for Retry-After and the same request is sent again", async () => {
  await withApi(async (api) => {
    const started = Date.now();
    const res = await api.patch("/twice-limited?fail=2", { data: { x: 1 } });
    expect(res.status()).toBe(200);
    expect(hits.get("/twice-limited"), "two 429s, then the third attempt passes").toBe(3);
    expect(Date.now() - started, "it waited Retry-After (1s) before each retry").toBeGreaterThanOrEqual(2_000);
  });
});

test("attempts are bounded: the last 429 is returned to the spec unchanged", async () => {
  await withApi(async (api) => {
    const res = await api.delete("/always-limited?fail=99");
    expect(res.status()).toBe(429);
    expect(hits.get("/always-limited"), "three attempts, no more").toBe(3);
  });
});

test("any other failure is not retried", async () => {
  await withApi(async (api) => {
    const res = await api.get("/server-error?fail=1&status=500");
    expect(res.status()).toBe(500);
    expect(hits.get("/server-error"), "a 500 is returned at once").toBe(1);
  });
});

test("Retry-After is clamped and defaults to a minute", () => {
  expect(retryAfterSeconds("7")).toBe(7);
  expect(retryAfterSeconds("0")).toBe(1);
  expect(retryAfterSeconds("3600")).toBe(65);
  expect(retryAfterSeconds(undefined)).toBe(60);
  expect(retryAfterSeconds("soon")).toBe(60);
});
