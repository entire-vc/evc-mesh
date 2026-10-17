import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { toast } from "@/components/ui/toast";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { apiErrorMessage } from "@/lib/api-error";
import { capabilityText, describeCapabilities } from "@/lib/extension-capabilities";
import {
  installRepoExtension, listRepoRefs, previewRepoExtension, shortSha,
  type RepoPreview, type RepoRefs,
} from "@/lib/extension-repo";

type RefKind = "tag" | "branch";

export function RepoInstallDialog({ onClose, onInstalled }: { onClose: () => void; onInstalled: () => void }) {
  const { t } = useTranslation("integrations");
  const [url, setUrl] = useState("");
  const [isPrivate, setIsPrivate] = useState(false);
  // Uncontrolled on purpose: a controlled password input mirrors its value into
  // the DOM `value` attribute, which would put the token into any DOM snapshot.
  const tokenRef = useRef<HTMLInputElement>(null);
  const [hasToken, setHasToken] = useState(false);
  const [refs, setRefs] = useState<RepoRefs | null>(null);
  const [kind, setKind] = useState<RefKind>("tag");
  const [ref, setRef] = useState("");
  const [preview, setPreview] = useState<RepoPreview | null>(null);
  const [busy, setBusy] = useState<"refs" | "preview" | "install" | null>(null);
  const [error, setError] = useState("");

  const body = () => ({ repo_url: url.trim(), ref: ref.trim(), ...(isPrivate && tokenRef.current?.value ? { auth_token: tokenRef.current.value } : {}) });
  const reset = () => {
    setPreview(null);
    setError("");
  };

  const loadRefs = async () => {
    setBusy("refs");
    reset();
    try {
      const r = await listRepoRefs(body());
      setRefs(r);
      const nextKind: RefKind = r.tags.length > 0 ? "tag" : "branch";
      setKind(nextKind);
      setRef((nextKind === "tag" ? r.tags : r.branches)[0]?.name ?? "");
    } catch (err) {
      setRefs(null);
      setError(apiErrorMessage(err, t("repoInstall.refsFailed")));
    } finally {
      setBusy(null);
    }
  };

  const readManifest = async () => {
    setBusy("preview");
    setError("");
    try {
      setPreview(await previewRepoExtension(body()));
    } catch (err) {
      setPreview(null);
      setError(apiErrorMessage(err, t("repoInstall.previewFailed")));
    } finally {
      setBusy(null);
    }
  };

  const install = async () => {
    setBusy("install");
    setError("");
    try {
      await installRepoExtension(body());
      toast.success(t("repoInstall.installed", { name: preview?.name ?? "" }));
      onInstalled();
      onClose();
    } catch (err) {
      setError(apiErrorMessage(err, t("repoInstall.installFailed")));
    } finally {
      setBusy(null);
    }
  };

  const list = kind === "tag" ? (refs?.tags ?? []) : (refs?.branches ?? []);
  const lines = preview ? describeCapabilities(preview.manifest?.capabilities) : [];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} className="max-w-xl">
      <DialogContent onClose={onClose} className="max-h-[90vh] overflow-y-auto" role="dialog" aria-label={t("repoInstall.title")}>
        <DialogHeader>
          <DialogTitle>{t("repoInstall.title")}</DialogTitle>
          <DialogDescription>{t("repoInstall.hint")}</DialogDescription>
        </DialogHeader>

        <div className="mt-4 space-y-4">
          <div className="space-y-1">
            <label htmlFor="repo-url" className="text-sm font-medium">{t("repoInstall.url")}</label>
            <Input
              id="repo-url"
              value={url}
              inputMode="url"
              autoComplete="off"
              placeholder="https://github.com/owner/repo"
              onChange={(e) => { setUrl(e.target.value); setRefs(null); reset(); }}
            />
          </div>

          <label className="inline-flex items-center gap-2 text-sm">
            <input type="checkbox" className="h-4 w-4 accent-primary" checked={isPrivate} onChange={(e) => { setIsPrivate(e.target.checked); setRefs(null); reset(); }} />
            {t("repoInstall.private")}
          </label>

          {isPrivate && (
            <div className="space-y-1">
              <label htmlFor="repo-token" className="text-sm font-medium">{t("repoInstall.token")}</label>
              <Input
                id="repo-token"
                type="password"
                autoComplete="off"
                spellCheck={false}
                ref={tokenRef}
                onChange={(e) => { setHasToken(e.target.value !== ""); setRefs(null); reset(); }}
              />
              <p className="text-xs text-muted-foreground">{t("repoInstall.tokenHint")}</p>
            </div>
          )}

          <Button variant="outline" size="sm" disabled={!url.trim() || (isPrivate && !hasToken) || busy !== null} onClick={() => void loadRefs()}>
            {busy === "refs" ? t("repoInstall.loadingRefs") : t("repoInstall.loadRefs")}
          </Button>

          {refs && (
            <div className="space-y-3">
              <fieldset className="flex flex-wrap items-center gap-4">
                <legend className="sr-only">{t("repoInstall.refKind")}</legend>
                {(["tag", "branch"] as const).map((k) => (
                  <label key={k} className="inline-flex items-center gap-2 text-sm">
                    <input
                      type="radio"
                      name="ref-kind"
                      className="h-4 w-4 accent-primary"
                      checked={kind === k}
                      onChange={() => { setKind(k); setRef((k === "tag" ? refs.tags : refs.branches)[0]?.name ?? ""); reset(); }}
                    />
                    {t(`repoInstall.kind.${k}`)}
                  </label>
                ))}
              </fieldset>
              {list.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t(`repoInstall.noRefs.${kind}`)}</p>
              ) : (
                <div className="space-y-1">
                  <label htmlFor="repo-ref" className="text-sm font-medium">{t(`repoInstall.pick.${kind}`)}</label>
                  <Select id="repo-ref" value={ref} onChange={(e) => { setRef(e.target.value); reset(); }}>
                    {list.map((r) => (
                      <option key={r.name} value={r.name}>{r.name}</option>
                    ))}
                  </Select>
                  {kind === "branch" && <p className="text-xs text-muted-foreground">{t("repoInstall.betaHint")}</p>}
                </div>
              )}
              <Button size="sm" disabled={!ref || busy !== null} onClick={() => void readManifest()}>
                {busy === "preview" ? t("repoInstall.reading") : t("repoInstall.readManifest")}
              </Button>
            </div>
          )}

          {error && (
            <p role="alert" data-testid="repo-install-error" className="flex items-start gap-2 text-sm text-destructive">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
              {error}
            </p>
          )}

          {preview && (
            <section data-testid="repo-preview" aria-label={t("repoInstall.manifest")} className="space-y-3 rounded-lg border border-border p-3">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-sm font-semibold">{preview.name}</h3>
                <Badge variant="outline">{preview.version}</Badge>
                <Badge variant="warning">{t("catalog.trust.custom")}</Badge>
                <Badge variant="success">{t("repoInstall.compatible", { range: preview.mesh_api })}</Badge>
              </div>
              <p className="text-sm text-muted-foreground">{preview.description}</p>
              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <dt>{t("repoInstall.publisher")}</dt><dd className="min-w-0 break-words">{preview.publisher}</dd>
                <dt>{t("repoInstall.commit")}</dt><dd className="font-mono">{shortSha(preview.commit_sha)}</dd>
              </dl>
              <div>
                <p className="mb-1 text-sm font-medium">{t("repoInstall.permissions")}</p>
                {lines.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("repoInstall.noPermissions")}</p>
                ) : (
                  <ul className="list-disc space-y-0.5 pl-5 text-sm">
                    {lines.map((l) => <li key={l.raw}>{capabilityText(t, l)}</li>)}
                  </ul>
                )}
              </div>
              <p role="note" className="flex items-start gap-2 rounded-md border border-border bg-muted p-2 text-sm">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
                {t("repoInstall.customWarning")}
              </p>
            </section>
          )}
        </div>

        <DialogFooter className="mt-4">
          <Button variant="outline" onClick={onClose}>{t("repoInstall.cancel")}</Button>
          <Button disabled={!preview || busy !== null} onClick={() => void install()}>
            {busy === "install" ? t("repoInstall.installing") : t("repoInstall.install")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
