// @vitest-environment node
import { createRequire } from "node:module";
import { expect, it } from "vitest";

// Exercise the transitive copy used by jsdom, without adding a direct dependency.
const require = createRequire(import.meta.url);
const jsdomRequire = createRequire(require.resolve("jsdom"));
const { BalancedPool } = jsdomRequire("undici");

it("BalancedPool preserves custom TLS connectors (GHSA-w293-vg96-wgc3)", async () => {
  const rejectedPeer = new Error("custom TLS verification rejected the peer");
  const pool = new BalancedPool("https://127.0.0.1:1", {
    connect: (_options: unknown, callback: (error: Error) => void) => {
      callback(rejectedPeer);
    },
  });

  try {
    await expect(pool.request({ path: "/", method: "GET" })).rejects.toBe(rejectedPeer);
  } finally {
    await pool.destroy();
  }
});
