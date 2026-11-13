import { test, expect, type APIRequestContext } from "@playwright/test";
import net from "node:net";
// The fixture's HTTP connection contract. route.fetch reuses pooled keep-alive sockets and ignores the
// Keep-Alive hint, so an idle socket must survive the old close at keepAliveTimeout+buffer (6 s); abnormal connection ends
// are reported in /__fixture/conn-log (and on stderr), normal ones are not. Raw sockets: the socket identity is the point.
const PORT = 8019;
const GET = "GET /__fixture HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n";

const open = () => new Promise<net.Socket>((resolve, reject) => {
  const s = net.connect(PORT, "127.0.0.1", () => resolve(s)); s.once("error", reject);
});
// One full response on the socket: headers plus a Content-Length body; rejects if the server ends the socket instead.
const roundTrip = (s: net.Socket, raw = GET) => new Promise<string>((resolve, reject) => {
  let buf = "";
  const onData = (b: Buffer) => {
    buf += b.toString("latin1");
    const head = buf.indexOf("\r\n\r\n"); if (head < 0) return;
    const len = Number(/content-length:\s*(\d+)/i.exec(buf.slice(0, head))?.[1] ?? 0);
    if (buf.length >= head + 4 + len) { done(); resolve(buf.slice(0, buf.indexOf("\r\n"))); }
  };
  const onEnd = () => { done(); reject(new Error(`server closed the socket; got ${JSON.stringify(buf.slice(0, 40))}`)); };
  const onErr = (e: Error) => { done(); reject(e); };
  const done = () => { s.off("data", onData); s.off("end", onEnd); s.off("close", onEnd); s.off("error", onErr); };
  if (s.destroyed || s.readableEnded) return reject(new Error("server closed the socket before the request"));
  s.on("data", onData); s.once("end", onEnd); s.once("close", onEnd); s.once("error", onErr);
  s.write(raw);
});
const API = `http://127.0.0.1:${PORT}`;
const linesFor = async (request: APIRequestContext, port: number) =>
  ((await (await request.get(`${API}/__fixture/conn-log`)).json()) as { lines: string[] }).lines.filter((l) => l.includes(` :${port} `));

test("an idle keep-alive socket is still served after the old 6 s close", async ({ request }) => {
  test.setTimeout(30_000);
  const s = await open();
  try {
    expect(await roundTrip(s)).toBe("HTTP/1.1 200 OK");
    await new Promise((r) => setTimeout(r, 7_000));
    expect(await roundTrip(s), "second request on the same idle socket").toBe("HTTP/1.1 200 OK");
    // a clean close of a healthy socket is not an abnormal end
    const port = s.localPort!;
    await new Promise<void>((r) => { s.once("close", () => r()); s.end(); });
    await new Promise((r) => setTimeout(r, 200));
    expect(await linesFor(request, port)).toEqual([]);
  } finally { s.destroy(); }
});

test("a peer reset and a malformed request are logged with the socket's history", async ({ request }) => {
  const reset = await open();
  expect(await roundTrip(reset)).toBe("HTTP/1.1 200 OK");
  const resetPort = reset.localPort!;
  reset.resetAndDestroy();
  const bad = await open();
  const badPort = bad.localPort!;
  await roundTrip(bad, "BOGUS\r\n\r\n").catch(() => "");
  bad.destroy();
  await expect.poll(async () => (await linesFor(request, resetPort)).join("\n")).toMatch(/ECONNRESET served=1 idle=\d+ms keepAliveFired=false/);
  await expect.poll(async () => (await linesFor(request, badPort)).join("\n")).toMatch(/clientError HPE_INVALID_METHOD .* served=0/);
});
