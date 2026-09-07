/**
 * Class-name recipes for the sidebar rail icon tiles (`#bb8f1092`).
 *
 * These live in their own module, free of any import, for one reason: the
 * contrast guard (`scripts/assert-rail-icon-contrast.mjs`) imports THIS file
 * and feeds its output to a real browser. If the recipes lived inline in
 * `sidebar.tsx`, the guard would have to restate them, and a restated recipe
 * is a copy that drifts — the guard would keep passing over class strings the
 * component had stopped emitting. Here there is exactly one definition, and
 * both the component and the guard read it.
 *
 * Each function returns the class PARTS, unmerged. The merge (`cn`, i.e.
 * tailwind-merge over clsx) is where the interesting failure lives — a later
 * class silently displacing an earlier one — so the guard must perform the
 * real merge on the real parts rather than be handed a pre-merged string.
 */

export type WorkspaceLogoVariant = "expanded" | "collapsed";

/**
 * One size table for both sidebar states instead of two hand-tuned literals
 * that only happen to look alike today. Root cause of the "logo and its
 * container drift apart when collapsed" report: the collapsed header never
 * read `icon_url` at all — it unconditionally rendered the generic MeshIcon
 * mark, which sits inside its box with padding, while the expanded header's
 * <img> fills its box edge-to-edge via `h-full w-full object-cover`. So the
 * moment a workspace had a real uploaded logo, collapsing it silently swapped
 * a full-bleed image for a padded fallback mark — a visible size/fill jump
 * that read as "the logo got bigger, the box got smaller". Fixing it means
 * both states must obey the SAME two rules, not just similar-looking ones:
 *   1. the image, when present and loaded, always fills 100% of its box
 *      (`h-full w-full object-cover`) — identical rule in both variants.
 *   2. the fallback mark is always sized as a fixed PERCENTAGE of its box
 *      (not a fixed px number picked per-variant), so scaling the box
 *      (24px expanded vs 32px collapsed) scales the mark by construction.
 */
export const WORKSPACE_LOGO_CONTAINER: Record<WorkspaceLogoVariant, string> = {
  expanded: "h-6 w-6 rounded",
  collapsed: "h-8 w-8 rounded-lg",
};

/** Fallback mark sized as a percentage of its box, never a fixed px. */
export const WORKSPACE_LOGO_ICON_FILL = "h-[58%] w-[58%]";

/**
 * The tinted-box treatment used whenever there is no loaded image.
 *
 * `bg-sidebar-primary` and `text-primary-foreground` are a PAIR: the second
 * is the foreground the design system defines for the first, and it is
 * theme-aware (white on teal in light, near-black on mint in dark). Splitting
 * the pair — keeping this fill but taking a foreground meant for a different
 * surface — is exactly the defect `#bb8f1092` shipped and this module now
 * makes hard to reintroduce: the two names sit on one line, together.
 */
export const WORKSPACE_LOGO_TINTED_BOX = "bg-sidebar-primary";
export const WORKSPACE_LOGO_TINTED_BOX_FOREGROUND = "text-primary-foreground";

/**
 * Container class parts for a WorkspaceLogo tile, in merge order.
 * `className` is the caller's override and therefore goes last.
 */
export function workspaceLogoContainerParts({
  variant,
  isLoaded,
  className,
}: {
  variant: WorkspaceLogoVariant;
  isLoaded: boolean;
  className?: string;
}): (string | false | undefined)[] {
  return [
    `flex shrink-0 items-center justify-center overflow-hidden ${WORKSPACE_LOGO_TINTED_BOX_FOREGROUND}`,
    WORKSPACE_LOGO_CONTAINER[variant],
    !isLoaded && WORKSPACE_LOGO_TINTED_BOX,
    className,
  ];
}

/**
 * Interactive state for a project tile in the collapsed rail.
 *
 * Every state here swaps background and foreground TOGETHER, because the
 * letter has to stay readable against whatever the box is filled with:
 *  - resting: inherits the tinted-box pair above (teal box, its own foreground)
 *  - hover:   the accent pair, both halves
 *  - active:  the same accent pair, permanently
 * The bug this replaces set only `text-sidebar-foreground` — a foreground
 * calculated for the sidebar's own surface — over the teal fill, which
 * measured 1.28:1 in light and 1.74:1 in dark: a filled box with an
 * effectively invisible letter in it.
 */
export function projectRailIconStateClasses(isActive: boolean): string {
  return [
    "hover:bg-sidebar-accent hover:text-sidebar-primary",
    isActive ? "bg-sidebar-accent text-sidebar-primary" : "",
  ]
    .filter(Boolean)
    .join(" ");
}

/** Full, unmerged container parts for a collapsed-rail project tile. */
export function projectRailIconParts(
  isActive: boolean,
): (string | false | undefined)[] {
  return workspaceLogoContainerParts({
    variant: "collapsed",
    isLoaded: false,
    className: projectRailIconStateClasses(isActive),
  });
}

/** The letter/emoji inside a project tile. */
export const PROJECT_RAIL_ICON_LABEL = "text-xs font-medium";
