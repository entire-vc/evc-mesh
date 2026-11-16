import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { Widget } from "../slots";
import type { WidgetInstance } from "../types";

// The size a view gets is the instance's size clamped by the client descriptor (slots.tsx). The server's templates
// place instances by catalog.go, so a descriptor narrower than the catalog cut them: «Инициативы» puts initiatives at
// 12×8, which the old 3×2–12×6 bound drew as 12×6. The group's real views.tsx is replaced by a probe that prints the
// size it got; the registry, the descriptors and the clamp are the real ones.
vi.mock("@/lib/api", () => ({ api: vi.fn(async () => ({})) }));
vi.mock("../registry", async (importActual) => {
  const actual = await importActual<typeof import("../registry")>();
  const Probe = ({ size, instance }: { size: { w: number; h: number }; instance: WidgetInstance }) => (
    <output data-testid={instance.id}>{`${size.w}×${size.h}`}</output>
  );
  const cache = actual.createViewCache(
    { descriptors: [...actual.WIDGETS], catalogOnly: new Set(), viewLoaders: {} },
    { initiatives: Probe, initiative: Probe, "event-feed": Probe },
  );
  return { ...actual, peekWidgetView: cache.peek, loadWidgetView: cache.load };
});

function draw(instance: Omit<WidgetInstance, "x" | "y" | "settings">) {
  window.matchMedia = ((q: string) => ({ matches: q.includes("768"), media: q, addEventListener() {}, removeEventListener() {} })) as never;
  useMemberStore.setState({ myRole: "member" });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [] });
  render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Routes>
        <Route path="/w/:wsSlug/dashboard" element={<Widget instance={{ x: 0, y: 0, settings: {}, ...instance } as WidgetInstance} />} />
      </Routes>
    </MemoryRouter>,
  );
  return screen.getByTestId(instance.id).textContent;
}

afterEach(() => cleanup());

describe("a view gets the instance size within the catalog's bounds", () => {
  it("the «Инициативы» template's initiatives at 12×8 reach the view as 12×8", () => {
    expect(draw({ id: "i1", type: "initiatives", w: 12, h: 8 } as never)).toBe("12×8");
  });

  it("the «События» template's event-feed at 12×10 and an initiative at 6×6 are drawn as placed", () => {
    expect(draw({ id: "e1", type: "event-feed", w: 12, h: 10 } as never)).toBe("12×10");
    expect(draw({ id: "i2", type: "initiative", w: 6, h: 6 } as never)).toBe("6×6");
  });

  it("control: a size beyond the catalog's max is still clamped (initiatives max 12×10)", () => {
    expect(draw({ id: "i3", type: "initiatives", w: 12, h: 12 } as never)).toBe("12×10");
  });
});
