import type { TaskDependenciesSource } from "@/hooks/use-task-dependencies";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";

export function DependencyBadge({ source }: { source: TaskDependenciesSource }) {
  const { t } = useTranslation("task");
  let label: string;
  if (source.phase === "loading") {
    label = t("deps.badge.loading");
  } else if (source.phase === "error") {
    label = t("deps.badge.unavailable");
  } else {
    let blockers = t("deps.badge.open", { n: source.openBlockers });
    if (source.checking) {
      blockers = t("deps.badge.checking");
    } else if (source.unknownBlockers) {
      const known = source.openBlockers ? t("deps.badge.openKnown", { n: source.openBlockers }) : "";
      blockers = `${known}${t("deps.badge.unknown", { n: source.unknownBlockers })}`;
    }
    label = `${source.rows.length} · ${blockers}`;
  }
  return <Badge variant="secondary" className="shrink-0 text-[10px]" aria-live="polite">{label}</Badge>;
}
