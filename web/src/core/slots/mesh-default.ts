// Registers the built-in `mesh-default` views. Imported for its side effect.
import { registerSlotView, DEFAULT_THEME_ID } from "./registry";
import { MeshDefaultAuthScreen } from "./auth-screen/mesh-default-view";

registerSlotView("auth.screen", DEFAULT_THEME_ID, MeshDefaultAuthScreen);
