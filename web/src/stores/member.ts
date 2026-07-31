import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CreateInviteResponse,
  InviteDelivery,
  ProjectMemberWithUser,
  ProjectRole,
  UserSearchResult,
  WorkspaceInvite,
  WorkspaceMemberWithUser,
  WorkspaceRole,
} from "@/types";

interface MemberState {
  workspaceMembers: WorkspaceMemberWithUser[];
  myRole: WorkspaceRole | null;
  projectMembers: ProjectMemberWithUser[];
  userSearchResults: UserSearchResult[];
  isLoadingWorkspaceMembers: boolean;
  isLoadingProjectMembers: boolean;
  isSearching: boolean;
  workspaceInvites: WorkspaceInvite[];
  isLoadingInvites: boolean;

  // Workspace member actions
  fetchWorkspaceMembers: (workspaceId: string) => Promise<void>;
  fetchMyRole: (workspaceId: string) => Promise<void>;
  addWorkspaceMember: (
    workspaceId: string,
    email: string,
    role: WorkspaceRole,
    password?: string,
  ) => Promise<WorkspaceMemberWithUser>;
  updateWorkspaceMemberRole: (
    workspaceId: string,
    userId: string,
    role: WorkspaceRole,
  ) => Promise<void>;
  removeWorkspaceMember: (workspaceId: string, userId: string) => Promise<void>;

  // User search
  searchUsers: (workspaceId: string, query: string) => Promise<void>;
  clearSearchResults: () => void;

  // Invite actions
  fetchWorkspaceInvites: (workspaceId: string) => Promise<void>;
  createInvite: (
    workspaceId: string,
    email: string,
    role: WorkspaceRole,
  ) => Promise<CreateInviteResponse>;
  resendInvite: (workspaceId: string, inviteId: string) => Promise<InviteDelivery>;
  revokeInvite: (workspaceId: string, inviteId: string) => Promise<void>;

  // Project member actions
  fetchProjectMembers: (projectId: string) => Promise<void>;
  addProjectMember: (
    projectId: string,
    userId: string,
    role: ProjectRole,
  ) => Promise<ProjectMemberWithUser>;
  addProjectAgentMember: (
    projectId: string,
    agentId: string,
    role: ProjectRole,
  ) => Promise<ProjectMemberWithUser>;
  updateProjectMemberRole: (
    projectId: string,
    userId: string,
    role: ProjectRole,
  ) => Promise<void>;
  removeProjectMember: (projectId: string, userId: string) => Promise<void>;
  removeProjectAgentMember: (projectId: string, agentId: string) => Promise<void>;
}

export const useMemberStore = create<MemberState>((set) => ({
  workspaceMembers: [],
  myRole: null,
  projectMembers: [],
  userSearchResults: [],
  isLoadingWorkspaceMembers: false,
  isLoadingProjectMembers: false,
  isSearching: false,
  workspaceInvites: [],
  isLoadingInvites: false,

  fetchWorkspaceMembers: async (workspaceId: string) => {
    set({ isLoadingWorkspaceMembers: true });
    try {
      const resp = await api<{ members: WorkspaceMemberWithUser[]; count: number }>(
        `/api/v1/workspaces/${workspaceId}/members`,
      );
      set({ workspaceMembers: resp?.members ?? [], isLoadingWorkspaceMembers: false });
    } catch {
      set({ isLoadingWorkspaceMembers: false });
    }
  },

  fetchMyRole: async (workspaceId: string) => {
    try {
      const me = await api<{ role: string }>(
        `/api/v1/workspaces/${workspaceId}/members/me`,
      );
      set({ myRole: (me?.role as WorkspaceRole) ?? null });
    } catch {
      set({ myRole: null });
    }
  },

  addWorkspaceMember: async (
    workspaceId: string,
    email: string,
    role: WorkspaceRole,
    password?: string,
  ): Promise<WorkspaceMemberWithUser> => {
    const body: Record<string, string> = { email, role };
    if (password) body.password = password;
    const member = await api<WorkspaceMemberWithUser>(
      `/api/v1/workspaces/${workspaceId}/members`,
      { method: "POST", body },
    );
    set((state) => ({
      workspaceMembers: [...state.workspaceMembers, member],
    }));
    return member;
  },

  updateWorkspaceMemberRole: async (
    workspaceId: string,
    userId: string,
    role: WorkspaceRole,
  ) => {
    const updated = await api<WorkspaceMemberWithUser>(
      `/api/v1/workspaces/${workspaceId}/members/${userId}`,
      { method: "PATCH", body: { role } },
    );
    set((state) => ({
      workspaceMembers: state.workspaceMembers.map((m) =>
        m.user_id === userId ? updated : m,
      ),
    }));
  },

  removeWorkspaceMember: async (workspaceId: string, userId: string) => {
    await api(`/api/v1/workspaces/${workspaceId}/members/${userId}`, {
      method: "DELETE",
    });
    set((state) => ({
      workspaceMembers: state.workspaceMembers.filter(
        (m) => m.user_id !== userId,
      ),
    }));
  },

  searchUsers: async (workspaceId: string, query: string) => {
    if (!query.trim()) {
      set({ userSearchResults: [] });
      return;
    }
    set({ isSearching: true });
    try {
      const results = await api<UserSearchResult[]>(
        `/api/v1/workspaces/${workspaceId}/users/search`,
        { params: { q: query } },
      );
      set({ userSearchResults: results ?? [], isSearching: false });
    } catch {
      set({ userSearchResults: [], isSearching: false });
    }
  },

  clearSearchResults: () => {
    set({ userSearchResults: [] });
  },

  fetchWorkspaceInvites: async (workspaceId: string) => {
    set({ isLoadingInvites: true });
    try {
      const resp = await api<{ invites: WorkspaceInvite[]; count: number }>(
        `/api/v1/workspaces/${workspaceId}/invites`,
      );
      set({ workspaceInvites: resp?.invites ?? [], isLoadingInvites: false });
    } catch {
      set({ isLoadingInvites: false });
    }
  },

  createInvite: async (
    workspaceId: string,
    email: string,
    role: WorkspaceRole,
  ): Promise<CreateInviteResponse> => {
    // The response carries the invite AND what became of its email. Callers
    // must read email_sent rather than infer delivery from the 201 — on an
    // instance with no SMTP server the invite is created and nothing is sent.
    const invite = await api<CreateInviteResponse>(
      `/api/v1/workspaces/${workspaceId}/invites`,
      { method: "POST", body: { email, role } },
    );
    set((state) => ({
      workspaceInvites: [invite, ...state.workspaceInvites],
    }));
    return invite;
  },

  resendInvite: async (workspaceId: string, inviteId: string): Promise<InviteDelivery> => {
    return await api<InviteDelivery>(
      `/api/v1/workspaces/${workspaceId}/invites/${inviteId}/resend`,
      { method: "POST" },
    );
  },

  revokeInvite: async (workspaceId: string, inviteId: string) => {
    await api(
      `/api/v1/workspaces/${workspaceId}/invites/${inviteId}`,
      { method: "DELETE" },
    );
    set((state) => ({
      workspaceInvites: state.workspaceInvites.filter((i) => i.id !== inviteId),
    }));
  },

  fetchProjectMembers: async (projectId: string) => {
    set({ isLoadingProjectMembers: true });
    try {
      const resp = await api<{ members: ProjectMemberWithUser[]; count: number }>(
        `/api/v1/projects/${projectId}/members`,
      );
      set({ projectMembers: resp?.members ?? [], isLoadingProjectMembers: false });
    } catch {
      set({ isLoadingProjectMembers: false });
    }
  },

  addProjectMember: async (
    projectId: string,
    userId: string,
    role: ProjectRole,
  ): Promise<ProjectMemberWithUser> => {
    const member = await api<ProjectMemberWithUser>(
      `/api/v1/projects/${projectId}/members`,
      { method: "POST", body: { user_id: userId, role } },
    );
    set((state) => ({
      projectMembers: [...state.projectMembers, member],
    }));
    return member;
  },

  addProjectAgentMember: async (
    projectId: string,
    agentId: string,
    role: ProjectRole,
  ): Promise<ProjectMemberWithUser> => {
    const member = await api<ProjectMemberWithUser>(
      `/api/v1/projects/${projectId}/members/agents`,
      { method: "POST", body: { agent_id: agentId, role } },
    );
    set((state) => ({
      projectMembers: [...state.projectMembers, member],
    }));
    return member;
  },

  updateProjectMemberRole: async (
    projectId: string,
    userId: string,
    role: ProjectRole,
  ) => {
    const updated = await api<ProjectMemberWithUser>(
      `/api/v1/projects/${projectId}/members/${userId}`,
      { method: "PATCH", body: { role } },
    );
    set((state) => ({
      projectMembers: state.projectMembers.map((m) =>
        m.user_id === userId ? updated : m,
      ),
    }));
  },

  removeProjectMember: async (projectId: string, userId: string) => {
    await api(`/api/v1/projects/${projectId}/members/${userId}`, {
      method: "DELETE",
    });
    set((state) => ({
      projectMembers: state.projectMembers.filter(
        (m) => m.user_id !== userId,
      ),
    }));
  },

  removeProjectAgentMember: async (projectId: string, agentId: string) => {
    await api(`/api/v1/projects/${projectId}/members/agents/${agentId}`, {
      method: "DELETE",
    });
    set((state) => ({
      projectMembers: state.projectMembers.filter(
        (m) => m.agent_id !== agentId,
      ),
    }));
  },
}));
