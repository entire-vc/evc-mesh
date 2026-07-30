import { describe, it, expect } from "vitest";
// Raw-text import (Vite's `?raw` suffix) rather than node:fs — this file lives
// under src/, which tsconfig.app.json builds WITHOUT Node types (only
// tsconfig.node.json, scoped to vite.config.ts, has `types: ["node"]`), so a
// node:fs import passes plain `tsc --noEmit` (that command silently checks
// nothing against the root tsconfig's empty `files: []`) but fails the real
// project build (`tsc -b`, what `pnpm build` runs) with TS2591.
import indexHtml from "../../index.html?raw";

// web/index.html is the Vite entry HTML, built verbatim into every deployment's
// served bundle (deploy/docker/mesh/Dockerfile.nginx: `COPY --from=web-builder
// /app/dist /srv/web`). Task #a1f0ae14: a self-hosted instance's meta description
// and canonical pointed at entire.vc, so its own link previews (Slack/Twitter/
// Telegram unfurlers ignore <meta name="robots">) advertised OUR marketing copy
// and domain instead of the self-hoster's own instance.
describe("web/index.html — no vendor domain in the served entry file", () => {
  const html = indexHtml;

  it("never hardcodes entire.vc or entire.host", () => {
    expect(html).not.toMatch(/entire\.(vc|host)/i);
  });

  it("still identifies the product (negative control — the assertion above isn't vacuous)", () => {
    expect(html).toContain("EVC Mesh");
  });
});
