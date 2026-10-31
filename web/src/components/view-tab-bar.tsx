import { useTranslation } from "react-i18next";
import { Suspense, lazy, memo, useEffect, useLayoutEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { useNavigate } from "react-router";
import {
  Bookmark,
  BookmarkPlus,
  Check,
  FilterX,
  MoreVertical,
  Share2,
  Trash2,
} from "lucide-react";
import { cn } from "@/lib/cn";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useSavedViewStore } from "@/stores/saved-view-store";
import { isBoardFiltersAtDefault } from "@/lib/board-saved-view";
import { toast } from "@/components/ui/toast";
import {
  BUILTIN_PROJECT_TABS,
  splitTabsByWidth,
  type ProjectViewTabDescriptor,
} from "@/lib/project-view-tabs";
import type { ProjectViewTab, ViewType } from "@/types";

const SaveViewDialog = lazy(() => import("@/components/view-tab-save-dialog"));

interface ViewTabBarProps {
  // null on an open task: the strip is navigation only, no tab is active.
  currentView: ProjectViewTab | null;
  wsSlug: string;
  projectSlug: string;
  projectId?: string;
  className?: string;
  // Tab descriptions, in strip order. Defaults to the built-in project tabs.
  tabs?: readonly ProjectViewTabDescriptor[];
}

// Room kept for the "…" trigger and its divider until the real one is measured.
// On a phone the trigger grows to the 44px touch target (index.css), so the
// measured width, not this, decides what fits.
const KEBAB_RESERVE_PX = 44;

// The "…" trigger's left margin (`ml-1`), a constant of the class below, so the
// measure does not need getComputedStyle (a style recalculation per resize).
const KEBAB_MARGIN_PX = 4;

// The last split, kept per viewport width so the next mount renders the right
// strip at once. Measuring after mount costs one extra commit and layout
// whenever the strip overflows; a repeat visit at the same width does not pay.
const SPLIT_KEY = "mesh.projectTabs.split";

const tabLabel = (tab: ProjectViewTabDescriptor, t: (key: string) => string) =>
  tab.label ?? t(tab.labelKey);

// Tab ids and viewport width: a stored split is only good for the same strip.
const splitStamp = (tabs: readonly ProjectViewTabDescriptor[]) =>
  `${tabs.map((t) => t.id)}|${window.innerWidth}`;

function storeOverflow(tabs: readonly ProjectViewTabDescriptor[], ids: string[]) {
  try {
    localStorage.setItem(SPLIT_KEY, JSON.stringify([splitStamp(tabs), ids]));
  } catch {
    // Storage blocked: the next mount measures again.
  }
}

function readStoredOverflow(tabs: readonly ProjectViewTabDescriptor[]): string[] {
  try {
    const [stamp, ids] = JSON.parse(localStorage.getItem(SPLIT_KEY) ?? "[]");
    if (stamp === splitStamp(tabs) && Array.isArray(ids)) {
      return ids;
    }
  } catch {
    // Storage blocked or a malformed value: measure as usual.
  }
  return [];
}

const VIEW_TYPE_PATH: Record<ViewType, (ws: string, proj: string) => string> = {
  board: (ws, proj) => `/w/${ws}/p/${proj}`,
  list: (ws, proj) => `/w/${ws}/p/${proj}/list`,
  timeline: (ws, proj) => `/w/${ws}/p/${proj}/timeline`,
  calendar: (ws, proj) => `/w/${ws}/p/${proj}/calendar`,
};

// Only the four ViewType values are saved-view types; Docs, Chat, Updates and extension tabs must not reach createView().
function isSavedViewType(view: ProjectViewTab | null): view is ViewType {
  return view !== null && view in VIEW_TYPE_PATH;
}

// Hidden full-size copy of every tab. Memoized: the strip re-renders on every
// saved-view store change and on every route change, and this copy only
// depends on the tab list and the language.
const MeasureStrip = memo(function MeasureStrip({
  measureRef,
  tabs,
  t,
}: {
  measureRef: React.RefObject<HTMLDivElement | null>;
  tabs: readonly ProjectViewTabDescriptor[];
  t: (key: string) => string;
}) {
  return (
    <div
      ref={measureRef}
      aria-hidden
      data-testid="project-tabs-order"
      className="pointer-events-none invisible col-start-1 row-start-1 flex h-0 w-max overflow-hidden"
    >
      {tabs.map((tab) => (
        <span
          key={tab.id}
          className="flex h-9 shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 text-sm font-medium"
          tabIndex={-1}
          data-tab-id={tab.id}
        >
          <tab.Icon className="h-3.5 w-3.5" />
          <span>{tabLabel(tab, t)}</span>
        </span>
      ))}
      {/* Stands in for the trigger so the basis includes it. */}
      <span className="ml-1 flex items-center border-l border-border pl-1">
        <span className="h-7 w-7" />
      </span>
    </div>
  );
});

export function ViewTabBar({
  currentView,
  wsSlug,
  projectSlug,
  projectId,
  className,
  tabs = BUILTIN_PROJECT_TABS,
}: ViewTabBarProps) {
  const { t } = useTranslation("settings");
  const navigate = useNavigate();
  const {
    views,
    fetchViews,
    createView,
    applyView,
    updateView,
    deleteView,
    currentViewState,
    requestResetFilters,
  } = useSavedViewStore();

  const [saveDialogOpen, setSaveDialogOpen] = useState(false);
  const [saveName, setSaveName] = useState("");
  const [saveIsShared, setSaveIsShared] = useState(false);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    if (projectId) {
      fetchViews(projectId);
    }
  }, [projectId, fetchViews]);

  // The strip shows as many tabs as fit and moves the rest into the "…" menu.
  // Natural widths come from a hidden copy of every tab, so the decision does
  // not depend on which tabs are currently shown.
  const wrapRef = useRef<HTMLDivElement>(null);
  const measureRef = useRef<HTMLDivElement>(null);
  const kebabRef = useRef<HTMLDivElement>(null);
  // Only the ids that overflow live in state: a strip that fits (the common
  // case) never re-renders after mount.
  const [overflowIds, setOverflowIds] = useState<string[]>(() => readStoredOverflow(tabs));
  const shownRef = useRef<{ ids: string[] }>({ ids: overflowIds });

  useLayoutEffect(() => {
    const wrap = wrapRef.current;
    const measure = measureRef.current;
    const kebab = kebabRef.current;
    if (!wrap || !measure || !kebab) return;
    const update = (inObserver: boolean) => {
      // The trigger's whole box: its width plus the margin that separates it.
      const reserve =
        kebab.offsetWidth + KEBAB_MARGIN_PX || KEBAB_RESERVE_PX;
      const widths = Array.from(measure.children)
        .slice(0, tabs.length)
        .map((el) => (el as HTMLElement).offsetWidth);
      // 0 means no layout yet (hidden or not measured): show everything and
      // let the next resize decide.
      const available = wrap.clientWidth
        ? wrap.clientWidth - reserve
        : Infinity;
      const next = splitTabsByWidth(
        tabs,
        widths.length === tabs.length ? widths : tabs.map(() => 0),
        available,
      ).overflow.map((t) => t.id);
      const cur = shownRef.current.ids;
      // Only a real measurement is worth remembering (0 widths mean no layout).
      if (wrap.clientWidth) storeOverflow(tabs, next);
      // Same split as already rendered: no state update, so no extra commit.
      if (cur.length === next.length && cur.every((id, i) => id === next[i])) return;
      shownRef.current.ids = next;
      // From the observer, apply before paint so the strip never shows a frame
      // with tabs that do not fit.
      if (inObserver) flushSync(() => setOverflowIds(next));
      else setOverflowIds(next);
    };
    // The observer's first callback measures after layout (no forced reflow
    // here); without one (tests) measure once now.
    if (typeof ResizeObserver === "undefined") {
      update(false);
      return;
    }
    const ro = new ResizeObserver(() => update(true));
    ro.observe(wrap);
    ro.observe(measure);
    ro.observe(kebab);
    return () => ro.disconnect();
  }, [tabs]);

  const visibleTabs = tabs.filter((t) => !overflowIds.includes(t.id));
  const overflowTabs = tabs.filter((t) => overflowIds.includes(t.id));

  // A tab that moved into the menu cannot show its active state in the strip,
  // so the trigger carries it.
  const activeOverflowTab = overflowTabs.find((t) => t.id === currentView);
  const overflowTabActive = activeOverflowTab !== undefined;

  // Filter views for current view type
  const relevantViews = views.filter((v) => v.view_type === currentView);
  const personalViews = relevantViews.filter((v) => !v.is_shared);
  const sharedViews = relevantViews.filter((v) => v.is_shared);
  const hasViews = relevantViews.length > 0;

  const go = (tab: ProjectViewTabDescriptor) => {
    if (tab.id !== currentView) navigate(tab.path(wsSlug, projectSlug));
  };

  const handleApply = (view: import("@/types").SavedView) => {
    const targetPath = VIEW_TYPE_PATH[view.view_type];
    if (targetPath && view.view_type !== currentView) {
      navigate(targetPath(wsSlug, projectSlug));
    }
    applyView(view);
    toast(t("viewTabs.toast.applied", { name: view.name }));
  };

  const handleResetFilters = () => {
    requestResetFilters();
    toast(t("viewTabs.toast.filtersReset"));
  };

  // Only meaningful on the board tab — list/timeline/calendar don't feed
  // their filter state into currentViewState the same way, and the task's
  // filters (search/priority/assignee/tags/custom fields/show-closed/
  // show-subtasks) are board-specific.
  const filtersAtDefault =
    currentView === "board" && isBoardFiltersAtDefault(currentViewState.filters ?? {});

  const handleToggleShare = async (view: import("@/types").SavedView) => {
    try {
      await updateView(view.id, { is_shared: !view.is_shared });
      toast(view.is_shared ? t("viewTabs.toast.madePrivate") : t("viewTabs.toast.sharedWithTeam"));
    } catch {
      toast(t("viewTabs.toast.updateFailed"));
    }
  };

  const handleDelete = async (view: import("@/types").SavedView) => {
    try {
      await deleteView(view.id);
      toast(t("viewTabs.toast.deleted"));
    } catch {
      toast(t("viewTabs.toast.deleteFailed"));
    }
  };

  const handleSave = async () => {
    if (!saveName.trim() || !projectId || !isSavedViewType(currentView)) return;
    setIsSaving(true);
    try {
      await createView(projectId, {
        name: saveName.trim(),
        view_type: currentView,
        filters: currentViewState.filters,
        sort_by: currentViewState.sortBy,
        sort_order: currentViewState.sortOrder,
        columns: currentViewState.columns,
        is_shared: saveIsShared,
      });
      toast(t("viewTabs.toast.saved"));
      setSaveDialogOpen(false);
      setSaveName("");
      setSaveIsShared(false);
    } catch {
      toast(t("viewTabs.toast.saveFailed"));
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <>
      <div
        ref={wrapRef}
        className={cn("relative grid min-w-0", className)}
        // One grid cell holds the hidden full-size copy and the live strip, so
        // the strip's basis is the width of every tab (it claims that when
        // there is room) and it shrinks, rather than wraps, when there is not.
        // `shrink-0` from a caller's className must not win here.
        style={{ gridTemplateColumns: "minmax(0, 1fr)", flexShrink: 1 }}
      >
        <MeasureStrip measureRef={measureRef} tabs={tabs} t={t} />
        <div className="col-start-1 row-start-1 flex min-w-0 items-center">
          {visibleTabs.map((tab) => {
            const { id, Icon } = tab;
            const isActive = currentView === id;
            return (
              <button
                key={id}
                data-tab-id={id}
                onClick={() => go(tab)}
                className={cn(
                  "flex h-9 shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 text-sm transition-colors",
                  isActive
                    ? "border-primary font-medium text-foreground"
                    : "border-transparent font-normal text-muted-foreground hover:text-foreground",
                )}
                aria-current={isActive ? "page" : undefined}
              >
                <Icon className="h-3.5 w-3.5" />
                <span>{tabLabel(tab, t)}</span>
              </button>
            );
          })}

          {/* Formats + saved views */}
          <div
            ref={kebabRef}
            className="ml-1 flex items-center border-l border-border pl-1"
          >
            <DropdownMenu>
              <DropdownMenuTrigger
                aria-haspopup="menu"
                className={cn(
                  "flex h-7 w-7 items-center justify-center rounded hover:bg-muted hover:text-foreground",
                  overflowTabActive ? "bg-muted text-foreground" : "text-muted-foreground",
                )}
                title={
                  activeOverflowTab
                    ? t("viewTabs.moreViewsActive", { name: tabLabel(activeOverflowTab, t) })
                    : t("viewTabs.moreViews")
                }
              >
                <MoreVertical className="h-3.5 w-3.5" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-56">
                {overflowTabs.map((tab) => {
                  const { id, Icon } = tab;
                  const isActive = currentView === id;
                  return (
                    <DropdownMenuItem
                      key={id}
                      data-tab-id={id}
                      onClick={() => go(tab)}
                      className="gap-2"
                      aria-current={isActive ? "page" : undefined}
                    >
                      <Icon className="h-3.5 w-3.5" />
                      <span className="flex-1">{tabLabel(tab, t)}</span>
                      {isActive && <Check className="h-3.5 w-3.5" />}
                    </DropdownMenuItem>
                  );
                })}

                                {/* Saved views need a project to scope them to. */}
                {projectId && (
                  <>
                    <DropdownMenuSeparator />

                    {/* Docs has no server-side saved-view type, so it can't be
                        saved as one. */}
                    {isSavedViewType(currentView) && (
                      <DropdownMenuItem
                        onClick={() => setSaveDialogOpen(true)}
                        className="gap-2"
                      >
                        <BookmarkPlus className="h-3.5 w-3.5" />
                        {t("viewTabs.saveCurrent")}
                      </DropdownMenuItem>
                    )}

                    {/* Reset filters — board only; list/timeline/calendar don't
                        report their filter state into currentViewState. */}
                    {currentView === "board" && (
                      <DropdownMenuItem
                        onClick={filtersAtDefault ? undefined : handleResetFilters}
                        aria-disabled={filtersAtDefault}
                        className={cn(
                          "gap-2",
                          filtersAtDefault && "pointer-events-none opacity-50",
                        )}
                      >
                        <FilterX className="h-3.5 w-3.5" />
                        {t("viewTabs.resetFilters")}
                      </DropdownMenuItem>
                    )}

                    {/* Personal views */}
                    {personalViews.length > 0 && (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuLabel className="text-[10px] uppercase tracking-wide text-muted-foreground">
                          {t("viewTabs.myViews")}
                        </DropdownMenuLabel>
                        {personalViews.map((view) => (
                          <SavedViewRow
                            key={view.id}
                            view={view}
                            onApply={() => handleApply(view)}
                            onToggleShare={() => void handleToggleShare(view)}
                            onDelete={() => void handleDelete(view)}
                          />
                        ))}
                      </>
                    )}

                    {/* Shared views */}
                    {sharedViews.length > 0 && (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuLabel className="text-[10px] uppercase tracking-wide text-muted-foreground">
                          {t("viewTabs.sharedViews")}
                        </DropdownMenuLabel>
                        {sharedViews.map((view) => (
                          <SavedViewRow
                            key={view.id}
                            view={view}
                            onApply={() => handleApply(view)}
                            onToggleShare={() => void handleToggleShare(view)}
                            onDelete={() => void handleDelete(view)}
                          />
                        ))}
                      </>
                    )}

                    {!hasViews && (
                      <div className="px-2 py-2 text-center text-xs text-muted-foreground">
                        {t("viewTabs.noSavedViews")}
                      </div>
                    )}
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </div>

      {saveDialogOpen && (
        <Suspense fallback={null}>
          <SaveViewDialog
            t={t}
            onOpenChange={setSaveDialogOpen}
            name={saveName}
            onNameChange={setSaveName}
            isShared={saveIsShared}
            onSharedChange={setSaveIsShared}
            isSaving={isSaving}
            onSave={() => void handleSave()}
          />
        </Suspense>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Single saved view row
// ---------------------------------------------------------------------------

function SavedViewRow({
  view,
  onApply,
  onToggleShare,
  onDelete,
}: {
  view: import("@/types").SavedView;
  onApply: () => void;
  onToggleShare: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation("settings");
  return (
    <div className="group flex items-center justify-between rounded px-2 py-1.5 hover:bg-accent">
      <button
        className="flex flex-1 items-center gap-2 truncate text-left text-xs"
        onClick={onApply}
        title={view.name}
      >
        <Bookmark className="h-3 w-3 shrink-0 text-muted-foreground" />
        <span className="truncate">{view.name}</span>
      </button>
      <div className="hidden shrink-0 items-center gap-0.5 group-hover:flex">
        <button
          className="rounded p-0.5 hover:bg-muted"
          title={view.is_shared ? t("viewTabs.row.makePrivate") : t("viewTabs.row.shareWithTeam")}
          onClick={(e) => {
            e.stopPropagation();
            onToggleShare();
          }}
        >
          <Share2
            className={cn(
              "h-3 w-3",
              view.is_shared ? "text-primary" : "text-muted-foreground",
            )}
          />
        </button>
        <button
          className="rounded p-0.5 hover:bg-muted"
          title={t("viewTabs.row.deleteView")}
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="h-3 w-3 text-muted-foreground hover:text-destructive" />
        </button>
      </div>
    </div>
  );
}
