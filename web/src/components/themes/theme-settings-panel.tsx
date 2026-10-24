import { useParams } from "react-router";
import { applyWorkspaceTheme } from "@/core/themes/runtime";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Lock, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { SchemaForm, type FieldSchema, type Values } from "@/components/extensions/schema-form";
import { apiErrorMessage } from "@/lib/api-error";
import {
  getInstanceTheme,
  getResolvedInstanceTheme,
  getResolvedWorkspaceTheme,
  getWorkspaceTheme,
  listThemes,
  putInstanceTheme,
  putWorkspaceTheme,
  type ContrastWarning,
  type ThemeInfo,
  type ThemeList,
  type ThemeSelection,
} from "@/lib/themes";
import { parseTokensCss } from "./color";
import { ThemeCard } from "./theme-card";
import { ThemePreview, settingsTokenOverrides } from "./theme-preview";

export type ThemeLevel = "workspace" | "instance";

const FORM_ID = "theme-settings-form";
const MESH_DEFAULT = "mesh-default";

export function ContrastNotice({ warnings }: { warnings: ContrastWarning[] }) {
  const { t } = useTranslation("settings");
  if (warnings.length === 0) return null;
  return (
    <div role="status" className="flex gap-2 rounded-lg border border-warning/50 bg-warning/10 p-3 text-sm">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" aria-hidden />
      <div className="space-y-1">
        <p className="font-medium">{t("themes.contrast.title")}</p>
        <p className="text-muted-foreground">{t("themes.contrast.hint")}</p>
        <ul className="list-disc pl-4 text-xs text-muted-foreground">
          {warnings.slice(0, 5).map((w, i) => (
            <li key={i}>{w.message ?? [w.mode, w.pair, w.ratio != null ? w.ratio.toFixed(1) : null].filter(Boolean).join(" · ")}</li>
          ))}
        </ul>
      </div>
    </div>
  );
}

function hasFields(s: FieldSchema | undefined): s is FieldSchema {
  return Boolean(s && s.properties && Object.keys(s.properties).length > 0);
}

/**
 * Theme choice and its settings of one level: cards with a preview, the chosen theme's
 * form (its schema for this level, in full), a live preview, and reset. Used for a
 * workspace (inherit = "as the instance") and for the instance (default = built-in look).
 */
export function ThemeSettingsPanel({
  level,
  workspaceId,
  readOnly = false,
  roleLoading = false,
  roleUnknown = false,
  onForbidden,
}: {
  level: ThemeLevel;
  workspaceId?: string;
  readOnly?: boolean;
  /** The caller's role is not known yet: show a placeholder instead of guessing read-only. */
  roleLoading?: boolean;
  /** The role lookup failed: the panel stays read-only but must not claim the user lacks the role. */
  roleUnknown?: boolean;
  /** The server refused the admin routes (not an instance administrator). */
  onForbidden?: () => void;
}) {
  const { t } = useTranslation("settings");
  const { wsSlug } = useParams();
  // the board reads the theme from the runtime, which only refetches on a workspace switch: re-apply after every change
  const reapply = () => {
    if (wsSlug) void applyWorkspaceTheme(wsSlug);
  };
  const [list, setList] = useState<ThemeList | null>(null);
  const [selection, setSelection] = useState<ThemeSelection | null>(null);
  const [inheritTokens, setInheritTokens] = useState<ThemeInfo["tokens"] | undefined>();
  const [error, setError] = useState<string | null>(null);
  const [chosen, setChosen] = useState<string | null>(null);
  // a member cannot read the saved selection (admin-only): the card of the theme in force comes from the resolved theme
  const [activeId, setActiveId] = useState<string | null | undefined>();
  const [draft, setDraft] = useState<Values>({});
  const [saving, setSaving] = useState(false);
  const [warnings, setWarnings] = useState<ContrastWarning[]>([]);
  // The form's first emission after (re)mount is its defaults, not an edit: dirty is measured against it.
  const baseline = useRef<{ key: string; values: string } | null>(null);
  // The instance has no "as the instance" to fall back on: nothing chosen there is mesh-default, the app's own look,
  // which carries a settings schema like any other theme (the server stores it as "no theme" while it has no settings).
  const baseId = level === "instance" ? MESH_DEFAULT : null;
  const storedId = selection?.theme_id ?? baseId;
  const formKey = `${chosen}:${selection?.version ?? 0}`;

  const load = useCallback(async () => {
    setError(null);
    try {
      const [l, sel] = await Promise.all([
        listThemes(),
        readOnly ? Promise.resolve(null) : level === "workspace" ? getWorkspaceTheme(workspaceId as string) : getInstanceTheme(),
      ]);
      setList(l);
      setSelection(sel);
      setChosen(sel?.theme_id ?? baseId);
      setDraft(sel?.settings ?? {});
      setWarnings(sel?.contrast_warnings ?? []);
      if (readOnly && level === "workspace" && wsSlug) {
        const r = await getResolvedWorkspaceTheme(wsSlug).catch(() => undefined);
        // a failed request leaves the theme unknown (undefined): no card is marked
        // a resolved theme that is not an installed one (the built-in default) is the base card
        setActiveId(r === undefined ? undefined : l.themes.some((x) => x.id === r.theme_id) ? r.theme_id : null);
      }
      if (level === "workspace") {
        const r = await getResolvedInstanceTheme().catch(() => null);
        setInheritTokens(r ? parseTokensCss(r.tokens_css) : undefined);
      }
    } catch (e) {
      if ((e as { status?: number })?.status === 403 && level === "instance") onForbidden?.();
      setError(apiErrorMessage(e, t("themes.loadFailed")));
    }
  }, [level, workspaceId, readOnly, wsSlug, onForbidden, baseId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const theme = useMemo(() => list?.themes.find((x) => x.id === chosen), [list, chosen]);
  const schema = theme?.settings[level];
  const overrides = useMemo(() => settingsTokenOverrides([schema], draft), [schema, draft]);
  const previewTokens = chosen ? theme?.tokens : level === "workspace" ? inheritTokens : undefined;
  const stored: Values = selection && storedId === chosen ? selection.settings : {};

  const save = async (settings: Values): Promise<void> => {
    setSaving(true);
    try {
      const res = await (level === "workspace" ? putWorkspaceTheme(workspaceId as string, chosen, settings) : putInstanceTheme(chosen, settings));
      setSelection(res);
      setWarnings(res.contrast_warnings ?? []);
      reapply();
      toast.success(t("themes.saved"));
    } finally {
      setSaving(false);
    }
  };

  const reset = async () => {
    setSaving(true);
    try {
      const res = await putWorkspaceTheme(workspaceId as string, null, {});
      setSelection(res);
      setChosen(null);
      setDraft({});
      setWarnings([]);
      reapply();
      toast.success(t("themes.resetDone"));
    } catch (e) {
      toast.error(apiErrorMessage(e, t("themes.saveFailed")));
    } finally {
      setSaving(false);
    }
  };

  if (roleLoading) {
    return <div role="status" aria-busy="true" data-testid="theme-role-loading" className="h-24 animate-pulse rounded-lg bg-muted/40" />;
  }

  if (error) {
    return (
      <div role="alert" className="space-y-3 rounded-lg border border-destructive/40 p-4">
        <p className="text-sm text-destructive">{error}</p>
        <Button type="button" variant="outline" size="sm" onClick={() => void load()}>
          {t("themes.retry")}
        </Button>
      </div>
    );
  }
  if (!list) {
    return (
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true" aria-label={t("themes.loading")}>
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-60 rounded-xl" />
        ))}
      </div>
    );
  }

  const baseName = t("themes.picker.inherit");
  const formBase = baseline.current?.key === formKey ? baseline.current.values : null;
  const dirty = storedId !== chosen || (hasFields(schema) && formBase !== null && formBase !== JSON.stringify(draft));
  const changeable = !readOnly;
  const currentId = readOnly ? activeId : chosen;

  return (
    <div className="space-y-5">
      {readOnly && !roleUnknown && (
        <p className="flex items-center gap-2 rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
          <Lock className="h-4 w-4 shrink-0" aria-hidden />
          {t("themes.readOnly")}
        </p>
      )}
      {readOnly && activeId === undefined && wsSlug && level === "workspace" && !error && (
        <p className="text-sm text-muted-foreground">{t("themes.activeUnknown")}</p>
      )}
      <div role="radiogroup" aria-label={t("themes.picker.label")} className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {level === "workspace" && (
          <ThemeCard
            name={baseName}
            description={t("themes.picker.inheritHint")}
            tokens={inheritTokens}
            selected={currentId === null}
            disabled={!changeable}
            onSelect={() => {
              setChosen(null);
              setDraft({});
              setWarnings([]);
            }}
          />
        )}
        {list.themes.map((th) => (
          <ThemeCard
            key={th.id}
            name={th.name}
            description={th.description}
            version={th.version}
            tokens={th.tokens}
            selected={currentId === th.id}
            disabled={!changeable}
            onSelect={() => {
              setChosen(th.id);
              setDraft(storedId === th.id ? (selection?.settings ?? {}) : {});
              // warnings describe the saved selection; they do not carry over to another theme
              setWarnings(storedId === th.id ? (selection?.contrast_warnings ?? []) : []);
            }}
          />
        ))}
      </div>
      {list.themes.length === 0 && <p className="text-sm text-muted-foreground">{level === "workspace" ? t("themes.empty.workspace") : t("themes.empty.instance")}</p>}

      {changeable && (hasFields(schema) || chosen !== null) && (
        <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
          <div className="order-2 space-y-4 lg:order-1">
            {hasFields(schema) ? (
              <SchemaForm
                key={formKey}
                formId={FORM_ID}
                schema={schema}
                manifest={{ id: chosen }}
                value={stored}
                hideSubmit
                disabled={saving}
                onValuesChange={(v) => {
                  if (baseline.current?.key !== formKey) baseline.current = { key: formKey, values: JSON.stringify(v) };
                  setDraft(v);
                }}
                onSubmit={async (settings) => {
                  await save(settings);
                }}
              />
            ) : (
              chosen && <p className="text-sm text-muted-foreground">{t("themes.noSettings")}</p>
            )}
            <ContrastNotice warnings={warnings} />
          </div>
          <aside className="order-1 space-y-2 lg:sticky lg:top-4 lg:order-2 lg:self-start">
            <h3 className="text-sm font-medium">{t("themes.preview.title")}</h3>
            <ThemePreview tokens={previewTokens} overrides={overrides} />
            <p className="text-xs text-muted-foreground">{t("themes.preview.hint")}</p>
          </aside>
        </div>
      )}

      {changeable && (
        <div className="sticky bottom-0 -mx-1 flex flex-wrap items-center gap-3 border-t bg-background/95 px-1 py-3 backdrop-blur">
          <Button
            type={hasFields(schema) ? "submit" : "button"}
            form={hasFields(schema) ? FORM_ID : undefined}
            disabled={saving || !dirty}
            onClick={hasFields(schema) ? undefined : () => void save({}).catch((e) => toast.error(apiErrorMessage(e, t("themes.saveFailed"))))}
          >
            {t("themes.save")}
          </Button>
          {level === "workspace" && (
            <Button type="button" variant="outline" disabled={saving || (selection?.theme_id ?? null) === null} onClick={() => void reset()}>
              <RotateCcw className="mr-2 h-4 w-4" aria-hidden />
              {t("themes.reset")}
            </Button>
          )}
          {dirty && <span className="text-xs text-muted-foreground">{t("themes.unsaved")}</span>}
        </div>
      )}
    </div>
  );
}
