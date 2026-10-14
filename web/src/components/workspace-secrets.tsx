import * as i18nFmt from "@/i18n/format";
import { agentLabel } from "@/lib/agent-label";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, RotateCw, Trash2 } from "lucide-react";

import { api } from "@/lib/api";
import { apiErrorMessage } from "@/lib/api-error";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useAgentStore } from "@/stores/agent";
import { useProjectStore } from "@/stores/project";
import type { CreateSecretRequest, Secret, SecretScope } from "@/types";

// ---------------------------------------------------------------------------
// Write-only secret store (task #64e84eb1, S5).
//
// The one property this component exists to preserve: a value goes in and is
// never rendered again. That is enforced by the types rather than by care taken
// here — `Secret` has no value field, so there is nothing to render even by
// mistake, and the plaintext lives only in the `value` state of an open form.
//
// Consequences that look like missing features but are the feature:
//   * no edit-in-place — "replace" calls rotate, which inserts a NEW row and
//     supersedes the old one. Nothing reads the old value to prefill a form,
//     because no endpoint can return it.
//   * no "reveal" toggle. There is nothing to reveal.
//   * delete stops materialization; it does not erase history.
//
// COPY STATUS (§1r.A). Pavel approved a specific table of strings on
// 2026-08-21 (task #705013f4): the section heading and its subtitle, both
// input placeholders, Store/Storing, the seven column headers, the empty
// state, both dialog titles and bodies, Replace/Replacing, and the two
// validation errors. Those are approved and must not be reworded casually.
//
// NOT covered by that approval, and still pending: the three success toasts,
// the four "Could not …" error fallbacks, the rotate dialog's own placeholder
// and validation string, the two Cancel buttons, the delete-confirm button
// label, and the "expired" badge. They are each either an existing string
// elsewhere in this app or a mechanical inflection of an approved word — but
// "not new voice" is an argument, not an approval. Presence in main does not
// make them approved.
//
// SCOPE SELECTOR (task #73e9b55e, security fix). Until this change the create
// form hardcoded `scope: "workspace"` — the only scope that materializes into
// EVERY agent's spawn environment (checkoutLeaseReaper / fiddler.py). A human
// trying to hand a token to one lane through this form was actually handing
// it to all 22, because there was no way to say otherwise: the API already
// supported `scope: "agent"` + `agent_id` (and `project`), the UI just never
// asked. Measured live (#d1270354): a workspace-scope probe secret
// materialized into two unrelated lanes from one POST.
//
// The list view has the same defect from the other side: `GET
// /workspaces/:id/secrets` with no query params returns ONLY the
// workspace-scoped rows — an agent- or project-scoped secret is invisible
// until you ask for it BY that specific agent_id/project_id (this mirrors the
// spawn materializer's own resolution, not a "list everything" endpoint; see
// SecretRepo.ListCurrent's SQL). So the list here fans out one extra GET per
// known agent and per known project and merges the results client-side —
// there is no server-side "give me every scope" call to make instead.
//
// Scope is NOT editable on an existing row (see handleCreate: it is only ever
// set at creation). Letting a PATCH change scope later would be a silent
// radius expansion on a secret whoever approved it believed was narrow —
// changing scope means delete and create again, same as every other
// "identity" field on a secret (name, too, cannot be renamed via rotate).
// ---------------------------------------------------------------------------

const NAME_PATTERN = /^[A-Z][A-Z0-9_]*$/;

interface WorkspaceSecretsProps {
  workspaceId: string;
  /** Owner/admin only. The API enforces this independently — this just avoids
   *  rendering a form whose every submit would 403. */
  canManage: boolean;
}

function formatDate(iso: string): string {
  return i18nFmt.formatDate(iso, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

function isExpired(secret: Secret): boolean {
  return !!secret.expires_at && new Date(secret.expires_at).getTime() < Date.now();
}

export function WorkspaceSecrets({ workspaceId, canManage }: WorkspaceSecretsProps) {
  const { t } = useTranslation("settings");
  const [secrets, setSecrets] = useState<Secret[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);

  const { agents, fetchAgents } = useAgentStore();
  const { projects, fetchProjects } = useProjectStore();

  // Create form
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [scope, setScope] = useState<SecretScope>("workspace");
  const [scopeAgentId, setScopeAgentId] = useState("");
  const [scopeProjectId, setScopeProjectId] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [isSaving, setIsSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // Rotate / delete dialogs
  const [rotating, setRotating] = useState<Secret | null>(null);
  const [rotateValue, setRotateValue] = useState("");
  const [isRotating, setIsRotating] = useState(false);
  const [rotateError, setRotateError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<Secret | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);

  // See the top-of-file comment: there is no single call that returns every
  // scope, so this fans out one GET per known agent/project and keeps only
  // the rows that actually belong to that id — the base (no-param) call
  // already carries every workspace-scoped row, so it is not repeated per
  // fan-out call, only unioned with them once.
  const load = useCallback(async () => {
    setIsLoading(true);
    setLoadError(null);
    try {
      const base = await api<Secret[]>(`/api/v1/workspaces/${workspaceId}/secrets`);
      const workspaceScoped = (base ?? []).filter((s) => s.scope === "workspace");

      const agentRows = await Promise.all(
        agents.map((a) =>
          api<Secret[]>(`/api/v1/workspaces/${workspaceId}/secrets?agent_id=${a.id}`)
            .then((rows) => (rows ?? []).filter((s) => s.scope === "agent" && s.agent_id === a.id))
            .catch(() => [] as Secret[]),
        ),
      );
      const projectRows = await Promise.all(
        projects.map((p) =>
          api<Secret[]>(`/api/v1/workspaces/${workspaceId}/secrets?project_id=${p.id}`)
            .then((rows) => (rows ?? []).filter((s) => s.scope === "project" && s.project_id === p.id))
            .catch(() => [] as Secret[]),
        ),
      );

      setSecrets([...workspaceScoped, ...agentRows.flat(), ...projectRows.flat()]);
    } catch (err) {
      setLoadError(apiErrorMessage(err, t("secrets.error.load")));
    } finally {
      setIsLoading(false);
    }
  }, [workspaceId, agents, projects, t]);

  useEffect(() => {
    void fetchAgents(workspaceId);
    void fetchProjects(workspaceId);
  }, [workspaceId, fetchAgents, fetchProjects]);

  useEffect(() => {
    void load();
  }, [load]);

  function targetLabel(s: Secret): string {
    if (s.scope === "agent") {
      return agents.find((a) => a.id === s.agent_id)?.name ?? t("secrets.target.unknownAgent");
    }
    if (s.scope === "project") {
      return projects.find((p) => p.id === s.project_id)?.name ?? t("secrets.target.unknownProject");
    }
    return t("secrets.target.allAgents");
  }

  const resetCreateForm = () => {
    setName("");
    setValue("");
    setScope("workspace");
    setScopeAgentId("");
    setScopeProjectId("");
    setExpiresAt("");
    setFormError(null);
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!NAME_PATTERN.test(name)) {
      setFormError(t("secrets.validation.name"));
      return;
    }
    if (!value) {
      setFormError(t("secrets.validation.value"));
      return;
    }
    if (scope === "agent" && !scopeAgentId) {
      setFormError(t("secrets.validation.agent"));
      return;
    }
    if (scope === "project" && !scopeProjectId) {
      setFormError(t("secrets.validation.project"));
      return;
    }

    setIsSaving(true);
    setFormError(null);
    const payload: CreateSecretRequest = {
      name,
      scope,
      value,
      ...(scope === "agent" ? { agent_id: scopeAgentId } : {}),
      ...(scope === "project" ? { project_id: scopeProjectId } : {}),
      ...(expiresAt ? { expires_at: new Date(expiresAt).toISOString() } : {}),
    };
    try {
      await api<Secret>(`/api/v1/workspaces/${workspaceId}/secrets`, {
        method: "POST",
        body: payload,
      });
      // Clear the plaintext from component state before anything else, so it
      // does not outlive the request even if the reload below is slow.
      resetCreateForm();
      toast.success(t("secrets.toast.stored", { name }));
      await load();
    } catch (err) {
      setFormError(apiErrorMessage(err, t("secrets.error.store")));
    } finally {
      setIsSaving(false);
    }
  };

  const handleRotate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!rotating) return;
    if (!rotateValue) {
      setRotateError(t("secrets.validation.newValue"));
      return;
    }
    setIsRotating(true);
    setRotateError(null);
    try {
      await api<Secret>(`/api/v1/secrets/${rotating.id}/rotate`, {
        method: "POST",
        body: { value: rotateValue },
      });
      setRotateValue("");
      setRotating(null);
      toast.success(t("secrets.toast.replaced", { name: rotating.name }));
      await load();
    } catch (err) {
      setRotateError(apiErrorMessage(err, t("secrets.error.replace")));
    } finally {
      setIsRotating(false);
    }
  };

  const handleDelete = async () => {
    if (!deleting) return;
    setIsDeleting(true);
    try {
      await api<void>(`/api/v1/secrets/${deleting.id}`, { method: "DELETE" });
      toast.success(t("secrets.toast.removed", { name: deleting.name }));
      setDeleting(null);
      await load();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("secrets.error.remove")));
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <div className="space-y-4" data-testid="workspace-secrets">
      <div className="space-y-1">
        <h3 className="text-sm font-medium flex items-center gap-1.5">
          <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
          {t("secrets.title")}
        </h3>
        <p className="text-xs text-muted-foreground">
          {t("secrets.subtitle")}
        </p>
      </div>

      {canManage && (
        <form onSubmit={handleCreate} className="space-y-2" data-testid="secret-create-form">
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              aria-label={t("secrets.form.nameLabel")}
              placeholder="GITHUB_TOKEN"
              value={name}
              onChange={(e) => setName(e.target.value.toUpperCase())}
              className="sm:w-56 font-mono"
              autoComplete="off"
              spellCheck={false}
              data-testid="secret-name-input"
            />
            <Input
              aria-label={t("secrets.form.valueLabel")}
              type="password"
              placeholder={t("secrets.form.valuePlaceholder")}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              className="flex-1"
              autoComplete="new-password"
              spellCheck={false}
              data-testid="secret-value-input"
            />
            <Input
              aria-label={t("secrets.form.expiresLabel")}
              type="date"
              value={expiresAt}
              onChange={(e) => setExpiresAt(e.target.value)}
              className="sm:w-40"
              data-testid="secret-expires-input"
            />
            <Button type="submit" disabled={isSaving} data-testid="secret-submit">
              {isSaving ? t("secrets.form.storing") : t("secrets.form.store")}
            </Button>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Select
              aria-label={t("secrets.form.scopeLabel")}
              value={scope}
              onChange={(e) => {
                setScope(e.target.value as SecretScope);
                setScopeAgentId("");
                setScopeProjectId("");
              }}
              className="sm:w-56"
              data-testid="secret-scope-select"
            >
              <option value="workspace">{t("secrets.form.scopeWorkspace")}</option>
              <option value="agent">{t("secrets.form.scopeAgent")}</option>
              <option value="project">{t("secrets.form.scopeProject")}</option>
            </Select>
            {scope === "agent" && (
              <Select
                aria-label={t("secrets.form.agentLabel")}
                value={scopeAgentId}
                onChange={(e) => setScopeAgentId(e.target.value)}
                className="flex-1"
                data-testid="secret-scope-agent-select"
              >
                <option value="">{t("secrets.form.chooseAgent")}</option>
                {agents.map((a) => (
                  <option key={a.id} value={a.id}>
                    {agentLabel(a.name, a.short_tag)}
                  </option>
                ))}
              </Select>
            )}
            {scope === "project" && (
              <Select
                aria-label={t("secrets.form.projectLabel")}
                value={scopeProjectId}
                onChange={(e) => setScopeProjectId(e.target.value)}
                className="flex-1"
                data-testid="secret-scope-project-select"
              >
                <option value="">{t("secrets.form.chooseProject")}</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            )}
          </div>
          {formError && (
            <p className="text-xs text-destructive" data-testid="secret-form-error">
              {formError}
            </p>
          )}
        </form>
      )}

      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : loadError ? (
        <p className="text-xs text-destructive" data-testid="secret-load-error">
          {loadError}
        </p>
      ) : secrets.length === 0 ? (
        <p className="text-xs text-muted-foreground" data-testid="secret-empty">
          {t("secrets.empty")}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs" data-testid="secret-list">
            <thead>
              <tr className="text-left text-muted-foreground">
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.name")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.scope")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.goesTo")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.fingerprint")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.length")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.characters")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.added")}</th>
                <th className="py-1.5 pr-3 font-medium">{t("secrets.columns.expires")}</th>
                {canManage && <th className="py-1.5 font-medium sr-only">{t("secrets.columns.actions")}</th>}
              </tr>
            </thead>
            <tbody>
              {secrets.map((s) => (
                <tr key={s.id} className="border-t" data-testid={`secret-row-${s.name}`}>
                  <td className="py-1.5 pr-3 font-mono">{s.name}</td>
                  <td className="py-1.5 pr-3">
                    <Badge variant="outline">{t(`secrets.scope.${s.scope}`, { defaultValue: s.scope })}</Badge>
                  </td>
                  <td className="py-1.5 pr-3 text-muted-foreground" data-testid={`secret-target-${s.name}`}>
                    {targetLabel(s)}
                  </td>
                  <td className="py-1.5 pr-3 font-mono text-muted-foreground">
                    {s.value_sha256_prefix}
                  </td>
                  <td className="py-1.5 pr-3 tabular-nums">{s.value_length}</td>
                  <td className="py-1.5 pr-3 font-mono text-muted-foreground">
                    {s.value_char_class}
                  </td>
                  <td className="py-1.5 pr-3 text-muted-foreground">
                    {formatDate(s.created_at)}
                  </td>
                  <td className="py-1.5 pr-3">
                    {s.expires_at ? (
                      isExpired(s) ? (
                        <Badge variant="destructive" data-testid={`secret-expired-${s.name}`}>
                          {t("secrets.expired")}
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground">{formatDate(s.expires_at)}</span>
                      )
                    ) : null}
                  </td>
                  {canManage && (
                    <td className="py-1.5 whitespace-nowrap">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          setRotateValue("");
                          setRotateError(null);
                          setRotating(s);
                        }}
                        data-testid={`secret-rotate-${s.name}`}
                        aria-label={t("secrets.rotate.aria", { name: s.name })}
                      >
                        <RotateCw className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setDeleting(s)}
                        data-testid={`secret-delete-${s.name}`}
                        aria-label={t("secrets.remove.aria", { name: s.name })}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Replace = rotate. The dialog opens with an EMPTY field: there is no
          old value to prefill, and asking for one would be the only way this
          component could ever hold a stored plaintext. */}
      <Dialog open={!!rotating} onOpenChange={(open) => !open && setRotating(null)}>
        <DialogContent>
          <form onSubmit={handleRotate}>
            <DialogHeader>
              <DialogTitle>{t("secrets.rotate.title", { name: rotating?.name ?? "" })}</DialogTitle>
              <DialogDescription>
                {t("secrets.rotate.body")}
              </DialogDescription>
            </DialogHeader>
            <div className="py-3 space-y-2">
              <Input
                aria-label={t("secrets.rotate.valueLabel")}
                type="password"
                placeholder={t("secrets.rotate.placeholder")}
                value={rotateValue}
                onChange={(e) => setRotateValue(e.target.value)}
                autoComplete="new-password"
                spellCheck={false}
                data-testid="secret-rotate-input"
              />
              {rotateError && (
                <p className="text-xs text-destructive" data-testid="secret-rotate-error">
                  {rotateError}
                </p>
              )}
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setRotating(null)}>
                {t("secrets.cancel")}
              </Button>
              <Button type="submit" disabled={isRotating} data-testid="secret-rotate-submit">
                {isRotating ? t("secrets.rotate.replacing") : t("secrets.rotate.replace")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={!!deleting} onOpenChange={(open) => !open && setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("secrets.remove.title", { name: deleting?.name ?? "" })}</DialogTitle>
            <DialogDescription>
              {t("secrets.remove.body")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setDeleting(null)}>
              {t("secrets.cancel")}
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={isDeleting}
              data-testid="secret-delete-confirm"
            >
              {isDeleting ? t("secrets.remove.removing") : t("secrets.remove.remove")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
