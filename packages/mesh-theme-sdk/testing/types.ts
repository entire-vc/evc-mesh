// Contract-test harness for theme slot views (Mesh Doc "theme-slot-catalog-v1" §6).
// A view is tested with the host's real primitives; only the transport (fetch, WebSocket) is replaced, at the
// harness boundary. The host (Mesh core, or a core-compatible test host) is injected through `HarnessHost`.
import type { ComponentType, ReactElement, ReactNode } from "react";
import type { UserEvent } from "@testing-library/user-event";
import type * as Sdk from "../src/index";
import type { RealtimeEvent, SlotContext } from "../src/index";

export type Role = "member" | "viewer";

/** Insertion points an extension can fill (catalog §4 rule 6). */
export type ExtensionPoint = "task.badge" | "task.panel" | "task.actions" | "sidebar.item";

/** Every slot a theme can declare in `theme.slots` (v1). `dashboard.widget.<id>` is open-ended. */
export type SlotName =
  | "board.layout"
  | "board.column"
  | "board.card"
  | "task.row"
  | "task.view"
  | "nav.sidebar"
  | "nav.topbar"
  | "doc.tree"
  | "doc.view"
  | "auth.screen"
  | "dashboard.layout"
  | `dashboard.widget.${string}`;

/** The runtime half of the SDK the harness (and the reference views it composes) calls. */
export type SdkRuntime = Pick<
  typeof Sdk,
  "DndProvider" | "Draggable" | "DropZone" | "Can" | "InlineEdit" | "TaskLink" | "useRealtime"
>;

export interface HostProviderProps {
  role: Role;
  ctx: SlotContext;
  /** What the core primitives call: open a task, quick-edit a field. Wired to the spies of the mounted slot. */
  coreActions: {
    openTask(taskId: string): void;
    quickEdit(taskId: string, field: string, value: unknown): Promise<void>;
  };
  children: ReactNode;
}

/** The host the contract tests run on. Mesh core provides one in `web/src/test-utils/slot-host.tsx`. */
export interface HarnessHost {
  sdk: SdkRuntime;
  /** Router, permissions for `role`, core actions and slot context, as the page container sets them up. */
  Provider: ComponentType<HostProviderProps>;
  /** Pushes one event through the host's realtime transport (what the WebSocket would deliver). */
  emitRealtime(event: RealtimeEvent): void;
  /** Makes an extension fill the insertion point; the host answers its own registry request itself. */
  installExtension(point: ExtensionPoint): void;
  /** CSS selector of what the host draws for an extension at the point (present after `installExtension`). */
  extensionSelector(point: ExtensionPoint): string;
  /**
   * Answers the requests the host's own blocks legitimately make (the extension registry, the data endpoints behind
   * the SDK hooks a theme's own widget reads). `undefined` = not ours. A `Response` is returned as is, a promise
   * is awaited (a never-settling one keeps the hook loading); anything else is the JSON body of a 200.
   */
  handleRequest?(url: string, init?: RequestInit, scenario?: { state: AsyncState }): unknown | undefined;
  /** Back to a clean state before each mount. */
  reset(): void;
}

export type Spy = ((...args: unknown[]) => unknown) & {
  mock: { calls: unknown[][] };
  mockClear(): void;
};

export type Spies = Record<string, Spy>;

export interface Scenario<D = unknown> {
  slot: SlotName;
  /** Overrides on top of the slot's default fixture: an object is shallow-merged, a function gets the default. */
  data?: Partial<D> | ((base: D) => D);
  role?: Role;
  locale?: string;
  mode?: "light" | "dark";
  isMobile?: boolean;
  themeSettings?: Record<string, unknown>;
  /** Extensions installed at insertion points before the mount. */
  extensions?: ExtensionPoint[];
  /** Replaces an action's implementation (e.g. `moveTask` rejecting). The spy still records the call. */
  actions?: Record<string, (...args: never[]) => unknown>;
  /** Slot-specific knobs read by the slot's mounter (e.g. `groupBy` of the board). */
  options?: Record<string, unknown>;
}

export interface Mounted<D = unknown> {
  spies: Spies;
  user: UserEvent;
  container: HTMLElement;
  /** Delivers an event through the host transport; the container reacts to it like the page does. */
  emitRealtime(event: RealtimeEvent): Promise<void>;
  /** Changes the data the view gets, like a store update would. */
  update(next: Partial<D> | ((prev: D) => D)): Promise<void>;
  data(): D;
  /** Requests the view made that the harness did not expect (must stay empty: CT-common-03). */
  unexpectedRequests: string[];
  unmount(): void;
}

/** What a slot's mounter supplies: default data, the action set, and the composition around the view. */
export interface SlotMounter<D = never> {
  slot: string;
  defaults(scenario: Scenario<D>): D;
  /** Actions as the container supplies them. `omit` lists the names the role has no right to (absent from `actions`). */
  actions: ActionSpec[];
  /** How the container reacts to a realtime event (a new data object; untouched pieces keep their identity). */
  reduce?(data: D, event: RealtimeEvent): D;
  /** Draws the view inside its container (reference neighbours, DnD provider, ...). */
  render(env: RenderEnv<D>): ReactElement;
  /** Installs what the slot needs from the environment (e.g. jsdom layout for drag and drop); returns the undo. */
  setup?(scenario: Scenario<D>): () => void;
  /** Data for the Async state under test (loading / error / refreshing) where the slot has one. */
  asyncData?(data: D, state: AsyncState, spies: Spies): D;
}

export type AsyncState = "ready" | "loading" | "error" | "refreshing";

export interface ActionSpec {
  name: string;
  /** Right needed for the action to exist (`edit`/`move`/`create` vanish for `viewer`); absent = always there. */
  needs?: "edit" | "move" | "create";
  /** Resolves a promise (async actions in the contract). */
  async?: boolean;
}

export interface RenderEnv<D> {
  view: ComponentType<never>;
  data: D;
  actions: Record<string, unknown>;
  themeSettings: Record<string, unknown>;
  ctx: SlotContext;
  scenario: Scenario<D>;
  host: HarnessHost;
  spies: Spies;
  role: Role;
}
