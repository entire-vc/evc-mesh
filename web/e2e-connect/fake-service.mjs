// Synthetic external boundary for the Connect e2e, one loopback process:
//  - the extension service of keep / keep-rotator: health and install handshake;
//  - the secret store (Keep REST, TLS) Mesh keeps the per-connection signing secret in.
// Both only record and answer; neither holds a real credential.
import http from "node:http";
import https from "node:https";
import { readFileSync } from "node:fs";

const port = Number(process.env.FAKE_EXT_PORT || 8097);
// Keep is only ever dialled over TLS; the job makes a throwaway loopback cert and
// points the API at it with SSL_CERT_FILE.
const keepPort = Number(process.env.FAKE_KEEP_PORT || 8098);
const tlsDir = process.env.FAKE_TLS_DIR;
const state = { health: 0, installs: 0, secretWrites: 0 };
const secrets = new Map();
const json = (res, body) =>
  res.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" }).end(JSON.stringify(body));

const handle = (req, res) => {
  const chunks = [];
  req.on("data", c => chunks.push(c));
  req.on("end", () => {
    const path = new URL(req.url, "http://x").pathname;
    if (path === "/healthz") { state.health++; res.writeHead(200).end(); return; }
    if (path === "/mesh/install" && req.method === "POST") { state.installs++; res.writeHead(200).end(); return; }
    if (path === "/__state") { json(res, state); return; }
    const m = /^\/orgs\/[^/]+\/mesh\/secrets\/([^/]+?)(\/resolve)?$/.exec(path);
    if (m && req.method === "PUT") {
      secrets.set(m[1], JSON.parse(Buffer.concat(chunks).toString()).value);
      state.secretWrites++;
      json(res, { ref: m[1], version: "1" });
      return;
    }
    if (m && m[2] && req.method === "POST") {
      if (!secrets.has(m[1])) { res.writeHead(404).end(); return; }
      json(res, { value: secrets.get(m[1]) });
      return;
    }
    res.writeHead(404).end();
  });
};

http.createServer(handle).listen(port, "127.0.0.1", () => console.log(`fake extension service on ${port}`));
if (tlsDir) {
  https
    .createServer({ key: readFileSync(`${tlsDir}/keep.key`), cert: readFileSync(`${tlsDir}/keep.crt`) }, handle)
    .listen(keepPort, "127.0.0.1", () => console.log(`fake secret store (TLS) on ${keepPort}`));
}
