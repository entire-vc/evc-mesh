import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams } from "react-router";
import { ArrowLeft, Lock } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { InstalledThemes } from "@/components/themes/installed-themes";
import { ThemeSettingsPanel } from "@/components/themes/theme-settings-panel";

/** `w/:ws/instance/themes` — default theme of the instance and the installed set. Instance administrator only. */
export function InstanceThemesPage() {
  const { t } = useTranslation("settings");
  const { wsSlug } = useParams();
  const [forbidden, setForbidden] = useState(false);
  // Installing or removing a theme changes what the default-theme cards offer.
  const [rev, setRev] = useState(0);

  return (
    <div className="mx-auto max-w-5xl space-y-6 p-4 sm:p-6">
      <div>
        <Link to={`/w/${wsSlug}/settings`} className="mb-2 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-4 w-4" aria-hidden />
          {t("themes.instance.back")}
        </Link>
        <h1 className="text-2xl font-semibold">{t("themes.instance.title")}</h1>
      </div>
      {forbidden ? (
        <p role="alert" className="flex items-center gap-2 rounded-lg border p-4 text-sm text-muted-foreground">
          <Lock className="h-4 w-4" aria-hidden />
          {t("themes.instance.forbidden")}
        </p>
      ) : (
        <>
          <Card>
            <CardHeader>
              <CardTitle>{t("themes.instance.defaultTitle")}</CardTitle>
              <CardDescription>{t("themes.instance.defaultHint")}</CardDescription>
            </CardHeader>
            <CardContent>
              <ThemeSettingsPanel key={rev} level="instance" onForbidden={() => setForbidden(true)} />
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t("themes.instance.installedTitle")}</CardTitle>
              <CardDescription>{t("themes.instance.installedHint")}</CardDescription>
            </CardHeader>
            <CardContent>
              <InstalledThemes onChanged={() => setRev((r) => r + 1)} />
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}
