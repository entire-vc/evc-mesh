import { describe, expect, it, vi } from "vitest";
import { columnCountLabel, type ClosedColumnInfo } from "@/pages/board";

const info = (hasMore: boolean, filtersActive: boolean): ClosedColumnInfo => ({
  slice: { pages: 1, loaded: 200, total: 3600, hasMore },
  filtersActive, onLoadMore: vi.fn(),
});

describe("closed column header count", () => {
  it("an open column counts its (complete) cards", () => {
    expect(columnCountLabel(58, undefined)).toBe("58");
  });
  it("a partial closed column without filters shows the server total", () => {
    expect(columnCountLabel(200, info(true, false))).toBe("3600");
  });
  it("a partial closed column with a filter says 'at least'", () => {
    expect(columnCountLabel(12, info(true, true))).toBe("12+");
  });
  it("a fully loaded closed column counts exactly, filters or not", () => {
    expect(columnCountLabel(12, info(false, true))).toBe("12");
  });
});
