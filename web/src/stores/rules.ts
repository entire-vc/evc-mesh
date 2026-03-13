import { create } from "zustand";
import { api, getAccessToken } from "@/lib/api";
import type {
  AssignmentRulesConfig,
  EffectiveAssignmentRules,
  ImportResult,
  OrgChartData,
  RuleViolation,
  TeamDirectory,
  TeamImportResult,
  WorkflowRulesConfig,
  WorkflowRulesResponse,
} from "@/types";

const BASE_URL = import.meta.env.VITE_API_URL || "";

interface RulesState {
  // Team Directory
  teamDirectory: TeamDirectory | null;
  isTeamLoading: boolean;
  fetchTeamDirectory: (workspaceId: string) => Promise<void>;

  // Org Chart (tree format)
  orgChart: OrgChartData | null;
  isOrgChartLoading: boolean;
  fetchOrgChart: (workspaceId: string) => Promise<void>;

  // Assignment Rules (workspace level)
  wsAssignmentRules: AssignmentRulesConfig | null;
  isWsRulesLoading: boolean;
  fetchWsAssignmentRules: (workspaceId: string) => Promise<void>;
  saveWsAssignmentRules: (
    workspaceId: string,
    config: AssignmentRulesConfig,
  ) => Promise<void>;

  // Assignment Rules (project level, effective)
  effectiveAssignmentRules: EffectiveAssignmentRules | null;
  isProjRulesLoading: boolean;
  fetchEffectiveAssignmentRules: (projectId: string) => Promise<void>;
  saveProjectAssignmentRules: (
    projectId: string,
    config: AssignmentRulesConfig,
  ) => Promise<void>;

  // Workflow Rules (project level)
  workflowRules: WorkflowRulesResponse | null;
  isWorkflowLoading: boolean;
  fetchWorkflowRules: (projectId: string) => Promise<void>;
  saveWorkflowRules: (
    projectId: string,
    config: WorkflowRulesConfig,
  ) => Promise<void>;

  // Violations
  violations: RuleViolation[];
  isViolationsLoading: boolean;
  fetchViolations: (workspaceId: string) => Promise<void>;

  // Config Import/Export
  importConfig: (workspaceId: string, yamlContent: string) => Promise<ImportResult>;
  exportConfig: (workspaceId: string) => Promise<string>;
  importTeam: (workspaceId: string, yamlContent: string) => Promise<TeamImportResult>;

  // Workflow Templates
  workflowTemplates: Record<string, WorkflowRulesConfig>;
  isTemplatesLoading: boolean;
  fetchWorkflowTemplates: (workspaceId: string) => Promise<void>;
  saveWorkflowTemplates: (workspaceId: string, templates: Record<string, WorkflowRulesConfig>) => Promise<void>;
}

export const useRulesStore = create<RulesState>((set) => ({
  // Team Directory
  teamDirectory: null,
  isTeamLoading: false,

  fetchTeamDirectory: async (workspaceId: string) => {
    set({ isTeamLoading: true });
    try {
      const data = await api<TeamDirectory>(
        `/api/v1/workspaces/${workspaceId}/team`,
      );
      set({ teamDirectory: data, isTeamLoading: false });
    } catch {
      set({ isTeamLoading: false });
    }
  },

  // Org Chart (tree format)
  orgChart: null,
  isOrgChartLoading: false,

  fetchOrgChart: async (workspaceId: string) => {
    set({ isOrgChartLoading: true });
    try {
      const data = await api<OrgChartData>(
        `/api/v1/workspaces/${workspaceId}/team?format=tree`,
      );
      set({ orgChart: data, isOrgChartLoading: false });
    } catch {
      set({ isOrgChartLoading: false });
    }
  },

  // Assignment Rules (workspace level)
  wsAssignmentRules: null,
  isWsRulesLoading: false,

  fetchWsAssignmentRules: async (workspaceId: string) => {
    set({ isWsRulesLoading: true });
    try {
      const data = await api<AssignmentRulesConfig>(
        `/api/v1/workspaces/${workspaceId}/rules/assignment`,
      );
      set({ wsAssignmentRules: data, isWsRulesLoading: false });
    } catch {
      set({ isWsRulesLoading: false });
    }
  },

  saveWsAssignmentRules: async (
    workspaceId: string,
    config: AssignmentRulesConfig,
  ) => {
    await api<AssignmentRulesConfig>(
      `/api/v1/workspaces/${workspaceId}/rules/assignment`,
      { method: "PUT", body: config },
    );
    // Re-fetch to ensure store has the canonical saved config
    const saved = await api<AssignmentRulesConfig>(
      `/api/v1/workspaces/${workspaceId}/rules/assignment`,
    );
    set({ wsAssignmentRules: saved });
  },

  // Assignment Rules (project level, effective)
  effectiveAssignmentRules: null,
  isProjRulesLoading: false,

  fetchEffectiveAssignmentRules: async (projectId: string) => {
    set({ isProjRulesLoading: true });
    try {
      const data = await api<EffectiveAssignmentRules>(
        `/api/v1/projects/${projectId}/rules/assignment`,
      );
      set({ effectiveAssignmentRules: data, isProjRulesLoading: false });
    } catch {
      set({ isProjRulesLoading: false });
    }
  },

  saveProjectAssignmentRules: async (
    projectId: string,
    config: AssignmentRulesConfig,
  ) => {
    await api<AssignmentRulesConfig>(
      `/api/v1/projects/${projectId}/rules/assignment`,
      { method: "PUT", body: config },
    );
    // Re-fetch effective rules after save
    const effective = await api<EffectiveAssignmentRules>(
      `/api/v1/projects/${projectId}/rules/assignment`,
    );
    set({ effectiveAssignmentRules: effective });
  },

  // Workflow Rules (project level)
  workflowRules: null,
  isWorkflowLoading: false,

  fetchWorkflowRules: async (projectId: string) => {
    set({ isWorkflowLoading: true });
    try {
      const data = await api<WorkflowRulesResponse>(
        `/api/v1/projects/${projectId}/rules/workflow`,
      );
      set({ workflowRules: data, isWorkflowLoading: false });
    } catch {
      set({ isWorkflowLoading: false });
    }
  },

  saveWorkflowRules: async (projectId: string, config: WorkflowRulesConfig) => {
    const updated = await api<WorkflowRulesResponse>(
      `/api/v1/projects/${projectId}/rules/workflow`,
      { method: "PUT", body: config },
    );
    set({ workflowRules: updated });
  },

  // Violations
  violations: [],
  isViolationsLoading: false,

  fetchViolations: async (workspaceId: string) => {
    set({ isViolationsLoading: true });
    try {
      const resp = await api<{ items: RuleViolation[]; total_count: number }>(
        `/api/v1/workspaces/${workspaceId}/violations`,
      );
      set({ violations: resp?.items ?? [], isViolationsLoading: false });
    } catch {
      set({ isViolationsLoading: false });
    }
  },

  // Config Import/Export
  importConfig: async (workspaceId: string, yamlContent: string): Promise<ImportResult> => {
    const token = getAccessToken();
    const res = await fetch(`${BASE_URL}/api/v1/workspaces/${workspaceId}/config/import`, {
      method: "POST",
      headers: {
        "Content-Type": "text/yaml",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: yamlContent,
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json() as Promise<ImportResult>;
  },

  exportConfig: async (workspaceId: string): Promise<string> => {
    const token = getAccessToken();
    const res = await fetch(`${BASE_URL}/api/v1/workspaces/${workspaceId}/config/export`, {
      headers: {
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
    });
    if (!res.ok) throw new Error("Export failed");
    return res.text();
  },

  importTeam: async (workspaceId: string, yamlContent: string): Promise<TeamImportResult> => {
    const token = getAccessToken();
    const res = await fetch(`${BASE_URL}/api/v1/workspaces/${workspaceId}/team/import`, {
      method: "POST",
      headers: {
        "Content-Type": "text/yaml",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: yamlContent,
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json() as Promise<TeamImportResult>;
  },

  // Workflow Templates
  workflowTemplates: {},
  isTemplatesLoading: false,

  fetchWorkflowTemplates: async (workspaceId: string) => {
    set({ isTemplatesLoading: true });
    try {
      const data = await api<Record<string, WorkflowRulesConfig>>(
        `/api/v1/workspaces/${workspaceId}/rules/workflow-templates`,
      );
      set({ workflowTemplates: data ?? {}, isTemplatesLoading: false });
    } catch {
      set({ isTemplatesLoading: false });
    }
  },

  saveWorkflowTemplates: async (workspaceId: string, templates: Record<string, WorkflowRulesConfig>) => {
    const updated = await api<Record<string, WorkflowRulesConfig>>(
      `/api/v1/workspaces/${workspaceId}/rules/workflow-templates`,
      { method: "PUT", body: templates },
    );
    set({ workflowTemplates: updated ?? {} });
  },
}));
