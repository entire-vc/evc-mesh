import { type FormEvent, useState } from "react";
import { Link } from "react-router";
import { Eye, EyeOff } from "lucide-react";
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
import type { AuthField, AuthScreenView } from "./types";

// mesh-default view of auth.screen. Markup and classes are carried over
// unchanged from the former login / register / accept-invite pages; all state,
// validation and navigation live in the container.

const MESH_LOGO = (
  <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">
    M
  </div>
);

function CardField({ field, onChange }: { field: AuthField; onChange: (v: string) => void }) {
  return (
    <div className="space-y-2">
      <label htmlFor={field.name} className="text-sm font-medium">
        {field.label}
      </label>
      <Input
        id={field.name}
        name={field.name}
        type={field.type}
        placeholder={field.placeholder}
        value={field.value}
        onChange={(e) => onChange(e.target.value)}
        required={field.required}
        minLength={field.minLength}
        autoComplete={field.autoComplete || undefined}
        aria-invalid={field.error ? true : undefined}
        aria-describedby={field.error ? `${field.name}-error` : undefined}
      />
      {field.error && (
        <p id={`${field.name}-error`} role="alert" className="text-sm text-destructive">
          {field.error}
        </p>
      )}
    </div>
  );
}

function InviteField({
  field,
  shown,
  onToggle,
  onChange,
}: {
  field: AuthField;
  shown: boolean;
  onToggle: () => void;
  onChange: (v: string) => void;
}) {
  const id = `ai-${field.name === "confirm_password" ? "confirm" : field.name}`;
  const isPassword = field.type === "password";
  const input = (
    <Input
      id={id}
      name={field.name}
      type={isPassword && shown ? "text" : field.type}
      placeholder={field.placeholder}
      value={field.value}
      onChange={(e) => onChange(e.target.value)}
      className={field.revealable ? "pr-9" : undefined}
      autoComplete={field.autoComplete || undefined}
      autoFocus={field.autoFocus}
      maxLength={field.maxLength}
      aria-invalid={field.error ? true : undefined}
      aria-describedby={field.error ? `${id}-error` : undefined}
    />
  );
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="text-sm font-medium">
        {field.label} {field.required && <span className="text-destructive">*</span>}
      </label>
      {field.revealable ? (
        <div className="relative">
          {input}
          <button
            type="button"
            className="absolute inset-y-0 right-3 flex items-center text-muted-foreground hover:text-foreground"
            onClick={onToggle}
            tabIndex={-1}
          >
            {shown ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
      ) : (
        input
      )}
      {field.hint && <p className="text-xs text-muted-foreground">{field.hint}</p>}
      {field.error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-destructive">
          {field.error}
        </p>
      )}
    </div>
  );
}

export const MeshDefaultAuthScreen: AuthScreenView = ({ data, actions }) => {
  const [shown, setShown] = useState(false);
  const { heading, footer } = data;
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (data.submitting) return;
    void actions.submit();
  };

  if (data.status === "loading") {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <p className="text-muted-foreground">{heading.title}</p>
      </div>
    );
  }

  if (data.status === "invalid") {
    return (
      <div className="flex min-h-screen items-center justify-center p-4">
        <div className="w-full max-w-md text-center space-y-4">
          <h1 className="text-2xl font-bold">{heading.title}</h1>
          <p className="text-muted-foreground">{heading.subtitle}</p>
          {footer && (
            <Button variant="outline" onClick={() => actions.navigate(footer.to)}>
              {footer.linkLabel}
            </Button>
          )}
        </div>
      </div>
    );
  }

  if (data.mode === "accept-invite") {
    return (
      <div className="flex min-h-screen items-center justify-center p-4">
        <div className="w-full max-w-md space-y-6">
          <div className="text-center space-y-1">
            <h1 className="text-2xl font-bold">{heading.title}</h1>
            <p className="text-muted-foreground">{heading.subtitle}</p>
            <p className="text-sm text-muted-foreground">{heading.detail}</p>
          </div>
          <form onSubmit={onSubmit} className="space-y-4">
            {data.fields.map((f) => (
              <InviteField
                key={f.name}
                field={f}
                shown={shown}
                onToggle={() => setShown((v) => !v)}
                onChange={(v) => actions.setValue(f.name, v)}
              />
            ))}
            {data.formError && (
              <p role="alert" className="text-sm text-destructive">
                {data.formError}
              </p>
            )}
            <Button type="submit" className="w-full" disabled={data.submitting}>
              {data.submitLabel}
            </Button>
          </form>
        </div>
      </div>
    );
  }

  const header = (
    <CardHeader className="text-center">
      {MESH_LOGO}
      <CardTitle className="text-2xl">{heading.title}</CardTitle>
      <CardDescription>{heading.subtitle}</CardDescription>
    </CardHeader>
  );

  if (data.status === "closed") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background px-4">
        <Card className="w-full max-w-md">
          {header}
          {footer && (
            <CardFooter className="justify-center">
              <Link to={footer.to} className="text-primary hover:underline">
                {footer.linkLabel}
              </Link>
            </CardFooter>
          )}
        </Card>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <Card className="w-full max-w-md">
        {header}
        <form onSubmit={onSubmit}>
          <CardContent className="space-y-4">
            {data.formError && (
              <div role="alert" className="rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
                {data.formError}
              </div>
            )}
            {data.fields.map((f) => (
              <CardField key={f.name} field={f} onChange={(v) => actions.setValue(f.name, v)} />
            ))}
          </CardContent>
          <CardFooter className="flex-col gap-4">
            <Button type="submit" className="w-full" disabled={data.submitting}>
              {data.submitLabel}
            </Button>
            {footer && (
              <p className="text-center text-sm text-muted-foreground">
                {footer.text}{" "}
                <Link to={footer.to} className="text-primary hover:underline">
                  {footer.linkLabel}
                </Link>
              </p>
            )}
          </CardFooter>
        </form>
      </Card>
    </div>
  );
};
