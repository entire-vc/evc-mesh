import { describe, it, expect } from "vitest";
import { toPreviewText } from "../preview-text";

describe("toPreviewText", () => {
  // The literal string from the reported screenshot. If this ever regresses,
  // the bug the user actually saw is back.
  it("flattens the reported notification body", () => {
    const raw =
      "---\n\n❓ **Blocking @pavel**: нужен один из двух вариантов, чтобы двигаться дальше.";
    expect(toPreviewText(raw)).toBe(
      "❓ Blocking @pavel: нужен один из двух вариантов, чтобы двигаться дальше.",
    );
  });

  it("drops a thematic break on its own line", () => {
    expect(toPreviewText("---\nhello")).toBe("hello");
    expect(toPreviewText("***\nhello")).toBe("hello");
    expect(toPreviewText("___\nhello")).toBe("hello");
  });

  it("keeps a hyphen that is not a thematic break", () => {
    expect(toPreviewText("well-known issue")).toBe("well-known issue");
    expect(toPreviewText("a - b")).toBe("a - b");
  });

  it("unwraps bold and italic but leaves bare markers alone", () => {
    expect(toPreviewText("**bold** and *italic*")).toBe("bold and italic");
    expect(toPreviewText("__bold__ and _italic_")).toBe("bold and italic");
    // Arithmetic and stray markers must survive — stripping them would change
    // the sentence rather than clean it.
    expect(toPreviewText("2 * 3 = 6")).toBe("2 * 3 = 6");
    expect(toPreviewText("a ** b")).toBe("a ** b");
  });

  it("keeps link and image labels, drops targets", () => {
    expect(toPreviewText("see [the doc](https://example.com/x)")).toBe(
      "see the doc",
    );
    expect(toPreviewText("![a diagram](https://example.com/i.png)")).toBe(
      "a diagram",
    );
  });

  it("strips leading block markers only at line start", () => {
    expect(toPreviewText("## Heading\ntext")).toBe("Heading text");
    expect(toPreviewText("> quoted")).toBe("quoted");
    expect(toPreviewText("- one\n- two")).toBe("one two");
    expect(toPreviewText("1. first\n2. second")).toBe("first second");
    // A hash mid-sentence is not a heading.
    expect(toPreviewText("see task #172cad33")).toBe("see task #172cad33");
  });

  it("keeps code contents, drops fences and ticks", () => {
    expect(toPreviewText("```go\nfmt.Println()\n```")).toBe("fmt.Println()");
    expect(toPreviewText("run `make ci` first")).toBe("run make ci first");
  });

  // Found on review, not by me: markup rules used to run over code that had
  // already been unfenced, so code containing text shaped like markup was
  // silently corrupted. These are the exact inputs that caught it.
  it("does not apply markup rules to text inside code", () => {
    expect(toPreviewText("```\narr[0](x) some code\n```")).toBe(
      "arr[0](x) some code",
    );
    expect(toPreviewText("```\n[link](url)\n```")).toBe("[link](url)");
    expect(toPreviewText("`a * b * c`")).toBe("a * b * c");
    expect(toPreviewText("`# not a heading`")).toBe("# not a heading");
    expect(toPreviewText("`__dunder__`")).toBe("__dunder__");
    // …while markup OUTSIDE the code is still flattened.
    expect(toPreviewText("**bold** then `arr[0](x)`")).toBe(
      "bold then arr[0](x)",
    );
  });

  it("handles an unterminated fence — a truncated body is the common case", () => {
    expect(toPreviewText("```go\nfmt.Println()")).toBe("fmt.Println()");
  });

  it("collapses whitespace into a single run", () => {
    expect(toPreviewText("a\n\n\nb   c\t d")).toBe("a b c d");
  });

  it("is safe on empty and marker-only input", () => {
    expect(toPreviewText("")).toBe("");
    expect(toPreviewText("---")).toBe("");
    expect(toPreviewText("   \n  ")).toBe("");
  });
});
