import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { IconPicker } from "@/components/ui/icon-picker";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { apiErrorMessage } from "@/lib/api-error";
import {
  MAX_DASHBOARD_NAME,
  createDashboard,
  deleteDashboard,
  listDashboardTemplates,
  renameDashboard,
  resetDashboard,
  setDashboardIcon,
  setDashboardSharing,
  type Dashboard,
  type DashboardPlacement,
  type DashboardTemplate,
} from "@/lib/dashboards";

export type DashboardDialogState =
  /** `templateId`: preselected template (an old page's address, §5.9); its name fills the name field. */
  | { kind: "create"; group: DashboardPlacement; templateId?: string }
  | { kind: "rename" | "icon" | "share" | "delete" | "reset"; id: string }
  | null;

interface Props {
  state: DashboardDialogState;
  onClose: () => void;
  workspaceId: string;
  dashboards: readonly Dashboard[];
  /** Called after the server accepted a change: reload the list; `openId` navigates to a created dashboard. */
  onChanged: (openId?: string) => void;
}


export function DashboardDialogs({ state, onClose, workspaceId, dashboards, onChanged }: Props) {
  const { t } = useTranslation("shell");
  const target = state && state.kind !== "create" ? dashboards.find((d) => d.id === state.id) : undefined;
  const [name, setName] = useState("");
  const [icon, setIcon] = useState("");
  const [group, setGroup] = useState<DashboardPlacement>("bottom");
  const [shared, setShared] = useState(false);
  const [membersCanEdit, setMembersCanEdit] = useState(false);
  const [templateId, setTemplateId] = useState("");
  const [templates, setTemplates] = useState<DashboardTemplate[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const stateKey = state ? `${state.kind}:${"id" in state ? state.id : `${state.group}:${state.templateId ?? ""}`}` : "";
  useEffect(() => {
    setError(null);
    setBusy(false);
    if (!state) return;
    if (state.kind === "create") {
      const preset = state.templateId ?? "";
      setName(preset ? t(`dashboards.templates.${preset}`, { defaultValue: "" }) : "");
      setIcon("");
      setGroup(state.group);
      setShared(false);
      setTemplateId(preset);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stateKey]);
  useEffect(() => {
    if (!target) return;
    setName(target.name);
    setIcon(target.icon);
    setShared(target.shared);
    setMembersCanEdit(!!target.members_can_edit);
  }, [target?.id, state?.kind]); // eslint-disable-line react-hooks/exhaustive-deps

  const creating = state?.kind === "create";
  useEffect(() => {
    if (!creating || templates) return;
    let stale = false;
    listDashboardTemplates(workspaceId)
      .then((r) => stale || setTemplates(r))
      .catch(() => stale || setTemplates([]));
    return () => {
      stale = true;
    };
  }, [creating, templates, workspaceId]);
  // A preselected template the server does not offer falls back to «Empty» rather than a create that fails.
  useEffect(() => {
    if (templates && templateId && !templates.some((tpl) => tpl.id === templateId)) setTemplateId("");
  }, [templates, templateId]);

  if (!state) return null;

  const run = async (fn: () => Promise<Dashboard | void>, failed: string) => {
    setBusy(true);
    setError(null);
    try {
      const r = await fn();
      onClose();
      onChanged(state.kind === "create" && r ? (r as Dashboard).id : undefined);
    } catch (err) {
      setError(apiErrorMessage(err, failed));
      setBusy(false);
    }
  };
  const nameOk = name.trim().length > 0 && name.trim().length <= MAX_DASHBOARD_NAME;
  const asForm = state.kind !== "delete" && state.kind !== "reset";
  const footer = (label: string, onOk: () => void, ok: boolean, danger = false) => (
    <DialogFooter>
      <Button variant="outline" size="sm" type="button" onClick={onClose}>{t("actions.cancel")}</Button>
      <Button size="sm" type={asForm ? "submit" : "button"} variant={danger ? "destructive" : "default"} disabled={!ok || busy} onClick={asForm ? undefined : onOk}>{label}</Button>
    </DialogFooter>
  );
  const err = error && <p role="alert" className="text-sm text-destructive">{error}</p>;
  const form = (title: string, body: React.ReactNode, onSubmit: () => void) => (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>
        <form className="space-y-4 pt-2" onSubmit={(e) => { e.preventDefault(); onSubmit(); }}>{body}</form>
      </DialogContent>
    </Dialog>
  );
  const field = (label: string, control: React.ReactNode) => (
    <label className="block text-sm font-medium"><span className="mb-1.5 block">{label}</span>{control}</label>
  );
  const iconField = (
    field(t("dashboards.dialog.icon"), (
      <IconPicker value={icon} onChange={setIcon} aria-label={t("dashboards.dialog.icon")} />
    ))
  );

  if (state.kind === "create") {
    const submit = () => void run(() => createDashboard(workspaceId, { name: name.trim(), icon, placement: group, shared, template_id: templateId || undefined }), t("dashboards.dialog.createFailed"));
    return form(t("dashboards.dialog.createTitle"), (
      <>
        {field(t("dashboards.dialog.name"), <Input autoFocus value={name} maxLength={MAX_DASHBOARD_NAME} onChange={(e) => setName(e.target.value)} placeholder={t("dashboards.dialog.namePlaceholder")} />)}
        {iconField}
        {field(t("dashboards.dialog.group"), (
          <Select value={group} onChange={(e) => setGroup(e.target.value as DashboardPlacement)}>
            <option value="top">{t("dashboards.dialog.groupTop")}</option>
            <option value="bottom">{t("dashboards.dialog.groupBottom")}</option>
          </Select>
        ))}
        <fieldset className="space-y-1.5 text-sm">
          <legend className="font-medium">{t("dashboards.dialog.visibility")}</legend>
          <label className="flex items-center gap-2"><input className="accent-primary" type="radio" name="dash-vis" checked={!shared} onChange={() => setShared(false)} />{t("dashboards.dialog.personal")}</label>
          <label className="flex items-center gap-2"><input className="accent-primary" type="radio" name="dash-vis" checked={shared} onChange={() => setShared(true)} />{t("dashboards.dialog.shared")}</label>
        </fieldset>
        {field(t("dashboards.dialog.template"), (
          <Select value={templateId} onChange={(e) => setTemplateId(e.target.value)} disabled={templates === null}>
            <option value="">{t("dashboards.dialog.templateEmpty")}</option>
            {(templates ?? []).map((tpl) => <option key={tpl.id} value={tpl.id}>{t(`dashboards.templates.${tpl.id}`, { defaultValue: tpl.id })}</option>)}
          </Select>
        ))}
        {err}
        {footer(busy ? t("actions.creating") : t("actions.create"), submit, nameOk)}
      </>
    ), submit);
  }

  if (!target) return null;

  if (state.kind === "rename") {
    const submit = () => void run(() => renameDashboard(workspaceId, target.id, name.trim()), t("dashboards.dialog.saveFailed"));
    return form(t("dashboards.menu.rename"), (
      <>
        {field(t("dashboards.dialog.name"), <Input autoFocus value={name} maxLength={MAX_DASHBOARD_NAME} onChange={(e) => setName(e.target.value)} />)}
        {err}{footer(t("dashboards.dialog.save"), submit, nameOk && name.trim() !== target.name)}
      </>
    ), submit);
  }
  if (state.kind === "icon") {
    const submit = () => void run(() => setDashboardIcon(workspaceId, target.id, icon), t("dashboards.dialog.saveFailed"));
    return form(t("dashboards.menu.changeIcon"), (<>{iconField}{err}{footer(t("dashboards.dialog.save"), submit, icon !== target.icon)}</>), submit);
  }
  if (state.kind === "share") {
    const submit = () => void run(() => setDashboardSharing(workspaceId, target.id, { shared, members_can_edit: shared ? membersCanEdit : false }), t("dashboards.dialog.saveFailed"));
    return form(t("dashboards.menu.sharing"), (
      <>
        <label className="flex items-center gap-2 text-sm"><input className="accent-primary" type="checkbox" checked={shared} onChange={(e) => setShared(e.target.checked)} />{t("dashboards.dialog.shareWithWorkspace")}</label>
        <label className="flex items-center gap-2 text-sm"><input className="accent-primary" type="checkbox" checked={membersCanEdit} disabled={!shared} onChange={(e) => setMembersCanEdit(e.target.checked)} />{t("dashboards.dialog.membersCanEdit")}</label>
        {err}{footer(t("dashboards.dialog.save"), submit, shared !== target.shared || (shared && membersCanEdit !== !!target.members_can_edit))}
      </>
    ), submit);
  }
  const isDelete = state.kind === "delete";
  const submit = () =>
    void run(
      async () => {
        if (isDelete) await deleteDashboard(workspaceId, target.id);
        else await resetDashboard(workspaceId, target.id);
      },
      isDelete ? t("dashboards.dialog.deleteFailed") : t("dashboards.dialog.saveFailed"),
    );
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{isDelete ? t("dashboards.dialog.deleteTitle", { name: target.name }) : t("dashboards.menu.reset")}</DialogTitle>
          <DialogDescription>
            {isDelete ? (target.shared ? t("dashboards.dialog.deleteSharedHint") : t("dashboards.dialog.deleteHint")) : t("dashboards.dialog.resetHint")}
          </DialogDescription>
        </DialogHeader>
        {err}
        {footer(isDelete ? t("dashboards.menu.delete") : t("dashboards.menu.reset"), submit, true, isDelete)}
      </DialogContent>
    </Dialog>
  );
}
