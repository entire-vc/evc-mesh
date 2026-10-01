import { describe, expect, it } from "vitest";
import { joinFrontmatter, splitFrontmatter } from "@/lib/docs/frontmatter";

const FLEET_DOC = `---
created: 2026-10-01T17:50+03:00
updated: 2026-10-01T18:05+03:00
author: Garfield-Mesh
status: review
project: contenthub
type: spec
tags:
  - contenthub
  - "mesh docs"
source: https://mesh.entire.host/d/abc
---

# PRD

Body text.
`;

describe("splitFrontmatter", () => {
  it("splits the block off and gives the body back byte for byte", () => {
    const s = splitFrontmatter(FLEET_DOC);
    expect(s.raw.startsWith("---\n")).toBe(true);
    expect(s.raw.endsWith("---\n\n")).toBe(true);
    expect(s.body).toBe("# PRD\n\nBody text.\n");
    expect(joinFrontmatter(s.raw, s.body)).toBe(FLEET_DOC);
  });

  it("reads scalars, block lists and URLs with colons in them", () => {
    const { entries } = splitFrontmatter(FLEET_DOC);
    const byKey = Object.fromEntries(entries.map((e) => [e.key, e.value]));
    expect(entries.map((e) => e.key)).toEqual([
      "created", "updated", "author", "status", "project", "type", "tags", "source",
    ]);
    expect(byKey.created).toBe("2026-10-01T17:50+03:00");
    expect(byKey.tags).toEqual(["contenthub", "mesh docs"]);
    expect(byKey.source).toBe("https://mesh.entire.host/d/abc");
  });

  it("reads a flow list", () => {
    const { entries } = splitFrontmatter("---\ntags: [a, 'b c', d]\n---\nx\n");
    expect(entries).toEqual([{ key: "tags", value: ["a", "b c", "d"] }]);
  });

  it.each([
    ["no frontmatter", "# Title\n\nText\n"],
    ["a rule in the middle", "Intro\n\n---\n\nAfter the rule\n"],
    ["setext underline mid-document", "Para\n\nHeading\n---\n\nmore\n"],
    ["an opening fence that is never closed", "---\nkey: value\nno close\n"],
    ["a fence that is not on line one", "\n---\nkey: v\n---\nbody\n"],
    ["empty", ""],
  ])("leaves %s alone", (_label, source) => {
    const s = splitFrontmatter(source);
    expect(s.raw).toBe("");
    expect(s.body).toBe(source);
    expect(s.entries).toEqual([]);
  });

  it.each([
    ["CRLF", "---\r\nk: v\r\n---\r\n\r\nbody\r\n"],
    ["BOM", "﻿---\nk: v\n---\nbody\n"],
    ["frontmatter only, no trailing newline", "---\nk: v\n---"],
    ["frontmatter only, trailing blank lines", "---\nk: v\n---\n\n\n"],
    ["`...` closing fence", "---\nk: v\n...\nbody\n"],
  ])("round-trips %s exactly", (_label, source) => {
    const s = splitFrontmatter(source);
    expect(s.raw).not.toBe("");
    expect(joinFrontmatter(s.raw, s.body)).toBe(source);
    expect(s.entries[0]).toEqual({ key: "k", value: "v" });
  });
});
