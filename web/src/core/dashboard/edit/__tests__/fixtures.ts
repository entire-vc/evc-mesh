import { StaleLayoutError, type CatalogType, type DashboardLayout, type LayoutApi, type LayoutItem } from "../types";

const size = (w: number, h: number, minW: number, minH: number, maxW: number, maxH: number) => ({ default: { w, h }, min: { w: minW, h: minH }, max: { w: maxW, h: maxH } });

export const CATALOG: CatalogType[] = [
  { id: "my-tasks", title: "My tasks", description: "Tasks assigned to me", category: "Tasks", source: { kind: "core", name: "Mesh" }, size: size(6, 4, 3, 2, 12, 8),
    settings_schema: { type: "object", properties: { limit: { type: "integer", title: "Rows", minimum: 5, maximum: 50, default: 5 } } } },
  { id: "mentions", title: "Mentions", description: "Mentions of you", category: "Communications", source: { kind: "core", name: "Mesh" }, size: size(6, 4, 3, 2, 12, 10) },
  { id: "agents-summary", title: "Agents summary", description: "Online, busy, offline", category: "Agents", source: { kind: "core", name: "Mesh" }, size: size(4, 2, 3, 1, 6, 2) },
  { id: "gitlab-pipelines", title: "Failed pipelines", description: "Pipelines that failed", category: "CI", source: { kind: "extension", name: "GitLab" }, size: size(6, 3, 3, 2, 12, 6) },
];

export const item = (id: string, type: string, x: number, y: number, w: number, h: number): LayoutItem => ({ id, type, x, y, w, h });

/** Two rows full width, one free 6x4 hole on the right of row two: a, b stacked on the left, c top right. */
export const LAYOUT: DashboardLayout = {
  dashboardId: "d1",
  version: 7,
  items: [item("a", "my-tasks", 0, 0, 6, 4), item("b", "mentions", 0, 4, 6, 4), item("c", "agents-summary", 6, 0, 4, 2)],
};

/** In-memory server with optimistic versioning: what two browser tabs share. */
export class FakeServer {
  version: number;
  items: LayoutItem[];
  saves: { version: number; items: LayoutItem[]; sent: LayoutItem[] }[] = [];
  failNext = false;
  /** Emulates the real server: widgets sent without an id get one (srv-N), the response carries them. */
  assignIds = false;
  private seq = 0;
  /** Resolves a save later, to hold a second save in the queue behind it. */
  hold: Promise<void> | null = null;
  catalogFails = false;
  constructor(l: DashboardLayout = LAYOUT) {
    this.version = l.version;
    this.items = structuredClone(l.items);
  }
  api(): LayoutApi {
    return {
      catalog: async () => {
        if (this.catalogFails) throw new Error("down");
        return CATALOG;
      },
      save: async (_id, version, items) => {
        if (this.failNext) {
          this.failNext = false;
          throw new Error("500");
        }
        if (this.hold) await this.hold;
        if (version !== this.version) throw new StaleLayoutError();
        this.version += 1;
        const known = new Set(this.items.map((x) => x.id));
        this.items = structuredClone(items).map((i) => {
          const { fresh, ...rest } = i; // `fresh` is a client mark, never stored
          if (!this.assignIds) return rest;
          // like the real server: no id, or an id it no longer has, is a new instance
          return fresh || !known.has(i.id) ? { ...rest, id: `srv-${++this.seq}` } : i;
        });
        this.saves.push({ version: this.version, items: structuredClone(this.items), sent: structuredClone(items) });
        return { version: this.version, items: structuredClone(this.items) };
      },
      load: async (dashboardId) => ({ dashboardId, version: this.version, items: structuredClone(this.items) }),
    };
  }
}
