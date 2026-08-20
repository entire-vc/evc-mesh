import { beforeEach, describe, expect, it } from "vitest";
import {
  loadDocCommentsRailOpen,
  saveDocCommentsRailOpen,
} from "@/lib/docs-layout-storage";

beforeEach(() => {
  localStorage.clear();
});

describe("comments rail default", () => {
  // Pavel's call on #a4a8db69: with the thread tree under the document, an
  // open-by-default rail showed the same discussion twice on a wide screen.
  // The default is a product decision, so it gets an assert rather than living
  // only in a `return` that a later refactor can flip back without noticing.
  it("starts closed for a reader who has never touched it", () => {
    expect(loadDocCommentsRailOpen()).toBe(false);
  });

  it("honours a reader who opened it — the default is only the starting point", () => {
    saveDocCommentsRailOpen(true);
    expect(loadDocCommentsRailOpen()).toBe(true);
  });

  it("honours a reader who closed it", () => {
    saveDocCommentsRailOpen(false);
    expect(loadDocCommentsRailOpen()).toBe(false);
  });

  it("falls back to closed when localStorage throws", () => {
    const original = Storage.prototype.getItem;
    Storage.prototype.getItem = () => {
      throw new Error("private browsing");
    };
    try {
      expect(loadDocCommentsRailOpen()).toBe(false);
    } finally {
      Storage.prototype.getItem = original;
    }
  });
});
