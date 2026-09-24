import { type FormEvent, useCallback, useEffect, useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { useAuthConfig } from "@/hooks/use-auth-config";
import { ApiRequestError } from "@/lib/api";
import {
  clearStaticShellDraft,
  peekStaticShellDraft,
} from "@/lib/static-shell";
import { prefetchNextRouteAfterLogin } from "@/lib/prefetch-next-route";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

export function LoginPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { isAuthenticated, login } = useAuthStore();
  const { registrationEnabled } = useAuthConfig();
  // Seeded from the static shell in index.html, so input typed before the
  // bundle loaded survives the hand-off to React.
  const [email, setEmail] = useState(() => peekStaticShellDraft()?.email ?? "");
  const [password, setPassword] = useState(
    () => peekStaticShellDraft()?.password ?? "",
  );
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    const focusedId = peekStaticShellDraft()?.focusedId;
    clearStaticShellDraft();
    if (!focusedId) return;
    const el = document.getElementById(focusedId);
    if (el instanceof HTMLInputElement) {
      el.focus();
      // type=email doesn't support selection APIs; the caret lands at the end
      // on focus there anyway.
      try {
        el.setSelectionRange(el.value.length, el.value.length);
      } catch {
        /* not a text-selectable input type */
      }
    }
  }, []);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      setError(null);
      setLoading(true);

      try {
        await login({ email, password });
        prefetchNextRouteAfterLogin();
        const redirect = searchParams.get("redirect");
        navigate(redirect && redirect.startsWith("/") ? redirect : "/");
      } catch (err) {
        if (err instanceof ApiRequestError) {
          setError(err.message);
        } else {
          setError("An unexpected error occurred");
        }
      } finally {
        setLoading(false);
      }
    },
    [email, password, login, navigate, searchParams],
  );

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">
            M
          </div>
          <CardTitle className="text-2xl">Welcome back</CardTitle>
          <CardDescription>
            Sign in to your EVC Mesh account
          </CardDescription>
        </CardHeader>
        <form onSubmit={handleSubmit}>
          <CardContent className="space-y-4">
            {error && (
              <div className="rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
                {error}
              </div>
            )}
            <div className="space-y-2">
              <label htmlFor="email" className="text-sm font-medium">
                Email
              </label>
              <Input
                id="email"
                type="email"
                placeholder="you@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
                autoComplete="email"
              />
            </div>
            <div className="space-y-2">
              <label htmlFor="password" className="text-sm font-medium">
                Password
              </label>
              <Input
                id="password"
                type="password"
                placeholder="Your password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                autoComplete="current-password"
              />
            </div>
          </CardContent>
          <CardFooter className="flex-col gap-4">
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? "Signing in..." : "Sign in"}
            </Button>
            {registrationEnabled && (
              <p className="text-center text-sm text-muted-foreground">
                Don&apos;t have an account?{" "}
                <Link to="/register" className="text-primary hover:underline">
                  Register
                </Link>
              </p>
            )}
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
