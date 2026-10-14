import { ArrowRight } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatActivityField, formatActivityValue, type ActivityDetail } from "@/lib/activity";

export function ActivityDetails({ details, names }: { details: ActivityDetail[]; names?: Map<string, string> }) {
  const { t } = useTranslation("activity");
  if (details.length === 0) return null;
  const valueClass = "min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded bg-muted px-1 py-0.5";
  return (
    <div className="mt-1.5 space-y-1" aria-label={t("details.label")}>
      {details.map((detail) => (
        <div key={detail.field} className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <span className="font-medium">{formatActivityField(detail.field)}:</span>
          {detail.kind === "metadata" ? (
            <code className={valueClass}>{formatActivityValue(detail.value, names)}</code>
          ) : detail.unchanged ? (
            <><code className={valueClass}>{formatActivityValue(detail.new, names)}</code><span>{t("details.unchanged")}</span></>
          ) : (
            <>
              <code className={valueClass}>{formatActivityValue(detail.old, names)}</code>
              <span aria-label={t("details.changedTo")}><ArrowRight className="h-3 w-3" /></span>
              <code className={valueClass}>{formatActivityValue(detail.new, names)}</code>
            </>
          )}
        </div>
      ))}
    </div>
  );
}
