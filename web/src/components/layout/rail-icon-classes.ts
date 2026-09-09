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

/**
 * Fallback mark sized as a percentage of its box, never a fixed px — and the
 * percentage is DERIVED from the rail, not from the mark's own history.
 *
 * The collapsed rail draws one glyph size: every navigation item renders a
 * 16px icon inside a 32px tile. The workspace mark stands in the same column,
 * in an identically-sized tile, so it is the same kind of glyph and takes the
 * same size — 16 of 32 is one half, and that is where this number comes from.
 * The expanded header (24px tile) then inherits 12px by construction, which is
 * the point of expressing it as a ratio at all.
 *
 * It used to be 58%, and 58 had no derivation. It was reverse-engineered in
 * `1a63877f` to reproduce the two hand-tuned literals that commit replaced
 * (`MeshIcon size={18}` collapsed, `size={14}` expanded — 18/32 = 56.3%,
 * 14/24 = 58.3%). So the one number in this module that looked principled was
 * in fact the old ad-hoc pair wearing a percentage sign, and it carried the
 * collapsed rail's oldest visual defect through three consecutive fixes of
 * that rail without any of them touching it: at 58% the mark rendered
 * 18.55×18.55 while all ten of its neighbours rendered 16×16.
 *
 * Why that reads as "the icon slides" even though nothing moves: measured on
 * the collapsed rail, every tile is 32×32 at cx=23.5 and every glyph's ink is
 * centred to within 0.01px — there is no offset to find, and three separate
 * measurement passes correctly found none. What differs is ink WIDTH. This
 * mark's paths span its whole viewBox horizontally, so its ink width equals
 * its svg width: 18.55px at 58%, against 13.33–14.67px for every lucide
 * neighbour. A mark a quarter wider than the column it sits in breaks the
 * column's vertical edge, and a broken edge is read as the odd item being
 * out of line — the one thing a coordinate assert can never catch, because
 * the coordinates are right.
 *
 * At 50% the svg is 16×16 (identical to every neighbour) and the ink is
 * 16×11.46, whose geometric mean is 13.54px against the lucide design box's
 * 13.33px — the same optical size, not merely the same declared box.
 */
export const WORKSPACE_LOGO_ICON_FILL = "h-1/2 w-1/2";

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
 * Muted "chip" pair for a collapsed-rail PROJECT tile.
 *
 * Deliberately NOT `WORKSPACE_LOGO_TINTED_BOX` / `_FOREGROUND`. Reusing the
 * workspace logo's brand-teal pair here (via `workspaceLogoContainerParts`)
 * is the defect Pavel rejected on `#119078b0` (2026-09-08): with 13 real
 * projects, every resting tile turned the same teal as the workspace mark
 * above them, and the mark stopped reading as an anchor — "было все ок"
 * pointed straight back at the pre-`#bb8f1092` look, where project tiles had
 * no fill at rest at all.
 *
 * This pair is built from the sidebar's own neutral surfaces, not the brand
 * one — but the two themes need DIFFERENT tokens to both stay visible AND
 * stay readable, because `bg-sidebar-accent` (the tile's own hover/active
 * fill) is only ~1% lighter than the sidebar surface itself in light mode:
 * contrast-safe as a hover highlight (it's momentary, next to a page the eye
 * is already scanning), but a chip that faint at REST recreates the exact
 * defect `#bb8f1092` was filed over — a "container" no one can actually see.
 * `bg-sidebar-border` is the sidebar's next surface up in light mode (a real,
 * ~20-unit RGB step, confirmed in the rendered swatch) and stays a valid
 * pairing with `text-sidebar-foreground`; in dark mode that same token is
 * too DARK to pair with that foreground (measured 3.56:1, below the 4.5:1
 * floor — see the guard's negative control), so dark keeps `sidebar-accent`,
 * which is already both visible (dark mode's "raised" surface) and
 * contrast-proven there. Two tokens, one per theme, chosen for what's
 * actually visible AND actually readable in each — not one token reused for
 * convenience.
 */
export const PROJECT_RAIL_RESTING_BOX = "bg-sidebar-border dark:bg-sidebar-accent";
export const PROJECT_RAIL_RESTING_FOREGROUND = "text-sidebar-foreground";
export const PROJECT_RAIL_ACTIVE_FOREGROUND = "text-sidebar-primary";

/**
 * Full, unmerged container class parts for a collapsed-rail project tile.
 *
 * Geometry matches every other rail item (`h-8 w-8 rounded-lg` — same as
 * Dashboard/Initiatives/Triage), which is the "container of the same size
 * and roundedness as the rest of the rail" `#bb8f1092` criterion #1 asked
 * for. The chip fill is constant across resting/hover/active — only the
 * letter's color moves, from the sidebar's own muted foreground to the
 * brand accent — so interaction state stays legible without the fill ever
 * pretending to be the workspace's own mark.
 */
export function projectRailIconParts(
  isActive: boolean,
): (string | false | undefined)[] {
  return [
    "flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded-lg",
    PROJECT_RAIL_RESTING_BOX,
    isActive ? PROJECT_RAIL_ACTIVE_FOREGROUND : PROJECT_RAIL_RESTING_FOREGROUND,
    "hover:text-sidebar-primary",
  ];
}

/** The letter/emoji inside a project tile. */
export const PROJECT_RAIL_ICON_LABEL = "text-xs font-medium";
