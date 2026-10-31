import { useCallback } from "react";
import { useLocation, useNavigate, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { ExternalFrame } from "@/components/extensions/external-frame";
import { normalizeSection } from "@/lib/extension-frame-protocol";
import { useProjectScreens, useSidebarItems } from "@/lib/extension-slots";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";

/**
 * Full-area iframe screen of an external extension (W3.3), at
 * /w/:wsSlug/x/:extId/* (`sidebar.item`) and /w/:wsSlug/p/:projectSlug/x/:extId/*
 * (`project.view`, else the extension's per-project `sidebar.item`). What follows
 * the extension id, plus the hash, is the section of the screen: the frame opens
 * there and reports where it went with `mesh.state`, so a link to a section
 * survives a reload. An extension that is off, or declares no such screen, gets
 * the same "unavailable" answer as one whose service is down.
 */
export function ExtensionScreenPage() {
  const { extId = "", projectSlug } = useParams<{ extId: string; projectSlug?: string }>();
  const { t } = useTranslation("task");
  const navigate = useNavigate();
  const { pathname, hash } = useLocation();
  // The project of the address, from the loaded list of THIS workspace: after a workspace switch the
  // store still holds the previous list until the new request resolves (an unknown slug is
  // "unavailable", not a spinner).
  const workspaceId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const projectId = useProjectStore((s) =>
    projectSlug
      ? s.projects.find((p) => p.slug === projectSlug && p.workspace_id === workspaceId)?.id
      : undefined,
  );
  const projectsLoaded = useProjectStore(
    (s) =>
      s.loaded &&
      !s.isLoading &&
      (s.projects.length === 0 || s.projects.some((p) => p.workspace_id === workspaceId)),
  );
  const projectScreen = useProjectScreens().find((i) => i.extensionId === extId);
  const sidebarItem = useSidebarItems().find((i) => i.extensionId === extId);

  // The screen's own address is everything up to the extension id; the rest is the section.
  const marker = `/x/${extId}`;
  const at = pathname.indexOf(marker);
  const base = at < 0 ? pathname : pathname.slice(0, at + marker.length);
  const section = normalizeSection((at < 0 ? "" : pathname.slice(at + marker.length)) + hash) ?? "";
  const onSection = useCallback(
    (next: string, replace: boolean) => navigate(base + next, { replace }),
    [navigate, base],
  );

  // On a project address the frame always carries the project, whichever slot serves it.
  const inProject = Boolean(projectSlug);
  const item = inProject ? (projectScreen ?? sidebarItem) : sidebarItem;
  const slot = inProject && projectScreen ? "project.view" : "sidebar.item";
  if (!item || (inProject && projectsLoaded && !projectId)) {
    return (
      <div className="p-4 text-sm text-muted-foreground" role="status" data-testid="extension-screen-unavailable">
        {t("slots.unavailable", { name: extId })}
      </div>
    );
  }
  // A project tab names its project in the token; wait for the project before asking for one.
  if (inProject && !projectId) {
    return (
      <div className="flex h-full min-h-[60vh] flex-col" aria-busy="true" data-testid="extension-screen">
        <div className="space-y-2 p-3">
          <div className="h-4 w-32 animate-pulse rounded bg-muted" />
          <div className="h-10 w-full animate-pulse rounded bg-muted" />
        </div>
      </div>
    );
  }
  return (
    <div className="flex h-full min-h-[60vh] flex-col" data-testid="extension-screen">
      <h1 className="sr-only">{item.title}</h1>
      <ExternalFrame
        key={`${item.extensionId}:${slot}:${inProject ? projectId : ""}`}
        extensionId={item.extensionId}
        name={item.title}
        slot={slot}
        projectId={inProject ? projectId : undefined}
        layout="screen"
        section={section}
        onSection={onSection}
      />
    </div>
  );
}
