import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ListTree, Package } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { usePanelTabs } from "@/components/extensions/slots/slot-host";
import { resolveSlotTab } from "@/lib/extension-slots";
import { cn } from "@/lib/cn";
import type { TaskViewBlockId, TaskViewView } from "@/core/slots/types";

type MobileTabId = "details" | "description" | "comments" | "subtasks" | "dependencies" | "artifacts" | "activity" | SlotTabId;
type RightTabId = "comments" | "subtasks" | "dependencies" | "artifacts" | "activity" | SlotTabId;
/** A `task.panel` tab contributed by an extension: `slot:<extension id>`. */
type SlotTabId = `slot:${string}`;
const slotTabId = (extensionId: string): SlotTabId => `slot:${extensionId}`;

// Labels resolve at render: t(`panel.tabs.${id}`).
const MOBILE_TABS: { id: MobileTabId }[] = [
  { id: "details" },
  { id: "description" },
  { id: "comments" },
  { id: "subtasks" },
  { id: "dependencies" },
  { id: "artifacts" },
  { id: "activity" },
];

const tabClass = (active: boolean, extra = "") =>
  cn(
    "flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2.5 text-xs font-medium transition-colors",
    extra,
    active ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:border-border hover:text-foreground",
  );

/**
 * `task.view` of `mesh-default`: below 1024px a title and a tab bar, from 1024px two columns (title,
 * properties and description on the left; comments, subtasks, dependencies, artifacts, activity as tabs
 * on the right). Every block is the core's; this view only places them.
 */
export const TaskView: TaskViewView = ({ data }) => {
  const { t } = useTranslation("task");
  const { components, requestedBlock } = data;
  const panelTabs = usePanelTabs();
  const [selectedMobileTab, setActiveMobileTab] = useState<MobileTabId>("details");
  const [selectedDesktopTab, setActiveDesktopTab] = useState<RightTabId>("comments");
  // A slot tab that left the registry (extension disabled while open) falls back instead of a blank pane.
  const extensionIds = panelTabs.map((p) => p.extensionId);
  const activeDesktopTab = resolveSlotTab<RightTabId>(selectedDesktopTab, extensionIds, "comments");
  const activeMobileTab = resolveSlotTab<MobileTabId>(selectedMobileTab, extensionIds, "details");
  const openSlotPanel = (extensionId: string) => {
    setActiveDesktopTab(slotTabId(extensionId));
    setActiveMobileTab(slotTabId(extensionId));
  };

  // The core brings a block forward: a mention (`?comment=`) or an artifact link lands on its tab, a
  // subtask that was opened shows the subtask list. Desktop keeps the description beside the tabs, so
  // only the mobile tab changes for it.
  const nonce = requestedBlock?.nonce;
  const block: TaskViewBlockId | undefined = requestedBlock?.block;
  useEffect(() => {
    if (!block) return;
    if (block === "description") setActiveMobileTab("description");
    else if (block === "properties") setActiveMobileTab("details");
    else if (block === "comments" || block === "subtasks" || block === "dependencies" || block === "artifacts" || block === "activity") {
      setActiveMobileTab(block);
      setActiveDesktopTab(block);
    }
  }, [nonce, block]);

  const task = data.task.state === "ready" || data.task.state === "refreshing" ? data.task.value : null;

  const topBar = <components.TopBar openPanel={openSlotPanel} />;

  if (data.task.state === "loading") {
    return (
      <>
        {topBar}
        <div key="skeleton" className="flex-1 overflow-auto p-5">
          <Skeleton className="mb-4 h-8 w-3/4" />
          <div className="grid grid-cols-5 gap-4">
            <div className="col-span-3 space-y-4">
              <Skeleton className="h-4 w-1/3" />
              <Skeleton className="h-24 w-full" />
              <Skeleton className="h-4 w-1/4" />
              <Skeleton className="h-32 w-full" />
            </div>
            <div className="col-span-2 space-y-3">
              <Skeleton className="h-48 w-full" />
            </div>
          </div>
        </div>
      </>
    );
  }

  if (!task) {
    const error = data.task.state === "error" ? data.task.error : null;
    return (
      <>
        {topBar}
        {error?.message ? (
          <div role="alert" className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground">
            <p className="text-sm">{error.message}</p>
            {data.task.state === "error" ? (
              <Button variant="outline" size="sm" onClick={() => data.task.state === "error" && data.task.retry()}>
                {t("actions.retry")}
              </Button>
            ) : null}
          </div>
        ) : null}
      </>
    );
  }

  const subtaskCount = task.counts.subtasks;
  const artifactCount = task.counts.artifacts;

  return (
    <>
      {topBar}
      {/* ============================================================= */}
      {/* MOBILE LAYOUT (<1024px): title + 6-tab bar                     */}
      {/* ============================================================= */}
      <div key="mobile" className="flex min-h-0 flex-1 flex-col [overflow:clip] lg:hidden" data-mesh-layout="mobile">
        {/* Title */}
        <div className="shrink-0 px-5 pt-4 pb-2" data-mesh-block="header">
          <components.header />
        </div>

        {/* Mobile tabs with horizontal scroll */}
        <div className="sticky top-[44px] z-10 shrink-0 bg-background">
          <div className="flex overflow-x-auto border-b border-border [scrollbar-width:none] [scroll-snap-type:x_mandatory] [-webkit-overflow-scrolling:touch]">
            {MOBILE_TABS.map((tab) => (
              <button
                key={tab.id}
                type="button"
                className={tabClass(activeMobileTab === tab.id, "[scroll-snap-align:start]")}
                onClick={(event) => {
                  setActiveMobileTab(tab.id);
                  if (tab.id === "dependencies") {
                    event.currentTarget.scrollIntoView?.({ block: "nearest", inline: "start" });
                  }
                }}
                aria-pressed={activeMobileTab === tab.id}
              >
                {tab.id === "subtasks" && <ListTree className="h-3.5 w-3.5" />}
                {tab.id === "artifacts" && <Package className="h-3.5 w-3.5" />}
                {t(`panel.tabs.${tab.id}`)}
                {tab.id === "dependencies" && <components.DependencyBadge />}
                {tab.id === "subtasks" && subtaskCount > 0 && (
                  <Badge variant="secondary" className="text-[10px]">
                    {subtaskCount}
                  </Badge>
                )}
                {tab.id === "artifacts" && artifactCount > 0 && (
                  <Badge variant="secondary" className="text-[10px]">
                    {artifactCount}
                  </Badge>
                )}
              </button>
            ))}
            {panelTabs.map((tab) => (
              <button
                key={tab.extensionId}
                type="button"
                className={tabClass(activeMobileTab === slotTabId(tab.extensionId), "[scroll-snap-align:start]")}
                onClick={() => setActiveMobileTab(slotTabId(tab.extensionId))}
                aria-pressed={activeMobileTab === slotTabId(tab.extensionId)}
                data-testid={`slot-tab-mobile-${tab.extensionId}`}
              >
                {tab.name}
              </button>
            ))}
          </div>
          {/* Right-edge fade mask */}
          <div className="pointer-events-none absolute inset-y-0 right-0 w-10 bg-gradient-to-l from-background to-transparent" />
        </div>

        {/* Mobile tab content — comments gets its own bounded flex container so
            the reply form stays pinned at the bottom; all other tabs share a
            single scrollable wrapper. */}
        {activeMobileTab === "comments" ? (
          <div className="flex flex-1 min-h-0 flex-col overflow-hidden" data-mesh-block="comments">
            <components.comments />
          </div>
        ) : (
          <div className="flex-1 overflow-y-auto">
            {activeMobileTab === "details" && (
              <div className="space-y-5 px-5 py-4" data-mesh-block="properties">
                <components.properties />
              </div>
            )}
            {activeMobileTab === "description" && (
              <div className="px-5 py-4" data-mesh-block="description">
                <components.description />
              </div>
            )}
            {activeMobileTab === "subtasks" && (
              <div className="p-3" data-mesh-block="subtasks">
                <components.subtasks />
              </div>
            )}
            {activeMobileTab === "dependencies" && (
              <div className="p-3" data-mesh-block="dependencies">
                <components.dependencies />
              </div>
            )}
            {activeMobileTab === "artifacts" && (
              <div className="p-3" data-mesh-block="artifacts">
                <components.artifacts />
              </div>
            )}
            {activeMobileTab === "activity" && (
              <div className="p-4" data-mesh-block="activity">
                <components.activity />
              </div>
            )}
            {panelTabs.map(
              (tab) =>
                activeMobileTab === slotTabId(tab.extensionId) && (
                  <components.ExtensionPanel key={tab.extensionId} extensionId={tab.extensionId} name={tab.name} external={tab.external} />
                ),
            )}
          </div>
        )}
      </div>

      {/* ============================================================= */}
      {/* DESKTOP LAYOUT (>=1024px): two-column                          */}
      {/* ============================================================= */}
      <div key="desktop" className="hidden min-h-0 flex-1 overflow-hidden lg:flex lg:flex-row" data-mesh-layout="desktop">
        {/* LEFT PANEL */}
        <div className="flex min-w-0 flex-1 flex-col overflow-y-auto lg:border-r lg:border-border">
          <div className="flex-1 space-y-5 px-5 py-4">
            <div data-mesh-block="header">
              <components.header />
            </div>
            <div data-mesh-block="properties">
              <components.properties />
            </div>
            <div data-mesh-block="description">
              <components.description />
            </div>
          </div>
        </div>

        {/* RIGHT PANEL — Comments / Subtasks / Dependencies / Artifacts / Activity */}
        <div className="flex w-full shrink-0 flex-col overflow-hidden border-t border-border lg:w-2/5 lg:border-t-0">
          {/* Tab bar */}
          <div className="flex shrink-0 overflow-x-auto border-b border-border">
            <button type="button" className={tabClass(activeDesktopTab === "comments")} onClick={() => setActiveDesktopTab("comments")}>
              {t("panel.tabs.comments")}
            </button>
            <button type="button" className={tabClass(activeDesktopTab === "subtasks")} onClick={() => setActiveDesktopTab("subtasks")}>
              <ListTree className="h-3.5 w-3.5" />
              {t("panel.tabs.subtasks")}
              {subtaskCount > 0 && (
                <Badge variant="secondary" className="text-[10px]">
                  {subtaskCount}
                </Badge>
              )}
            </button>
            <button
              type="button"
              className={tabClass(activeDesktopTab === "dependencies")}
              onClick={() => setActiveDesktopTab("dependencies")}
              aria-pressed={activeDesktopTab === "dependencies"}
            >
              {t("panel.tabs.dependencies")} <components.DependencyBadge />
            </button>
            <button type="button" className={tabClass(activeDesktopTab === "artifacts")} onClick={() => setActiveDesktopTab("artifacts")}>
              <Package className="h-3.5 w-3.5" />
              {t("panel.tabs.artifacts")}
              {artifactCount > 0 && (
                <Badge variant="secondary" className="text-[10px]">
                  {artifactCount}
                </Badge>
              )}
            </button>
            <button type="button" className={tabClass(activeDesktopTab === "activity")} onClick={() => setActiveDesktopTab("activity")}>
              {t("panel.tabs.activity")}
            </button>
            {panelTabs.map((tab) => (
              <button
                key={tab.extensionId}
                type="button"
                className={tabClass(activeDesktopTab === slotTabId(tab.extensionId))}
                onClick={() => setActiveDesktopTab(slotTabId(tab.extensionId))}
                data-testid={`slot-tab-${tab.extensionId}`}
              >
                {tab.name}
              </button>
            ))}
          </div>

          {/* Tab content */}
          {activeDesktopTab === "comments" && (
            <div data-mesh-block="comments" className="contents">
              <components.comments />
            </div>
          )}
          {activeDesktopTab === "subtasks" && (
            <div className="flex-1 overflow-y-auto p-3" data-mesh-block="subtasks">
              <components.subtasks />
            </div>
          )}
          {activeDesktopTab === "dependencies" && (
            <div className="flex-1 overflow-y-auto p-3" data-mesh-block="dependencies">
              <components.dependencies />
            </div>
          )}
          {activeDesktopTab === "artifacts" && (
            <div className="flex-1 overflow-y-auto p-3" data-mesh-block="artifacts">
              <components.artifacts />
            </div>
          )}
          {activeDesktopTab === "activity" && (
            <div className="flex-1 overflow-y-auto p-4" data-mesh-block="activity">
              <components.activity />
            </div>
          )}
          {panelTabs.map(
            (tab) =>
              activeDesktopTab === slotTabId(tab.extensionId) && (
                <components.ExtensionPanel key={tab.extensionId} extensionId={tab.extensionId} name={tab.name} external={tab.external} />
              ),
          )}
        </div>
      </div>
    </>
  );
};
