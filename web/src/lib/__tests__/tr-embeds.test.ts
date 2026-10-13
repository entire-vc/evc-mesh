import { describe, it, expect } from "vitest";

import { applyTrEmbedAttachments } from "@/lib/tr-embeds";
import type { DocumentAttachment } from "@/lib/document-attachments";

function att(sourcePath: string, id = sourcePath): DocumentAttachment {
  return {
    id,
    document_id: "d1",
    name: sourcePath.slice(sourcePath.lastIndexOf("/") + 1),
    mime_type: "image/png",
    size_bytes: 100,
    storage_key: "documents/p/d/attachments/x.png",
    uploaded_by: "u1",
    uploaded_by_type: "system",
    created_at: "2026-10-04T00:00:00Z",
    source_path: sourcePath,
    source_sha256: "sha",
  };
}

const DL = (id: string) => `/api/v1/document-attachments/${id}/download?disposition=inline`;

describe("applyTrEmbedAttachments", () => {
  it("resolves explicit relative paths against the referencing document", () => {
    const attachments = [att("Notes/images/chart.png", "parent"), att("Notes/sub/pic.png", "local"), att("pic.png", "root")];
    expect(applyTrEmbedAttachments("![chart](../images/chart.png)", "Notes/sub/Plan.md", attachments)).toContain(DL("parent"));
    expect(applyTrEmbedAttachments("![[./pic.png]]", "Notes/sub/Plan.md", attachments)).toContain(DL("local"));
  });

  it("rewrites a wiki embed to the attachment download path", () => {
    const out = applyTrEmbedAttachments("Plan ![[pic.png]] end", "Notes/Plan.md", [att("Notes/pic.png", "a1")]);
    expect(out).toBe(`Plan ![pic.png](${DL("a1")}) end`);
  });

  it("drops the wiki size suffix but keeps the rest of the token intact", () => {
    const out = applyTrEmbedAttachments("![[pic.png|300]]", "Notes/Plan.md", [att("Notes/pic.png", "a1")]);
    expect(out).toBe(`![pic.png](${DL("a1")})`);
  });

  it("rewrites markdown image syntax, preserving the alt text", () => {
    const out = applyTrEmbedAttachments("![a shot](shot.png)", "Notes/Plan.md", [att("Notes/shot.png", "a2")]);
    expect(out).toBe(`![a shot](${DL("a2")})`);
  });

  it("resolves a bare name next to the document before the share root", () => {
    // The doc sits in Notes/; the attachment exists only at Notes/pic.png.
    const out = applyTrEmbedAttachments("![[pic.png]]", "Notes/Plan.md", [att("Notes/pic.png", "a1")]);
    expect(out).toContain(DL("a1"));
    // Same body, but only a root-level pic.png was imported — the second
    // candidate still resolves.
    const outRoot = applyTrEmbedAttachments("![[pic.png]]", "Notes/Plan.md", [att("pic.png", "a3")]);
    expect(outRoot).toContain(DL("a3"));
  });

  it("treats a target carrying a directory as share-relative, as written", () => {
    const out = applyTrEmbedAttachments("![[assets/logo.png]]", "Notes/Plan.md", [att("assets/logo.png", "a4")]);
    expect(out).toBe(`![logo.png](${DL("a4")})`);
  });

  it("rewrites every occurrence of the same target to the same attachment", () => {
    const body = "![[pic.png]] then ![one](pic.png) then ![[pic.png|50]]";
    const out = applyTrEmbedAttachments(body, "Notes/Plan.md", [att("Notes/pic.png", "a1")]);
    expect(out.split(DL("a1")).length).toBe(4);
  });

  it("leaves a reference with no matching attachment exactly as written", () => {
    const body = "![[missing.png]] and ![x](also-missing.png)";
    expect(applyTrEmbedAttachments(body, "Notes/Plan.md", [att("Notes/pic.png", "a1")])).toBe(body);
  });

  it("leaves non-share targets alone (http, rooted, fragment)", () => {
    const body = "![[https://evil.example/x.png]] and ![[/etc/passwd.png]] and ![a](#anchor)";
    expect(applyTrEmbedAttachments(body, "Notes/Plan.md", [att("Notes/pic.png", "a1")])).toBe(body);
  });

  it("leaves references to non-embeddable files alone (a note embed)", () => {
    const body = "![[Other Note.md]]";
    expect(applyTrEmbedAttachments(body, "Notes/Plan.md", [att("Notes/Other Note.md", "a1")])).toBe(body);
  });

  it("renders a pdf embed as a link, not a broken image", () => {
    const out = applyTrEmbedAttachments("![[spec.pdf]]", "Spec.md", [att("spec.pdf", "a5")]);
    expect(out).toBe(`[spec.pdf](${DL("a5")})`);
  });

  it("renders a markdown pdf reference as a link too", () => {
    const out = applyTrEmbedAttachments("![the spec](spec.pdf)", "Spec.md", [att("spec.pdf", "a5")]);
    expect(out).toBe(`[the spec](${DL("a5")})`);
  });

  it("ignores human-uploaded attachments (no source_path) and returns the body", () => {
    const plain: DocumentAttachment = { ...att("anything", "a9"), source_path: null, source_sha256: null };
    const body = "![[pic.png]]";
    expect(applyTrEmbedAttachments(body, "Notes/Plan.md", [plain])).toBe(body);
  });

  it("returns the body unchanged when there are no attachments at all", () => {
    expect(applyTrEmbedAttachments("![[pic.png]]", "Notes/Plan.md", [])).toBe("![[pic.png]]");
  });

  it("falls back to the share root when an explicit relative target has no local copy", () => {
    // Keep root-level copies imported by earlier versions usable when the
    // document-relative candidate has no matching attachment.
    const out = applyTrEmbedAttachments("![[./pic.png]]", "Notes/Plan.md", [att("pic.png", "a1")]);
    expect(out).toContain(DL("a1"));
  });
});
