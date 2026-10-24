import type { ProxyOptions } from "vite";

/** Proxy table for authed-e2e on a merge request: /api and /s3 go to `target`,
 * and absolute `target` links in JSON responses are pointed back at the
 * origin the browser used. Non-JSON responses (streams, files) pass through
 * untouched. */
export function e2eProxy(target: string) {
  const base = target.replace(/\/+$/, "");
  const entry = {
    target: base,
    changeOrigin: true,
    headers: { Origin: base, "Accept-Encoding": "identity" },
    cookieDomainRewrite: "",
    selfHandleResponse: true,
    configure: ((proxy) => {
      proxy.on("proxyRes", (proxyRes, req, res) => {
        const type = String(proxyRes.headers["content-type"] || "");
        const local = `http://${req.headers.host}`;
        const headers = { ...proxyRes.headers };
        if (!type.includes("json")) {
          res.writeHead(proxyRes.statusCode || 502, headers);
          proxyRes.pipe(res);
          return;
        }
        const chunks: Buffer[] = [];
        proxyRes.on("data", (c: Buffer) => chunks.push(c));
        proxyRes.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf8").split(base).join(local);
          // Re-framed with a length: a chunked upstream must not keep both.
          delete headers["transfer-encoding"];
          headers["content-length"] = String(Buffer.byteLength(body));
          res.writeHead(proxyRes.statusCode || 502, headers);
          res.end(body);
        });
      });
    }) as NonNullable<ProxyOptions["configure"]>,
  };
  return { "/api": entry, "/s3": entry };
}
