// Lazy chunk: the panel of IconPicker (tabs, search, recents, the emoji catalogue and the core icon set).
import { type KeyboardEvent, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Ban, Search } from "lucide-react";
import { cn } from "@/lib/cn";
import CORE_ICONS from "@/core/icons/core-icons";
import keywords from "@/core/icons/core-icon-keywords.json";
import { seedCoreIcons } from "@/core/icons/core-loader";
import { getThemeIcons, useThemeIconsVersion, type ThemeIconMeta } from "@/core/icons/theme-icons";
import { iconValue, parseIconValue } from "@/core/icons/value";
import { useThemeStore } from "@/core/themes/store";
import { EntityIcon } from "./entity-icon";
import {
  EMOJI_BY_CATEGORY,
  EMOJI_CATEGORY_ORDER,
  type EmojiCategoryId,
  searchEmoji,
} from "./emoji-data";

// The icons drawn in the grid come from this chunk; EntityIcon must not wait for a second load.
seedCoreIcons(CORE_ICONS);

const RECENT_KEY = "mesh.icon-picker.recent";
const RECENT_MAX = 16;

function readRecent(): string[] {
  try {
    const raw = window.localStorage.getItem(RECENT_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed)
      ? parsed.filter((x): x is string => typeof x === "string").slice(0, RECENT_MAX)
      : [];
  } catch {
    return [];
  }
}

function writeRecent(value: string): void {
  try {
    const next = [value, ...readRecent().filter((x) => x !== value)].slice(0, RECENT_MAX);
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(next));
  } catch {
    // storage blocked or full: recents are a nicety, not a requirement
  }
}

const KW = keywords as Record<string, { en: string; ru: string }>;
const norm = (x: string) => x.toLowerCase().replace(/\u0451/g, "\u0435");
const words = (w: string | string[] | undefined) => (Array.isArray(w) ? w.join(" ") : (w ?? ""));

type Tab = "emoji" | "icons";

export interface IconPickerPanelProps {
  value: string;
  onSelect: (value: string) => void;
}

export default function IconPickerPanel({ value, onSelect }: IconPickerPanelProps) {
  const { t } = useTranslation("iconPicker");
  const themeId = useThemeStore((s) => s.viewsThemeId);
  const iconsVersion = useThemeIconsVersion();
  const themeSet = getThemeIcons(themeId);
  const [tab, setTab] = useState<Tab>(parseIconValue(value).kind === "icon" ? "icons" : "emoji");
  const [query, setQuery] = useState("");
  const [recent] = useState(readRecent);
  const searchRef = useRef<HTMLInputElement>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  const tabRefs = useRef<Record<Tab, HTMLButtonElement | null>>({ emoji: null, icons: null });

  useEffect(() => {
    searchRef.current?.focus();
  }, []);

  // Icon names the picker offers: the live theme's own first, then the core set; one entry per name.
  const iconSections = useMemo(() => {
    const themeNames = Object.keys(themeSet?.icons ?? {}).sort();
    const coreNames = Object.keys(CORE_ICONS).filter((n) => !(themeSet && n in themeSet.icons));
    const text = (name: string): string => {
      const m: ThemeIconMeta | undefined = themeSet?.meta[name];
      const k = KW[name];
      return norm(`${name.replace(/-/g, " ")} ${k?.en ?? ""} ${k?.ru ?? ""} ${words(m?.en)} ${words(m?.ru)}`);
    };
    const q = norm(query.trim());
    const keep = (names: string[]) => (q ? names.filter((n) => text(n).includes(q)) : names);
    return { theme: keep(themeNames), core: keep(coreNames) };
  }, [themeSet, iconsVersion, query]);

  const emojiResults = useMemo(() => (query.trim() ? searchEmoji(query) : null), [query]);

  const pick = (v: string) => {
    if (v) writeRecent(v);
    onSelect(v);
  };

  const cells = () =>
    Array.from(gridRef.current?.querySelectorAll<HTMLButtonElement>("button[data-cell]") ?? []);

  const moveFocus = (from: HTMLButtonElement, key: string) => {
    const list = cells();
    const i = list.indexOf(from);
    let target: HTMLButtonElement | undefined;
    if (key === "ArrowRight") target = list[i + 1];
    else if (key === "ArrowLeft") target = list[i - 1];
    else if (key === "Home") target = list[0];
    else if (key === "End") target = list[list.length - 1];
    else {
      // Up/Down: the nearest cell in the adjacent row by horizontal position (sections have
      // headers, so a flat ±columns step would drift).
      const r = from.getBoundingClientRect();
      const down = key === "ArrowDown";
      const cand = list.filter((b) => {
        const br = b.getBoundingClientRect();
        return down ? br.top > r.bottom - 2 : br.bottom < r.top + 2;
      });
      if (cand.length) {
        const rowTop = down
          ? Math.min(...cand.map((b) => b.getBoundingClientRect().top))
          : Math.max(...cand.map((b) => b.getBoundingClientRect().top));
        target = cand
          .filter((b) => Math.abs(b.getBoundingClientRect().top - rowTop) < 2)
          .sort(
            (a, b) =>
              Math.abs(a.getBoundingClientRect().left - r.left) -
              Math.abs(b.getBoundingClientRect().left - r.left),
          )[0];
      } else if (!down) {
        searchRef.current?.focus();
        return;
      }
    }
    target?.focus();
  };

  const onCellKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (["ArrowRight", "ArrowLeft", "ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) {
      e.preventDefault();
      moveFocus(e.currentTarget, e.key);
    }
  };

  const firstMatch = (): string | undefined => {
    if (tab === "emoji") return emojiResults?.[0]?.e;
    const n = iconSections.theme[0] ?? iconSections.core[0];
    return n ? iconValue(n) : undefined;
  };

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      cells()[0]?.focus();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const first = query.trim() ? firstMatch() : undefined;
      if (first) pick(first);
    }
  };

  const onTabKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const next: Tab = tab === "emoji" ? "icons" : "emoji";
      setTab(next);
      tabRefs.current[next]?.focus();
    }
  };

  const cellClass = (selected: boolean) =>
    cn(
      "flex h-9 w-9 items-center justify-center rounded-md text-xl leading-none",
      "hover:bg-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
      selected && "bg-accent",
    );

  const emojiCell = (emoji: string) => (
    <button
      key={emoji}
      type="button"
      data-cell
      data-emoji={emoji}
      aria-label={emoji}
      aria-pressed={emoji === value}
      onClick={() => pick(emoji)}
      onKeyDown={onCellKey}
      className={cellClass(emoji === value)}
    >
      {emoji}
    </button>
  );

  const iconCell = (name: string) => (
    <button
      key={name}
      type="button"
      data-cell
      data-icon={name}
      aria-label={name.replace(/-/g, " ")}
      title={name}
      aria-pressed={iconValue(name) === value}
      onClick={() => pick(iconValue(name))}
      onKeyDown={onCellKey}
      className={cellClass(iconValue(name) === value)}
    >
      <EntityIcon value={iconValue(name)} className="h-5 w-5" />
    </button>
  );

  const section = (id: string, title: string, items: string[], render: (x: string) => React.ReactNode) =>
    items.length ? (
      <section key={id} id={`icon-sec-${id}`} aria-label={title} className="mb-2">
        <h3 className="px-1 pb-1 text-xs font-medium text-muted-foreground">{title}</h3>
        <div className="grid grid-cols-8 gap-0.5">{items.map(render)}</div>
      </section>
    ) : null;

  const jump = (id: EmojiCategoryId) => {
    gridRef.current?.querySelector(`#icon-sec-${id}`)?.scrollIntoView({ block: "start" });
  };

  const recentEmoji = recent.filter((v) => parseIconValue(v).kind === "emoji");
  const recentIcons = recent
    .map((v) => parseIconValue(v))
    .flatMap((p) => (p.kind === "icon" && (CORE_ICONS[p.name] || themeSet?.icons[p.name]) ? [p.name] : []));

  const tabButton = (id: Tab, label: string) => (
    <button
      key={id}
      ref={(el) => {
        tabRefs.current[id] = el;
      }}
      type="button"
      role="tab"
      id={`icon-tab-${id}`}
      aria-selected={tab === id}
      aria-controls="icon-tabpanel"
      tabIndex={tab === id ? 0 : -1}
      onClick={() => setTab(id)}
      onKeyDown={onTabKey}
      className={cn(
        "h-8 flex-1 rounded-md px-3 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        tab === id ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:bg-accent/60",
      )}
    >
      {label}
    </button>
  );

  const emptyIcons = iconSections.theme.length + iconSections.core.length === 0;

  return (
    <div className="flex max-h-[min(24rem,70vh)] w-full flex-col">
      <div role="tablist" aria-label={t("tabs")} className="flex gap-1 border-b border-border p-2">
        {tabButton("emoji", t("tabEmoji"))}
        {tabButton("icons", t("tabIcons"))}
      </div>
      <div className="flex items-center gap-2 border-b border-border p-2">
        <div className="relative flex-1">
          <Search
            className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <input
            ref={searchRef}
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onSearchKey}
            aria-label={t("search")}
            placeholder={t("searchPlaceholder")}
            className="h-9 w-full rounded-md border border-input bg-background pl-8 pr-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
          />
        </div>
        <button
          type="button"
          onClick={() => pick("")}
          className="inline-flex h-9 shrink-0 items-center gap-1 rounded-md px-2 text-sm text-muted-foreground hover:bg-accent hover:text-accent-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Ban className="h-4 w-4" aria-hidden="true" />
          {t("none")}
        </button>
      </div>

      {tab === "emoji" && !emojiResults && (
        <div role="toolbar" aria-label={t("tabEmoji")} className="flex gap-0.5 border-b border-border px-2 py-1">
          {EMOJI_CATEGORY_ORDER.map((c) => (
            <button
              key={c}
              type="button"
              onClick={() => jump(c)}
              aria-label={t(`categories.${c}`)}
              title={t(`categories.${c}`)}
              className="flex h-7 w-7 items-center justify-center rounded text-base hover:bg-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {EMOJI_BY_CATEGORY[c][0]?.e}
            </button>
          ))}
        </div>
      )}

      <div
        ref={gridRef}
        id="icon-tabpanel"
        role="tabpanel"
        aria-labelledby={`icon-tab-${tab}`}
        className="min-h-0 flex-1 overflow-y-auto p-2"
      >
        {tab === "emoji" ? (
          emojiResults ? (
            emojiResults.length ? (
              <div className="grid grid-cols-8 gap-0.5">{emojiResults.map((r) => emojiCell(r.e))}</div>
            ) : (
              <p className="py-6 text-center text-sm text-muted-foreground">{t("empty")}</p>
            )
          ) : (
            <>
              {section("recent", t("recent"), recentEmoji, emojiCell)}
              {EMOJI_CATEGORY_ORDER.map((c) =>
                section(c, t(`categories.${c}`), EMOJI_BY_CATEGORY[c].map((x) => x.e), emojiCell),
              )}
            </>
          )
        ) : emptyIcons ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{t("iconsEmpty")}</p>
        ) : (
          <>
            {!query.trim() && section("recent", t("recent"), recentIcons, iconCell)}
            {section("theme", t("themeIcons"), iconSections.theme, iconCell)}
            {section("core", t("coreIcons"), iconSections.core, iconCell)}
          </>
        )}
      </div>
    </div>
  );
}
