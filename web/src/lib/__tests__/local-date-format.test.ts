import { describe, it, expect } from "vitest";
import { format } from "date-fns";
import { localClock, localDateKey, toDateTimeLocal } from "@/lib/utils";

// localDateKey/localClock replace date-fns format() (bundle budget); they must
// stay byte-identical to it for the fixed patterns they stand in for.
describe("localDateKey / localClock", () => {
  const dates = [
    new Date(2026, 0, 5, 3, 7),
    new Date(2026, 11, 31, 23, 59),
    new Date(2024, 1, 29, 0, 0),
    new Date(2026, 9, 19, 12, 30),
  ];
  const ancient = new Date(2000, 5, 15, 9, 5);
  ancient.setFullYear(999);

  it.each([...dates, ancient])("matches date-fns for %s", (d) => {
    expect(localDateKey(d)).toBe(format(d, "yyyy-MM-dd"));
    expect(localClock(d)).toBe(format(d, "HH:mm"));
  });

  it("zero-pads the year below 1000", () => {
    expect(localDateKey(ancient)).toBe("0999-06-15");
  });

  it("toDateTimeLocal keeps the datetime-local shape", () => {
    const d = new Date(2026, 0, 5, 3, 7);
    expect(toDateTimeLocal(d.toISOString())).toBe(format(d, "yyyy-MM-dd'T'HH:mm"));
  });
});
