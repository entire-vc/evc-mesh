/// <reference types="node" />
/**
 * WCAG contrast, computed from the theme file the app actually ships.
 *
 * Colour pairings that are chosen by eye are the reason this exists: the Docs
 * tree's selected row and the document editor's toolbar were both given
 * `--accent` as a fill while the text kept the inherited body foreground, which
 * measures 2.82:1 in light and 1.73:1 in dark — below the 4.5:1 WCAG AA floor
 * for body text, and reported by a user as "you cannot see what is there" both
 * times.
 *
 * So the check belongs to the tokens rather than to a rendered pixel: this
 * reads `src/styles/brandkit.css`, follows `var(--x)` indirection, and computes
 * the ratio. A test that pins the number cannot be regressed by someone picking
 * a nicer-looking token.
 *
 * Test-only — it reads the file from disk and is never imported by app code.
 */
import { readFileSync } from "node:fs";
import { resolve as resolvePath } from "node:path";

/**
 * The stylesheet, comments stripped.
 *
 * Resolved from the package root rather than `import.meta.url`: the jsdom
 * environment reports an http:// module URL, which `fileURLToPath` rejects.
 * Comments go first because several declarations sit on the line after a
 * `/* ... *\/` and the line-wise parse below would otherwise skip exactly those.
 */
function readBrandkit(): string {
  return readFileSync(
    resolvePath(process.cwd(), "src/styles/brandkit.css"),
    "utf8",
  ).replace(/\/\*[\s\S]*?\*\//g, "");
}

/** Raw `--name: <value>` declarations inside every block matching `selector`. */
function tokens(css: string, selector: string): Map<string, string> {
  const found = new Map<string, string>();
  // Several :root blocks exist (primitives, then semantics); take them all,
  // later declarations winning, exactly as the cascade does.
  const blocks = css.matchAll(new RegExp(`${selector}\\s*\\{([^}]*)\\}`, "g"));
  for (const block of blocks) {
    for (const line of block[1]!.split(";")) {
      const m = /^\s*(--[\w-]+)\s*:\s*(.+)$/.exec(line);
      if (m) found.set(m[1]!, m[2]!.trim());
    }
  }
  return found;
}

/** One theme's resolved token table. */
export type Theme = Map<string, string>;

/** The two themes brandkit.css defines, `.dark` layered over `:root`. */
export function brandkitThemes(): { light: Theme; dark: Theme } {
  const css = readBrandkit();
  const light = tokens(css, ":root");
  return { light, dark: new Map([...light, ...tokens(css, "\\.dark")]) };
}

/** Follows `var(--x)` indirection down to three raw HSL numbers. */
function resolveToken(scope: Theme, name: string): [number, number, number] {
  let value = scope.get(name);
  for (let hops = 0; value && hops < 10; hops++) {
    const ref = /^var\((--[\w-]+)\)$/.exec(value);
    if (!ref) break;
    value = scope.get(ref[1]!);
  }
  const parts = (value ?? "").match(/[\d.]+/g);
  if (!parts || parts.length < 3) throw new Error(`cannot resolve ${name}: ${value}`);
  return [Number(parts[0]), Number(parts[1]) / 100, Number(parts[2]) / 100];
}

/** HSL (h in degrees, s/l in 0-1) to sRGB, each channel 0-1. */
function hslToRgb01([h, s, l]: [number, number, number]): [number, number, number] {
  const c = (1 - Math.abs(2 * l - 1)) * s;
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
  const m = l - c / 2;
  const seg = Math.floor(h / 60) % 6;
  const rgb = [
    [c, x, 0],
    [x, c, 0],
    [0, c, x],
    [0, x, c],
    [x, 0, c],
    [c, 0, x],
  ][seg]!.map((v) => v + m);
  return [rgb[0]!, rgb[1]!, rgb[2]!];
}

function relativeLuminanceRGB01([r, g, b]: [number, number, number]): number {
  const lin = [r, g, b].map((v) =>
    v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4,
  );
  return 0.2126 * lin[0]! + 0.7152 * lin[1]! + 0.0722 * lin[2]!;
}

function relativeLuminance(hsl: [number, number, number]): number {
  return relativeLuminanceRGB01(hslToRgb01(hsl));
}

function contrastFromLuminance(la: number, lb: number): number {
  const [hi, lo] = la > lb ? [la, lb] : [lb, la];
  return (hi + 0.05) / (lo + 0.05);
}

/** WCAG 2.x contrast ratio between two semantic tokens, in one theme. */
export function contrast(scope: Theme, a: string, b: string): number {
  return contrastFromLuminance(
    relativeLuminance(resolveToken(scope, a)),
    relativeLuminance(resolveToken(scope, b)),
  );
}

/**
 * Contrast of an opaque token (`fg`) against another token (`bg`) rendered at
 * `alpha` opacity OVER a third, fully-opaque token (`base`) — the shape
 * Tailwind's `bg-accent/30` produces: a translucent fill blended with
 * whatever surface actually sits behind it, not `bg` at full strength.
 * `contrast()` alone cannot express this — it only ever compares two tokens
 * at face value, and an alpha-suffixed utility class is not a token.
 */
export function contrastOverBlend(
  scope: Theme,
  fg: string,
  bg: string,
  alpha: number,
  base: string,
): number {
  const fgRgb = hslToRgb01(resolveToken(scope, fg));
  const bgRgb = hslToRgb01(resolveToken(scope, bg));
  const baseRgb = hslToRgb01(resolveToken(scope, base));
  const blended: [number, number, number] = [
    bgRgb[0] * alpha + baseRgb[0] * (1 - alpha),
    bgRgb[1] * alpha + baseRgb[1] * (1 - alpha),
    bgRgb[2] * alpha + baseRgb[2] * (1 - alpha),
  ];
  return contrastFromLuminance(
    relativeLuminanceRGB01(fgRgb),
    relativeLuminanceRGB01(blended),
  );
}

/** WCAG AA for body text. */
export const AA_TEXT = 4.5;
/** WCAG 1.4.11, non-text contrast — borders, rules, state indicators. */
export const AA_NON_TEXT = 3;
