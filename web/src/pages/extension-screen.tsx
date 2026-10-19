import { useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ExternalFrame } from "@/components/extensions/external-frame";
import { useSidebarItems } from "@/lib/extension-slots";

/**
 * Full-area iframe screen of an external extension (`sidebar.item`, W3.3),
 * at /w/:wsSlug/x/:extId. An extension that is off, or declares no such screen,
 * gets the same "unavailable" answer as one whose service is down.
 */
export function ExtensionScreenPage() {
  const { extId = "" } = useParams<{ extId: string }>();
  const { t } = useTranslation("task");
  const item = useSidebarItems().find((i) => i.extensionId === extId);
  if (!item) {
    return (
      <div className="p-4 text-sm text-muted-foreground" role="status" data-testid="extension-screen-unavailable">
        {t("slots.unavailable", { name: extId })}
      </div>
    );
  }
  return (
    <div className="flex h-full min-h-[60vh] flex-col" data-testid="extension-screen">
      <h1 className="sr-only">{item.title}</h1>
      <ExternalFrame extensionId={item.extensionId} name={item.title} slot="sidebar.item" layout="screen" />
    </div>
  );
}
