import { useCallback, useRef, useState, type ComponentType } from "react";
import { act, cleanup, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import type { RealtimeEvent, SlotContext } from "../src/index";
import { harnessHost } from "./host";
import { mounterFor } from "./mounters/registry";
import type { AsyncState, Mounted, RenderEnv, Role, Scenario, SlotMounter, Spies } from "./types";

const CONTRACT_VERSION = "1.0.0";

const mounts = new Set<() => void>();

/** Unmounts everything `mountSlot` mounted and restores what it replaced. Called by the runner after each test. */
export function cleanupMounts(): void {
  // Last mounted, first undone: each mount's fetch trap wraps the previous one, so they unwind in reverse.
  for (const undo of [...mounts].reverse()) undo();
  mounts.clear();
  cleanup();
}

function buildCtx(s: Scenario): SlotContext {
  return {
    slot: s.slot,
    contractVersion: CONTRACT_VERSION,
    locale: s.locale ?? "en",
    mode: s.mode ?? "light",
    isMobile: s.isMobile ?? false,
    // auth.screen runs before sign-in: the workspace is unknown (ADR D3).
    workspace: s.slot === "auth.screen" ? null : { id: "w1", slug: "ws", name: "Workspace" },
  };
}

/** `fetch` is the one network door a view has; anything the host does not answer is recorded and refused. */
function trapFetch(unexpected: string[], state: AsyncState): () => void {
  const host = harnessHost();
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const answer = host.handleRequest?.(url, init, { state });
    if (answer !== undefined) {
      if (answer instanceof Response || answer instanceof Promise) return answer;
      return new Response(JSON.stringify(answer), { status: 200, headers: { "content-type": "application/json" } });
    }
    unexpected.push(`${init?.method ?? "GET"} ${url}`);
    throw new TypeError(`mountSlot: unexpected request ${url} (a view reaches data only through its props and actions)`);
  }) as typeof fetch;
  return () => {
    globalThis.fetch = original;
  };
}

function buildActions(mounter: SlotMounter<never>, role: Role, scenario: Scenario<never>, spies: Spies) {
  const out: Record<string, unknown> = {};
  for (const spec of mounter.actions) {
    if (role === "viewer" && spec.needs) continue; // no right = no method (catalog §4 rule 4)
    const impl = scenario.actions?.[spec.name];
    const fn = vi.fn((...args: unknown[]) => {
      if (impl) return (impl as (...a: unknown[]) => unknown)(...args);
      return spec.async ? Promise.resolve() : undefined;
    });
    spies[spec.name] = fn as unknown as Spies[string];
    out[spec.name] = fn;
  }
  return out;
}

/**
 * Mounts `view` as the slot named in `scenario.slot`, inside a container that does what the page does:
 * real primitives from the host, data held in state, realtime delivered through the host transport.
 */
export function mountSlot<D = unknown>(view: ComponentType<never>, scenario: Scenario<D>): Mounted<D> {
  const host = harnessHost();
  host.reset();
  const mounter = mounterFor(scenario.slot) as unknown as SlotMounter<D>;
  const role = scenario.role ?? "member";
  const ctx = buildCtx(scenario as Scenario);
  for (const point of scenario.extensions ?? []) host.installExtension(point);

  const unexpectedRequests: string[] = [];
  const state = (scenario.options?.state as AsyncState | undefined) ?? "ready";
  const undo: Array<() => void> = [trapFetch(unexpectedRequests, state)];
  if (mounter.setup) undo.push(mounter.setup(scenario));

  const spies: Spies = {};
  const actions = buildActions(mounter as unknown as SlotMounter<never>, role, scenario as unknown as Scenario<never>, spies);

  let base = mounter.defaults(scenario);
  if (typeof scenario.data === "function") base = (scenario.data as (b: D) => D)(base);
  else if (scenario.data) base = { ...base, ...scenario.data };

  const latest = { data: base };
  const handle: { set: ((u: (d: D) => D) => void) | null } = { set: null };

  function Container() {
    const [data, setData] = useState<D>(base);
    handle.set = setData;
    latest.data = data;
    const reduce = useRef(mounter.reduce);
    host.sdk.useRealtime(
      { kind: "project", id: "p1" },
      useCallback((e: RealtimeEvent) => setData((d) => reduce.current?.(d, e) ?? d), []),
    );
    const shown = state !== "ready" && mounter.asyncData ? mounter.asyncData(data, state, spies) : data;
    const env: RenderEnv<D> = {
      view: view as RenderEnv<D>["view"],
      data: shown,
      actions,
      themeSettings: scenario.themeSettings ?? {},
      ctx,
      scenario,
      host,
      spies,
      role,
    };
    return (
      <host.Provider
        role={role}
        ctx={ctx}
        coreActions={{
          openTask: (id) => spies.openTask?.(id),
          quickEdit: (id, field, value) => Promise.resolve(spies.quickEdit?.(id, { field, value }) as Promise<void>),
        }}
      >
        {mounter.render(env)}
      </host.Provider>
    );
  }

  // Registered before render: a view that throws while rendering must still give back the fetch trap and the rest of `undo`.
  let r: ReturnType<typeof render> | undefined;
  let unmounted = false;
  const unmount = () => {
    if (unmounted) return;
    unmounted = true;
    mounts.delete(unmount);
    r?.unmount();
    for (const u of undo.reverse()) u();
  };
  mounts.add(unmount);
  try {
    r = render(<Container />);
  } catch (e) {
    unmount();
    throw e;
  }
  const mountedRender = r;

  return {
    spies,
    user: userEvent.setup({ delay: null }),
    container: mountedRender.container,
    unexpectedRequests,
    unmount,
    data: () => latest.data,
    async emitRealtime(event) {
      await act(async () => {
        host.emitRealtime(event);
        await new Promise((res) => setTimeout(res, 0));
      });
    },
    async update(next) {
      await act(async () => {
        handle.set?.((d) => (typeof next === "function" ? (next as (p: D) => D)(d) : { ...d, ...next }));
      });
    },
  };
}
