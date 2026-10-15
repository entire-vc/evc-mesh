export type Widget =
  | "toggle"
  | "select"
  | "textarea"
  | "list"
  | "group"
  | "project-picker"
  | "agent-picker";

/** One condition value: equals, one of, negated, or presence. */
export type VisibleIfExpected =
  | string
  | number
  | boolean
  | null
  | unknown[]
  | { not?: unknown; in?: unknown[]; exists?: boolean };

/**
 * `x-visible-if`: map of sibling field name -> expected value. All entries
 * must hold (AND). Array = "one of", `{ not }`, `{ in }`, `{ exists }` for the rest.
 */
export type VisibleIf = Record<string, VisibleIfExpected>;

export interface FieldSchema {
  $ref?: string;
  type?: "string" | "number" | "integer" | "boolean" | "array" | "object";
  title?: string;
  description?: string;
  format?: string;
  enum?: unknown[];
  default?: unknown;
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  minimum?: number;
  maximum?: number;
  items?: FieldSchema;
  properties?: Record<string, FieldSchema>;
  required?: string[];
  "x-secret"?: boolean;
  "x-widget"?: Widget;
  "x-visible-if"?: VisibleIf;
  "x-help"?: string;
}

export type Values = Record<string, unknown>;

/** Server marks a stored secret with one of these instead of returning it. */
export const SECRET_MASKS: readonly string[] = ["***", "********"];

export type ErrorCode =
  | "required"
  | "type"
  | "format"
  | "minLength"
  | "maxLength"
  | "pattern"
  | "enum"
  | "minimum"
  | "maximum";

export interface FieldError {
  code: ErrorCode;
  params?: Record<string, unknown>;
}
