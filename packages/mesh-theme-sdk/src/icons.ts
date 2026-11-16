import type { ComponentType } from "react";

/** An icon a theme draws: a component that takes `className` and paints with `currentColor`. */
export type ThemeIcon = ComponentType<{ className?: string }>;

/** Search keywords of a theme icon, per language (a string is split on spaces). */
export interface ThemeIconMeta {
  en?: string | string[];
  ru?: string | string[];
}

/**
 * The icon part of a views bundle (next to `views`): `icons` maps kebab-case names (`[a-z0-9-]{1,40}`)
 * to components, `icons_meta` gives the picker keywords for them. A value `icon:<name>` is drawn with
 * the live theme's icon of that name, then the core icon of that name, then the entity's default, so a
 * stored value survives a theme change. A theme icon with the name of a core icon redraws it.
 */
export interface ThemeBundleIcons {
  icons?: Record<string, ThemeIcon>;
  icons_meta?: Record<string, ThemeIconMeta>;
}
