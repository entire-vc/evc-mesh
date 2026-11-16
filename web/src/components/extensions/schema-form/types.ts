export type Widget =
  | "toggle"
  | "select"
  | "textarea"
  | "list"
  | "group"
  | "project-picker"
  | "agent-picker"
  | "grant-picker"
  | "workspace-picker"
  | "color"
  | "font-select"
  | "image"
  | "field-picker"
  | "widget-picker"
  | "layout-picker"
  | "order-list"
  | "period"
  | "event-type-picker";

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
  /** Labelled choices (JSON Schema): `{ const, title }` each; read as `enum` with `x-options` labels (resolveSchema). */
  oneOf?: OneOfOption[];
  default?: unknown;
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  minimum?: number;
  maximum?: number;
  minItems?: number;
  uniqueItems?: boolean;
  items?: FieldSchema;
  properties?: Record<string, FieldSchema>;
  /** An object without `properties` whose values follow this schema is a map (key → value). */
  additionalProperties?: FieldSchema | boolean;
  /** Allowed map keys (`enum`) of a map. */
  propertyNames?: FieldSchema;
  required?: string[];
  /** Derived by the server; shown, never edited or sent back. */
  readOnly?: boolean;
  "x-secret"?: boolean;
  "x-widget"?: Widget;
  "x-visible-if"?: VisibleIf;
  "x-help"?: string;
  /** Theme settings: the design token this field drives; a colour token takes "H S% L%". */
  "x-token"?: string;
  /** Labels for `enum` values (select, checklist, layout-picker, font-select, order-list); a value without one shows as itself. */
  "x-options"?: Record<string, string>;
  /** Names a set of existing things the value must be one of; the host turns it into `enum` (bindRefs). */
  "x-ref"?: string;
  /** The same for the keys of a map (into `propertyNames.enum`). */
  "x-key-ref"?: string;
  /** grant-picker: sibling field holding the agent whose grants are offered (changing it clears this one). */
  "x-agent-field"?: string;
  /** grant-picker: sibling field set to the picked grant's workspace (a workspace-picker). */
  "x-workspace-field"?: string;
}

/** One labelled choice of `oneOf`: the stored value and what the user reads. */
export interface OneOfOption {
  const: unknown;
  title?: string;
  /** Host-side translation key of `title` (the dashboard settings dialog resolves it); the form itself ignores it. */
  "x-title-key"?: string;
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
  | "minItems"
  | "minimum"
  | "maximum";

export interface FieldError {
  code: ErrorCode;
  params?: Record<string, unknown>;
}
