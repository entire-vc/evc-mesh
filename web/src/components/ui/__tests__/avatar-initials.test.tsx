import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Avatar } from "@/components/ui/avatar";

/**
 * Initials used to be the first character of every space-separated word, so a
 * display name like "Hana (QA Mesh)" rendered as "H(". Only letters and digits
 * may become initials, in any script.
 */
describe("Avatar initials", () => {
  function initialsOf(name: string) {
    const { container } = render(<Avatar name={name} />);
    return container.textContent;
  }

  it.each([
    ["Hana (QA Mesh)", "HQ"],
    ["  Bob  ", "B"],
    ["(x)", "X"],
    ["Павел Р", "ПР"],
    ["ada lovelace", "AL"],
    ["agent-7 #2", "A2"],
  ])("%j -> %j", (name, expected) => {
    expect(initialsOf(name)).toBe(expected);
  });

  it("never renders punctuation", () => {
    for (const name of ["(x)", "- dash", "« quoted »", "Hana (QA Mesh)"]) {
      expect(initialsOf(name)).toMatch(/^[\p{L}\p{N}]+$/u);
    }
  });
});
