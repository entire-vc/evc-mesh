import type { TaskDependenciesSource } from "@/hooks/use-task-dependencies";
import { Badge } from "@/components/ui/badge";

export function DependencyBadge({ source }: { source: TaskDependenciesSource }) {
  let label: string;
  if (source.phase === "loading") {
    label = "Loading";
  } else if (source.phase === "error") {
    label = "Unavailable";
  } else {
    let blockers = `${source.openBlockers} open`;
    if (source.checking) {
      blockers = "checking";
    } else if (source.unknownBlockers) {
      const known = source.openBlockers ? `${source.openBlockers}+ open · ` : "";
      blockers = `${known}${source.unknownBlockers} unknown`;
    }
    label = `${source.rows.length} · ${blockers}`;
  }
  return <Badge variant="secondary" className="shrink-0 text-[10px]" aria-live="polite">{label}</Badge>;
}
