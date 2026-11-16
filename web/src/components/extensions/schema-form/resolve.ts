import type { FieldSchema, OneOfOption } from "./types";

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

const isConstChoice = (o: unknown): o is OneOfOption => !!o && typeof o === "object" && "const" in o;

/**
 * Labelled choices `oneOf: [{ const, title }]` become the `enum` the widgets read plus `x-options` labels for it
 * (an explicit `x-options` label wins). A bare `enum` and a `oneOf` of real sub-schemas are left as they are.
 */
function labelledChoices(node: FieldSchema): FieldSchema {
  const choices = node.oneOf;
  if (node.enum || !Array.isArray(choices) || choices.length === 0 || !choices.every(isConstChoice)) return node;
  const labels: Record<string, string> = {};
  for (const c of choices) if (typeof c.title === "string" && c.title) labels[String(c.const)] = c.title;
  return { ...node, enum: choices.map((c) => c.const), "x-options": { ...labels, ...node["x-options"] } };
}

/**
 * Inline every local `$ref` (`#/schemas/connection`) against `root` (the
 * manifest). Sibling keywords next to the `$ref` win. Cycles stop at depth 8. Labelled `oneOf` choices are
 * read as `enum` + `x-options` on the way.
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
  out = labelledChoices(out);
  if (out.properties) {
    const props: Record<string, FieldSchema> = {};
    for (const [k, v] of Object.entries(out.properties)) props[k] = resolveSchema(v, root, depth + 1);
    out = { ...out, properties: props };
  }
  if (out.items) out = { ...out, items: resolveSchema(out.items, root, depth + 1) };
  if (out.additionalProperties && typeof out.additionalProperties === "object") {
    out = { ...out, additionalProperties: resolveSchema(out.additionalProperties, root, depth + 1) };
  }
  if (out.propertyNames) out = { ...out, propertyNames: resolveSchema(out.propertyNames, root, depth + 1) };
  return out;
}
