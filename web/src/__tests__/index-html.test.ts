import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

// web/index.html is the Vite entry HTML, built verbatim into every deployment's
// served bundle (deploy/docker/mesh/Dockerfile.nginx: `COPY --from=web-builder
// /app/dist /srv/web`). Task #a1f0ae14: a self-hosted instance's meta description
// and canonical pointed at entire.vc, so its own link previews (Slack/Twitter/
// Telegram unfurlers ignore <meta name="robots">) advertised OUR marketing copy
// and domain instead of the self-hoster's own instance.
const indexHtmlPath = resolve(dirname(fileURLToPath(import.meta.url)), "../../index.html");

describe("web/index.html — no vendor domain in the served entry file", () => {
  const html = readFileSync(indexHtmlPath, "utf-8");

  it("never hardcodes entire.vc or entire.host", () => {
    expect(html).not.toMatch(/entire\.(vc|host)/i);
  });

  it("still identifies the product (negative control — the assertion above isn't vacuous)", () => {
    expect(html).toContain("EVC Mesh");
  });
});
