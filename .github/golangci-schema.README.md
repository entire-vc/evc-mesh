# golangci-schema.json

Vendored copy of the JSON Schema `golangci-lint config verify` validates
`.golangci.yml` against, so CI never has to fetch it over the network
(task #848579ba — a merge-queue eject when that fetch timed out).

Pinned to the `version` used in `.github/workflows/ci.yml` and
`deploy-backend.yml` (currently v2.11.3). Re-vendor when that version
changes:

```
curl -o .github/golangci-schema.json \
  https://golangci-lint.run/jsonschema/golangci.v2.11.jsonschema.json
```
(swap `v2.11` for the new minor version).
