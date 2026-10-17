import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams } from "react-router";
import { AlertTriangle, ArrowLeft, Check, CheckCircle2, Loader2, Lock, PackageSearch, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ExtensionIcon, KindTrustBadges } from "@/components/extensions/catalog";
import { SchemaForm, resolveSchema, type FieldSchema, type Values } from "@/components/extensions/schema-form";
import { useExtensionCatalog, type CatalogEntry } from "@/hooks/useExtensionCatalog";
import { allowedDomains, capabilityText, domainText, consentTokens, describeCapabilities } from "@/lib/extension-capabilities";
import { connectWorkspaceExtension, testWorkspaceExtension } from "@/lib/extension-connect";
import { getInstanceOverview } from "@/lib/extension-instance";
import { apiErrorMessage } from "@/lib/api-error";
import { useWorkspaceStore } from "@/stores/workspace";
import { cn } from "@/lib/cn";

export const WIZARD_STEPS = ["permissions", "settings", "test", "done"] as const;
export type WizardStep = (typeof WIZARD_STEPS)[number];

function Stepper({ step }: { step: WizardStep }) {
  const { t } = useTranslation("integrations");
  const current = WIZARD_STEPS.indexOf(step);
  return (
    <ol aria-label={t("connect.stepsLabel")} className="mb-6 flex items-center gap-2 overflow-x-auto">
      {WIZARD_STEPS.map((id, i) => {
        const state = i < current ? "done" : i === current ? "current" : "todo";
        return (
          <li key={id} aria-current={state === "current" ? "step" : undefined} className="flex shrink-0 items-center gap-2">
            <span
              className={cn(
                "flex h-6 w-6 items-center justify-center rounded-full border text-xs font-medium",
                state === "done" && "border-primary bg-primary text-primary-foreground",
                state === "current" && "border-primary text-primary",
                state === "todo" && "border-border text-muted-foreground",
              )}
            >
              {state === "done" ? <Check className="h-3.5 w-3.5" aria-hidden /> : i + 1}
            </span>
            <span className={cn("text-sm", state === "current" ? "font-medium" : "text-muted-foreground", state !== "current" && "hidden sm:inline")}>
              {t(`connect.steps.${id}`)}
            </span>
            {i < WIZARD_STEPS.length - 1 && <span className="h-px w-4 bg-border sm:w-8" aria-hidden />}
          </li>
        );
      })}
    </ol>
  );
}

function PermissionsStep({ entry, consented, consentError, onConsent, onNext }: {
  entry: CatalogEntry; consented: boolean; consentError: boolean; onConsent: (v: boolean) => void; onNext: () => void;
}) {
  const { t } = useTranslation("integrations");
  const caps = entry.manifest.capabilities;
  const lines = describeCapabilities(caps);
  const domains = allowedDomains(caps);
  return (
    <section aria-labelledby="wiz-h-permissions" className="max-w-2xl space-y-4">
      <h2 id="wiz-h-permissions" className="text-lg font-semibold">{t("connect.permissions.title", { name: entry.name })}</h2>
      {entry.trust === "custom" && (
        <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm font-medium text-destructive">
          {t("connect.permissions.unchecked")}
        </div>
      )}
      {lines.length === 0 && domains.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("detail.permissions.none")}</p>
      ) : (
        <ul className="space-y-2" aria-label={t("detail.tabs.permissions")}>
          {lines.map((l) => (
            <li key={`${l.group}:${l.raw}`} className="flex items-start gap-2 text-sm">
              <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
              <span>{capabilityText(t, l)}</span>
            </li>
          ))}
        </ul>
      )}
      {domains.length > 0 && (
        <div>
          <h3 className="mb-2 text-sm font-semibold">{t("detail.permissions.domains")}</h3>
          <ul className="flex flex-wrap gap-2">
            {domains.map((d) => <li key={d}><code className="rounded bg-muted px-2 py-0.5 text-xs">{domainText(t, d)}</code></li>)}
          </ul>
        </div>
      )}
      <div>
        <label
          className={cn("flex cursor-pointer items-start gap-2 rounded-lg border p-3 text-sm", consentError ? "border-destructive" : "border-border")}
        >
          <input
            type="checkbox"
            checked={consented}
            onChange={(e) => onConsent(e.target.checked)}
            aria-invalid={consentError || undefined}
            aria-describedby={consentError ? "wiz-consent-error" : undefined}
            className="mt-0.5 h-4 w-4 shrink-0 accent-primary"
          />
          <span>{t("connect.permissions.consent")}</span>
        </label>
        {consentError && (
          <p id="wiz-consent-error" role="alert" className="mt-1 text-sm text-destructive">{t("connect.permissions.consentError")}</p>
        )}
      </div>
      <Button onClick={onNext} disabled={!consented}>{t("connect.next")}</Button>
    </section>
  );
}

function SettingsStep({ entry, fromInstance, isAdmin, wsSlug, onBack, onConnect, onSkip }: {
  entry: CatalogEntry; fromInstance: boolean; isAdmin: boolean; wsSlug: string;
  onBack: () => void; onConnect: (settings: Values) => Promise<void | false>; onSkip: () => void;
}) {
  const { t } = useTranslation("integrations");
  const navigate = useNavigate();
  const declared: FieldSchema | undefined = entry.manifest.settings?.workspace;
  // the workspace level is usually a `$ref` into the manifest's `schemas`: resolve before asking for fields
  const schema = declared ? resolveSchema(declared, entry.manifest) : undefined;
  const hasFields = Boolean(schema?.properties && Object.keys(schema.properties).length > 0);
  return (
    <section aria-labelledby="wiz-h-settings" className="max-w-2xl space-y-4">
      <h2 id="wiz-h-settings" className="text-lg font-semibold">{t("connect.settings.title")}</h2>
      {fromInstance && (
        <div role="note" className="flex items-start gap-2 rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm">
          <Lock className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <div className="space-y-1">
            <p className="font-medium">{t("connect.settings.byInstance")}</p>
            <p className="text-muted-foreground">{t("connect.settings.byInstanceHint")}</p>
            {isAdmin && (
              <Button variant="outline" size="sm" onClick={() => navigate(`/w/${wsSlug}/instance/extensions`)}>
                {t("connect.settings.editInstance")}
              </Button>
            )}
          </div>
        </div>
      )}
      {schema && hasFields ? (
        <SchemaForm
          schema={schema}
          manifest={entry.manifest}
          disabled={fromInstance}
          hideSubmit={fromInstance}
          submitLabel={t("connect.next")}
          onSubmit={onConnect}
        />
      ) : (
        <>
          <p className="text-sm text-muted-foreground">{t("connect.settings.none")}</p>
          {!fromInstance && <ConnectNoFields onConnect={() => onConnect({})} />}
        </>
      )}
      <div className="flex gap-2">
        <Button variant="outline" onClick={onBack}>{t("connect.back")}</Button>
        {fromInstance && <Button onClick={onSkip}>{t("connect.next")}</Button>}
      </div>
    </section>
  );
}

function ConnectNoFields({ onConnect }: { onConnect: () => Promise<void | false> }) {
  const { t } = useTranslation("integrations");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <div className="space-y-2">
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <Button
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setError(null);
          try { await onConnect(); } catch (e) { setError(apiErrorMessage(e, t("connect.settings.failed"))); } finally { setBusy(false); }
        }}
      >
        {t("connect.next")}
      </Button>
    </div>
  );
}

type TestState = { kind: "idle" } | { kind: "running" } | { kind: "ok" } | { kind: "failed" } | { kind: "error"; message: string };

function TestStep({ wsId, entry, onDone }: { wsId: string; entry: CatalogEntry; onDone: () => void }) {
  const { t } = useTranslation("integrations");
  const [state, setState] = useState<TestState>({ kind: "idle" });
  const run = async () => {
    setState({ kind: "running" });
    try {
      const res = await testWorkspaceExtension(wsId, entry.id);
      // The health text is raw server output (ADR 789a9235): never shown, only ok/not ok.
      setState({ kind: res.ok ? "ok" : "failed" });
    } catch (e) {
      setState({ kind: "error", message: apiErrorMessage(e, t("connect.test.requestFailed")) });
    }
  };
  return (
    <section aria-labelledby="wiz-h-test" className="max-w-2xl space-y-4">
      <h2 id="wiz-h-test" className="text-lg font-semibold">{t("connect.test.title")}</h2>
      <p className="text-sm text-muted-foreground">{t("connect.test.hint")}</p>
      <div aria-live="polite" className="min-h-6 text-sm">
        {state.kind === "running" && (
          <span role="status" className="inline-flex items-center gap-2 text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden />{t("connect.test.running")}
          </span>
        )}
        {state.kind === "ok" && (
          <span role="status" className="inline-flex items-center gap-2 text-success">
            <CheckCircle2 className="h-4 w-4" aria-hidden />{t("connect.test.ok")}
          </span>
        )}
        {state.kind === "failed" && (
          <span role="alert" className="inline-flex items-start gap-2 text-destructive">
            <XCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />{t("connect.test.failed")}
          </span>
        )}
        {state.kind === "error" && (
          <span role="alert" className="inline-flex items-start gap-2 text-destructive">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />{state.message}
          </span>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        {state.kind === "ok" ? (
          <Button onClick={onDone}>{t("connect.next")}</Button>
        ) : (
          <>
            <Button onClick={run} disabled={state.kind === "running"}>
              {t(state.kind === "idle" ? "connect.test.run" : "connect.test.retry")}
            </Button>
            {(state.kind === "failed" || state.kind === "error") && (
              <Button variant="ghost" onClick={onDone}>{t("connect.test.skip")}</Button>
            )}
          </>
        )}
      </div>
    </section>
  );
}

function DoneStep({ entry, wsSlug }: { entry: CatalogEntry; wsSlug: string }) {
  const { t } = useTranslation("integrations");
  const navigate = useNavigate();
  const slots = describeCapabilities(entry.manifest.capabilities).filter((l) => l.group === "ui");
  return (
    <section aria-labelledby="wiz-h-done" className="max-w-2xl space-y-4">
      <h2 id="wiz-h-done" className="flex items-center gap-2 text-lg font-semibold">
        <CheckCircle2 className="h-5 w-5 text-success" aria-hidden />{t("connect.done.title", { name: entry.name })}
      </h2>
      <div>
        <h3 className="mb-2 text-sm font-semibold">{t("connect.done.appeared")}</h3>
        {slots.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("connect.done.noSlots")}</p>
        ) : (
          <ul className="space-y-2">
            {slots.map((l) => (
              <li key={l.raw} className="flex items-start gap-2 text-sm">
                <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
                <span>{t(l.key, l.values)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
      {entry.manifest.levels?.project && (
        <p className="text-sm">
          <Link to={`/w/${wsSlug}/extensions?tab=installed`} className="text-primary hover:underline">
            {t("connect.done.bindProjects")}
          </Link>
        </p>
      )}
      <Button variant="outline" onClick={() => navigate(`/w/${wsSlug}/extensions?tab=installed`)}>
        {t("connect.done.toInstalled")}
      </Button>
    </section>
  );
}

export function ExtensionConnectPage() {
  const { t } = useTranslation("integrations");
  const { wsSlug = "", extId = "" } = useParams();
  const { currentWorkspace } = useWorkspaceStore();
  const catalog = useExtensionCatalog();
  const [step, setStep] = useState<WizardStep>("permissions");
  const [consented, setConsented] = useState(false);
  const [consentError, setConsentError] = useState(false);
  const [visitedSettings, setVisitedSettings] = useState(false);
  // the instance administrator is not the workspace admin: only the instance API can tell
  const [isAdmin, setIsAdmin] = useState(false);
  useEffect(() => {
    let live = true;
    getInstanceOverview().then(
      () => live && setIsAdmin(true),
      () => live && setIsAdmin(false),
    );
    return () => {
      live = false;
    };
  }, []);
  const entry = catalog.entries.find((e) => e.id === extId);
  const wsId = currentWorkspace?.id ?? "";

  const back = (
    <Link to={`/w/${wsSlug}/extensions/${extId}`} className="mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="h-4 w-4" aria-hidden />{t("connect.backToExtension")}
    </Link>
  );

  if (catalog.state === "loading") {
    return (
      <div>
        {back}
        <div role="status" aria-label={t("detail.loading")} className="space-y-3">
          <Skeleton className="h-16 w-full rounded-xl" /><Skeleton className="h-48 w-full rounded-xl" />
        </div>
      </div>
    );
  }
  if (catalog.state === "error" || !entry) {
    const notFound = catalog.state === "ready";
    const Icon = notFound ? PackageSearch : AlertTriangle;
    return (
      <div>
        {back}
        <div role={notFound ? undefined : "alert"} className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center">
          <Icon className="h-8 w-8 text-muted-foreground" aria-hidden />
          <p className="text-sm font-medium">{t(notFound ? "detail.notFoundTitle" : "detail.errorTitle")}</p>
          <p className="max-w-prose text-sm text-muted-foreground">{t(notFound ? "detail.notFoundBody" : "detail.errorBody")}</p>
          {!notFound && <Button variant="outline" size="sm" onClick={catalog.reload}>{t("extensionsPage.catalog.retry")}</Button>}
        </div>
      </div>
    );
  }
  if (entry.blocked || !entry.installed || !entry.allowed) {
    return (
      <div>
        {back}
        <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm">
          {t("connect.unavailable")}
        </div>
      </div>
    );
  }
  // A workspace connection already exists: connect would answer 409, so the wizard has nothing to do.
  if (entry.status?.source === "workspace" && entry.connected && step !== "test" && step !== "done") {
    return (
      <div>
        {back}
        <div role="note" className="rounded-lg border border-border px-3 py-2 text-sm">
          {t("connect.alreadyConnected")}{" "}
          <Link to={`/w/${wsSlug}/extensions?tab=installed`} className="text-primary hover:underline">{t("connect.done.toInstalled")}</Link>
        </div>
      </div>
    );
  }

  const fromInstance = entry.status?.source === "instance";

  const connect = async (settings: Values): Promise<void | false> => {
    try {
      await connectWorkspaceExtension(wsId, entry.id, settings, consentTokens(entry.manifest.capabilities));
    } catch (e) {
      // details is an object only for field errors; a 404 ("Extension not found") carries a string or nothing
      const raw = (e as { details?: unknown } | null)?.details;
      const details = raw !== null && typeof raw === "object" ? (raw as Record<string, string>) : undefined;
      if (details && "consent" in details) {
        // consent is a step-1 problem: go back there with the checkbox marked
        setConsented(false);
        setConsentError(true);
        setStep("permissions");
        // not stored: tell the form to keep what was typed, secrets included
        return false;
      }
      // a whole-form settings error has no field to highlight: show it as the form message
      if (details && "settings" in details) throw new Error(details.settings);
      throw e;
    }
    setStep("test");
  };

  return (
    <div className="flex h-full flex-col">
      {back}
      <header className="mb-4 flex items-start gap-3">
        <ExtensionIcon entry={entry} className="h-12 w-12" />
        <div className="min-w-0">
          <h1 className="truncate text-xl font-semibold">{t("connect.heading", { name: entry.name })}</h1>
          <div className="mt-1 flex flex-wrap items-center gap-1.5"><KindTrustBadges entry={entry} /></div>
        </div>
      </header>
      <Stepper step={step} />
      <div className="flex-1 overflow-y-auto pb-6">
        {step === "permissions" && (
          <PermissionsStep
            entry={entry}
            consented={consented}
            consentError={consentError}
            onConsent={(v) => { setConsented(v); if (v) setConsentError(false); }}
            onNext={() => { setVisitedSettings(true); setStep("settings"); }}
          />
        )}
        {/* stays mounted once visited: a consent 400 sends the user to step 1 and must not drop the typed settings */}
        {(step === "settings" || (visitedSettings && step === "permissions")) && (
          <div hidden={step !== "settings"}>
            <SettingsStep
              entry={entry}
              fromInstance={fromInstance}
              isAdmin={isAdmin}
              wsSlug={wsSlug}
              onBack={() => setStep("permissions")}
              onConnect={connect}
              onSkip={() => setStep("test")}
            />
          </div>
        )}
        {step === "test" && <TestStep wsId={wsId} entry={entry} onDone={() => setStep("done")} />}
        {step === "done" && <DoneStep entry={entry} wsSlug={wsSlug} />}
      </div>
    </div>
  );
}
