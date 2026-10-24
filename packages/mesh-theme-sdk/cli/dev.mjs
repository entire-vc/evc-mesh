import { createServer } from "node:http";
import path from "node:path";
import { context } from "esbuild";
import { bundleOptions } from "./esbuild-options.mjs";
import { projectInputs } from "./build.mjs";

const TYPES = { "views.js": "text/javascript", "theme.css": "text/css" };
export const DEV_STORAGE_KEY = "mesh-theme-dev-bundle";

/**
 * `mesh-theme dev --instance <url>`: serves the bundle from memory, rebuilds on every change and tells
 * the page through /events. The instance's own web build (dev build only) loads it in place of the
 * installed theme's bundle once `localStorage[DEV_STORAGE_KEY]` points here.
 */
export async function dev({ cwd, entry, css, instance, port = 4173, log = console.log }) {
  const inputs = projectInputs(cwd, { entry, css });
  if (!inputs.entry) throw new Error(`views entry not found: ${path.relative(cwd, entry ?? "src/index.tsx")}`);
  const origin = new URL(instance).origin;
  const files = {};
  const clients = new Set();
  let version = 0;

  const ctx = await context({
    ...bundleOptions(inputs),
    plugins: [
      {
        name: "serve-from-memory",
        setup(b) {
          b.onEnd((res) => {
            if (res.errors.length) {
              log(`build failed: ${res.errors.map((e) => e.text).join("; ")} (still serving the last good bundle)`);
              return;
            }
            for (const f of res.outputFiles) files[path.basename(f.path)] = Buffer.from(f.contents);
            version++;
            for (const c of clients) c.write(`data: ${version}\n\n`);
            log(`rebuilt #${version}`);
          });
        },
      },
    ],
  });
  await ctx.watch();

  // Only the instance's own origin may read the bundle: it runs with the user's session there.
  const cors = { "access-control-allow-origin": origin, vary: "origin" };
  const server = createServer((req, res) => {
    const name = new URL(req.url, "http://x").pathname.slice(1);
    if (req.method === "OPTIONS") return res.writeHead(204, cors).end();
    if (name === "events") {
      res.writeHead(200, { ...cors, "content-type": "text/event-stream", "cache-control": "no-store" });
      res.write(`data: ${version}\n\n`);
      clients.add(res);
      req.on("close", () => clients.delete(res));
      return;
    }
    const body = files[name];
    if (!body) return res.writeHead(404, cors).end("not found");
    res.writeHead(200, { ...cors, "content-type": TYPES[name] ?? "application/octet-stream", "cache-control": "no-store" }).end(body);
  });
  await new Promise((resolve, reject) => server.once("error", reject).listen(port, "127.0.0.1", resolve));
  const url = `http://127.0.0.1:${server.address().port}/views.js`;

  let reachable = "reachable";
  try {
    await fetch(origin, { signal: AbortSignal.timeout(5000) });
  } catch (e) {
    reachable = `NOT reachable (${e.message})`;
  }
  log(`instance ${origin}: ${reachable}`);
  log(`serving ${url}`);
  log(`in the instance's browser console (dev build of the instance only):`);
  log(`  localStorage.setItem(${JSON.stringify(DEV_STORAGE_KEY)}, ${JSON.stringify(url)}); location.reload()`);
  log(`to go back: localStorage.removeItem(${JSON.stringify(DEV_STORAGE_KEY)})`);

  return {
    url,
    files,
    close: async () => {
      for (const c of clients) c.end();
      await ctx.dispose();
      await new Promise((r) => server.close(r));
    },
  };
}
