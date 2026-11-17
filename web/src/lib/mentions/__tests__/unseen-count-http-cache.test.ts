import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fetchUnseenMentionCount } from "@/lib/mentions/inbox";

// Regression test: the mentions_unseen tile and the bell re-read
// the unseen count after a live mention frame and after POST .../seen, but the
// server answered with Cache-Control: max-age=10 and the browser served the
// refetch from its disk cache — the tile kept 0 after a new mention and 2 after
// a seen, while the inbox list beside it was already fresh.
//
// The fetch stub below models the browser HTTP cache: a stored response that is
// still within its max-age is returned for a request in the default cache mode
// and is bypassed (revalidated with the server) only for "no-cache", "no-store"
// or "reload". The real api() and fetchUnseenMentionCount run on top of it.

const TASK = "/api/v1/me/mentions/unseen_count";
const DOC = "/api/v1/me/document-mentions/unseen_count";

let server: Record<string, number>;
let browserCache: Map<string, number>;
let hits: { url: string; fromCache: boolean }[];

function pathOf(url: string): string {
  return new URL(url, "http://mesh.test").pathname;
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json", "Cache-Control": "max-age=10" },
  });
}

beforeEach(() => {
  server = { [TASK]: 0, [DOC]: 0 };
  browserCache = new Map();
  hits = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const mode = init.cache ?? "default";
      const bypass = mode === "no-cache" || mode === "no-store" || mode === "reload";
      // Every entry is still fresh: the test runs well within a 10 s TTL.
      if (!bypass && browserCache.has(url)) {
        hits.push({ url: pathOf(url), fromCache: true });
        return json({ count: browserCache.get(url) });
      }
      const count = server[pathOf(url)] ?? 0;
      if (mode !== "no-store") browserCache.set(url, count);
      hits.push({ url: pathOf(url), fromCache: false });
      return json({ count });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

describe("fetchUnseenMentionCount with a still-fresh browser-cache entry", () => {
  it("sees a new task mention and then a new document mention without waiting for the TTL", async () => {
    await expect(fetchUnseenMentionCount("ws-1")).resolves.toBe(0);

    server[TASK] = 1;
    await expect(fetchUnseenMentionCount("ws-1")).resolves.toBe(1);

    server[DOC] = 1;
    await expect(fetchUnseenMentionCount("ws-1")).resolves.toBe(2);

    expect(hits.filter((h) => h.fromCache)).toEqual([]);
  });

  it("drops each seen mention right away: 2 -> 1 -> 0", async () => {
    server = { [TASK]: 1, [DOC]: 1 };
    await expect(fetchUnseenMentionCount("ws-1")).resolves.toBe(2);

    server[TASK] = 0;
    await expect(fetchUnseenMentionCount("ws-1")).resolves.toBe(1);

    server[DOC] = 0;
    await expect(fetchUnseenMentionCount("ws-1")).resolves.toBe(0);

    expect(hits.filter((h) => h.fromCache)).toEqual([]);
  });

  // Positive control for the stub itself: a default-mode read of the same URL
  // does come back stale, so the green cases above prove the cache mode, not a
  // stub that never caches.
  it("control: a default-mode read of the same entry is served stale", async () => {
    const url = `${TASK}?workspace_id=ws-1`;
    await fetch(url);
    server[TASK] = 5;
    const res = await fetch(url);
    await expect(res.json()).resolves.toEqual({ count: 0 });
    expect(hits[hits.length - 1]).toEqual({ url: TASK, fromCache: true });
  });
});
