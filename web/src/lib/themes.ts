import { api } from "@/lib/api";
import type { FieldSchema, Values } from "@/components/extensions/schema-form";

/** Theme API (docs: themes backend T7). Admin routes answer 403 to anyone else; the pages show it as "no access". */

export interface ContrastWarning {
  pair?: string;
  mode?: string;
  ratio?: number;
  message?: string;
  [k: string]: unknown;
}

export interface ThemeInfo {
  id: string;
  name: string;
  version: string;
  publisher: string;
  description: string;
  source: string;
  trust_level: string;
  modes: string[];
  has_css: boolean;
  has_views: boolean;
  settings: { instance?: FieldSchema; workspace?: FieldSchema };
  tokens: { light?: Record<string, string>; dark?: Record<string, string> };
  contrast_warnings: ContrastWarning[];
}

export interface ThemeList {
  default: string;
  themes: ThemeInfo[];
  builtin_available: string[];
}

export interface ThemeSelection {
  theme_id: string | null;
  settings: Values;
  version: number;
  contrast_warnings: ContrastWarning[];
}

export interface ThemeInstallResult {
  extension_id: string;
  version: string;
  contrast_warnings: ContrastWarning[];
}

export const listThemes = () => api<ThemeList>("/api/v1/themes");
export const getWorkspaceTheme = (wsId: string) => api<ThemeSelection>(`/api/v1/workspaces/${wsId}/theme`);
export const putWorkspaceTheme = (wsId: string, themeId: string | null, settings: Values) =>
  api<ThemeSelection>(`/api/v1/workspaces/${wsId}/theme`, { method: "PUT", body: { theme_id: themeId, settings } });
export const getInstanceTheme = () => api<ThemeSelection>("/api/v1/instance/theme");
export const putInstanceTheme = (themeId: string | null, settings: Values) =>
  api<ThemeSelection>("/api/v1/instance/theme", { method: "PUT", body: { theme_id: themeId, settings } });
/** What the instance paints now (public); its tokens_css is the preview base of "as the instance". */
export const getResolvedInstanceTheme = () => api<{ theme_id: string; tokens_css: string }>("/api/v1/theme");
/** What a workspace paints now; readable by any member, unlike the admin-only selection. */
export const getResolvedWorkspaceTheme = (slug: string) =>
  api<{ theme_id: string }>(`/api/v1/theme?workspace=${encodeURIComponent(slug)}`);

export const installBuiltinTheme = (id: string) =>
  api<ThemeInstallResult>("/api/v1/instance/themes", { method: "POST", body: { source: "builtin", id } });
export const installRepoTheme = (b: { repo_url: string; ref: string; auth_token?: string }) =>
  api<ThemeInstallResult>("/api/v1/instance/themes", { method: "POST", body: { source: "repo", ...b } });
/** `POST /extensions/:id/update` — pulls the newer version of a repo-installed theme. */
export const updateTheme = (id: string) => api<unknown>(`/api/v1/extensions/${id}/update`, { method: "POST" });
/** `DELETE /instance/themes/:id` — theme backend follow-up; an instance without it answers 404/405. */
export const deleteTheme = (id: string) => api<unknown>(`/api/v1/instance/themes/${id}`, { method: "DELETE" });
