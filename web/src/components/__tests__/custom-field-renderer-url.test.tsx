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

  it.each(["http://example.com/a?b=1", "https://example.com/a?b=1"])("renders %s as a link", (value) => {
    const { container } = renderUrl(value);
    expect(container.querySelector("a")?.getAttribute("href")).toBe(value);
  });

  it("shows the hostname in compact mode", () => {
    const { container } = renderUrl("https://example.com/a", true);
    expect(container.querySelector("a")?.textContent).toBe("example.com");
  });

  it("keeps mailto links", () => {
    const { container } = renderUrl("mailto:team@example.com");
    expect(container.querySelector("a")?.getAttribute("href")).toBe("mailto:team@example.com");
  });

  it("links a non-ASCII URL through its normalized href", () => {
    const value = "https://ru.wikipedia.org/wiki/Москва";
    const { container } = renderUrl(value);
    const link = container.querySelector("a");
    expect(link?.getAttribute("href")).toBe(new URL(value).href);
    expect(link?.textContent).toBe(value);
  });

  it("renders a non-URL as plain text", () => {
    const { container } = renderUrl("not a url");
    expect(container.querySelector("a")).toBeNull();
  });
});
