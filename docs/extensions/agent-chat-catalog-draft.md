# agent-chat: Spark catalog listing (DRAFT, not published)

> **Status: draft.** Nothing here is published. All visible text below is a draft and goes
> through `visible-text-approval` before it reaches the Spark catalog; publication happens only
> after E1–E5 are accepted and the final SHA check in E6. Catalog ownership is with
> Marketing; this page is preparation only.

Source of truth for behaviour: [runner-chat-protocol.md](runner-chat-protocol.md) (do not
duplicate it here). The shipped manifest is `internal/extensions/agentchat/mesh-extension.json`
(`runtime: builtin`); this draft adds what a catalog entry needs on top of it.

## 1. Manifest draft

Validates against `internal/extensions/schema/manifest.v1.json`. `requires` takes extension ids,
so "needs a runner" is `requires: ["runner"]` (the `runner` extension, which owns host
controllers and pools), not a free-form flag.

```json
{
  "schema": 1,
  "id": "agent-chat",
  "name": "Chat with Agent",
  "version": "0.1.0",
  "kind": "plugin",
  "runtime": "builtin",
  "mesh_api": ">=1 <2",
  "publisher": "entire-vc",
  "description": "Chat with Agent: private per-user threads with a project agent served by a workspace runner",
  "category": "agents",
  "tags": ["agents", "chat"],
  "license": "Apache-2.0",
  "repository": "https://github.com/entire-vc/evc-mesh",
  "levels": { "instance": false, "workspace": true, "project": true },
  "requires": ["runner"],
  "capabilities": {
    "api": ["agents:read", "projects:read"],
    "events": [],
    "webhooks_in": [],
    "ui": ["project.settings", "workspace.settings"],
    "secrets": []
  }
}
```

The `settings` block is unchanged from the shipped manifest and omitted here for brevity; the
listing must be generated from the shipped file at publication, not copied from this page.
Open points for the publisher (not decided here):

- `commit_sha` / `artifact_sha256` of the release: filled at publication (E6 SHA check).
- `runtime: builtin` ships inside Mesh; whether the catalog lists it as informational only or as
  an updatable release is a Spark/Marketing decision.

## 2. Disclosures (draft text)

| Heading | Draft text |
|---|---|
| Network | Mesh makes no outbound call for chat. A runner you operate connects outbound to your Mesh instance only (long-poll, up to 120 s). What the agent's tools reach is set by your runner profile, not by this extension. Declared `network.allowed_domains`: none. |
| Data sent to the runner | The user's message text, attachment metadata with short-lived download links, the requester's id and display name, and earlier messages of the same thread when the runner rebuilds context. |
| Data returned | Reply text, tool labels, token usage and cost, session id / harness / profile. |
| Storage | Mesh PostgreSQL (`ext_agent_chat_*` tables); attachments in the instance's object storage; the runner keeps its own session transcript on your host. |
| Retention | Workspace setting `history_retention_days` (0 = keep). A user delete is soft; data is purged after 30 days and the runner is told to delete its transcript. |
| Secrets | The agent key and runner token stay with the runner process. The agent session gets only a 5-minute `cst_` token limited to one project, with rights = agent ∩ user. |
| Permissions asked on connect | `agents:read`, `projects:read`, UI slots `project.settings`, `workspace.settings`. |

## 3. Not promised in the listing

Same limits as protocol §10: no guaranteed live streaming, no capacity reporting, no
write-workspace in a chat, cancel is polled (≤ 60 s). Until E3 an internal ticket, E4 an internal ticket and
E5 an internal ticket are accepted, the listing must not claim them.

## 4. Before publishing

1. E1–E5 accepted; E6 SHA check done on the release commit.
2. Visible text approved per `visible-text-approval` tier.
3. Marketing card agreed with Mark (catalog owner).
4. The [connection checklist](agent-chat-connect-checklist.md) passes on a clean instance.
