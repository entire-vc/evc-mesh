import type { FieldSchema } from "./types";

function pointer(root: unknown, ref: string): FieldSchema | undefined {
  if (!ref.startsWith("#/")) return undefined;
  let cur: unknown = root;
  for (const raw of ref.slice(2).split("/")) {
    const key = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    if (cur === null || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[key];
  }
  return cur as FieldSchema | undefined;
}

/**
 * Inline every local `$ref` (`#/schemas/connection`) against `root` (the
 * manifest). Sibling keywords next to the `$ref` win. Cycles stop at depth 8.
 */
export function resolveSchema(node: FieldSchema, root: unknown, depth = 0): FieldSchema {
  if (depth > 8) return node;
  let out: FieldSchema = node;
  if (node.$ref) {
    const target = pointer(root, node.$ref);
    const { $ref: _ref, ...rest } = node;
    void _ref;
    out = { ...(target ? resolveSchema(target, root, depth + 1) : {}), ...rest };
  }
  if (out.properties) {
    const props: Record<string, FieldSchema> = {};
    for (const [k, v] of Object.entries(out.properties)) props[k] = resolveSchema(v, root, depth + 1);
    out = { ...out, properties: props };
  }
  if (out.items) out = { ...out, items: resolveSchema(out.items, root, depth + 1) };
  if (out.additionalProperties && typeof out.additionalProperties === "object") {
    out = { ...out, additionalProperties: resolveSchema(out.additionalProperties, root, depth + 1) };
  }
  return out;
}
