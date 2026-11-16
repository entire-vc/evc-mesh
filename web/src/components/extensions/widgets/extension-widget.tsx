import { Component, Suspense, type ErrorInfo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Puzzle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/cn";
import { useExtensionWidget } from "@/lib/extension-widgets";
import { ExternalWidgetFrame } from "@/components/extensions/external-widget-frame";
import { BUILTIN_WIDGETS } from "./builtin-widgets";

export interface ExtensionWidgetHostProps {
  /** Widget type of the instance, `<ext_id>.<widget_id>`. */
  type: string;
  settings?: Record<string, unknown>;
  size?: { w: number; h: number };
  /** The dashboard and the instance: an iframe widget's token names both. */
  dashboardId?: string;
  instanceId?: string;
  /** Bump to tell an iframe widget to reload its data. */
  refreshNonce?: number;
  /** An iframe widget asked for its settings. */
  onConfigure?: () => void;
  /** Removes the instance from the dashboard; without it the stub has no button (a viewer who cannot edit). */
  onRemove?: () => void;
}

function Pending() {
  return (
    <div className="space-y-2 p-3" aria-busy="true" data-testid="extension-widget-pending">
      <div className="h-4 w-32 animate-pulse rounded bg-muted" />
      <div className="h-10 w-full animate-pulse rounded bg-muted" />
    </div>
  );
}

/** What an instance shows while its extension cannot draw it: it stays put, nothing disappears silently. */
export function ExtensionWidgetStub({ text, onRemove }: { text: string; onRemove?: () => void }) {
  const { t } = useTranslation("integrations");
  return (
    <div
      className="flex h-full min-h-24 flex-col items-center justify-center gap-3 p-4 text-center text-sm text-muted-foreground"
      role="status"
      data-testid="extension-widget-stub"
    >
      <Puzzle className="h-5 w-5" aria-hidden />
      <p>{text}</p>
      {onRemove && (
        <Button variant="outline" size="sm" onClick={onRemove} data-testid="extension-widget-remove">
          {t("widgets.remove")}
        </Button>
      )}
    </div>
  );
}

class WidgetErrorBoundary extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(_error: Error, _info: ErrorInfo) {
    // A broken extension widget degrades to its stub; the dashboard keeps working.
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

/**
 * One dashboard instance of an extension widget. A built-in extension's widget
 * is its lazy component; an extension that is off, waiting for consent or gone
 * shows the stub with "Remove widget" and draws again by itself when it is back.
 */
export function ExtensionWidget({ type, settings, size, dashboardId, instanceId, refreshNonce, onConfigure, onRemove }: ExtensionWidgetHostProps) {
  const { t } = useTranslation("integrations");
  const st = useExtensionWidget(type);
  if (st.state === "loading") return <Pending />;
  if (st.state !== "ready") {
    return <ExtensionWidgetStub text={t(`widgets.stub.${st.state}`, { name: st.extensionName })} onRemove={onRemove} />;
  }
  if (st.widget.render.frame) {
    // An external extension's widget: its own page in a sandboxed iframe (protocol v2), loaded
    // lazily and at most six at a time. Without a saved dashboard instance there is nothing to sign.
    if (!dashboardId || !instanceId) {
      return <ExtensionWidgetStub text={t("widgets.stub.unavailable", { name: st.extensionName })} onRemove={onRemove} />;
    }
    const projectId = st.widget.scope === "project" && typeof settings?.project_id === "string" ? settings.project_id : undefined;
    return (
      <ExternalWidgetFrame
        extensionId={st.extensionId}
        name={st.extensionName}
        widgetType={st.widget.id}
        dashboardId={dashboardId}
        instanceId={instanceId}
        projectId={projectId}
        settings={settings}
        size={size}
        refreshNonce={refreshNonce}
        onConfigure={onConfigure}
      />
    );
  }
  const View = st.widget.render.builtin ? BUILTIN_WIDGETS[st.widget.render.builtin] : undefined;
  if (!View) {
    return <ExtensionWidgetStub text={t("widgets.stub.unavailable", { name: st.extensionName })} onRemove={onRemove} />;
  }
  const failed = <ExtensionWidgetStub text={t("widgets.stub.unavailable", { name: st.extensionName })} onRemove={onRemove} />;
  return (
    <WidgetErrorBoundary key={type} fallback={failed}>
      <Suspense fallback={<Pending />}>
        <View type={type} settings={settings ?? {}} size={size} />
      </Suspense>
    </WidgetErrorBoundary>
  );
}

/** The manifest title in the interface language, else the plain title. */
function widgetTitle(widget: { title: string; title_i18n?: Record<string, string> }, lang: string): string {
  const base = lang.split("-")[0] ?? lang;
  return widget.title_i18n?.[lang] ?? widget.title_i18n?.[base] ?? widget.title;
}

/**
 * An extension widget as a dashboard cell: the same card as a core widget, the
 * manifest title on top while the extension can draw it, the extension's stub
 * ("GitLab is turned off", "Remove widget") while it cannot.
 */
export function ExtensionWidgetCard(props: ExtensionWidgetHostProps) {
  const { i18n } = useTranslation("integrations");
  const st = useExtensionWidget(props.type);
  return (
    <Card className="flex h-full min-h-0 flex-1 flex-col" data-testid="extension-widget-card" data-extension-widget={props.type}>
      <CardContent className="flex min-h-0 flex-1 flex-col pt-4">
        {st.state === "ready" && (
          <div className="mb-3 flex items-center gap-1.5 text-sm font-semibold">
            <Puzzle className="h-4 w-4" aria-hidden />
            <span className="truncate">{widgetTitle(st.widget, i18n.language || "en")}</span>
          </div>
        )}
        {/* A flex column, so an iframe widget's frame stretches to the card instead of its minimum height. */}
        <div className={cn("min-h-0 flex-1 overflow-auto", st.state === "ready" && st.widget.render.frame && "flex flex-col")}>
          <ExtensionWidget {...props} />
        </div>
      </CardContent>
    </Card>
  );
}
