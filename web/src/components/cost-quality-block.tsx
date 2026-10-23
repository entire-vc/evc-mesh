import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import type { TaskCostSummary } from "@/lib/api";

function formatCost(cost: number): string {
  if (cost < 0.01) return "<$0.01";
  return `$${cost.toFixed(2)}`;
}

function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

const qualityBadge: Record<
  string,
  { icon: string; className: string }
> = {
  golden: {
    icon: "🏆",
    className: "bg-tone-success-100 text-tone-success-800 dark:bg-tone-success-900/30 dark:text-tone-success-300",
  },
  rework: {
    icon: "🔁",
    className: "bg-tone-warning-100 text-tone-warning-800 dark:bg-tone-warning-900/30 dark:text-tone-warning-300",
  },
  "multi-turn": {
    icon: "⚡",
    className: "bg-tone-info-100 text-tone-info-800 dark:bg-tone-info-900/30 dark:text-tone-info-300",
  },
};

export function CostQualityBlock({ summary }: { summary: TaskCostSummary }) {
  const { t } = useTranslation("task");
  const { t: ti } = useTranslation("integrations");
  const badge = qualityBadge[summary.quality_flag];
  const spawns = t("cost.spawns", { count: summary.session_count });

  return (
    <div className="rounded-md border border-border bg-muted/30 px-3 py-2">
      <p className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        {t("cost.title")}
      </p>
      <p className="text-xs text-foreground">
        {formatCost(summary.total_cost)}
        {" · "}
        {formatTokens(summary.tokens_in)} / {formatTokens(summary.tokens_out)} {t("cost.tokens")}
        {" · "}
        {spawns}
      </p>
      {badge && (
        <span
          className={cn(
            "mt-1.5 inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium",
            badge.className,
          )}
        >
          {badge.icon} {ti(`costQuality.quality.${summary.quality_flag}`)}
        </span>
      )}
    </div>
  );
}
