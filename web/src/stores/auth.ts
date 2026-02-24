import { create } from "zustand";
import { api, clearTokens, loadTokens, setTokens } from "@/lib/api";
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
  logout: () => void;
  fetchMe: () => Promise<void>;
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  isAuthenticated: false,
  isLoading: true,

  initialize: async () => {
    loadTokens();
    const token = localStorage.getItem("access_token");
    if (!token) {
      set({ isLoading: false, isAuthenticated: false });
      return;
    }
    try {
      const user = await api<User>("/api/v1/auth/me");
      set({ user, isAuthenticated: true, isLoading: false });
    } catch {
      clearTokens();
      set({ user: null, isAuthenticated: false, isLoading: false });
    }
  },

  login: async (req: LoginRequest) => {
    const data = await api<AuthResponse>("/api/v1/auth/login", {
      method: "POST",
      body: req,
      noAuth: true,
    });
    setTokens(data.tokens.access_token, data.tokens.refresh_token);
    set({ user: data.user, isAuthenticated: true });
  },

  register: async (req: RegisterRequest) => {
    const data = await api<AuthResponse>("/api/v1/auth/register", {
      method: "POST",
      body: req,
      noAuth: true,
    });
    setTokens(data.tokens.access_token, data.tokens.refresh_token);
    set({ user: data.user, isAuthenticated: true });
  },

  logout: () => {
    clearTokens();
    set({ user: null, isAuthenticated: false });
  },

  fetchMe: async () => {
    const user = await api<User>("/api/v1/auth/me");
    set({ user });
  },
}));
