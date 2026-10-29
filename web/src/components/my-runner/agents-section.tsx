import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import type { FieldSchema } from "@/components/extensions/schema-form";
import { EntryEditor, type Editor } from "@/components/my-runner/resources-section";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { apiErrorMessage } from "@/lib/api-error";
import {
  agentState, bindingsByAgent, entrySchema, fieldErrorsByEntry, findSecretViolation, isForeignBinding, parseLimit, profileLabel, profilesByAgent,
  saveResources, sectionEntries, setAgentLimit, setRuntimeManager, withEntry, type OperatorAgent, type ResourceDoc,
} from "@/lib/my-runner";

/** Same threshold as the resource lists: below it the table is short enough to scan. */
const FILTER_FROM = 20;
/** Fleet agents carry six and more profiles; the rest is in the tooltip so the column stays narrow. */
const PROFILES_SHOWN = 2;

/** A table cell that, below md, becomes a labelled line of a stacked card. */
function Cell({ label, children, className = "" }: { label: string; children: ReactNode; className?: string }) {
  return (
    <td className={`block py-1 align-middle md:table-cell md:px-3 md:py-2 ${className}`}>
      <span className="mr-2 text-xs text-muted-foreground md:hidden">{label}:</span>
      {children}
    </td>
  );
}

function LimitEditor({ agent, onChanged }: { agent: OperatorAgent; onChanged: () => void }) {
  const { t } = useTranslation("runner");
  const [limit, setLimit] = useState(String(agent.max_concurrent_tasks));
  const [busy, setBusy] = useState(false);
  const parsed = parseLimit(limit);
  const dirty = parsed !== null && parsed !== agent.max_concurrent_tasks;

  const save = async () => {
    if (parsed === null || !dirty) return;
    setBusy(true);
    try {
      await setAgentLimit(agent.agent_id, parsed);
      toast.success(t("agents.saved"));
      onChanged();
    } catch (e) {
      toast.error(apiErrorMessage(e, t("agents.saveFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <Input
        inputMode="numeric"
        value={limit}
        onChange={(e) => setLimit(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") void save();
          // the first Escape puts the stored limit back; only an untouched field lets it close the dialog
          if (e.key === "Escape" && limit !== String(agent.max_concurrent_tasks)) {
            e.stopPropagation();
            setLimit(String(agent.max_concurrent_tasks));
          }
        }}
        aria-invalid={parsed === null}
        aria-label={`${t("agents.limit")}: ${agent.name}`}
        className="h-8 w-20"
      />
      {/* Save shows up only for a change, so an untouched table has no button per row. */}
      {dirty && <Button size="sm" disabled={busy} onClick={() => void save()}>{t("agents.save")}</Button>}
      {parsed === null && <span role="alert" className="text-xs text-destructive">{t("agents.limitInvalid")}</span>}
    </span>
  );
}

function ManagerToggle({ agent, onChanged }: { agent: OperatorAgent; onChanged: () => void }) {
  const { t } = useTranslation("runner");
  const [busy, setBusy] = useState(false);
  const toggle = async (granted: boolean) => {
    setBusy(true);
    try {
      await setRuntimeManager(agent.agent_id, granted);
      toast.success(t(granted ? "agents.managerGranted" : "agents.managerRevoked", { name: agent.name }));
      onChanged();
    } catch (e) {
      toast.error(apiErrorMessage(e, t("agents.managerFailed")));
    } finally {
      setBusy(false);
    }
  };
  return (
    <input
      type="checkbox"
      checked={agent.runtime_manager}
      disabled={busy}
      onChange={(e) => void toggle(e.target.checked)}
      className="h-4 w-4 align-middle accent-primary"
      aria-label={`${t("agents.manager")}: ${agent.name}`}
      title={t("agents.managerHint")}
    />
  );
}

/** Why the server does not run an agent's settings, in words; an unknown reason is shown as sent. */
function ExcludedNote({ reason, testId }: { reason: string; testId: string }) {
  const { t } = useTranslation("runner");
  return (
    <span className="mt-1 block text-xs font-medium text-warning" data-testid={testId}>
      {t(`resources.excluded.${reason}`, { defaultValue: t("resources.excluded.other", { reason }) })}
    </span>
  );
}

function AgentRow({ agent, me, profiles, off, excluded, canEdit, onEdit }: {
  agent: OperatorAgent; me?: string; profiles: string[]; off: boolean; excluded?: string; canEdit: boolean; onEdit: () => void;
}) {
  const { t } = useTranslation("runner");
  const state = agentState(agent);
  const own = !!me && agent.operator_user_id === me;
  const operator = own ? t("agents.operatorYou") : t(agent.operator_user_id ? "agents.operatorOther" : "agents.operatorNone");

  return (
    <tr className="block rounded-lg border border-border p-3 md:table-row md:rounded-none md:border-0 md:border-t md:p-0" data-testid={`my-runner-agent-${agent.agent_id}`}>
      <td className="block pb-1 font-medium break-words md:table-cell md:min-w-32 md:px-3 md:py-2">
        {agent.name}
        {off && <Badge variant="outline" className="ml-2 align-middle">{t("agents.off")}</Badge>}
        {excluded && <ExcludedNote reason={excluded} testId={`my-runner-agent-excluded-${agent.agent_id}`} />}
      </td>
      <Cell label={t("agents.colRunner")}>
        {state === "none" ? (
          <Badge variant="outline">{t("agents.noRunner")}</Badge>
        ) : (
          <Badge variant={state === "live" ? "success" : "warning"} className="whitespace-nowrap">
            {agent.controller?.name} · {t(state === "live" ? "agents.runnerLive" : "agents.runnerLapsed")}
          </Badge>
        )}
      </Cell>
      <Cell label={t("agents.colOperator")}>
        <span className={own ? "" : "text-muted-foreground"} title={own ? undefined : t(agent.operator_user_id ? "agents.otherOperator" : "agents.noOperator")}>
          {operator}
        </span>
      </Cell>
      <Cell label={t("agents.colProfiles")}>
        {profiles.length === 0 ? <span className="text-muted-foreground">—</span> : (
          // one profile per line: the readable names are long and commas between them blur into one run
          <span className="inline-block min-w-0 align-top break-words md:block" title={profiles.join("\n")}>
            {profiles.slice(0, PROFILES_SHOWN).map((p, i) => <span key={i} className="block">{p}</span>)}
            {profiles.length > PROFILES_SHOWN && <span className="block text-xs text-muted-foreground">{t("agents.moreProfiles", { n: profiles.length - PROFILES_SHOWN })}</span>}
          </span>
        )}
      </Cell>
      <Cell label={t("agents.limit")}>
        <span data-testid={`my-runner-agent-limit-${agent.agent_id}`}>{agent.max_concurrent_tasks}</span>
      </Cell>
      <Cell label={t("agents.colManager")}>
        <span className={agent.runtime_manager ? "" : "text-muted-foreground"}>{agent.runtime_manager ? t("agents.yes") : "—"}</span>
      </Cell>
      <td className="block pt-2 md:table-cell md:px-3 md:py-2 md:text-right">
        {canEdit && (
          <Button size="sm" variant="outline" onClick={onEdit} aria-label={`${t("agents.edit")}: ${agent.name}`}>{t("agents.edit")}</Button>
        )}
      </td>
    </tr>
  );
}

/** What the agent dialog edits: an agent of the list, or a catalog entry whose agent is not listed. */
type Target = { agent: OperatorAgent | null; binding: string | null; title: string };

/**
 * The runner settings of one agent, saved at once as a new catalog revision:
 * the allowed profiles (by readable name), first choice, on/off and the rest of the entry.
 */
function BindingForm({ doc, target, onSaved, onCancel }: { doc: Extract<ResourceDoc, { source: "workspace" }>; target: Target; onSaved: () => void; onCancel: () => void }) {
  const { t } = useTranslation("runner");
  const [editor, setEditor] = useState<Editor>({ section: "bindings", original: target.binding });
  // After a refused save the form reopens on what was sent, not on the stored entry.
  const [config, setConfig] = useState(doc.config);
  const schema = useMemo(() => {
    const base = entrySchema(doc.manifest, "bindings");
    const allowed = base?.properties?.permitted_profiles;
    if (!base || !allowed) return base;
    // the manifest's one-liner says nothing about what a profile is; the screen does
    return { ...base, properties: { ...base.properties, permitted_profiles: { ...allowed, "x-help": t("profiles.allowedHelp") } } } as FieldSchema;
  }, [doc.manifest, t]);
  // a new entry starts as this agent in this workspace; only the access grant is left to fill in
  const initial = useMemo(() => target.agent ? { binding: { agent_id: target.agent.agent_id, workspace_id: doc.wsId } } : undefined, [target.agent, doc.wsId]);
  const initialName = target.agent ? target.agent.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") : undefined;

  if (!schema) return <p role="alert" className="text-sm text-destructive">{t("resources.noSchema")}</p>;

  const save = async (name: string, value: unknown) => {
    let next = config;
    if (editor.original !== null && editor.original !== name) next = withEntry(next, "bindings", editor.original, undefined);
    next = withEntry(next, "bindings", name, value);
    const secret = findSecretViolation(next);
    if (secret) return void toast.error(t("resources.secretRejected", { path: secret }));
    try {
      await saveResources(doc, next);
      toast.success(t("agents.settingsSaved"));
      onSaved();
    } catch (e) {
      const conflict = (e as { status?: number }).status === 409;
      const mine = conflict ? undefined : fieldErrorsByEntry((e as { details?: Record<string, string> }).details, next).find((r) => r.section === "bindings" && r.name === name);
      if (mine) {
        setConfig(next);
        setEditor({ section: "bindings", original: name, serverErrors: mine.fields });
      }
      toast.error(conflict ? t("resources.conflict") : apiErrorMessage(e, t("agents.settingsFailed")));
      if (conflict) onSaved();
    }
  };

  return (
    <EntryEditor
      key={JSON.stringify(editor.serverErrors ?? {})}
      config={config}
      schema={schema}
      editor={editor}
      initial={initial}
      initialName={initialName}
      submitLabel={t("agents.saveSettings")}
      onApply={save}
      onCancel={onCancel}
    />
  );
}

function AgentDialog({ target, me, catalog, onClose, onAgentChanged, onCatalogSaved }: {
  target: Target; me?: string; catalog: ResourceDoc | null; onClose: () => void; onAgentChanged: () => void; onCatalogSaved: () => void;
}) {
  const { t } = useTranslation("runner");
  const own = !!me && !!target.agent && target.agent.operator_user_id === me;
  const workspace = catalog?.source === "workspace" ? catalog : null;
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }} className="max-w-2xl">
      <DialogContent
        role="dialog"
        aria-modal="true"
        aria-labelledby="my-runner-agent-dialog-title"
        onClose={onClose}
        closeLabel={t("agents.close")}
        onKeyDown={(e) => { if (e.key === "Escape") onClose(); }}
        className="max-h-[90dvh] space-y-5 overflow-y-auto p-4 sm:p-6"
        data-testid="my-runner-agent-dialog"
      >
        <DialogHeader>
          <DialogTitle id="my-runner-agent-dialog-title" className="pr-6 break-words">{t("agents.editTitle", { name: target.title })}</DialogTitle>
          <DialogDescription>{t(workspace ? "agents.editHint" : "agents.editHintPersonal")}</DialogDescription>
        </DialogHeader>
        {own && target.agent && (
          <section className="space-y-3" aria-label={t("agents.workSection")}>
            <h3 className="text-sm font-medium">{t("agents.workSection")}</h3>
            <label className="flex flex-wrap items-center gap-3 text-sm">
              <span className="w-40 text-muted-foreground">{t("agents.limit")}</span>
              <LimitEditor key={target.agent.max_concurrent_tasks} agent={target.agent} onChanged={onAgentChanged} />
            </label>
            <label className="flex items-start gap-3 text-sm">
              <span className="pt-0.5"><ManagerToggle agent={target.agent} onChanged={onAgentChanged} /></span>
              <span className="min-w-0 break-words">{t("agents.manager")}</span>
            </label>
            <p className="text-xs text-muted-foreground">{t("agents.managerHint")}</p>
          </section>
        )}
        {!own && target.agent && (
          <section className="space-y-3" aria-label={t("agents.workSection")} data-testid="my-runner-agent-work-readonly">
            <h3 className="text-sm font-medium">{t("agents.workSection")}</h3>
            <div className="flex flex-wrap items-center gap-3 text-sm">
              <span className="w-40 text-muted-foreground">{t("agents.limit")}</span>
              <span>{target.agent.max_concurrent_tasks}</span>
            </div>
            <div className="flex flex-wrap items-center gap-3 text-sm">
              <span className="w-40 text-muted-foreground">{t("agents.colManager")}</span>
              <span>{target.agent.runtime_manager ? t("agents.yes") : "—"}</span>
            </div>
            <p className="text-xs text-muted-foreground">{t(target.agent.operator_user_id ? "agents.otherOperator" : "agents.noOperator")}</p>
          </section>
        )}
        {workspace && (
          <section className="space-y-3" aria-label={t("agents.settingsSection")}>
            <h3 className="text-sm font-medium">{t("agents.settingsSection")}</h3>
            <p className="text-sm text-muted-foreground">{t("profiles.hint")}</p>
            <BindingForm doc={workspace} target={target} onSaved={() => { onCatalogSaved(); onClose(); }} onCancel={onClose} />
          </section>
        )}
      </DialogContent>
    </Dialog>
  );
}

function AgentsTable({ agents, me, catalog, reload, onCatalogSaved }: {
  agents: OperatorAgent[]; me?: string; catalog: ResourceDoc | null; reload: () => void; onCatalogSaved: () => void;
}) {
  const { t } = useTranslation("runner");
  const [query, setQuery] = useState("");
  const [target, setTarget] = useState<Target | null>(null);
  const config = catalog?.source === "workspace" ? catalog.config : null;
  // Another workspace's settings are not this workspace's agents: counted, never listed or edited,
  // and kept in the catalog as they are, since a save sends the whole loaded catalog back.
  const wsId = catalog?.source === "workspace" ? catalog.wsId : undefined;
  const excluded = catalog?.source === "workspace" ? catalog.excluded : {};
  // Runner settings are edited by whoever may save the workspace catalog (it loads only for them, with its form).
  const catalogEditable = catalog?.source === "workspace" && catalog.manifest !== null;
  const profiles = useMemo(() => {
    const out: Record<string, string[]> = {};
    for (const [id, names] of Object.entries(profilesByAgent(config, wsId))) out[id] = names.map((n) => profileLabel(n, config, t));
    return out;
  }, [config, wsId, t]);
  const bindings = useMemo(() => bindingsByAgent(config, wsId), [config, wsId]);
  const listed = new Set(agents.map((a) => a.agent_id));
  const own = sectionEntries(config, "bindings").filter(([, v]) => !(wsId && isForeignBinding(v, wsId)));
  const foreign = sectionEntries(config, "bindings").length - own.length;
  // catalog entries for agents this list does not show (another operator's, a removed agent): still reachable
  const unlisted = own.filter(([, v]) => {
    const id = (v as { binding?: { agent_id?: unknown } } | null)?.binding?.agent_id;
    return typeof id !== "string" || !listed.has(id);
  }).map(([name]) => name);
  const q = agents.length > FILTER_FROM ? query.trim().toLowerCase() : "";
  const shown = [...agents]
    .sort((a, b) => a.name.localeCompare(b.name))
    .filter((a) => !q || a.name.toLowerCase().includes(q));
  const offOf = (id: string) => {
    const name = bindings[id];
    return name !== undefined && (config?.bindings?.[name] as { enabled?: unknown } | undefined)?.enabled === false;
  };

  return (
    <div className="space-y-3">
      {agents.length > FILTER_FROM && (
        <Input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("agents.filter")}
          aria-label={t("agents.filter")}
          className="h-9 sm:max-w-xs"
        />
      )}
      {agents.length === 0 && <p className="text-sm text-muted-foreground" data-testid="my-runner-agents-empty">{t("agents.empty")}</p>}
      {agents.length > 0 && <table className="block w-full text-sm md:table">
        <thead className="hidden md:table-header-group">
          <tr className="text-left text-xs whitespace-nowrap text-muted-foreground">
            <th className="px-3 py-2 font-medium">{t("agents.colAgent")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colRunner")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colOperator")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colProfiles")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.limit")}</th>
            <th className="px-3 py-2 font-medium">{t("agents.colManager")}</th>
            <th className="px-3 py-2"><span className="sr-only">{t("agents.edit")}</span></th>
          </tr>
        </thead>
        <tbody className="block space-y-3 md:table-row-group md:space-y-0">
          {shown.map((a) => (
            <AgentRow
              key={a.agent_id}
              agent={a}
              me={me}
              profiles={profiles[a.agent_id] ?? []}
              off={offOf(a.agent_id)}
              excluded={bindings[a.agent_id] !== undefined ? excluded[bindings[a.agent_id]!] : undefined}
              canEdit={catalogEditable || (!!me && a.operator_user_id === me)}
              onEdit={() => setTarget({ agent: a, binding: bindings[a.agent_id] ?? null, title: a.name })}
            />
          ))}
        </tbody>
      </table>}
      {agents.length > 0 && shown.length === 0 && <p className="text-sm text-muted-foreground">{t("resources.noMatches")}</p>}
      {/* What the catalog holds is shown whenever it loaded; without its form (no manifest) only read, not opened. */}
      {config && unlisted.length > 0 && (
        <div className="space-y-2 border-t border-border pt-3" data-testid="my-runner-agents-unlisted">
          <p className="text-xs text-muted-foreground">{t("agents.unlisted")}</p>
          <ul className="flex flex-wrap gap-2">
            {unlisted.map((name) => (
              <li key={name}>
                {catalogEditable
                  ? <Button size="sm" variant="ghost" onClick={() => setTarget({ agent: null, binding: name, title: name })}>{name}</Button>
                  : <span className="inline-block px-3 py-1 text-sm">{name}</span>}
                {excluded[name] && <ExcludedNote reason={excluded[name]} testId={`my-runner-binding-excluded-${name}`} />}
              </li>
            ))}
          </ul>
        </div>
      )}
      {config && foreign > 0 && (
        <p role="status" className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm" data-testid="my-runner-agents-foreign">
          {t("resources.foreignBindings", { count: foreign })}
        </p>
      )}
      {target && (
        <AgentDialog
          // the agent as last loaded, so a saved limit shows at once in the open dialog
          target={{ ...target, agent: agents.find((a) => a.agent_id === target.agent?.agent_id) ?? target.agent }}
          me={me}
          catalog={catalog}
          onClose={() => setTarget(null)}
          onAgentChanged={reload}
          onCatalogSaved={onCatalogSaved}
        />
      )}
    </div>
  );
}

export function AgentsSection({ result, reload, me, catalog = null, onCatalogSaved = () => {} }: {
  result: Loaded<OperatorAgent[]>; reload: () => void; me?: string;
  /** The loaded resource document: the workspace catalog holds each agent's runner settings. */
  catalog?: ResourceDoc | null;
  onCatalogSaved?: () => void;
}) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("agents.title")} testId="my-runner-agents" result={result} onRetry={reload}>
      {(agents) =>
        // with no agents listed, catalog entries (unlisted, hidden foreign ones, not-run reasons) still show
        agents.length === 0 && sectionEntries(catalog?.source === "workspace" ? catalog.config : null, "bindings").length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="my-runner-agents-empty">{t("agents.empty")}</p>
        ) : (
          <AgentsTable agents={agents} me={me} catalog={catalog} reload={reload} onCatalogSaved={onCatalogSaved} />
        )
      }
    </SectionShell>
  );
}
