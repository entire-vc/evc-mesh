export type ExtensionsLoadState = "loading" | "error" | "ready";

export interface ExtensionsOverview {
  state: ExtensionsLoadState;
  catalogCount: number;
  updatesCount: number;
}

/**
 * Scaffold data source for the Extensions section (W2.1). The live
 * `GET /workspaces/{ws}/extensions` contract lands with W1.11; until then the
 * section renders its empty states. Replace the body, keep the shape.
 */
export function useExtensionsOverview(): ExtensionsOverview {
  return { state: "ready", catalogCount: 0, updatesCount: 0 };
}
