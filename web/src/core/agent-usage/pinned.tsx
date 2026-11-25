// The statistics widget pinned to one project or one agent (U5): the host page owns the filter, so every read
// carries it, «Subscription limits» is not offered (a pool belongs to an account, not to a project or an agent),
// and «Open in statistics» carries the pinned filter into /w/:ws/stats where the rest of the setup lives.
import { useMemo } from "react";
import { useNavigate, useParams } from "react-router";
import { AgentUsageWidget, statsHref } from "@/core/dashboard/widgets/agent-usage/view";
import type { UsageDimension } from "./types";

export interface PinnedAgentUsageProps {
  /** What the host page is about: a project's id or an agent's id (ids, not slugs: the API filters by id). */
  pin: { kind: "project" | "agent"; id: string };
  title?: string;
  /** Called before leaving for /stats: a dialog host closes itself. */
  onLeave?: () => void;
}

/** The breakdown a pinned widget opens with: by the other side of the pin, the question its host page asks. */
export const PINNED_BREAKDOWN: Record<PinnedAgentUsageProps["pin"]["kind"], UsageDimension> = {
  project: "agent",
  agent: "project",
};

export function PinnedAgentUsage({ pin, title, onLeave }: PinnedAgentUsageProps) {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const navigate = useNavigate();
  // Stable identities: both are memo dependencies inside the widget.
  const fixedFilter = useMemo(() => ({ [pin.kind]: [pin.id] }), [pin.kind, pin.id]);
  const settings = useMemo(() => ({ g: PINNED_BREAKDOWN[pin.kind] }), [pin.kind]);
  return (
    <div className="min-w-0" data-testid="pinned-agent-usage" data-pin={`${pin.kind}:${pin.id}`}>
      <AgentUsageWidget
        settings={settings}
        fixedFilter={fixedFilter}
        noQuota
        title={title}
        onOpenStats={
          wsSlug
            ? (s) => {
                onLeave?.();
                void navigate(statsHref(wsSlug, s));
              }
            : undefined
        }
      />
    </div>
  );
}
