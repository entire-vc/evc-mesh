import { useTranslation } from "react-i18next";
import { Activity } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import type { WidgetViewProps } from "../../types";
import { EventFeedWidget } from "./event-feed-widget";
import type { EventFeedSettings } from "./settings";
import { widgetTitle, type WidgetConfig } from "../../config";

/** Height of one grid row of the instance: four rows (the default) match the legacy cards beside the feed. */
export const ROW_REM = 8;

/**
 * The dashboard view: a card of the instance's height with a title, like the legacy widgets beside it, so 50 rows
 * scroll inside it instead of stretching the dashboard row. Instance settings set the default filters; «Развернуть»
 * is the dashboard cell's (the Events page is a dashboard template now, §5.9).
 */
export function EventFeedWidgetView({ settings, size }: WidgetViewProps<EventFeedSettings>) {
  const { t } = useTranslation("integrations");
  const title = widgetTitle((settings ?? {}) as WidgetConfig, t("eventFeed.title"));
  return (
    <Card className="flex flex-col overflow-hidden" style={{ height: `${size.h * ROW_REM}rem` }} data-testid="event-feed-card">
      <CardContent className="flex min-h-0 flex-1 flex-col gap-3 pt-4">
        <h3 className="flex items-center gap-1.5 text-sm font-semibold">
          <Activity className="h-4 w-4" />
          {title}
        </h3>
        <div className="flex min-h-0 flex-1 flex-col">
          <EventFeedWidget settings={settings} collapsibleFilters />
        </div>
      </CardContent>
    </Card>
  );
}
