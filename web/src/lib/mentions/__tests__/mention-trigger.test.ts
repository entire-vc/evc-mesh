import { describe, expect, it } from "vitest";
import {
  applyMentionInsertion,
  findMentionTrigger,
} from "@/lib/mentions/mention-trigger";

/**
 * The client-side half of the mention contract.
 *
 * The server refuses a comment naming a slug that resolves to nobody, which is
 * only fair if the menu opens in the same places the server's extraction looks —
 * offering a menu where the server will not read one, or staying shut where it
 * will, turns a helpful refusal into an unexplained 400.
 */
describe("findMentionTrigger", () => {
  it("opens on a bare @ at the start of the box", () => {
    expect(findMentionTrigger("@", 1)).toEqual({ start: 0, query: "" });
  });

  it("captures what has been typed so far", () => {
    expect(findMentionTrigger("ping @dae", 9)).toEqual({ start: 5, query: "dae" });
  });

  it("opens after an opening bracket, as the server's extraction does", () => {
    expect(findMentionTrigger("(@pav", 5)).toEqual({ start: 1, query: "pav" });
    expect(findMentionTrigger("[@pav", 5)).toEqual({ start: 1, query: "pav" });
    expect(findMentionTrigger("{@pav", 5)).toEqual({ start: 1, query: "pav" });
  });

  it("does not open inside an email address", () => {
    // The server refuses to read this as a mention for the same reason: the
    // character before the @ is not a boundary. A menu here would offer names
    // for a request that can only be rejected.
    expect(findMentionTrigger("write to bob@example", 20)).toBeNull();
  });

  it("closes on whitespace", () => {
    expect(findMentionTrigger("@pavel and then", 15)).toBeNull();
  });

  it("closes on a second @", () => {
    expect(findMentionTrigger("@pavel@", 7)).toBeNull();
  });

  it("reads the text before the caret, not the whole box", () => {
    // Caret parked mid-sentence: the trailing text is somebody else's words.
    expect(findMentionTrigger("@dae and @pavel", 4)).toEqual({ start: 0, query: "dae" });
  });

  it("is null when there is no @ at all", () => {
    expect(findMentionTrigger("nothing here", 12)).toBeNull();
  });
});

describe("applyMentionInsertion", () => {
  it("replaces the partial slug and leaves a trailing space", () => {
    const trigger = { start: 5, query: "dae" };
    expect(applyMentionInsertion("ping @dae", trigger, 9, "daedalus")).toEqual({
      value: "ping @daedalus ",
      caret: 15,
    });
  });

  it("keeps the text after the caret", () => {
    const trigger = { start: 0, query: "pav" };
    const out = applyMentionInsertion("@pav — what do you think?", trigger, 4, "pavel");
    expect(out.value).toBe("@pavel  — what do you think?");
    expect(out.caret).toBe(7);
  });

  it("completes a bare @ into a whole mention", () => {
    const trigger = { start: 0, query: "" };
    expect(applyMentionInsertion("@", trigger, 1, "pavel")).toEqual({
      value: "@pavel ",
      caret: 7,
    });
  });
});
