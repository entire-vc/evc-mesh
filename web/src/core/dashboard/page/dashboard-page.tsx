// Routes: w/:ws and w/:ws/dashboard → first dashboard; w/:ws/d/:docId[?w=:instanceId].
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, Navigate, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Check, Loader2, MoreHorizontal, Settings2, Users } from "lucide-react";
import { navIcon } from "@/core/icons/nav-icons";
import { useWorkspaceStore } from "@/stores/workspace";
import { Button } from "@/components/ui/button";
import { EntityIcon } from "@/components/ui/entity-icon";
import { Skeleton } from "@/components/ui/skeleton";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { DashboardContainer, DashboardLayoutHost } from "..";
import { DashboardEditor, EditGear, layoutFromDoc, workspaceApi, type DashboardLayout, type LayoutApi } from "../edit";
import { useLayoutMajor } from "../theme-widgets";
import { duplicateDashboard, pickHome, saveWidgets, useDashboard, useDashboardList } from "./data";
import { ViewGrid, WidgetCell, useCols } from "./view-grid";
import type { DashboardLayoutView, WidgetDescriptor, WidgetInstance, WidgetSize } from "../types";
import { sortWidgets, toSdkInstance, type DashboardDoc } from "./model";

// The nav's icon map is already shared with lazy chunks; importing the icon from it
// instead of lucide keeps one more export out of the entry chunk (login budget).
const LayoutDashboard = navIcon("LayoutDashboard");

function PageSkeleton() {
  return (
    <div data-testid="dashboard-loading" className="space-y-4 p-4" aria-busy="true">
      <Skeleton className="h-8 w-56" />
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <Skeleton className="h-40" />
        <Skeleton className="h-40" />
      </div>
    </div>
  );
}

function PageError({ error, retry }: { error: Error; retry: () => void }) {
  const { t } = useTranslation("notifications");
  return (
    <div role="alert" className="flex flex-col items-center gap-3 p-10 text-center text-sm text-muted-foreground">
      <p>{t("dashboard.page.loadFailed")}</p>
      <p className="text-xs">{error.message}</p>
      <Button type="button" variant="outline" size="sm" onClick={retry}>
        {t("dashboard.page.retry")}
      </Button>
    </div>
  );
}

/** `w/:ws` and `w/:ws/dashboard`: first `top` dashboard, else first `bottom`. */
export function DashboardEntry() {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const location = useLocation();
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const list = useDashboardList(wsId);
  if (list.state === "loading") return <PageSkeleton />;
  // The dashboards API is not on this server yet: the page that always worked stays.
  if (list.state === "missing") return <DashboardContainer />;
  if (list.state === "error") return <PageError error={list.error} retry={list.retry} />;
  const home = pickHome(list.value);
  // The query goes along: `?new-dashboard=<template>` of an old page's address opens the create dialog there.
  return <Navigate to={{ pathname: `/w/${wsSlug}/d/${home?.id ?? "default"}`, search: location.search }} replace />;
}

function Header({ doc, editing, leaving, canLayout, onToggleEdit, onCopy }: { doc: DashboardDoc; editing: boolean; leaving: boolean; canLayout: boolean; onToggleEdit: () => void; onCopy: () => void }) {
  const { t } = useTranslation("notifications");
  return (
    <header className="mb-4 flex items-center gap-2">
      <h1 className="flex min-w-0 items-center gap-2 text-lg font-semibold">
        {/* The one icon renderer: emoji as text, `icon:<name>` as the theme/core icon, empty → default. */}
        <span aria-hidden className="flex shrink-0 items-center">
          <EntityIcon value={doc.icon} fallback={LayoutDashboard} className="h-5 w-5" />
        </span>
        <span className="truncate">{doc.name}</span>
        {doc.shared && (
          <span title={t("dashboard.page.shared")} aria-label={t("dashboard.page.shared")} role="img">
            <Users className="h-4 w-4 text-muted-foreground" aria-hidden />
          </span>
        )}
      </h1>
      <div className="ml-auto flex items-center gap-1 print:hidden">
        {/* A theme layout on contract v1 cannot be edited (§5.10): the gear explains why instead of opening the mode. */}
        {doc.can_edit && !canLayout && <EditGear canEdit themeLayout="v1" editing={false} onToggle={() => {}} />}
        {doc.can_edit && canLayout && (
          <Button
            type="button"
            variant={editing ? "default" : "ghost"}
            size={editing ? "sm" : "icon"}
            aria-pressed={editing}
            // Done pressed while a save is in flight: the mode closes once the write has an answer.
            aria-busy={leaving || undefined}
            disabled={leaving}
            aria-label={leaving ? t("dashboard.page.leaving") : editing ? t("dashboard.page.done") : t("dashboard.page.edit")}
            title={leaving ? t("dashboard.page.leaving") : editing ? t("dashboard.page.done") : t("dashboard.page.edit")}
            onClick={onToggleEdit}
          >
            {editing ? (
              <>
                {leaving ? <Loader2 className="mr-1 h-4 w-4 animate-spin" aria-hidden /> : <Check className="mr-1 h-4 w-4" aria-hidden />}
                {leaving ? t("dashboard.page.leaving") : t("dashboard.page.done")}
              </>
            ) : (
              <Settings2 className="h-4 w-4" aria-hidden />
            )}
          </Button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="ghost" size="icon" aria-label={t("dashboard.page.menu")} title={t("dashboard.page.menu")}>
              <MoreHorizontal className="h-4 w-4" aria-hidden />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={() => window.print()}>{t("dashboard.page.print")}</DropdownMenuItem>
            <DropdownMenuItem disabled={doc.virtual} onClick={onCopy}>
              {t("dashboard.page.copyToMe")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
}

function Expanded({ doc, instanceId, backTo }: { doc: DashboardDoc; instanceId: string; backTo: string }) {
  const { t } = useTranslation("notifications");
  const instance = doc.widgets.find((w) => w.id === instanceId);
  return (
    <div data-testid="dashboard-expanded" className="p-4">
      <Link to={backTo} className="mb-3 -ml-2 inline-flex h-8 items-center gap-1 rounded-md px-2 text-sm hover:bg-accent">
        <ArrowLeft className="h-4 w-4" aria-hidden />
        {t("dashboard.page.back")}
      </Link>
      {instance ? (
        <WidgetCell instance={instance} dashboardId={doc.id} />
      ) : (
        <p role="status" className="py-10 text-center text-sm text-muted-foreground">
          {t("dashboard.page.widgetGone")}
        </p>
      )}
    </div>
  );
}

type GridProps = { doc: DashboardDoc; expandHref: (instanceId: string) => string; onRemove?: (instanceId: string) => void };

/** Descriptor for a type nobody registers (unknown or a switched-off extension): it stays on the grid as a stub. */
function stubDescriptor(instance: WidgetInstance): WidgetDescriptor {
  const size = { w: instance.w, h: instance.h } as WidgetSize;
  return {
    id: instance.type,
    title: instance.type,
    description: "",
    category: "unknown",
    origin: "external",
    scope: "user",
    actions: ["remove"],
    size: { default: size, min: size, max: size },
  };
}

/** `components.Widget` of the page: the same cell as the grid (stub + «Убрать виджет» for an unknown type, expand link). */
function PageCell({ instance: { id }, props: { doc, expandHref, onRemove } }: { instance: WidgetInstance; props: GridProps }) {
  const w = doc.widgets.find((x) => x.id === id);
  return w ? <WidgetCell instance={w} dashboardId={doc.id} expandTo={expandHref(id)} onRemove={doc.can_edit && onRemove ? () => onRemove(id) : undefined} /> : null;
}

/**
 * The page's dashboard.layout slot host. The coordinate grid is the mesh-default layout; an active
 * theme's layout (or a registered override) replaces it and receives the doc's widgets as SDK
 * instances with their coordinates (dashboard.layout 2; a `^1` theme gets them through the v1 adapter).
 */
function GridHost(props: GridProps) {
  const latest = useRef(props);
  latest.current = props;
  // One component identity for the page's life: a new one per render would remount every widget.
  const Default = useRef<DashboardLayoutView>(() => <ViewGrid {...latest.current} />).current;
  const Cell = useRef(({ instance }: { instance: WidgetInstance }) => <PageCell instance={instance} props={latest.current} />).current;
  // Every doc instance reaches the layout: a type without a descriptor keeps its stub (PRD §7.2).
  const place = useCallback(
    (registry: readonly WidgetDescriptor[]) =>
      sortWidgets(props.doc.widgets).map((w) => {
        const instance = toSdkInstance(w);
        return { descriptor: registry.find((d) => d.id === instance.type) ?? stubDescriptor(instance), instance };
      }),
    [props.doc.widgets],
  );
  return <DashboardLayoutHost Default={Default} place={place} Widget={Cell} />;
}

export function DashboardPage() {
  // The param is :docId (a dashboard is a DashboardDoc): the shorter name keeps the entry chunk within the login budget.
  const { wsSlug, docId: dashboardId = "default" } = useParams<{ wsSlug: string; docId: string }>();
  const [search] = useSearchParams();
  const navigate = useNavigate();
  const { t } = useTranslation("notifications");
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const load = useDashboard(wsId, dashboardId);
  // The layout the edit session starts from: taken once on entering the mode, so a re-read during the session
  // (our own saves announce dashboard.updated) does not reset the editor or its "Cancel changes" point.
  // The session belongs to one workspace and one dashboard route: another route ends it, and its transport keeps
  // the workspace it started in, so nothing is saved into a dashboard that is no longer on screen.
  const sessionKey = `${wsId ?? ""}:${dashboardId}`;
  const [session, setSession] = useState<{ key: string; from: DashboardLayout; api: LayoutApi } | null>(null);
  useEffect(() => setSession((s) => (s && s.key !== sessionKey ? null : s)), [sessionKey]);
  // Done asked to leave the session; the editor answers once its saves are through.
  const [leaving, setLeaving] = useState(false);
  // The re-read the last confirmed save started: the view is shown only after it succeeds, so it never shows the old
  // layout. It clears itself on success; a failed one stays here so the next Done reads again.
  const reread = useRef<Promise<boolean> | null>(null);
  useEffect(() => setLeaving(false), [session]);
  const canLayout = useLayoutMajor() >= 2;
  const [actionError, setActionError] = useState<string | null>(null);
  useCols(); // keeps the page on the same breakpoints as the grid it hosts

  if (load.state === "loading") return <PageSkeleton />;
  if (load.state === "missing") {
    return (
      <div role="status" className="p-10 text-center text-sm text-muted-foreground">
        <p>{t("dashboard.page.notFound")}</p>
        <Link to={`/w/${wsSlug}/dashboard`} className="mt-2 inline-block text-primary underline">
          {t("dashboard.page.toHome")}
        </Link>
      </div>
    );
  }
  if (load.state === "error") return <PageError error={load.error} retry={load.retry} />;

  const doc = load.value;
  const editSession = session?.key === sessionKey && doc.can_edit && canLayout && wsId ? session : null;
  const editing = editSession !== null;
  const base = `/w/${wsSlug}/d/${doc.id}`;
  const widgetParam = search.get("w");
  if (widgetParam) return <Expanded doc={doc} instanceId={widgetParam} backTo={base} />;

  const rereadAfterSave = () => {
    const mine = load.reload().then((ok) => {
      if (ok && reread.current === mine) reread.current = null;
      return ok;
    });
    reread.current = mine;
    return mine;
  };
  const removeWidget = (instanceId: string) => {
    setActionError(null);
    saveWidgets(wsId!, doc, doc.widgets.filter((w) => w.id !== instanceId))
      .then(() => load.reload())
      .catch((e: Error) => {
        setActionError(e.message);
        load.reload();
      });
  };
  const copyToMe = () =>
    duplicateDashboard(wsId!, doc.id)
      .then((d) => navigate(`/w/${wsSlug}/d/${d.id}`))
      .catch((e: Error) => setActionError(e.message));

  return (
    <div className="p-4">
      {/* Paper for this page only: the 12-column grid needs a desktop-wide sheet, so print turns it sideways. */}
      <style media="print">{"@page { size: landscape; margin: 10mm; }"}</style>
      <Header
        doc={doc}
        editing={editing}
        leaving={editing && leaving}
        canLayout={canLayout}
        onToggleEdit={() => {
          if (editing) {
            setActionError(null);
            setLeaving(true);
          }
          else if (wsId) setSession({ key: sessionKey, from: layoutFromDoc(doc), api: workspaceApi(wsId) });
        }}
        onCopy={copyToMe}
      />
      {actionError && (
        <p role="alert" className="mb-3 text-xs text-destructive">
          {actionError}
        </p>
      )}
      {editSession ? (
        <DashboardEditor
          key={editSession.key}
          layout={editSession.from}
          api={editSession.api}
          canEdit
          onChange={() => void rereadAfterSave()}
          leaving={leaving}
          onLeave={(outcome) => {
            // A failed or conflicting write keeps the editor: its notice is the result the user must see.
            if (outcome === "failed") return setLeaving(false);
            const ending = editSession;
            const end = () => setSession((s) => (s === ending ? null : s));
            const pending = reread.current;
            if (!pending) return end();
            // The write went through but the view could not be re-read: one more read, and if that fails too the
            // editor (which shows the saved layout) stays with the notice instead of an outdated view.
            void pending
              .then((ok) => ok || rereadAfterSave())
              .then((ok) => {
                if (ok) return end();
                setLeaving(false);
                setActionError(t("dashboard.page.refreshFailed"));
              });
          }}
        />
      ) : doc.widgets.length === 0 ? (
        <div data-testid="dashboard-empty" className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border py-16 text-center">
          <LayoutDashboard className="h-8 w-8 text-muted-foreground opacity-40" aria-hidden />
          <p className="text-sm font-medium">{t("dashboard.page.emptyTitle")}</p>
          <p className="text-xs text-muted-foreground">{doc.can_edit ? t("dashboard.page.emptyHintEdit") : t("dashboard.page.emptyHint")}</p>
        </div>
      ) : (
        <GridHost doc={doc} expandHref={(id) => `${base}?w=${encodeURIComponent(id)}`} onRemove={doc.can_edit ? removeWidget : undefined} />
      )}
    </div>
  );
}
