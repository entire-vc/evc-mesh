// @entire-vc/mesh-theme-sdk: the contract between Mesh and its themes. Version of this package =
// version of the slot contracts (see CONTRACT_VERSION).
export type * from "./api";
export type * from "./primitives";
export type * from "./slots";
export type * from "./nav";
export type * from "./auth";
export type * from "./dashboard";
export { BOARD_CONTRACT_VERSION, DOC_CONTRACT_VERSION } from "./slots";
export * from "./runtime";

/** Version of the slot contracts this SDK describes. */
export const CONTRACT_VERSION = "1.0.0";
