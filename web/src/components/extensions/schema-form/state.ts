import { SECRET_MASKS, type FieldSchema, type Values } from "./types";
import { isVisible } from "./visibility";

export interface InitialState {
  values: Values;
  secretsSet: Set<string>;
}

/**
 * Split the server value into form state and the set of stored secrets. The
 * server never returns a secret, only a mask: the mask is dropped here so it
 * can never land in an input or in the DOM.
 */
export function initialState(schema: FieldSchema, value: Values | undefined): InitialState {
  const secretsSet = new Set<string>();
  const walk = (s: FieldSchema, src: Values | undefined, prefix: string): Values => {
    const out: Values = {};
    for (const [name, f] of Object.entries(s.properties ?? {})) {
      const path = prefix ? `${prefix}.${name}` : name;
      const raw = src?.[name];
      if (f.type === "object" && f.properties) {
        out[name] = walk(f, raw && typeof raw === "object" ? (raw as Values) : undefined, path);
      } else if (f["x-secret"]) {
        if (typeof raw === "string" && SECRET_MASKS.includes(raw)) secretsSet.add(path);
        // a secret draft always starts empty
      } else if (raw !== undefined) {
        out[name] = raw;
      } else if (f.default !== undefined) {
        out[name] = f.default;
      }
    }
    return out;
  };
  return { values: walk(schema, value, ""), secretsSet };
}

/**
 * Payload for the server: visible fields only; a secret is sent only when the
 * user typed a new one (stored secrets are omitted and stay as they are).
 */
export function buildPayload(schema: FieldSchema, values: Values): Values {
  const out: Values = {};
  for (const [name, f] of Object.entries(schema.properties ?? {})) {
    if (!isVisible(f, values)) continue;
    const v = values[name];
    if (f.type === "object" && f.properties) {
      out[name] = buildPayload(f, (v && typeof v === "object" ? v : {}) as Values);
    } else if (f["x-secret"]) {
      if (typeof v === "string" && v !== "") out[name] = v;
    } else if (v !== undefined && v !== "") {
      out[name] = v;
    } else if (v === undefined && f.type === "boolean" && schema.required?.includes(name)) {
      out[name] = false; // an untouched required toggle shows "off"; send what the user sees
    }
  }
  return out;
}

export function setAtPath(values: Values, path: string[], next: unknown): Values {
  const [first, ...rest] = path;
  const head = first as string;
  if (rest.length === 0) return { ...values, [head]: next };
  const child = (values[head] && typeof values[head] === "object" ? values[head] : {}) as Values;
  return { ...values, [head]: setAtPath(child, rest, next) };
}
