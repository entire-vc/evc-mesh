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
  { label: string; icon: string; className: string }
> = {
  golden: {
    label: "golden",
    icon: "🏆",
    className: "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300",
  },
  rework: {
    label: "rework",
    icon: "🔁",
    className: "bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300",
  },
  "multi-turn": {
    label: "multi-turn",
    icon: "⚡",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-300",
  },
};

export function CostQualityBlock({ summary }: { summary: TaskCostSummary }) {
  const badge = qualityBadge[summary.quality_flag];
  const spawns = summary.session_count === 1 ? "1 spawn" : `${summary.session_count} spawns`;

  return (
    <div className="rounded-md border border-border bg-muted/30 px-3 py-2">
      <p className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
        Cost &amp; Quality
      </p>
      <p className="text-xs text-foreground">
        {formatCost(summary.total_cost)}
        {" · "}
        {formatTokens(summary.tokens_in)} / {formatTokens(summary.tokens_out)} tok
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
          {badge.icon} {badge.label}
        </span>
      )}
    </div>
  );
}
