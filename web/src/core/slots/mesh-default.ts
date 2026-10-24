// Registers the built-in `mesh-default` views. Imported for its side effect.
import { registerSlotView, DEFAULT_THEME_ID } from "./registry";
import { MeshDefaultAuthScreen } from "./auth-screen/mesh-default-view";
import { MeshDefaultNavSidebar } from "./nav-sidebar/mesh-default-view";
import { MeshDefaultNavTopbar } from "./nav-topbar/mesh-default-view";

registerSlotView("auth.screen", DEFAULT_THEME_ID, MeshDefaultAuthScreen);
registerSlotView("nav.sidebar", DEFAULT_THEME_ID, MeshDefaultNavSidebar);
registerSlotView("nav.topbar", DEFAULT_THEME_ID, MeshDefaultNavTopbar);
