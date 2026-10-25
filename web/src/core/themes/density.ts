// Interface density: comfortable | compact. Compact shrinks the rem (type and the
// --space-unit spacing scale with it), so the same width fits more, without browser zoom.
// Stored like the colour mode: server (user_ui_state, scope `appearance`, key `density`)
// plus the localStorage "density" cache that index.html reads before the first frame.
// Pulling the server's value after sign-in is done by syncModeFromServer (same request).
import { markDensityChoice, remember, saveAppearance, useModeStore } from "./mode";

export type Density = "comfortable" | "compact";

/** The density painted on <html> (index.html / the sign-in sync / chooseDensity). */
export const readDensity = (): Density => (document.documentElement.dataset.density === "compact" ? "compact" : "comfortable");

export function chooseDensity(density: Density): void {
  markDensityChoice();
  document.documentElement.dataset.density = density;
  remember("density", density);
  useModeStore.setState({ density });
  saveAppearance(useModeStore.getState().chosen);
}
