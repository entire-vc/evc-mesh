import type { FieldError, FieldSchema, Values } from "./types";
import { isVisible } from "./visibility";

export type Errors = Record<string, FieldError>;

export interface ValidateContext {
  /** Dot paths of secrets already stored on the server. */
  secretsSet: ReadonlySet<string>;
}

function empty(v: unknown): boolean {
  return v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);
}

/**
 * Required, yet asks for no value: a toggle always holds on or off, and `minItems: 0` says an
 * empty list is a valid answer (the key must be there, not a value). Validation and the `*` read this.
 */
export function emptyAllowed(field: FieldSchema): boolean {
  return field.type === "boolean" || (field.type === "array" && field.minItems === 0);
}

// A manifest pattern runs as a JS regex on the UI thread: bound it, leave the rest to the server.
const MAX_PATTERN_LEN = 256;
const MAX_PATTERN_INPUT = 2048;
const NESTED_QUANTIFIER = /[+*}]\)?[+*{]/;

// a colour widget holds `#rgb`/`#rrggbb`, or "H S% L%" when the field is bound to a token
const HEX_VALUE = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;
const TRIPLET_VALUE = /^(\d{1,3}(?:\.\d+)?) (\d{1,3}(?:\.\d+)?)% (\d{1,3}(?:\.\d+)?)%$/;

function validColor(v: string): boolean {
  if (HEX_VALUE.test(v)) return true;
  const m = TRIPLET_VALUE.exec(v);
  return m !== null && Number(m[1]) <= 360 && Number(m[2]) <= 100 && Number(m[3]) <= 100;
}

function checkScalar(f: FieldSchema, v: unknown): FieldError | null {
  const t = f.type ?? "string";
  if (t === "string") {
    if (typeof v !== "string") return { code: "type" };
    if (f["x-widget"] === "color" && !validColor(v)) return { code: "pattern" };
    if (f.minLength != null && v.length < f.minLength) return { code: "minLength", params: { min: f.minLength } };
    if (f.maxLength != null && v.length > f.maxLength) return { code: "maxLength", params: { max: f.maxLength } };
    if (f.format === "uri") {
      try {
        const u = new URL(v);
        if (u.protocol !== "http:" && u.protocol !== "https:") return { code: "format", params: { format: "uri" } };
      } catch {
        return { code: "format", params: { format: "uri" } };
      }
    }
    if (f.format === "email" && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v)) return { code: "format", params: { format: "email" } };
    if (f.pattern && f.pattern.length <= MAX_PATTERN_LEN && v.length <= MAX_PATTERN_INPUT && !NESTED_QUANTIFIER.test(f.pattern)) {
      try {
        if (!new RegExp(f.pattern).test(v)) return { code: "pattern" };
      } catch {
        /* an invalid pattern in a manifest is the publisher's bug, the server decides */
      }
    }
  } else if (t === "number" || t === "integer") {
    if (typeof v !== "number" || Number.isNaN(v)) return { code: "type" };
    if (t === "integer" && !Number.isInteger(v)) return { code: "type" };
    if (f.minimum != null && v < f.minimum) return { code: "minimum", params: { min: f.minimum } };
    if (f.maximum != null && v > f.maximum) return { code: "maximum", params: { max: f.maximum } };
  } else if (t === "boolean") {
    if (typeof v !== "boolean") return { code: "type" };
  }
  if (f.enum && !f.enum.some((e) => e === v)) return { code: "enum" };
  return null;
}

export function isMapSchema(f: FieldSchema): boolean {
  return f.type === "object" && !f.properties && typeof f.additionalProperties === "object";
}

/** A map: every key one of `propertyNames.enum` (when given), every value valid by `additionalProperties`. */
function checkMap(f: FieldSchema, v: unknown): FieldError | null {
  if (v === undefined || v === null) return null;
  if (typeof v !== "object" || Array.isArray(v)) return { code: "type" };
  const keys = f.propertyNames?.enum;
  const of = f.additionalProperties as FieldSchema;
  for (const [k, item] of Object.entries(v as Values)) {
    if (keys && !keys.includes(k)) return { code: "enum" };
    if (of.type === "array") {
      if (!Array.isArray(item)) return { code: "type" };
      for (const x of item) {
        const e = of.items ? checkScalar(of.items, x) : null;
        if (e) return e;
      }
    } else {
      const e = checkScalar(of, item);
      if (e) return e;
    }
  }
  return null;
}

/**
 * Client-side validation by the schema, keyed by dot path. Hidden fields
 * (`x-visible-if`) are skipped. A required secret that is already stored and
 * not being replaced counts as present.
 */
export function validate(
  schema: FieldSchema,
  values: Values,
  ctx: ValidateContext,
  prefix = "",
): Errors {
  const errors: Errors = {};
  const required = new Set(schema.required ?? []);
  for (const [name, field] of Object.entries(schema.properties ?? {})) {
    if (!isVisible(field, values)) continue;
    const path = prefix ? `${prefix}.${name}` : name;
    const v = values[name];

    if (field.type === "object" && field.properties) {
      const nested = (v && typeof v === "object" ? v : {}) as Values;
      Object.assign(errors, validate(field, nested, ctx, path));
      continue;
    }
    if (field.type === "object" && isMapSchema(field)) {
      const e = checkMap(field, v);
      if (e) errors[path] = e;
      continue;
    }
    if (field.type === "array" && v !== undefined && v !== null && !Array.isArray(v)) {
      errors[path] = { code: "type" };
      continue;
    }
    if (empty(v)) {
      const storedSecret = field["x-secret"] && ctx.secretsSet.has(path);
      if (required.has(name) && !storedSecret && !emptyAllowed(field)) errors[path] = { code: "required" };
      continue;
    }
    if (field.type === "array") {
      const items = v as unknown[];
      if (field.minItems != null && items.length < field.minItems) {
        errors[path] = { code: "minItems", params: { min: field.minItems } };
        continue;
      }
      for (let i = 0; i < items.length; i++) {
        const e = field.items ? checkScalar(field.items, items[i]) : null;
        if (e) {
          errors[path] = e;
          break;
        }
      }
      continue;
    }
    const e = checkScalar(field, v);
    if (e) errors[path] = e;
  }
  return errors;
}
