// Package extensions holds the Mesh extension contract. Rules for raising
// ExtensionsAPIVersion live in docs/extensions/api-version.md.
package extensions

// ExtensionsAPIVersion is the integer version of the extension API (manifest,
// slots, permissions, events). Extension manifests reference this number, not
// the Mesh build version. It is raised only by a breaking change to that
// contract, decided by the Mesh lead through an ADR.
const ExtensionsAPIVersion = 1
