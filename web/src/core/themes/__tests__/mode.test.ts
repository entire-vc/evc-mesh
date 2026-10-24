import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});
import { api, ApiRequestError } from "@/lib/api";
import { chooseMode, setThemeModes, syncModeFromServer, useModeStore, watchSystemMode } from "../mode";

// matchMedia stand-in whose `matches` the test flips and whose listeners it fires.
let osDark = false;
let listeners: Array<() => void> = [];
beforeEach(() => {
  osDark = false;
  listeners = [];
  vi.stubGlobal("matchMedia", () => ({
    get matches() {
      return osDark;
    },
    addEventListener: (_: string, f: () => void) => listeners.push(f),
    removeEventListener: (_: string, f: () => void) => (listeners = listeners.filter((l) => l !== f)),
  }));
  localStorage.clear();
  document.documentElement.className = "";
  useModeStore.setState({ chosen: "system", forced: null });
  vi.mocked(api).mockReset();
  vi.mocked(api).mockResolvedValue({});
});

const isDark = () => document.documentElement.classList.contains("dark");

describe("colour mode", () => {
  it("a choice made on another device wins here after the local cache is cleared", async () => {
    vi.mocked(api).mockResolvedValueOnce({ state: { mode: "dark" } });
    await syncModeFromServer();
    expect(useModeStore.getState().chosen).toBe("dark");
    expect(isDark()).toBe(true);
    expect(localStorage.getItem("theme")).toBe("dark");
  });

  it("a choice made while the sign-in sync is pending is not overwritten by the late answer", async () => {
    let answer!: (v: unknown) => void;
    vi.mocked(api).mockReturnValueOnce(new Promise((r) => (answer = r)));
    const sync = syncModeFromServer();
    chooseMode("light");
    answer({ state: { mode: "dark" } });
    await sync;
    expect(useModeStore.getState().chosen).toBe("light");
    expect(localStorage.getItem("theme")).toBe("light");
  });

  it("choosing saves to the server scope `appearance`", () => {
    chooseMode("light");
    expect(api).toHaveBeenCalledWith("/api/v1/me/ui-state/appearance", { method: "PUT", body: { state: { mode: "light" } } });
    expect(localStorage.getItem("theme")).toBe("light");
  });

  it("with nothing on the server, an explicit local choice is uploaded; no local choice uploads nothing", async () => {
    vi.mocked(api).mockRejectedValue(new ApiRequestError("not found", "not_found", 404));
    await syncModeFromServer();
    expect(api).toHaveBeenCalledTimes(1);

    localStorage.setItem("theme", "dark");
    vi.mocked(api).mockClear();
    vi.mocked(api).mockRejectedValueOnce(new ApiRequestError("not found", "not_found", 404)).mockResolvedValue({});
    await syncModeFromServer();
    expect(api).toHaveBeenLastCalledWith("/api/v1/me/ui-state/appearance", { method: "PUT", body: { state: { mode: "dark" } } });
  });

  it("an unreachable server changes nothing", async () => {
    localStorage.setItem("theme", "light");
    useModeStore.setState({ chosen: "light" });
    vi.mocked(api).mockRejectedValue(new Error("offline"));
    await syncModeFromServer();
    expect(useModeStore.getState().chosen).toBe("light");
    expect(api).toHaveBeenCalledTimes(1);
  });

  it("system follows a prefers-color-scheme change without reload", () => {
    const stop = watchSystemMode();
    useModeStore.setState({ chosen: "system" });
    expect(isDark()).toBe(false);
    osDark = true;
    listeners.forEach((f) => f());
    expect(isDark()).toBe(true);
    osDark = false;
    listeners.forEach((f) => f());
    expect(isDark()).toBe(false);
    stop();
    expect(listeners).toHaveLength(0);
  });

  it("an explicit choice ignores the OS change", () => {
    const stop = watchSystemMode();
    useModeStore.setState({ chosen: "light" });
    osDark = true;
    listeners.forEach((f) => f());
    expect(isDark()).toBe(false);
    stop();
  });

  it("a theme with one mode forces it over the user's choice, and releases it after", () => {
    useModeStore.setState({ chosen: "dark" });
    setThemeModes(["light"]);
    expect(isDark()).toBe(false);
    setThemeModes(["dark", "light"]);
    expect(isDark()).toBe(true);
    setThemeModes(["dark"]);
    useModeStore.setState({ chosen: "light" });
    expect(isDark()).toBe(true);
    setThemeModes(undefined);
    expect(isDark()).toBe(false);
  });
});
