import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { CustomFieldRenderer } from "@/components/custom-field-renderer";
import type { CustomFieldDefinition } from "@/types";

const field = { id: "f-1", name: "Link", field_type: "url", options: {} } as unknown as CustomFieldDefinition;

function renderUrl(value: string, compact = false) {
  return render(<CustomFieldRenderer field={field} value={value} onChange={() => {}} readOnly compact={compact} />);
}

describe("CustomFieldRenderer url (read-only)", () => {
  it.each(["javascript:alert(1)", "JavaScript:alert(1)", " javascript:alert(1)", "data:text/html,<script>alert(1)</script>", "vbscript:msgbox(1)"])(
    "renders %s as text, not a link",
    (value) => {
      const { container } = renderUrl(value);
      expect(container.querySelector("a")).toBeNull();
      expect(container.textContent).toBe(value);
    },
  );

  it("renders an http(s) URL as a link", () => {
    const { container } = renderUrl("https://example.com/a?b=1");
    expect(container.querySelector("a")?.getAttribute("href")).toBe("https://example.com/a?b=1");
  });

  it("shows the hostname in compact mode", () => {
    const { container } = renderUrl("https://example.com/a", true);
    expect(container.querySelector("a")?.textContent).toBe("example.com");
  });

  it("keeps mailto links", () => {
    const { container } = renderUrl("mailto:team@example.com");
    expect(container.querySelector("a")?.getAttribute("href")).toBe("mailto:team@example.com");
  });

  it("renders a non-URL as plain text", () => {
    const { container } = renderUrl("not a url");
    expect(container.querySelector("a")).toBeNull();
  });
});
