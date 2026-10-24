// auth.screen contract v1.0.0 (spec «Каталог слотов тем v1», §7 of
// slots-task-nav-doc-auth). Fields marked "(+)" extend the spec so that the
// default view reproduces the existing pages with zero visual difference.
import type { ReactNode } from "react";
import type { SlotView } from "./slots";

export type AuthMode = "login" | "register" | "accept-invite";

export interface AuthField {
  name: "email" | "password" | "confirm_password" | "name" | "username" | "invite_code";
  type: "email" | "password" | "text";
  label: string;
  required: boolean;
  autoComplete: string;
  error?: string;
  value: string; // (+) the container owns values; the view only renders them
  placeholder?: string; // (+)
  hint?: string; // (+)
  minLength?: number; // (+)
  maxLength?: number; // (+)
  autoFocus?: boolean; // (+)
  revealable?: boolean; // (+) explicit show/hide toggle for the password
}

export interface AuthScreenData {
  mode: AuthMode;
  // (+) what the screen is showing: the form, a wait for the invite lookup,
  // «registration closed», or an unusable invite.
  status: "form" | "loading" | "closed" | "invalid";
  heading: { title: string; subtitle?: ReactNode; detail?: ReactNode }; // (+)
  fields: AuthField[];
  submitLabel: string; // (+)
  submitting: boolean;
  formError: string | null;
  registrationEnabled: boolean;
  invite?: { workspaceName: string; inviterName: string } | null;
  links: { login: string; register: string | null; forgot: string | null };
  // (+) the line under the form: «Already have an account? Sign in».
  footer: { text?: string; linkLabel: string; to: string } | null;
  components: { OAuthButtons?: React.FC };
}

export interface AuthScreenActions {
  setValue(name: AuthField["name"], value: string): void;
  submit(): Promise<void>;
  navigate(to: string): void; // (+) used by the «go to sign in» button
}

export interface AuthScreenSettings {
  background_image?: string;
  show_logo?: boolean;
  [k: string]: unknown;
}

export type AuthScreenView = SlotView<AuthScreenData, AuthScreenActions, AuthScreenSettings>;
