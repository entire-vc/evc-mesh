import { readFileSync } from "node:fs";
import path from "node:path";
import Ajv2020 from "ajv/dist/2020.js";

const SCHEMA = JSON.parse(readFileSync(path.join(import.meta.dirname, "..", "schema", "manifest.v1.json"), "utf8"));
const validator = new Ajv2020({ allErrors: true, strict: false, validateFormats: false }).compile(SCHEMA);

/** Errors of `manifest` against the extension manifest JSON Schema, one line each; empty when valid. */
export function schemaErrors(manifest) {
  if (validator(manifest)) return [];
  return validator.errors.map((e) => `${e.instancePath || "/"} ${e.message}`);
}

/**
 * The manifest of a built theme: the author's base manifest with the `theme` block's views, css,
 * slots and contract_report filled from the build. Only called when every slot passed.
 */
export function assembleManifest(base, { bundleName, bundleSha, cssName, cssSha, slots, report }) {
  const theme = { ...base.theme };
  delete theme.views;
  delete theme.css;
  delete theme.slots;
  delete theme.contract_report;
  if (cssName) theme.css = { path: cssName, sha256: cssSha };
  if (bundleName) {
    theme.views = { bundle: bundleName, sha256: bundleSha };
    theme.slots = slots;
    theme.contract_report = report;
  }
  return { ...base, theme };
}
