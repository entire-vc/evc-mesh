import { create } from "zustand";
import { api, bootstrapSession, clearAccessToken, clearSessionCookies, setAccessToken } from "@/lib/api";
import type {
  AuthResponse,
  LoginRequest,
  RegisterRequest,
  User,
} from "@/types";

interface AuthState {
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;

  initialize: () => Promise<void>;
  login: (req: LoginRequest) => Promise<void>;
  register: (req: RegisterRequest) => Promise<void>;
  logout: () => Promise<void>;
  fetchMe: () => Promise<void>;
  updateProfile: (name: string, username?: string, avatarURL?: string) => Promise<void>;
  checkUsername: (username: string) => Promise<boolean>;
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  isAuthenticated: false,
  isLoading: true,

  initialize: async () => {
    // No token to check for anymore — the access token never survives a
    // reload. Silently trade the httpOnly refresh cookie for a new one; a
    // missing/expired cookie throws, which just means "not logged in".
    try {
      await bootstrapSession();
      const user = await api<User>("/api/v1/auth/me");
      set({ user, isAuthenticated: true, isLoading: false });
    } catch {
      clearAccessToken();
      set({ user: null, isAuthenticated: false, isLoading: false });
    }
  },

  login: async (req: LoginRequest) => {
    const data = await api<AuthResponse>("/api/v1/auth/login", {
      method: "POST",
      body: req,
      noAuth: true,
      withCredentials: true,
    });
    setAccessToken(data.tokens.access_token);
    set({ user: data.user, isAuthenticated: true });
  },

  register: async (req: RegisterRequest) => {
    const data = await api<AuthResponse>("/api/v1/auth/register", {
      method: "POST",
      body: req,
      noAuth: true,
      withCredentials: true,
    });
    setAccessToken(data.tokens.access_token);
    set({ user: data.user, isAuthenticated: true });
  },

  logout: async () => {
    try {
      await api("/api/v1/auth/logout", { method: "POST" });
    } catch {
      // Ignore errors — backend may be unreachable or token already expired.
      // We always clear local state regardless.
    }
    clearAccessToken();
    // Best-effort: clear any non-httpOnly session cookies that may have been
    // set by a previous Mesh/Casdoor instance. Prevents stale cookies from
    // blocking subsequent logins with an "unexpected error". (The httpOnly
    // refresh cookie itself was just cleared server-side by /auth/logout.)
    clearSessionCookies();
    set({ user: null, isAuthenticated: false });
  },

  fetchMe: async () => {
    const user = await api<User>("/api/v1/auth/me");
    set({ user });
  },

  updateProfile: async (name: string, username?: string, avatarURL?: string) => {
    const user = await api<User>("/api/v1/auth/me", {
      method: "PATCH",
      body: { name, username: username ?? "", avatar_url: avatarURL ?? "" },
    });
    set({ user });
  },

  checkUsername: async (username: string) => {
    const res = await api<{ available: boolean }>(
      `/api/v1/auth/check-username?username=${encodeURIComponent(username)}`,
    );
    return res.available;
  },
}));
