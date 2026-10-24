// @vitest-environment node
import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import { describe, expect, it } from "vitest";
import { e2eProxy } from "./e2e-proxy";

const PROD = "https://mesh.example.test";

// Drives the proxyRes handler the way http-proxy does: an upstream response
// stream in, a recording response out.
function run(headers: Record<string, string>, chunks: string[]) {
  const entry = e2eProxy(`${PROD}/`)["/api"];
  const proxy = new EventEmitter();
  (entry.configure as (p: unknown) => void)(proxy);

  const proxyRes = Object.assign(new PassThrough(), { headers, statusCode: 200 });
  const out: { head?: Record<string, string>; body: string; done: boolean } = { body: "", done: false };
  const res = {
    writeHead(_status: number, h: Record<string, string>) {
      out.head = h;
    },
    end(b: string) {
      out.body = b;
      out.done = true;
    },
  };
  proxy.emit("proxyRes", proxyRes, { headers: { host: "127.0.0.1:4410" } }, res);
  for (const c of chunks) proxyRes.write(c);
  proxyRes.end();
  return { out, proxyRes, res };
}

describe("e2eProxy", () => {
  it("sends /api and /s3 to the target, trailing slash trimmed", () => {
    const table = e2eProxy(`${PROD}/`);
    expect(Object.keys(table).sort()).toEqual(["/api", "/s3"]);
    expect(table["/api"].target).toBe(PROD);
  });

  it("points the deployment's own links in JSON back at the browser's origin", async () => {
    const { out } = run({ "content-type": "application/json" }, [
      `{"u":"${PROD}/s3/bucket/x?sig=1"`,
      `,"keep":"https://other.example/x"}`,
    ]);
    await new Promise((r) => setTimeout(r, 0));
    expect(out.done).toBe(true);
    expect(JSON.parse(out.body)).toEqual({
      u: "http://127.0.0.1:4410/s3/bucket/x?sig=1",
      keep: "https://other.example/x",
    });
  });

  it("re-frames a chunked JSON body with a length, never both", async () => {
    const { out } = run({ "content-type": "application/json", "transfer-encoding": "chunked" }, [`{"u":"${PROD}/s3/x"}`]);
    await new Promise((r) => setTimeout(r, 0));
    expect(out.head).not.toHaveProperty("transfer-encoding");
    expect(out.head!["content-length"]).toBe(String(Buffer.byteLength(out.body)));
  });

  it("leaves non-JSON responses untouched", () => {
    const entry = e2eProxy(PROD)["/api"];
    const proxy = new EventEmitter();
    (entry.configure as (p: unknown) => void)(proxy);
    const proxyRes = Object.assign(new PassThrough(), { headers: { "content-type": "text/csv" }, statusCode: 200 });
    const sink = new PassThrough();
    let head: Record<string, string> | undefined;
    const res = Object.assign(sink, {
      writeHead(_s: number, h: Record<string, string>) {
        head = h;
      },
    });
    proxy.emit("proxyRes", proxyRes, { headers: { host: "127.0.0.1:4410" } }, res);
    proxyRes.end(`a,b\n${PROD}/x,1\n`);
    expect(head).toEqual({ "content-type": "text/csv" });
    expect(sink.read().toString()).toBe(`a,b\n${PROD}/x,1\n`);
  });
});
