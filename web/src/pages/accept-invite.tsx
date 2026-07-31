import { type FormEvent, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { Eye, EyeOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, setTokens } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";

interface InviteInfo {
  id: string;
  workspace_id: string;
  email: string;
  role: string;
  expires_at: string;
}

export function AcceptInvitePage() {
  const { token } = useParams<{ token: string }>();
  const navigate = useNavigate();
  const { fetchMe } = useAuthStore();

  const [invite, setInvite] = useState<InviteInfo | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    api<InviteInfo>(`/api/v1/invites/${token}`)
      .then((data) => {
        setInvite(data);
        setIsLoading(false);
      })
      .catch(() => {
        setLoadError("This invite link is invalid or has expired.");
        setIsLoading(false);
      });
  }, [token]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    // Required, not optional. When it was optional the server fell back to
    // storing the address in display_name, and nothing in the product could
    // correct it afterwards — which is how an instance ends up showing every
    // member as their own address on every task and comment.
    if (!name.trim()) {
      setError("Please enter your name — it is what your team sees on tasks and comments");
      return;
    }
    if (!password) {
      setError("Password is required");
      return;
    }
    if (password !== confirmPassword) {
      setError("Passwords do not match");
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const resp = await api<{ access_token: string; refresh_token: string }>(
        `/api/v1/invites/${token}/accept`,
        { method: "POST", body: { name: name.trim(), password }, noAuth: true },
      );
      setTokens(resp.access_token, resp.refresh_token);
      await fetchMe();
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to accept invite");
    } finally {
      setIsSubmitting(false);
    }
  };

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <p className="text-muted-foreground">Loading invite…</p>
      </div>
    );
  }

  if (loadError || !invite) {
    return (
      <div className="flex min-h-screen items-center justify-center p-4">
        <div className="w-full max-w-md text-center space-y-4">
          <h1 className="text-2xl font-bold">Invite Not Found</h1>
          <p className="text-muted-foreground">{loadError ?? "This invite link is no longer valid."}</p>
          <Button variant="outline" onClick={() => navigate("/login")}>
            Go to login
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <div className="w-full max-w-md space-y-6">
        <div className="text-center space-y-1">
          <h1 className="text-2xl font-bold">Accept Invitation</h1>
          <p className="text-muted-foreground">
            You've been invited to join as <strong>{invite.role}</strong>.
          </p>
          <p className="text-sm text-muted-foreground">
            Signing in as <span className="font-medium text-foreground">{invite.email}</span>
          </p>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <label htmlFor="ai-name" className="text-sm font-medium">
              Your name <span className="text-destructive">*</span>
            </label>
            <Input
              id="ai-name"
              type="text"
              placeholder="Your name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              maxLength={100}
            />
            <p className="text-xs text-muted-foreground">
              Shown on tasks, comments and mentions. You can change it later.
            </p>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ai-password" className="text-sm font-medium">
              Password <span className="text-destructive">*</span>
            </label>
            <div className="relative">
              <Input
                id="ai-password"
                type={showPassword ? "text" : "password"}
                placeholder="Min 8 characters"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="pr-9"
                autoComplete="new-password"
              />
              <button
                type="button"
                className="absolute inset-y-0 right-3 flex items-center text-muted-foreground hover:text-foreground"
                onClick={() => setShowPassword((v) => !v)}
                tabIndex={-1}
              >
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="ai-confirm" className="text-sm font-medium">
              Confirm password <span className="text-destructive">*</span>
            </label>
            <Input
              id="ai-confirm"
              type={showPassword ? "text" : "password"}
              placeholder="Repeat password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              autoComplete="new-password"
            />
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}

          <Button type="submit" className="w-full" disabled={isSubmitting}>
            {isSubmitting ? "Joining…" : "Accept & Join"}
          </Button>
        </form>
      </div>
    </div>
  );
}
