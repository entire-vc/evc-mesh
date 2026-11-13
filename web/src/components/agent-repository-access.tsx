import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { useAgentStore } from "@/stores/agent";
import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import { formatRelative } from "@/lib/utils";
import type {
  Agent,
  RepositoryAccess,
  RepositoryAccessAuditEntry,
  RepositoryAccessAuditPage,
  RepositoryRef,
} from "@/types";

interface Props {
  agent: Agent;
  /** UI hint only: the server remains the authority on who may write. */
  canEdit: boolean;
}

// PUT /agents/:id/repository-access (CAS). A 409 rejects with ApiRequestError and leaves the store
// untouched; the caller keeps the draft. Lives here, not in the agent store, so the code ships with
// this lazy dialog chunk instead of the app-shell chunk.
const saveRepositoryAccess = async (
  agentId: string,
  expectedRevision: number,
  repositories: RepositoryRef[],
): Promise<RepositoryAccess> => {
  const saved = await api<RepositoryAccess>(`/api/v1/agents/${agentId}/repository-access`, {
    method: "PUT",
    body: { expected_revision: expectedRevision, repositories },
  });
  useAgentStore.setState((state) => ({
    agents: state.agents.map((a) => (a.id === agentId ? { ...a, repository_access: saved } : a)),
  }));
  return saved;
};

const fetchRepositoryAccessHistory = (agentId: string, cursor?: string | null) => {
  const q = new URLSearchParams({ limit: "10" });
  if (cursor) q.set("cursor", cursor);
  return api<RepositoryAccessAuditPage>(`/api/v1/agents/${agentId}/repository-access/history?${q.toString()}`);
};

const key = (r: RepositoryRef) => `${r.provider}:${r.host}/${r.project_path}`;
const emptyRow = (): RepositoryRef => ({ provider: "gitlab", host: "", project_path: "" });
const sameList = (a: RepositoryRef[], b: RepositoryRef[]) =>
  a.length === b.length &&
  a.every((r, i) => key(r) === (b[i] ? key(b[i]) : ""));

export function AgentRepositoryAccess({ agent, canEdit }: Props) {
  const { t } = useTranslation("integrations");
  const { fetchAgent } = useAgentStore();
  // The authoritative GET result lives in component state; the prop is only the initial value and
  // a hint (a stale or field-less agent object must never turn a configured agent into "legacy").
  const [access, setAccess] = useState<RepositoryAccess | null>(agent.repository_access ?? null);
  useEffect(() => {
    if (agent.repository_access) setAccess(agent.repository_access);
  }, [agent.repository_access]);
  const serverRepos = useMemo(() => access?.repositories ?? [], [access]);
  const serverRevision = access?.revision ?? 0;

  const [draft, setDraft] = useState<RepositoryRef[]>(serverRepos);
  const [dirty, setDirty] = useState(false);
  // Revision the draft was edited against. It follows the server only while the draft is clean;
  // a dirty draft keeps it until an explicit reread, so a stale list can never skip the CAS.
  const [baseRevision, setBaseRevision] = useState(serverRevision);
  // The agent list never carries repository_access: load the agent before showing or editing it.
  const [loadState, setLoadState] = useState<"loading" | "ready" | "error">("loading");
  const [attempt, setAttempt] = useState(0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [history, setHistory] = useState<RepositoryAccessAuditEntry[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [historyState, setHistoryState] = useState<"loading" | "ready" | "error">("loading");
  const agentId = useRef(agent.id);
  agentId.current = agent.id;

  // Follow the server value unless the user has unsaved input; a changed agent resets everything.
  useEffect(() => {
    setDirty(false);
    setConflict(false);
    setError(null);
    setAccess(agent.repository_access ?? null);
    setDraft(serverRepos);
    setBaseRevision(serverRevision);
    setHistory([]);
    setCursor(null);
    setLoadState("loading");
    const id = agent.id;
    fetchAgent(id)
      .then((fresh) => {
        if (agentId.current !== id) return;
        setAccess(fresh.repository_access ?? null);
        setBaseRevision(fresh.repository_access?.revision ?? 0);
        setDraft(fresh.repository_access?.repositories ?? []);
        setLoadState("ready");
      })
      .catch(() => {
        if (agentId.current === id) setLoadState("error");
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id, attempt]);
  useEffect(() => {
    if (!dirty) {
      setDraft(serverRepos);
      setBaseRevision(serverRevision);
    }
  }, [serverRepos, serverRevision, dirty]);

  const loadHistory = useCallback(
    async (next?: string | null) => {
      const id = agent.id;
      if (!next) setHistoryState("loading");
      try {
        const page = await fetchRepositoryAccessHistory(id, next);
        if (agentId.current !== id) return;
        setHistory((h) => (next ? [...h, ...page.items] : page.items));
        setCursor(page.next_cursor ?? null);
        setHistoryState("ready");
      } catch {
        if (agentId.current === id) setHistoryState("error");
      }
    },
    [agent.id],
  );
  useEffect(() => {
    void loadHistory();
  }, [loadHistory, serverRevision]);

  const update = (i: number, patch: Partial<RepositoryRef>) => {
    setDraft((d) => d.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    setDirty(true);
  };
  const cleaned = draft.map((r) => ({
    provider: r.provider.trim(),
    host: r.host.trim(),
    project_path: r.project_path.trim(),
  }));
  const incomplete = cleaned.some((r) => !r.provider || !r.host || !r.project_path);
  const dupes = new Set(cleaned.map(key)).size !== cleaned.length;

  const save = async () => {
    const id = agent.id;
    setSaving(true);
    setError(null);
    try {
      const saved = await saveRepositoryAccess(id, baseRevision, cleaned);
      if (agentId.current !== id) return;
      setAccess(saved);
      setDraft(saved.repositories);
      setBaseRevision(saved.revision);
      setDirty(false);
      setConflict(false);
    } catch (err) {
      if (agentId.current !== id) return;
      if ((err as { status?: number })?.status === 409) setConflict(true);
      else setError(apiErrorMessage(err, t("agentDetail.repositoryAccess.error.save")));
    } finally {
      setSaving(false);
    }
  };

  const reread = async () => {
    setError(null);
    try {
      const fresh = await fetchAgent(agent.id); // explicit new baseline; the draft stays untouched
      setAccess(fresh.repository_access ?? null);
      setBaseRevision(fresh.repository_access?.revision ?? 0);
      setConflict(false);
    } catch (err) {
      setError(apiErrorMessage(err, t("agentDetail.repositoryAccess.error.reload")));
    }
  };

  const discard = () => {
    setDraft(serverRepos);
    setDirty(false);
    setConflict(false);
  };

  if (loadState !== "ready") {
    return (
      <div data-testid="repository-access">
        <p className="mb-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {t("agentDetail.repositoryAccess.title")}
        </p>
        {loadState === "loading" ? (
          <p className="text-xs text-muted-foreground">{t("agentDetail.workspaces.loading")}</p>
        ) : (
          <p role="alert" className="text-xs text-destructive">
            {t("agentDetail.repositoryAccess.error.reload")}{" "}
            <button className="underline" onClick={() => setAttempt((n) => n + 1)}>
              {t("agentDetail.repositoryAccess.retry")}
            </button>
          </p>
        )}
      </div>
    );
  }

  const legacy = access === null;
  const denyAll = access !== null && access.repositories.length === 0;

  return (
    <div data-testid="repository-access">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {t("agentDetail.repositoryAccess.title")}
        </p>
        <Badge variant="outline" className="text-xs" data-testid="repository-access-revision">
          {legacy
            ? t("agentDetail.repositoryAccess.legacyBadge")
            : t("agentDetail.repositoryAccess.revision", { revision: access.revision })}
        </Badge>
      </div>

      {legacy && (
        <p className="mb-2 text-sm text-muted-foreground" data-testid="repository-access-legacy">
          {t("agentDetail.repositoryAccess.legacy")}
        </p>
      )}
      {denyAll && !dirty && (
        <p className="mb-2 text-sm text-tone-attention-600" data-testid="repository-access-deny-all">
          {t("agentDetail.repositoryAccess.denyAll")}
        </p>
      )}

      {conflict && (
        <div role="alert" className="mb-2 rounded-md border border-tone-attention-600 p-2 text-sm" data-testid="repository-access-conflict">
          <p>{t("agentDetail.repositoryAccess.conflict")}</p>
          {serverRevision > 0 || serverRepos.length > 0 ? (
            <p className="mt-1 break-all text-xs text-muted-foreground">
              {t("agentDetail.repositoryAccess.serverNow", { revision: serverRevision })}{" "}
              {serverRepos.map(key).join(", ") || t("agentDetail.repositoryAccess.denyAllShort")}
            </p>
          ) : null}
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="outline" onClick={() => void reread()}>
              {t("agentDetail.repositoryAccess.reread")}
            </Button>
            <Button size="sm" variant="ghost" onClick={discard}>
              {t("agentDetail.repositoryAccess.discard")}
            </Button>
          </div>
        </div>
      )}

      <div className="space-y-1.5">
        {draft.map((r, i) => (
          <div key={i} className="flex items-center gap-1.5" data-testid="repository-access-row">
            {canEdit ? (
              <>
                <Input disabled={saving} aria-label={t("agentDetail.repositoryAccess.provider")} className="h-8 w-20 shrink-0 text-xs" value={r.provider} onChange={(e) => update(i, { provider: e.target.value })} />
                <Input disabled={saving} aria-label={t("agentDetail.repositoryAccess.host")} className="h-8 min-w-0 flex-1 text-xs" value={r.host} onChange={(e) => update(i, { host: e.target.value })} />
                <Input disabled={saving} aria-label={t("agentDetail.repositoryAccess.path")} className="h-8 min-w-0 flex-[2] text-xs" value={r.project_path} onChange={(e) => update(i, { project_path: e.target.value })} />
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 shrink-0"
                  disabled={saving}
                  aria-label={t("agentDetail.repositoryAccess.remove")}
                  onClick={() => {
                    setDraft((d) => d.filter((_, j) => j !== i));
                    setDirty(true);
                  }}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              </>
            ) : (
              <span className="truncate rounded-md border border-border px-2.5 py-1.5 text-sm" title={key(r)}>
                {key(r)}
              </span>
            )}
          </div>
        ))}
      </div>

      {canEdit ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" className="gap-1" disabled={saving} onClick={() => { setDraft((d) => [...d, emptyRow()]); setDirty(true); }}>
            <Plus className="h-3.5 w-3.5" />
            {t("agentDetail.repositoryAccess.add")}
          </Button>
          <Button size="sm" disabled={!dirty || saving || incomplete || dupes} onClick={() => void save()}>
            {t("agentDetail.repositoryAccess.save")}
          </Button>
          {dirty && !sameList(cleaned, serverRepos) && draft.length === 0 && (
            <span className="text-xs text-tone-attention-600">{t("agentDetail.repositoryAccess.willDenyAll")}</span>
          )}
          {dupes && <span className="text-xs text-destructive">{t("agentDetail.repositoryAccess.duplicate")}</span>}
        </div>
      ) : (
        <p className="mt-2 text-xs text-muted-foreground" data-testid="repository-access-readonly">
          {t("agentDetail.repositoryAccess.readOnly")}
        </p>
      )}
      {error && <p role="alert" className="mt-2 text-xs text-destructive">{error}</p>}

      <div className="mt-3" data-testid="repository-access-history">
        <p className="mb-1 text-xs font-medium text-muted-foreground">{t("agentDetail.repositoryAccess.history")}</p>
        {historyState === "loading" && <p className="text-xs text-muted-foreground">{t("agentDetail.workspaces.loading")}</p>}
        {historyState === "error" && (
          <p className="text-xs text-destructive">
            {t("agentDetail.repositoryAccess.historyError")}{" "}
            <button className="underline" onClick={() => void loadHistory()}>{t("agentDetail.repositoryAccess.retry")}</button>
          </p>
        )}
        {historyState === "ready" && history.length === 0 && (
          <p className="text-xs text-muted-foreground">{t("agentDetail.repositoryAccess.historyEmpty")}</p>
        )}
        <ul className="space-y-1.5">
          {history.map((h) => {
            const oldKeys = new Set((h.old?.repositories ?? []).map(key));
            const newKeys = new Set(h.new.repositories.map(key));
            const added = [...newKeys].filter((k) => !oldKeys.has(k));
            const removed = [...oldKeys].filter((k) => !newKeys.has(k));
            return (
              <li key={h.id} className="rounded-md border border-border px-2.5 py-1.5 text-xs" data-testid="repository-access-audit-entry">
                <div className="flex flex-wrap items-center justify-between gap-x-2">
                  <span className="font-medium">{h.actor.name ?? h.actor.id}</span>
                  <span className="text-muted-foreground" title={h.created_at}>{formatRelative(h.created_at)}</span>
                </div>
                <div className="text-muted-foreground">
                  {t("agentDetail.repositoryAccess.revisionChange", { from: h.old?.revision ?? 0, to: h.new.revision })}
                </div>
                {added.map((k) => (<div key={`+${k}`} className="break-all text-tone-success-600" title={k}>+ {k}</div>))}
                {removed.map((k) => (<div key={`-${k}`} className="break-all text-destructive" title={k}>− {k}</div>))}
                {h.new.repositories.length === 0 && <div className="text-tone-attention-600">{t("agentDetail.repositoryAccess.denyAllShort")}</div>}
              </li>
            );
          })}
        </ul>
        {cursor && (
          <Button size="sm" variant="ghost" className="mt-1" onClick={() => void loadHistory(cursor)}>
            {t("agentDetail.repositoryAccess.more")}
          </Button>
        )}
      </div>
    </div>
  );
}
