import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ChatFeed, type ChatFeedProps } from "@/components/chat/chat-feed";
import type { ChatMessage } from "@/lib/chat/types";

// jsdom applies no Tailwind, so the colour a reader sees is resolved by hand: the nearest
// ancestor's text-<token> / bg-<token> utility, looked up in the brandkit's light and dark
// token blocks. A green screenshot cannot hide the defect this way: the file name inherited
// the user bubble's text-primary-foreground over the attachment's bg-muted (white on near-white
// in light, near-black on dark-raised in dark).

const css = readFileSync(join(process.cwd(), "src/styles/brandkit.css"), "utf8");

function block(selector: ":root" | ".dark"): Record<string, string> {
  const vars: Record<string, string> = {};
  const re = new RegExp(`(?:^|\\n)${selector.replace(".", "\\.")}\\s*\\{([^}]*)\\}`, "g");
  for (const m of css.matchAll(re)) {
    for (const d of m[1]!.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) vars[d[1]!] = d[2]!.trim();
  }
  return vars;
}

const LIGHT = block(":root");
const DARK = { ...LIGHT, ...block(".dark") };

function hsl(vars: Record<string, string>, token: string): [number, number, number] {
  let v = vars[`--${token}`];
  for (let i = 0; v && i < 5; i++) {
    const ref = /^var\((--[\w-]+)\)$/.exec(v);
    if (!ref) break;
    v = vars[ref[1]!];
  }
  const m = v && /^([\d.]+)\s+([\d.]+)%\s+([\d.]+)%$/.exec(v);
  if (!m) throw new Error(`token --${token} does not resolve to an HSL triple: ${v}`);
  return [Number(m[1]), Number(m[2]) / 100, Number(m[3]) / 100];
}

function luminance([h, s, l]: [number, number, number]): number {
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) => {
    const k = (n + h / 30) % 12;
    return l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
  };
  const lin = (c: number) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(f(0)) + 0.7152 * lin(f(8)) + 0.0722 * lin(f(4));
}

function contrast(vars: Record<string, string>, fg: string, bg: string): number {
  const [a, b] = [luminance(hsl(vars, fg)), luminance(hsl(vars, bg))].sort((x, y) => y - x);
  return (a! + 0.05) / (b! + 0.05);
}

const COLOR_TOKENS = /^(?:foreground|primary-foreground|muted-foreground|card-foreground|muted|primary|card|background|destructive)$/;

function nearest(el: Element, prefix: "text" | "bg"): string {
  for (let n: Element | null = el; n; n = n.parentElement) {
    const hit = [...n.classList].find((c) => c.startsWith(`${prefix}-`) && COLOR_TOKENS.test(c.slice(prefix.length + 1)));
    if (hit) return hit.slice(prefix.length + 1);
  }
  throw new Error(`no ${prefix}- colour on ${el.outerHTML}`);
}

const message: ChatMessage = {
  id: "m1",
  thread_id: "t1",
  role: "user",
  body: "see the file",
  status: "done",
  truncated: false,
  turn_id: null,
  error_code: null,
  client_message_id: null,
  attachments: [{ id: "a1", file_name: "notes-ac3.txt", mime: "text/plain", size: 17, width: null, height: null }],
  created_at: "2026-10-26T10:00:00Z",
};

const props: ChatFeedProps = {
  projectId: "p1",
  messages: [message],
  tools: [],
  activeTurn: null,
  error: null,
  agentName: "Agent",
  hasMore: false,
  loadingMore: false,
  loading: false,
  loadError: null,
  online: true,
  onLoadMore: () => {},
  onRetryLoad: () => {},
  onRetryTurn: () => {},
  language: "en",
};

describe("chat file attachment on the user's own message", () => {
  for (const [mode, vars] of [["light", LIGHT], ["dark", DARK]] as const) {
    it(`keeps the file name and size readable in ${mode} (WCAG >= 4.5:1)`, () => {
      render(
        <MemoryRouter>
          <ChatFeed {...props} />
        </MemoryRouter>,
      );
      const card = screen.getByTestId("chat-attachment-file");
      const name = screen.getByText("notes-ac3.txt");
      const size = screen.getByText("17 B");
      expect(card).toContainElement(name);
      expect(card).toContainElement(size);
      const bg = nearest(name, "bg");
      expect(bg, "the attachment keeps its own surface").toBe("muted");
      for (const [label, el] of [["name", name], ["size", size]] as const) {
        const fg = nearest(el, "text");
        const ratio = contrast(vars, fg, bg);
        expect(ratio, `${label}: text-${fg} on bg-${bg} in ${mode} = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5);
      }
    });
  }
});
