import { describe, expect, it } from "vitest";
import {
  displayName,
  inlineLabel,
  isNamePlaceholder,
  secondaryLabel,
} from "@/lib/user-display";

// The case these helpers exist for: on an instance where nobody was ever asked
// for a name, display_name literally holds the address. A naive
// name-over-address layout then prints the same string twice, which is what the
// member list and the assignee dropdown were doing.
const unnamed = { name: "mail-kmv21@yandex.ru", email: "mail-kmv21@yandex.ru", username: "mail-kmv21" };
const named = { name: "Konstantin M.", email: "mail-kmv21@yandex.ru", username: "mail-kmv21" };

describe("isNamePlaceholder", () => {
  it("detects an address standing in for a name", () => {
    expect(isNamePlaceholder(unnamed)).toBe(true);
  });

  it("ignores case and padding, because the two are the same identity", () => {
    expect(isNamePlaceholder({ name: "  MAIL-KMV21@YANDEX.RU ", email: "mail-kmv21@yandex.ru" })).toBe(true);
  });

  it("treats a real name as a real name", () => {
    expect(isNamePlaceholder(named)).toBe(false);
  });

  it("treats an empty or blank name as missing", () => {
    expect(isNamePlaceholder({ name: "", email: "a@b.c" })).toBe(true);
    expect(isNamePlaceholder({ name: "   ", email: "a@b.c" })).toBe(true);
    expect(isNamePlaceholder({ name: null, email: "a@b.c" })).toBe(true);
  });

  it("does not mistake a name that merely mentions the address", () => {
    expect(isNamePlaceholder({ name: "Jane <jane@example.com>", email: "jane@example.com" })).toBe(false);
  });

  it("treats a missing user as unnamed rather than throwing", () => {
    expect(isNamePlaceholder(null)).toBe(true);
    expect(isNamePlaceholder(undefined)).toBe(true);
  });
});

describe("displayName", () => {
  it("prefers the name", () => {
    expect(displayName(named)).toBe("Konstantin M.");
  });

  it("falls back to the address when there is no name, so the row is never blank", () => {
    expect(displayName(unnamed)).toBe("mail-kmv21@yandex.ru");
    expect(displayName({ name: "", email: "a@b.c" })).toBe("a@b.c");
  });

  it("never returns an empty string", () => {
    expect(displayName({ name: "", email: "" })).toBe("Unknown");
    expect(displayName(null)).toBe("Unknown");
  });
});

describe("secondaryLabel", () => {
  it("offers the address as a disambiguator when there is a name above it", () => {
    expect(secondaryLabel(named)).toBe("mail-kmv21@yandex.ru");
  });

  // The whole point: no second line when the first line is already the address.
  it("offers nothing when the address is already the primary label", () => {
    expect(secondaryLabel(unnamed)).toBeNull();
  });
});

describe("inlineLabel", () => {
  it("adds @username so two people with one name can be told apart", () => {
    expect(inlineLabel(named)).toBe("Konstantin M. (@mail-kmv21)");
  });

  it("does not append a handle to an address masquerading as a name", () => {
    expect(inlineLabel(unnamed)).toBe("mail-kmv21@yandex.ru");
  });

  it("degrades to the bare name when there is no username", () => {
    expect(inlineLabel({ name: "Jane Cooper", email: "jane@example.com" })).toBe("Jane Cooper");
  });

  // Regression guard for the assignee dropdown, which read
  // "mail-kmv21@yandex.ru — member" on an instance with no names.
  it("never emits an address for somebody who has a name", () => {
    expect(inlineLabel(named)).not.toContain("@yandex.ru");
  });
});
