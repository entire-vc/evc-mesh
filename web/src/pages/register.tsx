import { type FormEvent, useCallback, useState } from "react";
import { Link, Navigate, useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { useAuthStore } from "@/stores/auth";
import { useAuthConfig } from "@/hooks/use-auth-config";
import { ApiRequestError } from "@/lib/api";
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

export function RegisterPage() {
  // Same as LoginPage: rendered outside AppLayout, so no Suspense on the
  // `misc` chunk; the English default shows until it has loaded.
  const { t } = useTranslation("misc", { useSuspense: false });
  const navigate = useNavigate();
  const { isAuthenticated, register } = useAuthStore();
  const { registrationEnabled, loading: configLoading } = useAuthConfig();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      setError(null);
      setLoading(true);

      try {
        await register({ email, password, name });
        navigate("/");
      } catch (err) {
        if (err instanceof ApiRequestError) {
          setError(err.message);
        } else {
          setError(t("errors.unexpected", "An unexpected error occurred"));
        }
      } finally {
        setLoading(false);
      }
    },
    [email, password, name, register, navigate, t],
  );

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  if (!configLoading && !registrationEnabled) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background px-4">
        <Card className="w-full max-w-md">
          <CardHeader className="text-center">
            <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">
              M
            </div>
            <CardTitle className="text-2xl">{t("auth.register.closedTitle", "Registration is closed")}</CardTitle>
            <CardDescription>
              {t(
                "auth.register.closedBody",
                "This instance is not accepting new signups. Ask an admin for an invite, or sign in if you already have an account.",
              )}
            </CardDescription>
          </CardHeader>
          <CardFooter className="justify-center">
            <Link to="/login" className="text-primary hover:underline">
              {t("auth.register.backToSignIn", "Back to sign in")}
            </Link>
          </CardFooter>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">
            M
          </div>
          <CardTitle className="text-2xl">{t("auth.register.title", "Create an account")}</CardTitle>
          <CardDescription>
            {t("auth.register.subtitle", "Get started with EVC Mesh")}
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
              <label htmlFor="name" className="text-sm font-medium">
                {t("auth.name", "Name")}
              </label>
              <Input
                id="name"
                type="text"
                placeholder={t("auth.namePlaceholder", "Your name")}
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                autoComplete="name"
              />
            </div>
            <div className="space-y-2">
              <label htmlFor="email" className="text-sm font-medium">
                {t("auth.email", "Email")}
              </label>
              <Input
                id="email"
                type="email"
                placeholder={t("auth.emailPlaceholder", "you@example.com")}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
                autoComplete="email"
              />
            </div>
            <div className="space-y-2">
              <label htmlFor="password" className="text-sm font-medium">
                {t("auth.password", "Password")}
              </label>
              <Input
                id="password"
                type="password"
                placeholder={t("auth.register.passwordPlaceholder", "At least 8 characters")}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                minLength={8}
                autoComplete="new-password"
              />
            </div>
          </CardContent>
          <CardFooter className="flex-col gap-4">
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? t("auth.register.submitting", "Creating account...") : t("auth.register.submit", "Create account")}
            </Button>
            <p className="text-center text-sm text-muted-foreground">
              {t("auth.register.haveAccount", "Already have an account?")}{" "}
              <Link to="/login" className="text-primary hover:underline">
                {t("auth.register.signIn", "Sign in")}
              </Link>
            </p>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
