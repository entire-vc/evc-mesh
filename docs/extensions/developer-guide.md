> Canonical version: https://spark.entire.vc/api/v1/mesh-extensions/docs/developer-guide — this copy is synced automatically; edit the source in Spark, not this file.

# Build and publish a Mesh extension

Mesh extensions declare their API permissions, events, UI slots and external
service endpoints in `mesh-extension.json`. Spark checks each immutable release
before it becomes available to Mesh instances.

## Prerequisites

- Python 3.12 or later and pipx.
- Trivy 0.70.0 on PATH, with access to its vulnerability database. Without a
  working scanner, validation exits 2 (incomplete), never success.
- A repository on `git.entire.host` or GitHub that you can push to.
- A Spark account with publisher access (see below).

## Getting publisher access

Publishing requires an active MCP Gate Pro subscription or access granted by the
Spark team. Sign in to Spark and open **Profile → My Tools → Mesh extensions**
to check your access. If you do not have access, choose **Request access** or
email [spark@entire.vc](mailto:spark@entire.vc?subject=Publisher%20access%20request)
with your Spark account email, publisher name and repository URL. The Spark team
can grant publisher access independently of your subscription. A confirmed
publisher name and badge are reviewed separately; they do not grant publishing
access.

## Where your repository must live

Spark accepts HTTPS repository roots on **github.com** or **git.entire.host**.
Other GitLab hosts are not supported. If you use your own GitLab instance,
create a mirror on GitHub and submit the GitHub repository URL. Keep the mirror's
version tags and manifest in sync before submitting a release.

Public and private repositories are supported. Supply a token with push/write
access to the repository: a GitHub token with repository contents read/write,
or a git.entire.host GitLab token with API access and at least the **Developer**
role. Spark verifies write access before accepting a submission. Do not put the
token in the repository URL or commit it to your repository.

## Install the publisher tools

Install the publisher tools without cloning Spark:

```sh
pipx install https://spark.entire.vc/api/v1/mesh-extensions/docs/mesh-ext-0.2.1.tar.gz
mesh-ext --version
```

This URL serves the tools bundled with the running Spark release. The download
response includes `X-Artifact-SHA256`. Keep that digest with your build evidence.
For local development, `pipx install ./backend/mesh_ext` works from a Spark checkout.

## Start from the template

Fork or copy [mesh-extension-template](https://github.com/entire-vc/mesh-extension-template).
The template derives from `mesh-ext-hello`:
signed installation/events, authenticated UI requests, health and two UI slots.

```sh
git clone https://github.com/YOUR_ACCOUNT/YOUR_REPOSITORY.git my-extension
cd my-extension
```

Before your first release, edit `mesh-extension.json`:

| Field | What to set |
| --- | --- |
| `id` | Unique lowercase slug, 3–41 characters, starting with a letter. Do not include `mesh` or end with `plugin`/`extension`. |
| `name`, `publisher`, `description` | Your extension's name, organization and concise purpose. |
| `repository` | Your own HTTPS repository root, without a branch/file suffix. |
| `version` | Plain `major.minor.patch`, initially `0.1.0`. |
| `mesh_api` | Supported integer API range, such as `>=1 <2`. |
| `levels` | The scopes where installation is supported. |
| `capabilities` | Only the API scopes, events, inbound webhooks, network domains and UI slots your code uses. |

Keep `hello/__init__.py` in sync with the manifest version. The template declares
`tasks:read`, `task.updated`, `sidebar.item` and `task.panel`. API permissions are
`resource:read`/`resource:write`; declaring a scope does not implement a feature.
More than three write scopes require a README explanation headed “Justification”.
Use `--max-write-permissions` only when your target catalogue has a different
documented policy; it cannot change the server's policy.

For an external extension, `external.base_url_setting` names its configured
service URL. Keep `health_path`, `install_path`, `events_path` and each UI slot's
path consistent with your service. Installation delivers a signing secret and
JWKS URL. Events use HMAC-SHA256 over `timestamp.body` and reject replay skew
over five minutes. UI requests require a Mesh JWT verified against that JWKS.
The template keeps installation state in `HELLO_STATE_FILE`; mount persistent
storage when deploying it. State is atomically written with mode 0600; protect
it as a credential. Set `HELLO_JWT_AUDIENCE` to your manifest ID.

First installation is refused by default. For bootstrap, the operator must set
`HELLO_BOOTSTRAP_PEERS` to the exact trusted Mesh server IP(s), comma-separated.
The service checks its direct TCP peer, never `X-Forwarded-For`. If a reverse
proxy terminates HTTPS, restrict `/mesh/install` at that proxy to the trusted
Mesh source addresses **before** allowlisting the proxy's own peer address.
Allowlisting a shared proxy without that restriction exposes bootstrap to every
caller. Remove the bootstrap allowlist after the first successful installation.
Every later installation, including JWKS changes, requires a fresh HMAC signed
with the existing installation secret. Do not delete production state to reset
installation; restore it from your protected backup or provision a new instance.

Deploy your service at an HTTPS URL reachable from your Mesh instance, then set
`hello_url` in workspace settings when installing. To run locally:

```sh
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python -m hello
```

`GET /healthz` must return `status: ok` and your running version. The optional
publisher health check is recorded separately; catalogue validation does not
prove that your service has been deployed.

## Disclose behavior and validate

README must contain nonempty **Network**, **Accounts** and **Telemetry** sections.
“None” is valid when true. Explain outbound hosts, data storage and external
accounts. Include a LICENSE, lock dependency declarations, and keep credentials
outside the repository. Do not commit `.env`, development environments, binary
build products or symlinks. `.git`, `.venv`, `node_modules` and common Python
caches are skipped locally; committing them can still make the server refuse
the remote archive. Other untracked files are checked locally too.

```sh
mesh-ext validate . --tag 0.1.0
```

Exit 0 means all required checks passed; exit 1 means a refusal/error; exit 2
means some checks are incomplete. `mesh-ext validate . --static` is an offline
diagnostic: it deliberately exits 2 when otherwise clean because dependency and
image audits were not run. The same Python checks power the Spark pipeline.
They scan every source file without installing or executing repository code.
Unsupported dependency coverage, binary source, scanner/database failure and
review-steering patterns remain pending. HIGH/CRITICAL vulnerabilities fail.

Exact reviewed negative fixtures can carry `reviewed_fixtures` audit records in
the patterns check. Each record binds the repository, extension ID, exact file
path/hash and scanner family/rule. Legacy `snapshot-sha256-v1` records also bind
the complete snapshot and version. `file-sha256-v1` records survive changes to
the version and other files; their version records where the file was first
reviewed. A new, changed or moved flagged file requires a new reviewed record;
an unchanged flagged file at the same path does not. A file record does not
attest the complete snapshot or how other files use the admitted content.
Validate and package expose these records without source payloads;
receipt recovery exposes only records recognized by the installed CLI policy.
Packaging also returns its static checks alongside the archive size and SHA256.

The trusted registry ships inside Spark and its CLI source distribution. Its
admissions require a canonical Spark code change and independent review of each
fixture. Source manifests, approval files, environment variables and CLI flags
cannot supply admissions. Every file is still scanned; secrets, dependency and
image audits, coverage and publication moderation keep their existing gates.

For a negative control, copy the repository outside your working directory,
set the copy's manifest `version` to `invalid`, and run `mesh-ext validate` on it.
Require exit 1 and `checks.schema.status == "fail"`; then require exit 0 on the
original. Do not publish the deliberately broken copy.

The template does not declare an image. If you distribute one, build and push
it before publishing. For GitLab, set `external.image` to
`git.entire.host:5050/YOUR_GROUP/YOUR_REPOSITORY:0.1.0`. The scanner must pull that
exact repository/version and report its digest and package coverage. An image
on another registry leaves the catalogue check pending. GitHub publishers may
leave this optional field absent. Declare your dependency files regardless.

## Package and publish

Create a deterministic source archive outside the extension directory:

```sh
mesh-ext package . --output ../my-extension-0.1.0.tar.gz
```

The command prints its SHA-256 and size, refuses an existing output file, and
requires all static checks to pass. Packaging needs no scanner/network and does
not grant publication. The archive is a local source artifact; Spark builds its
own artifact from the exact remote tag, so package contents are not uploaded.

Commit the reviewed source to your default branch and push a matching tag:

```sh
git add .
git commit -m "feat: add initial extension"
git push origin HEAD
git tag 0.1.0
git push origin 0.1.0
```

Set credentials using your secret manager or a private shell configuration:

- `SPARK_TOKEN`: the bearer token of your signed-in Spark session. If your account
  has no separate token facility, sign in at `https://spark.entire.vc`, then use
  browser developer tools → Application → Local Storage → that origin →
  `spark_auth_token`. Read the JSON object's `token` field into your private
  configuration. Refresh it by signing in again when it expires.
- `GITLAB_TOKEN`: a token with API access and Developer-or-higher rights on the
  owning GitLab project. A token for a read-only project will be refused.
- For GitHub, use `GITHUB_TOKEN` with repository API access and push/admin rights
  instead of `GITLAB_TOKEN`.

Never put credential values in CLI arguments, a manifest, README, CI logs or a
support message. The CLI sends them only to the fixed Spark HTTPS origin and
does not follow redirects or print raw server error bodies.

```sh
mesh-ext publish .
```

Spark reads the **remote default branch** to discover the initial version, then
loads that version's exact tag commit. Your local directory is a preflight;
uncommitted/unpushed changes do not become a release. The returned slug/version
must match the local manifest. A moved existing tag is refused.

First releases enter human moderation even with green checks. A successful
submission prints `status: pending`, `published: false` and
`awaiting_moderation: true`. It is **not yet a published catalogue entry**.
Ask the catalogue reviewer to approve the returned release in Spark's existing
review queue. Pending required checks must be resolved before approval.
Rejected checks exit 1; incomplete checks exit 2.

If a submission was interrupted or the CLI rejected its response, recover the
existing receipt before submitting again:

```sh
mesh-ext publish . --receipt
```

This sends only an authenticated GET for the local manifest's ID/version. It
requires `SPARK_TOKEN`, but no repository token, and does not create, delete,
revalidate or approve a release. It prints the recorded remote commit and check
statuses, never README metadata. Use the original release directory; local
changes are not submitted by this command. A missing receipt exits 1 with
`reason: receipt_not_found`. An
existing pending receipt still requires moderation; do not delete or retag it.

CLI errors exit 1 and include a fixed JSON `reason` code:

| Code | Meaning and next step |
| --- | --- |
| `token_missing` | Required `SPARK_TOKEN` or repository token is absent. Configure it privately; receipt recovery needs only `SPARK_TOKEN`. |
| `invalid_configuration` | A token exceeds 4096 characters or contains whitespace/non-ASCII characters. Correct its private configuration. |
| `invalid_source` | Local source, manifest, identity, repository URL or baseline cannot be read or is invalid. Check the local input. |
| `invalid_output` | Package output is unsafe, oversized, already exists or cannot be written. Choose a new output outside the source directory. |
| `remote_refused` | Spark returned an unexpected HTTP status (including redirects). Check the numeric `http_status`, account access and catalogue availability. |
| `receipt_not_found` | Receipt recovery returned HTTP 404 or no release matches the local version. Check ID/version and whether it was submitted. |
| `invalid_receipt` | Spark's response is malformed, inconsistent or contains duplicate receipts for the version. Recover the receipt before retrying a submission. |
| `invalid_signature` | The catalogue signature does not match the trusted public key. Check the key through your operator's trusted configuration. |
| `artifact_mismatch` | Downloaded bytes differ from the signed artifact SHA-256. No output file is created. |
| `network` | The request failed at the transport/protocol layer, including a timeout. Check connectivity, then recover the receipt before retrying a submission. |

These codes never include response bodies, URLs or token values. A valid receipt
whose checks fail or await completion still reports its check statuses and exits
1 or 2 respectively; use those statuses to diagnose the policy verdict.

If your installed tools do not recognize `--receipt`, reinstall the current
bundle served by Spark, retaining the new `X-Artifact-SHA256`:

```sh
pipx install --force https://spark.entire.vc/api/v1/mesh-extensions/docs/mesh-ext-0.2.1.tar.gz
```

The bundle follows the running Spark release; its digest identifies the exact
bytes. A failed POST may already have created a release, so check its receipt
instead of assuming that an error means nothing was saved.

Publication is complete only when the public detail and signed catalogue
contain the expected version:

```sh
curl --fail https://spark.entire.vc/api/v1/mesh-extensions/YOUR_ID
curl --fail https://spark.entire.vc/api/v1/mesh-extensions/catalog.json
```

Check the response content for your ID/version and use Mesh's catalogue verifier
to verify `X-Catalog-Signature`; HTTP 200 alone is insufficient.

For later versions, update the manifest/runtime version, validate, commit and
push the new tag, then run:

```sh
mesh-ext validate . --baseline ../previous-published-manifest.json --tag 0.1.1
mesh-ext publish . --release
```

The baseline is the previous **published** manifest; permission deltas are
calculated by the shared checks. A patch with unchanged runtime/permissions can
publish automatically when every required check passes. Minor/major releases,
runtime or permission changes require review. There is no timed auto-rejection.
A pending or rejected new version leaves the old published version available.
Blocked versions enter the blocklist; yanked versions cannot be newly installed.

## Publish an exact version without changing the default catalogue

Tools 0.2.0 add an explicit publication scope. Existing commands keep the
`default` scope. For an authorized publisher to submit a scoped first release
or a new version of an existing extension:

```sh
mesh-ext publish . --catalog-scope scoped
mesh-ext publish . --release --catalog-scope scoped
mesh-ext publish . --receipt
```

Choose the appropriate POST command once; receipt recovery is read-only.
The submit/release JSON accepts `catalog_scope: "scoped"` (otherwise
`"default"`). The scope is pinned with the immutable tag, commit and manifest.
Retries must use the same scope. A webhook retry preserves an existing scope;
new webhook tags retain the default behavior. No flag promotes an existing
version or changes its owner/repository. All scanners, publisher authorization,
artifact pinning and moderation rules still apply.

Scoped versions are installable by exact selection but absent from default
catalogue selection, the default public release list and the card's default
version/description. To obtain a signed receipt, supply both ID and version:

```sh
mesh-ext catalog --scope scoped --slug YOUR_ID --version 0.1.1 --public-key catalog-key.b64 > scoped-receipt.json
mesh-ext download --slug YOUR_ID --version 0.1.1 --public-key catalog-key.b64 --output ../extension-0.1.1.tar.gz
mesh-ext catalog --public-key catalog-key.b64 > default-after.json
```

`catalog-key.b64` contains the trusted base64-encoded 32-byte Ed25519 public
key supplied by your catalogue operator, matching your installation's verifier.
Do not trust a key supplied alongside an untrusted receipt. These commands use
anonymous requests and never forward publisher credentials. Download fetches
a fresh exact signed receipt, follows the ordinary artifact endpoint, verifies
SHA256 before creating the output file, and refuses an existing output path.

API: `GET /api/v1/mesh-extensions/catalog.json?scope=scoped&slug=YOUR_ID&version=0.1.1`
optionally accepts `mesh_api=1`. Its signed JSON has
`selection: {scope: "scoped", slug: "YOUR_ID", version: "0.1.1"}` and exactly
one entry/release binding ID, version, commit and artifact SHA256. Exact
selection also works for existing default releases. `latest` in this receipt
means only the selected version; it does not change the global `latest`.
Unknown/duplicate query fields, partial selectors and selectors without
`scope=scoped` return 422. Missing, incompatible, yanked, blocked, suspended or
nonpublic selections return 404. Signed bytes, ETag and cache variants include
the selector. The CLI preserves the original signed bytes as `body_base64`
alongside `signature`, `etag` and the parsed `catalog`.

The ordinary download route remains
`GET /api/v1/mesh-extensions/YOUR_ID/releases/0.1.1/artifact`; it returns the
same immutable bytes and refuses revoked releases. Keep a default receipt
before publishing and compare the extension's `latest` and `releases` after
publication (the snapshot timestamp may change).

Rollback uses the existing reviewer-only moderation API, not a replacement
upload or a scope change. The authorization header is read from standard input
(`--header @-`, curl 7.55 or newer), so the token never appears in the command
line; `printf` is a shell builtin and starts no separate process:

```sh
printf 'Authorization: Bearer %s\n' "$SPARK_REVIEWER_TOKEN" | curl --fail --request PATCH --header @- --header 'Content-Type: application/json' --data '{"action":"yank","reason":"Release rollback"}' https://spark.entire.vc/api/v1/mesh-extensions/YOUR_ID/releases/0.1.1/moderation
```

The same reviewer can use `unyank` after resolving the issue; scope and hashes
stay pinned. After any scoped release exists, retain the additive database
column: schema downgrade deliberately refuses to discard a scope. Before
rolling application code back to a version without scoped-catalogue support,
yank every scoped release so legacy selection cannot promote it.

## Independent publication check

A reviewer who has not participated in implementation should use only this
guide: copy the public template, choose a unique ID and their own repository,
run the broken-manifest negative control and the green validation, package,
push the default branch/tag, submit through `mesh-ext publish`, request the
normal catalogue approval, and verify the published ID/version from the public
API and a verified catalogue. Record command exits, package digest, immutable
commit and final release status. Implementation help is not part of this check.
