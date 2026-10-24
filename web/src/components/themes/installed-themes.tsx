import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Download, Plus, RefreshCw, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import {
  deleteTheme,
  installBuiltinTheme,
  installRepoTheme,
  listThemes,
  updateTheme,
  type ThemeList,
  type ThemeInstallResult,
} from "@/lib/themes";
import { ThemeCard } from "./theme-card";
import { ContrastNotice } from "./theme-settings-panel";

/** Installed themes of the instance: install (built-in or from a repository), update, delete. */
export function InstalledThemes({ onChanged }: { onChanged?: () => void }) {
  const { t } = useTranslation("settings");
  const [list, setList] = useState<ThemeList | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [installOpen, setInstallOpen] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [repo, setRepo] = useState({ url: "", ref: "", token: "" });
  const [installError, setInstallError] = useState<string | null>(null);
  const [warn, setWarn] = useState<ThemeInstallResult["contrast_warnings"]>([]);

  const load = useCallback(async () => {
    setError(null);
    try {
      setList(await listThemes());
    } catch (e) {
      setError(apiErrorMessage(e, t("themes.loadFailed")));
    }
  }, [t]);
  useEffect(() => {
    void load();
  }, [load]);

  const run = async (key: string, fn: () => Promise<unknown>, ok: string, fail: string) => {
    setBusy(key);
    try {
      await fn();
      toast.success(ok);
      await load();
      onChanged?.();
      return true;
    } catch (e) {
      toast.error(apiErrorMessage(e, fail));
      return false;
    } finally {
      setBusy(null);
    }
  };

  const installBuiltin = (id: string) =>
    run(`install:${id}`, async () => setWarn((await installBuiltinTheme(id)).contrast_warnings ?? []), t("themes.installed.installDone"), t("themes.installed.installFailed"));

  const submitRepo = async (e: FormEvent) => {
    e.preventDefault();
    setInstallError(null);
    setBusy("install:repo");
    try {
      const res = await installRepoTheme({ repo_url: repo.url.trim(), ref: repo.ref.trim(), ...(repo.token ? { auth_token: repo.token } : {}) });
      setWarn(res.contrast_warnings ?? []);
      setInstallOpen(false);
      setRepo({ url: "", ref: "", token: "" });
      toast.success(t("themes.installed.installDone"));
      await load();
      onChanged?.();
    } catch (err) {
      setInstallError(apiErrorMessage(err, t("themes.installed.installFailed")));
    } finally {
      setBusy(null);
    }
  };

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
  if (!list) return <Skeleton className="h-60 rounded-xl" aria-busy="true" aria-label={t("themes.loading")} />;

  // mesh-default is the app's own look: it is chosen and tuned in the picker, never installed or removed here.
  const shown = list.themes.filter((th) => th.id !== "mesh-default");

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" onClick={() => setInstallOpen(true)}>
          <Plus className="mr-2 h-4 w-4" aria-hidden />
          {t("themes.installed.installRepo")}
        </Button>
        {list.builtin_available.map((id) => (
          <Button key={id} type="button" size="sm" variant="outline" disabled={busy === `install:${id}`} onClick={() => void installBuiltin(id)}>
            <Download className="mr-2 h-4 w-4" aria-hidden />
            {t("themes.installed.installBuiltin", { name: id })}
          </Button>
        ))}
      </div>
      <ContrastNotice warnings={warn} />
      {shown.length === 0 ? (
        <div className="rounded-lg border border-dashed p-6 text-sm text-muted-foreground">
          <p className="font-medium text-foreground">{t("themes.installed.emptyTitle")}</p>
          <p>{t("themes.installed.emptyHint")}</p>
        </div>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((th) => (
            <ThemeCard
              key={th.id}
              name={th.name}
              description={th.description}
              version={th.version}
              tokens={th.tokens}
              badge={t(`themes.installed.source.${th.source === "repo" ? "repo" : "builtin"}`)}
              actions={
                <>
                  {th.source === "repo" && (
                    <Button type="button" size="sm" variant="outline" disabled={busy === `update:${th.id}`} onClick={() => void run(`update:${th.id}`, () => updateTheme(th.id), t("themes.installed.updateDone", { name: th.name }), t("themes.installed.updateFailed"))}>
                      <RefreshCw className="mr-2 h-4 w-4" aria-hidden />
                      {t("themes.installed.update")}
                    </Button>
                  )}
                  <Button type="button" size="sm" variant="outline" aria-label={t("themes.installed.removeNamed", { name: th.name })} onClick={() => setRemoving(th.id)}>
                    <Trash2 className="mr-2 h-4 w-4" aria-hidden />
                    {t("themes.installed.remove")}
                  </Button>
                </>
              }
            />
          ))}
        </div>
      )}

      <Dialog open={installOpen} onOpenChange={setInstallOpen}>
        <DialogContent>
          <form onSubmit={submitRepo} className="space-y-4">
            <DialogHeader>
              <DialogTitle>{t("themes.installed.repoTitle")}</DialogTitle>
              <DialogDescription>{t("themes.installed.repoHint")}</DialogDescription>
            </DialogHeader>
            <div className="space-y-1.5">
              <label htmlFor="theme-repo-url" className="text-sm font-medium">{t("themes.installed.repoUrl")}</label>
              <Input id="theme-repo-url" type="url" required placeholder="https://git.example.com/team/theme" value={repo.url} onChange={(e) => setRepo({ ...repo, url: e.target.value })} />
            </div>
            <div className="space-y-1.5">
              <label htmlFor="theme-repo-ref" className="text-sm font-medium">{t("themes.installed.repoRef")}</label>
              <Input id="theme-repo-ref" required placeholder="v1.0.0" value={repo.ref} onChange={(e) => setRepo({ ...repo, ref: e.target.value })} />
            </div>
            <div className="space-y-1.5">
              <label htmlFor="theme-repo-token" className="text-sm font-medium">{t("themes.installed.repoToken")}</label>
              <Input id="theme-repo-token" type="password" autoComplete="new-password" value={repo.token} onChange={(e) => setRepo({ ...repo, token: e.target.value })} />
              <p className="text-xs text-muted-foreground">{t("themes.installed.repoTokenHint")}</p>
            </div>
            {installError && <p role="alert" className="text-sm text-destructive">{installError}</p>}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setInstallOpen(false)}>{t("themes.cancel")}</Button>
              <Button type="submit" disabled={busy === "install:repo"}>{t("themes.installed.install")}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        title={t("themes.installed.removeTitle", { name: list.themes.find((x) => x.id === removing)?.name ?? removing })}
        description={t("themes.installed.removeHint")}
        confirmText={t("themes.installed.remove")}
        variant="destructive"
        isLoading={busy === `remove:${removing}`}
        onConfirm={() => {
          const id = removing as string;
          setRemoving(null);
          void run(`remove:${id}`, () => deleteTheme(id), t("themes.installed.removeDone"), t("themes.installed.removeFailed"));
        }}
      />
    </div>
  );
}
