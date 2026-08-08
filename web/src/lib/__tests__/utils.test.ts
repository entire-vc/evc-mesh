import { describe, expect, it } from "vitest";
import { slugify, slugifyFieldKey } from "@/lib/utils";

// The two slug formats in this codebase are NOT interchangeable:
//
//   workspace/project slug -> hyphens   (^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$)
//   custom field slug      -> underscores only
//                             (migrations/20260224008: chk_custom_fields_slug_format
//                              CHECK (slug ~ '^[a-z0-9_]{1,100}$'))
//
// Sending a hyphenated slug to POST /projects/{id}/custom-fields passes every
// frontend check and then dies on the DB constraint, which is exactly the bug
// this helper exists to prevent. Hence the regex assertion below.
const CUSTOM_FIELD_SLUG_CONSTRAINT = /^[a-z0-9_]{1,100}$/;

describe("slugify", () => {
  it("lowercases and joins words with hyphens", () => {
    expect(slugify("Story Points")).toBe("story-points");
  });

  it("drops punctuation and collapses whitespace", () => {
    expect(slugify("Cost ($USD)")).toBe("cost-usd");
    expect(slugify("  Trim  Me  ")).toBe("trim-me");
  });

  it("transliterates Cyrillic instead of collapsing it to an empty string", () => {
    expect(slugify("Витрина")).toBe("vitrina");
    expect(slugify("Срок сдачи")).toBe("srok-sdachi");
    expect(slugify("ёж")).toBe("ezh");
  });
});

describe("slugifyFieldKey", () => {
  it("produces snake_case, not hyphens", () => {
    expect(slugifyFieldKey("Story Points")).toBe("story_points");
    expect(slugifyFieldKey("Due Date 2")).toBe("due_date_2");
  });

  it("never emits a hyphen, which the DB constraint rejects", () => {
    // A name that already contains a hyphen must not leak one through.
    expect(slugifyFieldKey("Story-Points")).not.toContain("-");
  });

  it("transliterates Cyrillic names", () => {
    expect(slugifyFieldKey("Витрина")).toBe("vitrina");
    expect(slugifyFieldKey("Срок сдачи")).toBe("srok_sdachi");
  });

  it("drops punctuation and trims leading/trailing separators", () => {
    expect(slugifyFieldKey("Cost ($USD)")).toBe("cost_usd");
    expect(slugifyFieldKey("  Trim  Me  ")).toBe("trim_me");
  });

  it("returns an empty string when nothing survives, so the dialog can ask for a manual slug", () => {
    // The dialog guards on this: empty slug -> "Slug is required".
    expect(slugifyFieldKey("日本語")).toBe("");
    expect(slugifyFieldKey("!!!")).toBe("");
  });

  it("satisfies the backend CHECK constraint for every non-empty output", () => {
    const names = [
      "Story Points",
      "Story-Points",
      "Витрина",
      "Срок сдачи",
      "Cost ($USD)",
      "  Trim  Me  ",
      "A__B",
      "ёж",
      "Due Date 2",
      "Priority!!!",
      "50% Done",
    ];
    for (const name of names) {
      const slug = slugifyFieldKey(name);
      expect(slug, `slugifyFieldKey(${JSON.stringify(name)})`).toMatch(
        CUSTOM_FIELD_SLUG_CONSTRAINT,
      );
    }
  });
});
