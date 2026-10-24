/** Token colours are "H S% L%" (brandkit.css); a colour input speaks #rrggbb. */

export function hexToHsl(hex: string): string | null {
  let h = hex.trim().replace(/^#/, "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  if (!/^[0-9a-fA-F]{6}$/.test(h)) return null;
  const r = parseInt(h.slice(0, 2), 16) / 255;
  const g = parseInt(h.slice(2, 4), 16) / 255;
  const b = parseInt(h.slice(4, 6), 16) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  const d = max - min;
  let hue = 0;
  let s = 0;
  if (d !== 0) {
    s = d / (1 - Math.abs(2 * l - 1));
    if (max === r) hue = ((g - b) / d) % 6;
    else if (max === g) hue = (b - r) / d + 2;
    else hue = (r - g) / d + 4;
    hue *= 60;
    if (hue < 0) hue += 360;
  }
  const f = (n: number) => String(Math.round(n * 10) / 10);
  return `${f(hue)} ${f(s * 100)}% ${f(l * 100)}%`;
}

const HSL = /^(\d{1,3}(?:\.\d+)?)\s+(\d{1,3}(?:\.\d+)?)%\s+(\d{1,3}(?:\.\d+)?)%$/;

export function isHslTriplet(v: string): boolean {
  return HSL.test(v.trim());
}

export function hslToHex(hsl: string): string | null {
  const m = HSL.exec(hsl.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const s = Number(m[2]) / 100;
  const l = Number(m[3]) / 100;
  const k = (n: number) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const ch = (n: number) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  const hex = (x: number) => Math.round(Math.min(1, Math.max(0, x)) * 255).toString(16).padStart(2, "0");
  return `#${hex(ch(0))}${hex(ch(8))}${hex(ch(4))}`;
}

/** `:root{--a:1;} .dark{--a:2;}` -> { light: {a:"1"}, dark: {a:"2"} }; the server's own output, so no general CSS parser. */
export function parseTokensCss(css: string): Record<"light" | "dark", Record<string, string>> {
  const out = { light: {} as Record<string, string>, dark: {} as Record<string, string> };
  for (const m of css.matchAll(/(:root|\.dark)\s*\{([^}]*)\}/g)) {
    const target = m[1] === ".dark" ? out.dark : out.light;
    for (const d of (m[2] ?? "").matchAll(/--([a-z0-9-]+)\s*:\s*([^;]+);/gi)) target[d[1] as string] = (d[2] as string).trim();
  }
  return out;
}
