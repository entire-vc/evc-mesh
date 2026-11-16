import { describe, it, expect, beforeEach } from "vitest";
import { registerThemeViews } from "@/core/themes/bundle";
import { clearThemeIcons, getThemeIcons } from "@/core/icons/theme-icons";

const Icon = () => null;

describe("registerThemeViews: bundle icons", () => {
  beforeEach(() => clearThemeIcons("t-icons"));

  it("registers valid kebab-case icons and their keywords, drops the invalid ones", () => {
    registerThemeViews("t-icons", {
      views: {},
      icons: { rocket: Icon, "my-glyph": Icon, "Bad Name": Icon, notAComponent: "x" as never },
      icons_meta: { "my-glyph": { en: "glyph", ru: ["глиф"] }, ghost: { en: "x" } },
    });
    const set = getThemeIcons("t-icons")!;
    expect(Object.keys(set.icons).sort()).toEqual(["my-glyph", "rocket"]);
    expect(Object.keys(set.meta)).toEqual(["my-glyph"]); // keywords for an unknown name are ignored
  });

  it("a new bundle of the same theme replaces the old icons whole", () => {
    registerThemeViews("t-icons", { views: {}, icons: { rocket: Icon } });
    registerThemeViews("t-icons", { views: {} });
    expect(getThemeIcons("t-icons")).toBeUndefined();
  });
});
