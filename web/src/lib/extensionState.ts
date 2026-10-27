import type { ExtensionStatusDto } from "@/hooks/useExtensionCatalog";

/** The five states an extension shows anywhere in the product: Catalog, Installed, its page, the instance admin. */
export type ExtensionStateName = "not_allowed" | "available" | "enabled" | "error" | "blocked";

/** Whose keys an enabled extension runs on: the instance admin's or the workspace's own connection. */
export type KeysFrom = "instance" | "workspace";

export interface ExtensionStateInput {
  kind?: string;
  allowed: boolean;
  blocked: boolean;
  blockedReason?: string;
  /** From `GET /workspaces/{ws}/extensions`: the one source of health and last error. */
  status?: Pick<ExtensionStatusDto, "enabled" | "source" | "health" | "last_error">;
  /** Themes only: this theme paints the workspace now; undefined when that is not known. */
  themeActive?: boolean;
  /**
   * The workspace's own connection row. It never decides the state on its own: an
   * instance-governed extension may carry an empty row, a switched-off one keeps its row.
   * `status.enabled`/`status.source` (the provider's Resolve) do.
   */
  connection?: unknown;
}

export interface ExtensionState {
  state: ExtensionStateName;
  keysFrom?: KeysFrom;
  /** The last error text, for `error`. */
  error?: string;
  /** The catalog's reason, for `blocked`; empty when the catalog gave none. */
  blockedReason?: string;
}

/**
 * The single place the state of an extension is decided. Order matters: a catalog
 * block beats the admin's allow-list, which beats a failing check, which beats "on".
 * A theme is chosen, not connected: it is enabled when it paints the workspace and
 * has no keys and no health check.
 */
export function extensionState(e: ExtensionStateInput): ExtensionState {
  if (e.blocked) return { state: "blocked", blockedReason: e.blockedReason ?? "" };
  if (!e.allowed) return { state: "not_allowed" };
  const s = e.status;
  if (e.kind === "theme") {
    const on = e.themeActive ?? Boolean(s?.enabled);
    return { state: on ? "enabled" : "available" };
  }
  if (!s?.enabled) return { state: "available" };
  const keysFrom: KeysFrom = s.source === "instance" ? "instance" : "workspace";
  if (s.health?.ok === false || s.last_error) {
    return { state: "error", keysFrom, error: s.last_error || s.health?.message || "" };
  }
  return { state: "enabled", keysFrom };
}

/** i18n key (namespace `integrations`) of a state's label. */
export const EXTENSION_STATE_KEY: Record<ExtensionStateName, string> = {
  not_allowed: "extensionState.notAllowed",
  available: "extensionState.available",
  enabled: "extensionState.enabled",
  error: "extensionState.error",
  blocked: "extensionState.blocked",
};

/** i18n key of the line under an enabled extension saying whose keys it runs on. */
export const KEYS_FROM_KEY: Record<KeysFrom, string> = {
  instance: "extensionState.keysFrom.instance",
  workspace: "extensionState.keysFrom.workspace",
};
