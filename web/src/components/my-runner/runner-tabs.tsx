import { useEffect, useRef, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { cn } from "@/lib/cn";

export const RUNNER_TABS = ["overview", "agents", "accounts", "pools", "profiles", "history"] as const;
export type RunnerTab = (typeof RUNNER_TABS)[number];

/** Tabs that were merged or split: an old link still lands on the right one. */
const TAB_ALIASES: Record<string, RunnerTab> = { resources: "accounts" };

/** The open tab lives in `?tab=`, so a link opens it; anything else is the overview. */
export function useRunnerTab(): [RunnerTab, (tab: RunnerTab) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab") ?? "";
  const tab = (RUNNER_TABS as readonly string[]).includes(raw) ? (raw as RunnerTab) : TAB_ALIASES[raw] ?? "overview";
  const select = (next: RunnerTab) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next === "overview") out.delete("tab");
      else out.set("tab", next);
      return out;
    }, { replace: true });
  return [tab, select];
}

export function RunnerTabList({ tab, onSelect }: { tab: RunnerTab; onSelect: (tab: RunnerTab) => void }) {
  const { t } = useTranslation("runner");
  const refs = useRef<Partial<Record<RunnerTab, HTMLButtonElement | null>>>({});
  // On a narrow screen the open tab may sit past the edge of the strip (a link to History): bring it in.
  useEffect(() => { refs.current[tab]?.scrollIntoView?.({ block: "nearest", inline: "nearest" }); }, [tab]);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = RUNNER_TABS.indexOf(tab);
    let next: number;
    if (e.key === "ArrowRight") next = (i + 1) % RUNNER_TABS.length;
    else if (e.key === "ArrowLeft") next = (i - 1 + RUNNER_TABS.length) % RUNNER_TABS.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = RUNNER_TABS.length - 1;
    else return;
    e.preventDefault();
    const target = RUNNER_TABS[next]!;
    onSelect(target);
    refs.current[target]?.focus();
  };

  return (
    // Six short labels nearly fit 393px; the rest scrolls the strip, never the page.
    <div role="tablist" aria-label={t("tabs.label")} onKeyDown={onKeyDown} className="flex overflow-x-auto border-b border-border">
      {RUNNER_TABS.map((id) => (
        <button
          key={id}
          ref={(el) => { refs.current[id] = el; }}
          type="button"
          role="tab"
          id={`my-runner-tab-${id}`}
          aria-selected={tab === id}
          // Only the open panel is mounted, so only its tab may point at it.
          aria-controls={tab === id ? `my-runner-panel-${id}` : undefined}
          tabIndex={tab === id ? 0 : -1}
          onClick={() => onSelect(id)}
          className={cn(
            "shrink-0 border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap",
            tab === id ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          {t(`tabs.${id}`)}
        </button>
      ))}
    </div>
  );
}
