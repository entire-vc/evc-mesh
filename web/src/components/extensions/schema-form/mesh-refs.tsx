import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Select } from "@/components/ui/select";
import { formatDate } from "@/i18n/format";
import { api, ApiRequestError } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import type { AgentWorkspaceGrantWithAgent, AgentWorkspaceGrantWithWorkspace, Workspace, WorkspaceRole } from "@/types";

/** One access grant as a picker shows it: where it lets the agent in and as whom. */
export interface GrantOption {
  id: string;
  workspaceId: string;
  workspaceName: string;
  role: WorkspaceRole;
  since: string;
}

/**
 * Active grants of one agent: all its workspaces when the caller may see them,
 * otherwise (403 on someone else's agent) its grants into the current workspace.
 */
async function fetchGrants(agentId: string, current: Workspace | null): Promise<GrantOption[]> {
  try {
    const resp = await api<{ workspaces?: AgentWorkspaceGrantWithWorkspace[] }>(`/api/v1/agents/${agentId}/workspaces`);
    return (resp?.workspaces ?? []).map((g) => ({ id: g.id, workspaceId: g.workspace_id, workspaceName: g.workspace?.name ?? "", role: g.role, since: g.created_at }));
  } catch (err) {
    // only "not yours to see" falls back; any other failure is a failed load, not "no access"
    if (!(err instanceof ApiRequestError && err.status === 403) || !current) throw err;
    const resp = await api<{ agent_grants?: AgentWorkspaceGrantWithAgent[] }>(`/api/v1/workspaces/${current.id}/agent-grants`);
    return (resp?.agent_grants ?? [])
      .filter((g) => g.agent_id === agentId)
      .map((g) => ({ id: g.id, workspaceId: g.workspace_id, workspaceName: current.name, role: g.role, since: g.created_at }));
  }
}

// the grant and workspace pickers of one form ask for the same agent at once: one request serves both
const inflight = new Map<string, Promise<GrantOption[]>>();

export function useAgentGrants(agentId: string | null): { grants: GrantOption[] | null; failed: boolean } {
  const current = useWorkspaceStore((s) => s.currentWorkspace);
  const [load, setLoad] = useState<{ agentId: string; grants: GrantOption[] | null }>();
  useEffect(() => {
    if (!agentId) return;
    let live = true;
    const key = agentId + (current?.id ?? "");
    let p = inflight.get(key);
    if (!p) inflight.set(key, (p = fetchGrants(agentId, current).finally(() => inflight.delete(key))));
    p.then(
      (grants) => live && setLoad({ agentId, grants }),
      () => live && setLoad({ agentId, grants: null }),
    );
    return () => {
      live = false;
    };
  }, [agentId, current]);
  // a load for the previous agent is not this agent's answer
  return load && load.agentId === agentId ? { grants: load.grants, failed: !load.grants } : { grants: null, failed: false };
}

interface PickerProps {
  id: string;
  disabled: boolean;
  invalid?: boolean;
  describedBy?: string;
  value: unknown;
}

/**
 * An access grant of the agent named by a sibling field, shown as "workspace · role · since".
 * The only grant is picked by itself; the picked grant's workspace goes to `onWorkspace`.
 */
export function GrantWidget({ id, disabled, invalid, describedBy, value, agentId, onChange, onWorkspace }: PickerProps & {
  agentId: string | null; onChange: (v: string | undefined) => void; onWorkspace?: (workspaceId: string | undefined) => void;
}) {
  const { t } = useTranslation("integrations");
  const { grants, failed } = useAgentGrants(agentId);
  const current = typeof value === "string" ? value : "";
  const only = grants?.length === 1 ? grants[0] : null;

  useEffect(() => {
    if (only && current === "") {
      onChange(only.id);
      onWorkspace?.(only.workspaceId);
    }
    // onChange/onWorkspace are new closures each render; the pick depends on the data only
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [only, current]);

  if (!agentId) return <p id={id} className="text-sm text-muted-foreground">{t("schemaForm.grant.pickAgent")}</p>;
  if (failed) return <p id={id} role="alert" className="text-sm text-destructive">{t("schemaForm.grant.loadFailed")}</p>;
  if (grants === null) return <p id={id} className="text-sm text-muted-foreground">{t("schemaForm.grant.loading")}</p>;
  if (grants.length === 0 && current === "") return <p id={id} className="text-sm text-muted-foreground">{t("schemaForm.grant.none")}</p>;

  const label = (g: GrantOption) =>
    [g.workspaceName, t(`schemaForm.grant.role.${g.role}`, { defaultValue: g.role }), g.since && t("schemaForm.grant.since", { date: formatDate(g.since) })].filter(Boolean).join(" · ");
  const known = grants.some((g) => g.id === current);
  return (
    <Select
      id={id}
      disabled={disabled}
      aria-invalid={invalid || undefined}
      aria-describedby={describedBy}
      value={current}
      onChange={(e) => {
        const g = grants.find((x) => x.id === e.target.value);
        onChange(g?.id);
        onWorkspace?.(g?.workspaceId);
      }}
    >
      <option value="">{t("schemaForm.selectPlaceholder")}</option>
      {grants.map((g) => (
        <option key={g.id} value={g.id}>
          {label(g)}
        </option>
      ))}
      {current !== "" && !known && <option value={current}>{t("schemaForm.missingEntity")}</option>}
    </Select>
  );
}

/**
 * A workspace reference that is never typed: it shows the name, starts as the current workspace
 * and follows the grant picked next to it.
 */
export function WorkspaceWidget({ id, describedBy, value, agentId = null, onChange }: PickerProps & { agentId?: string | null; onChange: (v: string) => void }) {
  const { t } = useTranslation("integrations");
  const current = useWorkspaceStore((s) => s.currentWorkspace);
  const workspaces = useWorkspaceStore((s) => s.workspaces);
  // the agent's access knows workspaces the user is not a member of
  const { grants, failed } = useAgentGrants(agentId);
  const stored = typeof value === "string" ? value : "";

  useEffect(() => {
    if (stored === "" && current) onChange(current.id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stored, current]);

  const name = [current, ...workspaces].find((w) => w?.id === stored)?.name ?? grants?.find((g) => g.workspaceId === stored)?.workspaceName;
  return (
    <p id={id} aria-describedby={describedBy} className="text-sm" data-testid="schema-form-workspace">
      {name || (
        <span className="text-muted-foreground">
          {/* "not found" only once the agent's access has answered: until then it may still name it */}
          {stored !== "" && (!agentId || grants || failed) ? t("schemaForm.missingEntity") : t("schemaForm.selectPlaceholder")}
        </span>
      )}
    </p>
  );
}
