import { Trans, useTranslation } from "react-i18next";
import { Server } from "lucide-react";
import { useWorkspaceStore } from "@/stores/workspace";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

// MCP is a reference card, not an extension: a static endpoint and a
// .mcp.json snippet. No enable/disable state, nothing stored per workspace.
export function McpReference() {
  const { t } = useTranslation("integrations");
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);

  return (
    <Card className="max-w-2xl">
      <CardHeader className="pb-3">
        <div className="flex items-center gap-3">
          <div className="flex h-10 w-10 items-center justify-center rounded-lg border border-border bg-muted">
            <Server className="h-5 w-5" />
          </div>
          <div>
            <CardTitle className="text-base">{t("integrationsPage.providers.mcpName")}</CardTitle>
            <p className="text-sm text-muted-foreground">{t("integrationsPage.providers.mcp")}</p>
          </div>
        </div>
      </CardHeader>
      <CardContent className="pt-0">
        <div className="space-y-3">
          <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
            <p className="text-xs font-medium text-muted-foreground mb-1">
              {t("integrationsPage.mcp.sseEndpoint")}
            </p>
            <code className="block rounded bg-background px-2 py-1.5 text-xs font-mono select-all">
              {window.location.origin}/mcp/sse
            </code>
            <p className="text-xs text-muted-foreground mt-1.5">
              <Trans
                t={t}
                i18nKey="integrationsPage.mcp.sseNote"
                components={{ code: <code className="rounded bg-muted px-1" /> }}
              />
            </p>
          </div>

          <div className="rounded-lg border border-dashed border-border bg-muted/30 p-3">
            <p className="text-xs font-medium text-muted-foreground mb-1">
              Claude Code / Cline — .mcp.json
            </p>
            <pre className="rounded bg-background px-2 py-1.5 text-xs font-mono overflow-x-auto select-all whitespace-pre">{`{
  "mcpServers": {
    "evc-mesh": {
      "command": "mesh-mcp",
      "env": {
        "MESH_API_URL": "${window.location.origin}",
        "MESH_AGENT_KEY": "agk_<your_agent_key>"
      }
    }
  }
}`}</pre>
          </div>

          <p className="text-xs text-muted-foreground">
            <Trans
              t={t}
              i18nKey="integrationsPage.mcp.registerHelp"
              components={{
                link: (
                  <a
                    href={`/w/${currentWorkspace?.slug}/agents`}
                    className="text-primary hover:underline font-medium"
                  />
                ),
                code: <code className="rounded bg-muted px-1" />,
              }}
            />
          </p>
        </div>
      </CardContent>
    </Card>
  );
}
